import Foundation
import NetworkExtension

/// Starts and stops sharing without the app's screens: used by the App
/// Intents behind Shortcuts, Siri, Control Center and the widgets, in
/// whichever process runs them. Sharing started this way always runs in the
/// background (the OpenBarsTunnel extension), since the app isn't on screen.
@MainActor
enum SharingActions {
    /// Whether sharing is running: background tunnel up, or the in-app
    /// engine (when called in the app's process).
    static func isSharing() async -> Bool {
        if InAppEngine.isRunning() { return true }
        let t = TunnelController()
        await t.load()
        if t.installed { return t.isActive }
        return SharedState.isSharing // couldn't read VPN settings from here
    }

    static func start() async throws {
        if InAppEngine.isRunning() { return } // already sharing in the app
        guard let config = SharedState.backgroundConfig else { throw SharingError.notSetUp }
        let t = TunnelController()
        try await t.install(config: config, onDemand: true)
        try t.start()
        SharedState.setSharing(true)
    }

    static func stop() async {
        InAppEngine.stop()
        let t = TunnelController()
        await t.load()
        if t.installed {
            try? await t.setOnDemand(false)
            t.stop()
        }
        SharedState.setSharing(false)
    }
}

enum SharingError: LocalizedError {
    case notSetUp
    var errorDescription: String? {
        "Open Open Bars once to set it up, then try again."
    }
}

/// Hooks into the in-app engine. The app sets these in its own process;
/// elsewhere (extensions) they do nothing.
@MainActor
enum InAppEngine {
    static var isRunning: () -> Bool = { false }
    static var stop: () -> Void = {}
}
