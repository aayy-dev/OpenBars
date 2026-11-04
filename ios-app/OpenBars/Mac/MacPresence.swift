import AppKit
import ServiceManagement
import SwiftUI

/// Where Open Bars shows up on the Mac. After setup the menu bar is the main
/// place to use it; setup and settings live in the window.
enum MacPresence: String, CaseIterable, Identifiable {
    case menuBar, menuBarAndDock, dock

    static let key = "macPresence"
    var id: String { rawValue }

    var title: String {
        switch self {
        case .menuBar: return "Menu bar"
        case .menuBarAndDock: return "Menu bar and Dock"
        case .dock: return "Dock only"
        }
    }

    var showsMenuBar: Bool { self != .dock }
    var showsInDock: Bool { self != .menuBar }

    static var current: MacPresence {
        MacPresence(rawValue: UserDefaults.standard.string(forKey: key) ?? "") ?? .menuBar
    }

    /// Open the window at launch when there's no menu bar item to use
    /// instead, or setup isn't done yet.
    static var showWindowAtLaunch: Bool {
        !current.showsMenuBar || UserDefaults.standard.data(forKey: "pairing") == nil
    }

    /// Dock icon on or off (an app without one is an "accessory" app).
    static func applyDockPolicy() {
        NSApp.setActivationPolicy(current.showsInDock ? .regular : .accessory)
    }
}

final class MacAppDelegate: NSObject, NSApplicationDelegate {
    func applicationWillFinishLaunching(_ notification: Notification) {
        MacPresence.applyDockPolicy()
    }

    /// Living in the menu bar: closing the window doesn't quit.
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // macOS 13 and 14 can't start a SwiftUI window closed, so close it right
        // away when Open Bars lives in the menu bar (macOS 15 uses
        // defaultLaunchBehavior, see OpenBarsMacApp15).
        if #unavailable(macOS 15.0) {
            if !MacPresence.showWindowAtLaunch {
                DispatchQueue.main.async {
                    for w in NSApp.windows where w.identifier?.rawValue.hasPrefix("main") == true { w.close() }
                }
            }
        }
        #if DEBUG
        snapshotIfRequested()
        #endif
    }

    #if DEBUG
    /// Screenshot mode (see DemoMode): `-snapshot <file.png>` saves the main
    /// window's contents at full resolution after a few seconds, then quits.
    /// The app draws its own window, so no screen-recording permission is needed.
    private func snapshotIfRequested() {
        let args = ProcessInfo.processInfo.arguments
        guard let i = args.firstIndex(of: "-snapshot"), i + 1 < args.count else { return }
        let path = args[i + 1]
        // Open the main window through its Window menu item if it isn't up.
        DispatchQueue.main.asyncAfter(deadline: .now() + 1) {
            guard !NSApp.windows.contains(where: { $0.isVisible && $0.frame.width > 200 }) else { return }
            let items = NSApp.mainMenu?.items.flatMap { $0.submenu?.items ?? [] } ?? []
            if let item = items.first(where: { $0.title == "Open Bars" }), let action = item.action {
                NSApp.sendAction(action, to: item.target, from: item)
            } else {
                FileHandle.standardError.write(Data("snapshot: menu items \(items.map(\.title))\n".utf8))
            }
            NSApp.activateCompat()
        }
        // Key window, so controls draw in their active colors.
        DispatchQueue.main.asyncAfter(deadline: .now() + 3) {
            NSApp.windows.filter { $0.isVisible && $0.frame.width > 200 }.first?.makeKeyAndOrderFront(nil)
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) {
            guard let window = NSApp.windows.filter({ $0.isVisible && $0.frame.width > 200 }).max(by: { $0.frame.width < $1.frame.width }),
                  let view = window.contentView?.superview ?? window.contentView,
                  let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else {
                let list = NSApp.windows.map { "\($0.title.isEmpty ? "(untitled)" : $0.title) visible=\($0.isVisible) \(Int($0.frame.width))x\(Int($0.frame.height))" }
                FileHandle.standardError.write(Data("snapshot: no window among \(list)\n".utf8))
                exit(1)
            }
            view.cacheDisplay(in: view.bounds, to: rep) // drawn as the key window (see below)
            try? rep.representation(using: .png, properties: [:])?.write(to: URL(fileURLWithPath: path))
            exit(0)
        }
    }
    #endif
}

/// Settings: where Open Bars shows up, and opening at login.
struct MacPresenceSection: View {
    @AppStorage(MacPresence.key) private var presence: MacPresence = .menuBar
    @State private var openAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        Section {
            Picker("Show Open Bars in", selection: $presence) {
                ForEach(MacPresence.allCases) { Text($0.title).tag($0) }
            }
            Toggle("Open at login", isOn: $openAtLogin)
        } header: {
            Text("Menu Bar & Dock")
        } footer: {
            Text("In the menu bar, Open Bars shows the connection and its speed, and you can connect or disconnect. Setup and settings stay in this window.")
        }
        .onChangeCompat(of: presence) { _ in MacPresence.applyDockPolicy() }
        .onChangeCompat(of: openAtLogin) { on in
            do {
                if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            } catch {
                openAtLogin = SMAppService.mainApp.status == .enabled
            }
        }
    }
}
