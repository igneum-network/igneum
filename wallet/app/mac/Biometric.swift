// Touch ID for the Igneum window hosts (IgneumWallet.swift, IgneumMiner.swift), compiled into each with
//   swiftc ... IgneumWallet.swift Biometric.swift -framework Cocoa -framework WebKit -framework LocalAuthentication
// 5 October 2026.
//
// The page in the WKWebView posts {id, op, ...} to the "biometric" script message handler; the host answers with
// window.__igneumBiometric(id, {ok, code, message, ...}). Ops: status, enrol, unlock (wallet), confirm.
//
// What is stored, and where. The packaging signs the apps ad hoc, and under an ad hoc signature macOS refuses any
// Keychain item with a biometric access control (errSecMissingEntitlement, -34018, on the login keychain and the
// data-protection keychain alike: keychain-access-groups needs a team signature). A Secure Enclave key with the same
// control is allowed, so the secret is sealed to one instead:
//   - enrol: a P-256 key is made in the Secure Enclave with SecAccessControl(.privateKeyUsage, .biometryCurrentSet);
//     an ephemeral P-256 key agrees a shared secret with its public half (no prompt), HKDF-SHA256 makes an AES-GCM key
//     and the secret (the wallet's password; a fixed marker for the miner) is sealed. The file the engine named
//     (<data root>/<app>/biometric.json, user-only permissions) holds the Secure Enclave key's wrapped form, the
//     ephemeral public key and the box. The wrapped key is useless off this Mac's Secure Enclave.
//   - unlock or confirm: the host evaluates LAPolicy.deviceOwnerAuthenticationWithBiometrics (no fallback button: the
//     wallet's own password is the fallback, in the window), then uses the Secure Enclave key under that context.
//     .biometryCurrentSet means a fingerprint added or removed in System Settings makes the key unusable: the host
//     reports "invalidated" and the window asks for the password and a fresh enrolment.
// The secret never goes through the page: on unlock the host posts it to the engine on 127.0.0.1 with the engine's
// URL token; the engine's host token (X-Igneum-Host, read from the engine's stdout) marks the calls only the host
// may make (the confirmations, taking the parked password at enrolment).

import Foundation
import AppKit
import LocalAuthentication
import CryptoKit
import Security

final class Biometric {
    let product: String
    /// the prompt's fallback button: "Use password" for the wallet (the window then asks for it), "" for the miner
    let fallbackTitle: String
    /// the enrolment prompt's line: "Turn on Touch ID for your wallet" / "... for the miner"
    let enrolReason: String
    private(set) var engineURL = ""
    private(set) var hostToken = ""
    private(set) var file: URL?
    private let queue = DispatchQueue(label: "network.igneum.biometric")
    private let salt = Data("igneum-biometric-v1".utf8)
    private let minerMarker = "igneum-biometric-marker-v1"

    init(product: String, fallbackTitle: String, enrolReason: String) {
        self.product = product
        self.fallbackTitle = fallbackTitle
        self.enrolReason = enrolReason
    }

    /// From the engine's HOST line. Reports availability to the engine at once.
    func configure(engineURL: String, hostToken: String, file: String) {
        self.engineURL = engineURL
        self.hostToken = hostToken
        self.file = file.isEmpty ? nil : URL(fileURLWithPath: file)
        let s = status()
        queue.async {
            _ = self.post("api/biometric/status", ["available": s.available, "kind": "touchid", "message": s.message])
        }
    }

    // ---- availability ----

