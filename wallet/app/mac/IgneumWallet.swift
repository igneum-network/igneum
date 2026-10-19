// Igneum Wallet for macOS: the window around the wallet engine. A copy of IgneumMiner.swift with the names, the
// bundle and the menu changed (no pause; a Lock item instead). Compiled by packaging/mac/build-wallet-dmg.sh with
//   swiftc -O -target arm64-apple-macos11 -o "Igneum Wallet" IgneumWallet.swift Biometric.swift -framework Cocoa -framework WebKit -framework LocalAuthentication
// The previous wallet's Mac host on the restart's engine (OLD-BASE.md step 8, 11 October 2026). It starts
// Contents/MacOS/igneum-wallet-engine with the bundle's page and signer and --exit-with-parent, reads the "ENGINE {...}"
// and "HOST {...}" lines from its stdout, shows the URL in a WKWebView, keeps a menu-bar item with the state (api/state
// every 10 s), and on quit closes the engine's stdin, which ends the engine and the signer. Closing the window hides it; the
// app lives on in the menu bar and the Dock. 4 October 2026. Touch ID (Biometric.swift, the "biometric" script
// message handler): 5 October 2026.
//
// Snapshot mode, used to make the design screenshots without a browser:
//   "Igneum Wallet" --snapshot <out.png> --url <window url> [--size 1120x780]

import Cocoa
import WebKit

let obsidian = NSColor(srgbRed: 12 / 255, green: 12 / 255, blue: 14 / 255, alpha: 1)

func flameImage(size: CGFloat) -> NSImage {
    // the brand mark's flame as a template image for the menu bar
    let img = NSImage(size: NSSize(width: size, height: size), flipped: true) { rect in
        let s = rect.width / 100
        let outer = NSBezierPath()
        let pts: [(CGFloat, CGFloat)] = [(50, 4), (74, 34), (67, 58), (80, 54), (61, 96), (39, 96), (20, 54), (33, 58), (26, 34)]
        outer.move(to: NSPoint(x: pts[0].0 * s, y: pts[0].1 * s))
        for p in pts.dropFirst() { outer.line(to: NSPoint(x: p.0 * s, y: p.1 * s)) }
        outer.close()
        let inner = NSBezierPath()
        let ip: [(CGFloat, CGFloat)] = [(50, 42), (59, 58), (50, 82), (41, 58)]
        inner.move(to: NSPoint(x: ip[0].0 * s, y: ip[0].1 * s))
        for p in ip.dropFirst() { inner.line(to: NSPoint(x: p.0 * s, y: p.1 * s)) }
        inner.close()
        outer.append(inner)
        outer.windingRule = .evenOdd
        NSColor.black.setFill()
        outer.fill()
        return true
    }
    img.isTemplate = true
    return img
}

/// A clear strip over the top band of the web view: WKWebView swallows window drags, this view lets the window move.
final class DragStrip: NSView {
    override var mouseDownCanMoveWindow: Bool { true }
    override func hitTest(_ point: NSPoint) -> NSView? { bounds.contains(point) ? self : nil }
}

