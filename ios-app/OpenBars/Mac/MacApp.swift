import SwiftUI

/// On a Mac the app only has Connect mode: Personal Hotspot is an iPhone
/// feature, so sharing happens on the phone. After setup it lives in the menu
/// bar (configurable in Settings); the window is for setup and settings.
@main
struct OpenBarsMacApp: App {
    @NSApplicationDelegateAdaptor(MacAppDelegate.self) private var appDelegate
    @StateObject private var connect = ConnectController()
    @AppStorage(MacPresence.key) private var presence: MacPresence = .menuBar

    var body: some Scene {
        Window("Open Bars", id: "main") {
            ClientView()
                .environmentObject(connect)
                .frame(minWidth: 460, minHeight: 560)
                .appAppearance()
        }
        .defaultLaunchBehavior(MacPresence.showWindowAtLaunch ? .presented : .suppressed)
        .commands {
            CommandGroup(replacing: .appSettings) {
                SettingsCommand(connect: connect)
            }
            CommandGroup(after: .windowList) {
                OpenLogCommand()
            }
        }

        // Separate window so the log can stay open next to the main one.
        Window("Connect Log", id: "connect-log") {
            LogBookView(book: .connect)
                .frame(minWidth: 520, minHeight: 320)
                .appAppearance()
        }
        .defaultSize(width: 720, height: 440)

        MenuBarExtra(isInserted: Binding(
            get: { presence.showsMenuBar },
            set: { if !$0 { presence = .dock } } // removed from the menu bar by hand
        )) {
            MenuBarView()
                .environmentObject(connect)
                .appAppearance()
        } label: {
            MenuBarIcon(connect: connect)
        }
        .menuBarExtraStyle(.window)
    }
}

/// Open Bars › Settings… (⌘,): the main window's Settings.
private struct SettingsCommand: View {
    @ObservedObject var connect: ConnectController
    @Environment(\.openWindow) private var openWindow
    var body: some View {
        Button("Settings…") {
            connect.showSettings = true
            showWindow("main", openWindow: openWindow)
        }
        .keyboardShortcut(",", modifiers: .command)
    }
}

/// Window › Show Connect Log (⌘L).
private struct OpenLogCommand: View {
    @Environment(\.openWindow) private var openWindow
    var body: some View {
        Button("Show Connect Log") { showWindow("connect-log", openWindow: openWindow) }
            .keyboardShortcut("l", modifiers: .command)
    }
}