    func status() -> (available: Bool, message: String) {
        let ctx = LAContext()
        var err: NSError?
        if ctx.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &err) {
            if ctx.biometryType != .touchID { return (false, "This Mac has no Touch ID sensor.") }
            return (true, "")
        }
        return (false, Biometric.text(for: err, op: "status").message)
    }

    // ---- the ops, one at a time on the queue ----

    func handle(_ msg: [String: Any], reply: @escaping ([String: Any]) -> Void) {
        let op = msg["op"] as? String ?? ""
        queue.async {
            let r: [String: Any]
            switch op {
            case "status":
                let s = self.status()
                r = ["ok": true, "available": s.available, "kind": "touchid", "message": s.message, "enrolled": self.sealedFileExists()]
            case "enrol": r = self.enrol(token: msg["token"] as? String)
            case "unlock": r = self.unlock()
            case "confirm": r = self.confirm(nonce: msg["nonce"] as? String ?? "")
            case "password": r = self.passwordSheet(reason: msg["reason"] as? String ?? "Unlock your wallet")
            default: r = ["ok": false, "code": "bad_op", "message": "unknown op \(op)"]
            }
            if op != "status" {
                _ = self.post("api/biometric/report", ["op": op, "ok": r["ok"] as? Bool ?? false, "code": r["code"] as? String ?? "", "message": r["message"] as? String ?? ""])
            }
            DispatchQueue.main.async { reply(r) }
        }
    }

    private func enrol(token: String?) -> [String: Any] {
        guard let file = file else { return fail("unavailable", "The engine has not named the sealed file yet.") }
        let s = status()
        if !s.available { return fail("unavailable", s.message) }
        let ctx = context()
        if let e = evaluate(ctx, reason: enrolReason) { return e }
        // the secret: the wallet's password from the engine (one-time token), or the miner's marker
        var secret: Data
        if let t = token, !t.isEmpty {
            let r = post("api/biometric/enrol/take", ["token": t])
            guard r.ok, let p = r.json["secret"] as? String else { return fail("engine", r.json["error"] as? String ?? "the engine did not hand over the password") }
            secret = Data(p.utf8)
        } else {
            secret = Data(minerMarker.utf8)
        }
        defer { secret.resetBytes(in: 0..<secret.count) }
        do {
            var acErr: Unmanaged<CFError>?
            guard let ac = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage, .biometryCurrentSet], &acErr) else {
                return fail("secure_enclave", "The access control could not be made: \(acErr?.takeRetainedValue().localizedDescription ?? "unknown")")
            }
            let key = try SecureEnclave.P256.KeyAgreement.PrivateKey(accessControl: ac, authenticationContext: ctx)
            let eph = P256.KeyAgreement.PrivateKey()
            let shared = try eph.sharedSecretFromKeyAgreement(with: key.publicKey)
            let sym = shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: salt, sharedInfo: Data(product.utf8), outputByteCount: 32)
            let box = try AES.GCM.seal(secret, using: sym)
            guard let combined = box.combined else { return fail("seal", "The box has no combined form.") }
            let doc: [String: Any] = ["version": 1, "kind": "touchid", "product": product, "created": Int(Date().timeIntervalSince1970),
                                      "key": key.dataRepresentation.base64EncodedString(), "eph": eph.publicKey.rawRepresentation.base64EncodedString(), "box": combined.base64EncodedString()]
            let data = try JSONSerialization.data(withJSONObject: doc, options: [.sortedKeys])
            try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
            try data.write(to: file, options: [.atomic])
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        } catch {
            return fail("secure_enclave", "The Secure Enclave refused: \(error.localizedDescription)")
        }
        let r = post("api/biometric/enrolled", ["kind": "touchid"])
        if !r.ok { return fail("engine", r.json["error"] as? String ?? "the engine did not record the enrolment") }
        return ["ok": true]
    }

    /// The host's own password sheet (wallet-security's G1, 12 October 2026): the password is typed into a native
    /// secure field on the main thread, posted by the host to the engine's unlock with the host token, and never
    /// reaches the page; the field's text is cleared after. The renderer's own field stays for a browser tab.
    private func passwordSheet(reason: String) -> [String: Any] {
        var typed: String? = nil
        DispatchQueue.main.sync {
            let alert = NSAlert()
            alert.messageText = reason
            alert.informativeText = "Your wallet's password, into the window's own sheet; the page never sees it."
            alert.alertStyle = .informational
            alert.addButton(withTitle: "Unlock")
            alert.addButton(withTitle: "Cancel")
            let field = NSSecureTextField(frame: NSRect(x: 0, y: 0, width: 280, height: 24))
            field.placeholderString = "Password"
            alert.accessoryView = field
            alert.window.initialFirstResponder = field
            if alert.runModal() == .alertFirstButtonReturn {
                typed = field.stringValue
            }
            field.stringValue = ""
        }
        guard var p = typed, !p.isEmpty else { return fail("cancelled", "No password given.") }
        defer { p.removeAll(keepingCapacity: false) }
        let r = post("api/unlock", ["password": p])
        if !r.ok { return fail("engine", r.json["error"] as? String ?? "the engine did not unlock") }
        // F-W1 (15 October 2026): the engine says the sealed password no longer matched: one prompt re-seals this one
        if (r.json["reseal"] as? Bool) == true, let t = r.json["token"] as? String { _ = enrol(token: t) }
        return ["ok": true]
    }

    /// Wallet: Touch ID, then the password out of the box and into the engine; never to the page.
    private func unlock() -> [String: Any] {
        guard sealedFileExists() else { return fail("not_enrolled", "Touch ID is not turned on for this wallet.") }
        let ctx = context()
        if let e = evaluate(ctx, reason: "Unlock your wallet") { return e }
        switch open(ctx) {
        case .failure(let e): return e.dict
        case .success(var secret):
            defer { secret.resetBytes(in: 0..<secret.count) }
            guard let p = String(data: secret, encoding: .utf8) else { return fail("seal", "The sealed password did not decode.") }
            let r = post("api/unlock", ["password": p])
            if !r.ok { return fail("engine", r.json["error"] as? String ?? "the engine did not unlock") }
            return ["ok": true]
        }
    }

    /// The engine's challenge: its reason on the prompt, then the confirmation with the host token. The Secure
    /// Enclave key is exercised too, so a changed fingerprint set is caught here as well.
    private func confirm(nonce: String) -> [String: Any] {
        guard !nonce.isEmpty else { return fail("bad_nonce", "No challenge.") }
        let c = get("api/biometric/challenge?nonce=\(nonce)")
        guard c.ok, let reason = c.json["reason"] as? String else { return fail("engine", c.json["error"] as? String ?? "the engine does not know this challenge") }
        let ctx = context()
        if let e = evaluate(ctx, reason: reason) { return e }
        if sealedFileExists() {
            if case .failure(let e) = agree(ctx) { return e.dict }
        }
        let r = post("api/biometric/confirm", ["nonce": nonce])
        if !r.ok { return fail("engine", r.json["error"] as? String ?? "the engine refused the confirmation") }
        return ["ok": true]
    }

    // ---- Touch ID ----

    func context() -> LAContext {
        let ctx = LAContext()
        // the policy is biometrics only, so the fallback button never reaches the device password: "Use password"
        // (the wallet) returns LAError.userFallback and the window asks for the wallet's own password; the miner
        // has no password, so no button
        ctx.localizedFallbackTitle = fallbackTitle
        ctx.localizedCancelTitle = "Cancel"
        return ctx
    }

    /// nil on success, else the reply for the page.
    private func evaluate(_ ctx: LAContext, reason: String) -> [String: Any]? {
        var err: NSError?
        guard ctx.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &err) else {
            let t = Biometric.text(for: err, op: "evaluate")
            return fail(t.code, t.message)
        }
        // macOS composes the sentence itself: "Igneum Wallet is trying to <reason>." (the bundled app's name and icon
        // are the title), so the line starts lowercase here; the engine's canonical lines keep their capital for
        // Windows Hello, which shows the line on its own
        let line = String(reason.prefix(80))
        let shown = line.prefix(1).lowercased() + line.dropFirst()
        let sem = DispatchSemaphore(value: 0)
        var ok = false
        var failure: Error?
        ctx.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, localizedReason: shown) { success, error in
            ok = success
            failure = error
            sem.signal()
        }
        sem.wait()
        if ok { return nil }
        let t = Biometric.text(for: failure as NSError?, op: "evaluate")
        return fail(t.code, t.message)
    }

    static func text(for err: NSError?, op: String) -> (code: String, message: String) {
        guard let e = err else { return ("failed", "Touch ID did not succeed.") }
        if e.domain == LAErrorDomain, let la = LAError.Code(rawValue: e.code) {
            switch la {
            case .userCancel, .systemCancel, .appCancel: return ("cancelled", "Touch ID was cancelled.")
            case .authenticationFailed: return ("failed", "Touch ID did not match.")
            case .biometryLockout: return ("locked", "Touch ID is locked after too many tries. Use the password; Touch ID comes back after the Mac is unlocked with its password.")
            case .biometryNotEnrolled: return ("not_set_up", "Touch ID is not set up on this Mac. Add a fingerprint in System Settings > Touch ID & Password.")
            case .biometryNotAvailable, .passcodeNotSet: return ("unavailable", "Touch ID is not available on this Mac.")
            case .userFallback: return ("fallback", "Enter your password.")
            default: break
            }
        }
        return ("failed", "Touch ID did not succeed: \(e.localizedDescription)")
    }

    // ---- the sealed file ----

    func sealedFileExists() -> Bool {
        guard let f = file else { return false }
        return FileManager.default.fileExists(atPath: f.path)
    }

    private struct Sealed {
        let key: SecureEnclave.P256.KeyAgreement.PrivateKey
        let eph: P256.KeyAgreement.PublicKey
        let box: AES.GCM.SealedBox
    }

    private func load(_ ctx: LAContext) -> Result<Sealed, Reply> {
        guard let f = file, let data = try? Data(contentsOf: f), let doc = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
              let k = doc["key"] as? String, let e = doc["eph"] as? String, let b = doc["box"] as? String,
              let kd = Data(base64Encoded: k), let ed = Data(base64Encoded: e), let bd = Data(base64Encoded: b) else {
            return .failure(failure("not_enrolled", "The sealed file is missing or unreadable. Turn Touch ID on again in Settings."))
        }
        do {
            let key = try SecureEnclave.P256.KeyAgreement.PrivateKey(dataRepresentation: kd, authenticationContext: ctx)
            let eph = try P256.KeyAgreement.PublicKey(rawRepresentation: ed)
            let box = try AES.GCM.SealedBox(combined: bd)
            return .success(Sealed(key: key, eph: eph, box: box))
        } catch {
            return .failure(failure("invalidated", "Touch ID no longer opens this: the sealed key is not usable (\(error.localizedDescription)). Use the password, then turn Touch ID on again in Settings."))
        }
    }

    /// The key agreement under the authenticated context: the Secure Enclave refuses it when the fingerprint set
    /// changed since enrolment (.biometryCurrentSet).
    private func agree(_ ctx: LAContext) -> Result<SymmetricKey, Reply> {
        switch load(ctx) {
        case .failure(let e): return .failure(e)
        case .success(let s):
            do {
                let shared = try s.key.sharedSecretFromKeyAgreement(with: s.eph)
                return .success(shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: salt, sharedInfo: Data(product.utf8), outputByteCount: 32))
            } catch {
                return .failure(failure("invalidated", "Touch ID no longer opens this: a fingerprint was added or removed since it was turned on. Use the password, then turn Touch ID on again in Settings."))
            }
        }
    }

    private func open(_ ctx: LAContext) -> Result<Data, Reply> {
        switch load(ctx) {
        case .failure(let e): return .failure(e)
        case .success(let s):
            do {
                let shared = try s.key.sharedSecretFromKeyAgreement(with: s.eph)
                let sym = shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: salt, sharedInfo: Data(product.utf8), outputByteCount: 32)
                return .success(try AES.GCM.open(s.box, using: sym))
            } catch {
                return .failure(failure("invalidated", "Touch ID no longer opens this: a fingerprint was added or removed since it was turned on. Use the password, then turn Touch ID on again in Settings."))
            }
        }
    }

    // ---- the engine on 127.0.0.1 ----

    /// A reply for the page that is also an Error, so Result can carry it.
    struct Reply: Error {
        let dict: [String: Any]
    }

    private func fail(_ code: String, _ message: String) -> [String: Any] {
        ["ok": false, "code": code, "message": message]
    }
    private func failure(_ code: String, _ message: String) -> Reply {
        Reply(dict: fail(code, message))
    }

    private struct Answer {
        let ok: Bool
        let json: [String: Any]
    }

    private func request(_ path: String, method: String, body: [String: Any]?) -> Answer {
        guard !engineURL.isEmpty, let url = URL(string: engineURL + path) else { return Answer(ok: false, json: ["error": "no engine URL"]) }
        var req = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 15)
        req.httpMethod = method
        req.setValue(hostToken, forHTTPHeaderField: "X-Igneum-Host")
        if let b = body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try? JSONSerialization.data(withJSONObject: b)
        }
        let sem = DispatchSemaphore(value: 0)
        var out = Answer(ok: false, json: ["error": "no answer from the engine"])
        let task = URLSession.shared.dataTask(with: req) { data, resp, error in
            defer { sem.signal() }
            if let e = error { out = Answer(ok: false, json: ["error": e.localizedDescription]); return }
            let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
            let j = data.flatMap { (try? JSONSerialization.jsonObject(with: $0)) as? [String: Any] } ?? [:]
            out = Answer(ok: code == 200 && (j["ok"] as? Bool ?? true), json: j)
        }
        task.resume()
        sem.wait()
        return out
    }

    private func post(_ path: String, _ body: [String: Any]) -> Answer {
        request(path, method: "POST", body: body)
    }

    private func get(_ path: String) -> Answer {
        request(path, method: "GET", body: nil)
    }
}
