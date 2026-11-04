import SwiftUI

/// On a Mac the app only has Connect mode: Personal Hotspot is an iPhone
/// feature, so sharing happens on the phone. After setup it lives in the menu
/// bar (configurable in Settings); the window is for setup and settings.
@main
enum OpenBarsMacMain {
    /// macOS 15 can start with the window closed (defaultLaunchBehavior). On
    /// macOS 13 and 14 the app delegate closes it right after launch instead.
    static func main() {
        if #available(macOS 15.0, *) {
            OpenBarsMacApp15.main()
        } else {
            OpenBarsMacApp.main()
        }
    }
}

/// macOS 13 and 14.
struct OpenBarsMacApp: App {
    @NSApplicationDelegateAdaptor(MacAppDelegate.self) private var appDelegate
    @StateObject private var connect = ConnectController()
    @AppStorage(MacPresence.key) private var presence: MacPresence = .menuBar

    var body: some Scene {
        MacScenes.mainWindow(connect)
        MacScenes.logWindow()
        MacScenes.menuBar(connect, presence: $presence)
    }
}

/// macOS 15 and later: after setup, the window stays closed at launch when
/// Open Bars lives in the menu bar.
@available(macOS 15.0, *)
struct OpenBarsMacApp15: App {
    @NSApplicationDelegateAdaptor(MacAppDelegate.self) private var appDelegate
    @StateObject private var connect = ConnectController()
    @AppStorage(MacPresence.key) private var presence: MacPresence = .menuBar

    var body: some Scene {
        MacScenes.mainWindow(connect)
            .defaultLaunchBehavior(MacPresence.showWindowAtLaunch ? .presented : .suppressed)
        MacScenes.logWindow()
        MacScenes.menuBar(connect, presence: $presence)
    }
}

/// The scenes both app versions share.
@MainActor
enum MacScenes {
    static func mainWindow(_ connect: ConnectController) -> some Scene {
        Window("Open Bars", id: "main") {
            ClientView()
                .environmentObject(connect)
                .frame(minWidth: 460, minHeight: 560)
                .appAppearance()
        }
        .commands {
            CommandGroup(replacing: .appSettings) {
                SettingsCommand(connect: connect)
            }
            CommandGroup(after: .windowList) {
                OpenLogCommand()
            }
        }
    }

    static func logWindow() -> some Scene {
        // Separate window so the log can stay open next to the main one.
        Window("Connect Log", id: "connect-log") {
            LogBookView(book: .connect)
                .frame(minWidth: 520, minHeight: 320)
                .appAppearance()
        }
        .defaultSize(width: 720, height: 440)
    }

    static func menuBar(_ connect: ConnectController, presence: Binding<MacPresence>) -> some Scene {
        MenuBarExtra(isInserted: Binding(
            get: { presence.wrappedValue.showsMenuBar },
            set: { if !$0 { presence.wrappedValue = .dock } } // removed from the menu bar by hand
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