final class App: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate, NSWindowDelegate, WKScriptMessageHandler {
    let bio = Biometric(product: "Igneum Wallet", fallbackTitle: "Use password", enrolReason: "Turn on Touch ID for your wallet")
    var enginePort = ""
    var window: NSWindow!
    var web: WKWebView!
    var engine: Process?
    var engineIn: FileHandle?
    var status: NSStatusItem!
    var menuOpen = NSMenuItem(title: "Open Igneum Wallet", action: #selector(showWindow), keyEquivalent: "")
    var menuPause = NSMenuItem(title: "Lock", action: #selector(lockWallet), keyEquivalent: "")
    var menuNode = NSMenuItem(title: "Node: looking", action: nil, keyEquivalent: "")
    var menuBlocks = NSMenuItem(title: "Balance: locked", action: nil, keyEquivalent: "")
    var url: URL?
    var paused = false
    var exited = false
    var quitting = false
    var buffer = Data()
    var placeholder: NSTextField!

    // snapshot mode
    var snapshotPath: String?
    var snapshotURL: String?
    var snapshotSize = NSSize(width: 1120, height: 780)

    func applicationDidFinishLaunching(_ note: Notification) {
        NSApp.setActivationPolicy(snapshotPath == nil ? .regular : .accessory)
        buildMenu()
        buildWindow()
        if let p = snapshotPath, let u = snapshotURL, let url = URL(string: u) {
            self.url = url
            enginePort = url.port.map(String.init) ?? ""
            // design screenshots and tests: IGNEUM_HOST_LINE (the engine's HOST json) wires the Touch ID bridge to a
            // scratch engine, IGNEUM_PROBE runs JS in the page, IGNEUM_SNAPSHOT_DELAY (s) waits before the capture
            if let h = ProcessInfo.processInfo.environment["IGNEUM_HOST_LINE"], let d = h.data(using: .utf8), let o = try? JSONSerialization.jsonObject(with: d) as? [String: Any] {
                bio.configure(engineURL: u.replacingOccurrences(of: "?host=mac", with: ""), hostToken: o["token"] as? String ?? "", file: o["biometric_file"] as? String ?? "")
            }
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            web.load(URLRequest(url: url))
            DispatchQueue.main.asyncAfter(deadline: .now() + 3.5) { self.snapshot(to: p) }
            return
        }
        buildStatusItem()
        startEngine()
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    func buildMenu() {
        let main = NSMenu()
        let appItem = NSMenuItem(); main.addItem(appItem)
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "About Igneum Wallet", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Igneum Wallet", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Quit Igneum Wallet", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        let editItem = NSMenuItem(); main.addItem(editItem)
        let edit = NSMenu(title: "Edit")
        edit.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        edit.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = edit
        let winItem = NSMenuItem(); main.addItem(winItem)
        let win = NSMenu(title: "Window")
        win.addItem(withTitle: "Minimize", action: #selector(NSWindow.miniaturize(_:)), keyEquivalent: "m")
        win.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        winItem.submenu = win
        NSApp.mainMenu = main
    }

    /// The launch variables an agent's capture job sets (the project's audit, 12 October 2026): the page to open and the theme
    /// as query parameters the page reads; the window size by IGNEUM_WALLET_SIZE ("900x620").
    func launchQuery() -> String {
        let env = ProcessInfo.processInfo.environment
        var q = ""
        if let o = env["IGNEUM_WALLET_OPEN"], !o.isEmpty, o.allSatisfy({ $0.isLetter }) { q += "&open=" + o }
        if let t = env["IGNEUM_WALLET_THEME"], t == "dark" || t == "light" { q += "&theme=" + t }
        return q
    }
    func launchSize() -> NSSize? {
        guard let s = ProcessInfo.processInfo.environment["IGNEUM_WALLET_SIZE"], s != "default" else { return nil }
        let parts = s.lowercased().split(separator: "x").compactMap { Double($0) }
        guard parts.count == 2, parts[0] >= 600, parts[1] >= 400 else { return nil }
        return NSSize(width: parts[0], height: parts[1])
    }
    func buildWindow() {
        let size = snapshotPath == nil ? (launchSize() ?? NSSize(width: 1120, height: 780)) : snapshotSize
        window = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        window.title = "Igneum Wallet"
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.isMovableByWindowBackground = true
        window.backgroundColor = obsidian
        window.minSize = NSSize(width: 900, height: 620)
        window.center()
        window.delegate = self
        window.isReleasedWhenClosed = false
        let conf = WKWebViewConfiguration()
        conf.userContentController.add(self, name: "biometric")   // the page's Touch ID bridge (Biometric.swift)
        web = WKWebView(frame: window.contentView!.bounds, configuration: conf)
        web.autoresizingMask = [.width, .height]
        web.navigationDelegate = self
        web.uiDelegate = self
        web.setValue(false, forKey: "drawsBackground")
        web.customUserAgent = "IgneumWallet/0.0.1 (Macintosh)"
        window.contentView?.addSubview(web)
        // the brand band is draggable; the pill and the settings button on the right stay clickable
        let strip = DragStrip(frame: NSRect(x: 0, y: size.height - 60, width: size.width - 300, height: 60))
        strip.autoresizingMask = [.width, .minYMargin]
        window.contentView?.addSubview(strip)
        placeholder = NSTextField(labelWithString: "Starting the engine")
        placeholder.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .medium)
        placeholder.textColor = NSColor(srgbRed: 154 / 255, green: 154 / 255, blue: 158 / 255, alpha: 1)
        placeholder.alignment = .center
        placeholder.frame = NSRect(x: 0, y: 18, width: size.width, height: 20)
        placeholder.autoresizingMask = [.width, .maxYMargin]
        placeholder.isHidden = snapshotPath != nil
        window.contentView?.addSubview(placeholder)
    }

    func buildStatusItem() {
        status = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        status.button?.image = flameImage(size: 18)
        status.button?.imagePosition = .imageLeading
        status.button?.title = ""
        let menu = NSMenu()
        menu.addItem(menuOpen)
        menu.addItem(menuPause)
        menu.addItem(.separator())
        menuNode.isEnabled = false
        menuBlocks.isEnabled = false
        menu.addItem(menuNode)
        menu.addItem(menuBlocks)
        menu.addItem(.separator())
        menu.addItem(withTitle: "Quit Igneum Wallet", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "")
        menu.autoenablesItems = false
        menuOpen.isEnabled = true
        menuPause.isEnabled = true
        status.menu = menu
    }

    // ---- the engine ----
    func startEngine() {
        // the restart's engine (OLD-BASE.md step 8: the previous wallet's host on it): the page from the bundle's
        // Resources, the signer beside the engine, --exit-with-parent so the engine (and with it the signer) ends
        // when this process's stdin closes; the engine prints ENGINE {url} then HOST {token, biometric_file}
        let macos = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS")
        let exe = macos.appendingPathComponent("igneum-wallet-engine")
        let signer = macos.appendingPathComponent("igneum-signer")
        let ui = Bundle.main.bundleURL.appendingPathComponent("Contents/Resources/ui")
        guard FileManager.default.isExecutableFile(atPath: exe.path), FileManager.default.isExecutableFile(atPath: signer.path) else {
            fail("The engine or the signer is missing from the app bundle. Copy Igneum Wallet from the disk image again.")
            return
        }
        let p = Process()
        p.executableURL = exe
        p.arguments = ["--ui", ui.path, "--signer", signer.path, "--exit-with-parent"]
        let out = Pipe(), inp = Pipe()
        p.standardOutput = out
        p.standardInput = inp
        p.standardError = FileHandle.standardError
        engineIn = inp.fileHandleForWriting
        out.fileHandleForReading.readabilityHandler = { h in
            let d = h.availableData
            if d.isEmpty { return }
            DispatchQueue.main.async { self.feed(d) }
        }
        p.terminationHandler = { _ in
            DispatchQueue.main.async { self.engineEnded() }
        }
        do { try p.run() } catch {
            fail("The engine could not start: \(error.localizedDescription)")
            return
        }
        engine = p
    }

    func feed(_ d: Data) {
        buffer.append(d)
        while let nl = buffer.firstIndex(of: 10) {
            let lineData = buffer.subdata(in: 0..<nl)
            buffer.removeSubrange(0...nl)
            guard let line = String(data: lineData, encoding: .utf8) else { continue }
            if line.hasPrefix("ENGINE ") {
                // ENGINE {"url": "http://127.0.0.1:<port>/t/<token>/", "pid": n}: the page's address for this run
                if let d = String(line.dropFirst(7)).data(using: .utf8), let o = try? JSONSerialization.jsonObject(with: d) as? [String: Any], let u = o["url"] as? String, let url = URL(string: u + "?host=mac" + launchQuery()) {
                    self.url = url
                    enginePort = url.port.map(String.init) ?? ""
                    placeholder.isHidden = true
                    web.load(URLRequest(url: url))
                    startStatePoll(base: u)
                }
            } else if line.hasPrefix("HOST ") {
                // the host token and the sealed file's path: the page never sees this line
                if let d = String(line.dropFirst(5)).data(using: .utf8), let o = try? JSONSerialization.jsonObject(with: d) as? [String: Any] {
                    bio.configure(engineURL: self.url?.absoluteString.replacingOccurrences(of: "?host=mac", with: "") ?? "", hostToken: o["token"] as? String ?? "", file: o["biometric_file"] as? String ?? "")
                }
            }
            // the restart's engine prints no STATE or EXIT line: the menu bar reads api/state on a timer and the
            // engine's end is the process's termination handler
        }
    }

    // ---- the menu bar's state: api/state every 10 s through the run's URL (the token is in the path) ----
    var stateTimer: Timer?
    var engineBase = ""
    func startStatePoll(base: String) {
        engineBase = base
        stateTimer?.invalidate()
        stateTimer = Timer.scheduledTimer(withTimeInterval: 10, repeats: true) { _ in self.pollState() }
        pollState()
    }

    func pollState() {
        guard !engineBase.isEmpty, let u = URL(string: engineBase + "api/state") else { return }
        var req = URLRequest(url: u); req.timeoutInterval = 5
        URLSession.shared.dataTask(with: req) { data, _, _ in
            guard let data = data, let o = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return }
            DispatchQueue.main.async { self.applyState(o) }
        }.resume()
    }

    func applyState(_ o: [String: Any]) {
        let node = o["node"] as? [String: Any] ?? [:]
        let network = o["network"] as? [String: Any] ?? [:]
        let nodeState = node["state"] as? String ?? ""
        let source = node["source"] as? String ?? ""
        let block = node["block"] as? Double ?? 0
        let unlocked = o["unlocked"] as? Bool ?? false
        let balance = o["balance_ign"] as? String ?? ""
        let phase = o["phase"] as? String ?? ""
        var title = ""
        if quitting { title = "stopping" }
        else if phase != "home" { title = "" }
        else if unlocked && !balance.isEmpty { title = "\(balance) IGN" }
        else if !unlocked { title = "locked" }
        status.button?.title = title.isEmpty ? "" : " " + title
        menuPause.title = unlocked ? "Lock" : "Locked"
        menuPause.isEnabled = unlocked
        let nf = NumberFormatter(); nf.numberStyle = .decimal
        let chain = network["name"] as? String ?? ""
        menuNode.title = "Node: \(source) \(nodeState == "ok" ? "synced" : nodeState), block \(nf.string(from: NSNumber(value: block)) ?? "0")\(chain.isEmpty ? "" : " (\(chain))")"
        menuBlocks.title = unlocked ? "Balance: \(balance) IGN" : "Balance: locked"
    }

    func engineEnded() {
        exited = true
        if quitting { NSApp.reply(toApplicationShouldTerminate: true); return }
        placeholder.stringValue = "The engine stopped. Quit and open Igneum Wallet again."
        placeholder.isHidden = false
        status?.button?.title = " stopped"
    }

    func fail(_ text: String) {
        placeholder.stringValue = text
        placeholder.isHidden = false
        let a = NSAlert()
        a.messageText = "Igneum Wallet"
        a.informativeText = text
        a.runModal()
    }

    @objc func showWindow() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc func lockWallet() {
        // the restart's engine takes commands on its API, not its stdin: Lock posts api/lock through the run's URL
        guard !engineBase.isEmpty, let u = URL(string: engineBase + "api/lock") else { return }
        var req = URLRequest(url: u); req.httpMethod = "POST"; req.httpBody = Data("{}".utf8)
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        URLSession.shared.dataTask(with: req) { _, _, _ in DispatchQueue.main.async { self.pollState() } }.resume()
    }

    // ---- quit: the engine ends when its stdin closes (--exit-with-parent), locking the signer with it ----
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if snapshotPath != nil { return .terminateNow }
        guard let e = engine, e.isRunning, !exited else { return .terminateNow }
        quitting = true
        status?.button?.title = " stopping"
        web.evaluateJavaScript("document.getElementById('pill-text').textContent='stopping'", completionHandler: nil)
        stateTimer?.invalidate()
        engineIn?.closeFile()
        // the engine exits on the closed pipe within a moment; five seconds, then the process is ended by its handle
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) {
            if self.engine?.isRunning == true { self.engine?.terminate() }
            NSApp.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showWindow()
        return true
    }

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        // closing hides the window; the wallet keeps running in the menu bar (the status item's Open and Quit); one
        // notice in the wallet's own words, with a box that keeps it from showing again (the founder's bug of
        // 14 October 2026: the miner's "mining" words on the wallet's close)
        window.orderOut(nil)
        let d = UserDefaults.standard
        if !d.bool(forKey: "trayHintSuppressed") && !hintShown {
            hintShown = true
            let a = NSAlert()
            a.messageText = "Igneum Wallet is still running in the menu bar"
            a.informativeText = "The window is hidden. Open it from the menu bar icon; Quit from the menu ends the wallet."
            a.alertStyle = .informational
            a.showsSuppressionButton = true
            a.suppressionButton?.title = "Do not show this again"
            a.addButton(withTitle: "OK")
            a.runModal()
            if a.suppressionButton?.state == .on { d.set(true, forKey: "trayHintSuppressed") }
        }
        return false
    }
    var hintShown = false

