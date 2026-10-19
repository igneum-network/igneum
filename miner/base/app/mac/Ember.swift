// Ember for macOS: the window around the engine. One Swift file, compiled by packaging/mac/build-dmg.sh with
//   swiftc -O -target arm64-apple-macos11 -o "Ember" Ember.swift -framework Cocoa -framework WebKit
// It starts Contents/MacOS/igneum-app --wrapper, reads "URL ..." and "STATE {...}" lines from its stdout, shows the URL
// in a WKWebView, keeps a menu-bar item with the state, and on quit writes "quit" to the engine's stdin and waits for
// its "EXIT" line (miners stop first, then the node). Closing the window hides it; the app lives on in the menu bar
// and the Dock. 4 October 2026.
//
// Snapshot mode, used to make the design screenshots without a browser:
//   "Ember" --snapshot <out.png> --url <dashboard url> [--size 1120x780]

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

final class App: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate, NSWindowDelegate {
    var window: NSWindow!
    var web: WKWebView!
    var engine: Process?
    var engineIn: FileHandle?
    var status: NSStatusItem!
    var menuOpen = NSMenuItem(title: "Open Ember", action: #selector(showWindow), keyEquivalent: "")
    var menuPause = NSMenuItem(title: "Pause mining", action: #selector(togglePause), keyEquivalent: "")
    var menuNode = NSMenuItem(title: "Node: starting", action: nil, keyEquivalent: "")
    var menuBlocks = NSMenuItem(title: "Blocks found: 0", action: nil, keyEquivalent: "")
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
    /// --wait <s>: the seconds the page gets before the snapshot (the audit's chain card needs its first feed read)
    var snapshotWait = 3.5

    func applicationDidFinishLaunching(_ note: Notification) {
        NSApp.setActivationPolicy(snapshotPath == nil ? .regular : .accessory)
        buildMenu()
        buildWindow()
        if let p = snapshotPath, let u = snapshotURL, let url = URL(string: u) {
            self.url = url
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            web.load(URLRequest(url: url))
            DispatchQueue.main.asyncAfter(deadline: .now() + snapshotWait) { self.snapshot(to: p) }
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
        appMenu.addItem(withTitle: "About Ember", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Ember", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Quit Ember", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
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

    func buildWindow() {
        // window-22 (7 October 2026, the one rule with app/windows/opening_size.h): the PRIMARY screen's work area, 80 percent
        // wide capped at 1440 up to a 1920-wide display and 1920 beyond, height from the width at 16:10 bounded by the work
        // area less 40, never wider than 1.6 times the height, never under 900 by 600, never over the work area
        let work = (NSScreen.screens.first ?? NSScreen.main)?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        func openingSize(_ workW: CGFloat, _ workH: CGFloat) -> NSSize {
            let minW = min(workW, 900), minH = min(workH, 600), capW: CGFloat = workW <= 1920 ? 1440 : 1920
            var w = min(floor(workW * 0.8), capW); var h = min(floor(w / 1.6), workH - 40)
            w = max(w, minW); h = max(h, minH); w = min(w, floor(h * 1.6)); w = min(w, workW); h = min(h, workH)
            return NSSize(width: w, height: h)
        }
        func fits(_ f: NSRect) -> Bool { f.width >= 900 && f.height >= 600 && f.width <= work.width && f.height <= work.height && f.width <= f.height * 1.632 }
        let opening = openingSize(work.width, work.height)
        let size = snapshotPath == nil ? opening : snapshotSize
        window = NSWindow(contentRect: NSRect(origin: .zero, size: size), styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        window.title = "Ember"
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.isMovableByWindowBackground = true
        window.backgroundColor = obsidian
        window.minSize = NSSize(width: 900, height: 600)
        if snapshotPath == nil {
            // the remembered frame is kept only when it fits the primary work area and the aspect cap, else discarded
            if !window.setFrameUsingName("EmberMain") || !fits(window.frame) { window.setContentSize(opening); window.center() }
            window.setFrameAutosaveName("EmberMain")
        } else { window.center() }
        window.delegate = self
        window.isReleasedWhenClosed = false
        window.appearance = NSAppearance(named: .darkAqua)
        let conf = WKWebViewConfiguration()
                web = WKWebView(frame: window.contentView!.bounds, configuration: conf)
        web.autoresizingMask = [.width, .height]
        web.navigationDelegate = self
        web.uiDelegate = self
        web.setValue(false, forKey: "drawsBackground")
        web.customUserAgent = "Ember/0.2.4 (Macintosh)"
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
        menu.addItem(withTitle: "Quit Ember", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "")
        menu.autoenablesItems = false
        menuOpen.isEnabled = true
        menuPause.isEnabled = true
        status.menu = menu
    }

    // ---- the engine ----
    func startEngine() {
        let exe = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/igneum-app")
        guard FileManager.default.isExecutableFile(atPath: exe.path) else {
            fail("igneum-app is missing from the app bundle. Copy Ember from the disk image again.")
            return
        }
        let p = Process()
        p.executableURL = exe
        p.arguments = ["--wrapper"]
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
            if line.hasPrefix("URL ") {
                let u = String(line.dropFirst(4)).trimmingCharacters(in: .whitespaces)
                if let url = URL(string: u + "?host=mac") {
                    self.url = url
                    placeholder.isHidden = true
                    web.load(URLRequest(url: url))
                }
            } else if line.hasPrefix("STATE ") {
                applyState(String(line.dropFirst(6)))
            } else if line.hasPrefix("SHOT ") {
                // Miner UI 4 (b): the engine's POST /api/shot asks for a PNG of what the window shows, at this path
                shotTo(String(line.dropFirst(5)).trimmingCharacters(in: .whitespaces))
            } else if line.hasPrefix("FATAL ") {
                fail(String(line.dropFirst(6)))
            } else if line == "EXIT" {
                exited = true
                if quitting { NSApp.reply(toApplicationShouldTerminate: true) }
            }
        }
    }

    func applyState(_ json: String) {
        guard let data = json.data(using: .utf8), let o = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return }
        let mining = o["mining"] as? String ?? ""
        let node = o["node"] as? String ?? ""
        let hash = o["hash_total"] as? Double ?? 0
        let blocks = o["blocks"] as? Double ?? 0
        let accepted = o["accepted_total"] as? Double ?? 0
        let peers = o["peers"] as? Double ?? 0
        paused = o["paused"] as? Bool ?? false
        let phase = o["phase"] as? String ?? ""
        var title = ""
        if o["quitting"] as? Bool == true { title = "stopping" }
        else if phase != "dashboard" { title = "" }
        else if mining == "mining" { title = String(format: "%.1f MH/s", hash) }
        else if mining == "paused" { title = "paused" }
        else if node == "syncing" || node == "starting" { title = "syncing" }
        else { title = mining }
        status.button?.title = title.isEmpty ? "" : " " + title
        menuPause.title = paused ? "Resume mining" : "Pause mining"
        let nf = NumberFormatter(); nf.numberStyle = .decimal
        menuNode.title = "Node: \(node), \(nf.string(from: NSNumber(value: blocks)) ?? "0") blocks, \(Int(peers)) peers"
        menuBlocks.title = "Blocks found: \(nf.string(from: NSNumber(value: accepted)) ?? "0")"
    }

    func engineEnded() {
        exited = true
        if quitting { NSApp.reply(toApplicationShouldTerminate: true); return }
        placeholder.stringValue = "The engine stopped. Quit and open Ember again."
        placeholder.isHidden = false
        status?.button?.title = " stopped"
    }

    func fail(_ text: String) {
        placeholder.stringValue = text
        placeholder.isHidden = false
        let a = NSAlert()
        a.messageText = "Ember"
        a.informativeText = text
        a.runModal()
    }

    @objc func showWindow() {
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc func togglePause() {
        send(paused ? "resume" : "pause")
    }

    func send(_ cmd: String) {
        if let d = (cmd + "\n").data(using: .utf8) { engineIn?.write(d) }
    }

    // ---- quit: miners first, then the node ----
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if snapshotPath != nil { return .terminateNow }
        guard let e = engine, e.isRunning, !exited else { return .terminateNow }
        quitting = true
        status?.button?.title = " stopping"
        web.evaluateJavaScript("document.getElementById('pill-text').textContent='stopping'", completionHandler: nil)
        send("quit")
        // 45 s is the engine's own worst case (8 s miners, 30 s node, the last upload); then force
        DispatchQueue.main.asyncAfter(deadline: .now() + 45) {
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
        // the window hides; the miner keeps running in the menu bar
        window.orderOut(nil)
        return false
    }

    // ---- links: anything off 127.0.0.1 opens in the default browser ----
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
        a.messageText = "Ember"
        a.informativeText = message
        a.addButton(withTitle: "Quit")
        a.addButton(withTitle: "Cancel")
        completionHandler(a.runModal() == .alertFirstButtonReturn)
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let a = NSAlert(); a.messageText = "Ember"; a.informativeText = message; a.runModal(); completionHandler()
    }

    // ---- a screenshot on request (the SHOT line), the app keeps running ----
    func shotTo(_ path: String) {
        let conf = WKSnapshotConfiguration()
        conf.rect = web.bounds
        web.takeSnapshot(with: conf) { image, error in
            guard let img = image, let tiff = img.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff), let png = rep.representation(using: .png, properties: [:]) else {
                FileHandle.standardError.write("shot failed: \(error?.localizedDescription ?? "no image")\n".data(using: .utf8)!)
                return
            }
            do { try png.write(to: URL(fileURLWithPath: path)) } catch { FileHandle.standardError.write("shot: could not write \(path): \(error)\n".data(using: .utf8)!) }
        }
    }

    // ---- snapshot ----
    func snapshot(to path: String) {
        if let js = ProcessInfo.processInfo.environment["IGNEUM_PROBE"] {
            web.evaluateJavaScript(js) { r, e in print("probe:", r ?? "nil", e?.localizedDescription ?? "") }
        }
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

let app = NSApplication.shared
let delegate = App()
var args = Array(CommandLine.arguments.dropFirst())
var i = 0
while i < args.count {
    switch args[i] {
    case "--snapshot": if i + 1 < args.count { delegate.snapshotPath = args[i + 1]; i += 1 }
    case "--url": if i + 1 < args.count { delegate.snapshotURL = args[i + 1]; i += 1 }
    case "--wait": if i + 1 < args.count { delegate.snapshotWait = Double(args[i + 1]) ?? 3.5; i += 1 }
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
