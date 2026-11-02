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
        .onChange(of: presence) { _, _ in MacPresence.applyDockPolicy() }
        .onChange(of: openAtLogin) { _, on in
            do {
                if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            } catch {
                openAtLogin = SMAppService.mainApp.status == .enabled
            }
        }
    }
}