    // ---- Touch ID: the page posts {id, op, ...}; the answer goes back as window.__igneumBiometric(id, {...}) ----
    func userContentController(_ ucc: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.name == "biometric", let body = message.body as? [String: Any], let id = body["id"] as? Int else { return }
        // only the engine's own page, same origin as the URL the engine printed
        let origin = message.frameInfo.securityOrigin
        guard origin.host == "127.0.0.1", String(origin.port) == enginePort else { return }
        bio.handle(body) { result in
            guard let d = try? JSONSerialization.data(withJSONObject: result), let j = String(data: d, encoding: .utf8) else { return }
            self.web.evaluateJavaScript("window.__igneumBiometric && window.__igneumBiometric(\(id), \(j))", completionHandler: nil)
        }
    }

    // ---- links: anything off 127.0.0.1 opens in the default browser ----
    /// The app smoke's witness (IGNEUM_WALLET_SMOKE_FILE, the bench Mac's queue, 11 October 2026): once the page has
    /// loaded, the host writes what it can see of its own window, since the queue's session has no window-server
    /// access for a capture: the frame, whether the window is visible and on screen (the occlusion state is the
    /// window server's word), and the page's path. Never set in a packaged run.
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard let path = ProcessInfo.processInfo.environment["IGNEUM_WALLET_SMOKE_FILE"], !path.isEmpty, let w = window else { return }
        let f = w.frame
        let onScreen = w.occlusionState.contains(.visible)
        let line = "window \(Int(f.width))x\(Int(f.height)) visible=\(w.isVisible) on_screen=\(onScreen) key=\(w.isKeyWindow) page=\(webView.url?.path ?? "") title=\(w.title)\n"
        try? line.write(toFile: path, atomically: true, encoding: .utf8)
    }

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let u = action.request.url, let scheme = u.scheme, scheme.hasPrefix("http"), u.host != "127.0.0.1" {
            NSWorkspace.shared.open(u)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let a = NSAlert()
        a.messageText = "Igneum Wallet"
        a.informativeText = message
        a.addButton(withTitle: "Quit")
        a.addButton(withTitle: "Cancel")
        completionHandler(a.runModal() == .alertFirstButtonReturn)
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let a = NSAlert(); a.messageText = "Igneum Wallet"; a.informativeText = message; a.runModal(); completionHandler()
    }

    // ---- snapshot ----
    func snapshot(to path: String) {
        if let js = ProcessInfo.processInfo.environment["IGNEUM_PROBE"] {
            web.evaluateJavaScript(js) { r, e in print("probe:", r ?? "nil", e?.localizedDescription ?? "") }
        }
        let delay = Double(ProcessInfo.processInfo.environment["IGNEUM_SNAPSHOT_DELAY"] ?? "") ?? 0
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { self.capture(to: path) }
    }

    func capture(to path: String) {
        let conf = WKSnapshotConfiguration()
        conf.rect = web.bounds
        web.takeSnapshot(with: conf) { image, error in
            defer { NSApp.terminate(nil) }
            guard let img = image, let tiff = img.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff), let png = rep.representation(using: .png, properties: [:]) else {
                FileHandle.standardError.write("snapshot failed: \(error?.localizedDescription ?? "no image")\n".data(using: .utf8)!)
                return
            }
            do { try png.write(to: URL(fileURLWithPath: path)); print("wrote \(path)") } catch { print("could not write \(path): \(error)") }
        }
    }
}

// The entry point. With Biometric.swift compiled alongside, top-level statements are not allowed here (only in a
// main.swift), so the launch lives in a @main type.
@main
struct Main {
    static func main() {
        let app = NSApplication.shared
        let delegate = App()
        let args = Array(CommandLine.arguments.dropFirst())
        var i = 0
        while i < args.count {
            switch args[i] {
            case "--snapshot": if i + 1 < args.count { delegate.snapshotPath = args[i + 1]; i += 1 }
            case "--url": if i + 1 < args.count { delegate.snapshotURL = args[i + 1]; i += 1 }
            case "--size":
                if i + 1 < args.count {
                    let parts = args[i + 1].split(separator: "x").compactMap { Double($0) }
                    if parts.count == 2 { delegate.snapshotSize = NSSize(width: parts[0], height: parts[1]) }
                    i += 1
                }
            default: break
            }
            i += 1
        }
        app.delegate = delegate
        app.run()
    }
}
