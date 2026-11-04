import AppIntents

/// Open Bars' actions in Shortcuts, Siri and Spotlight, with no setup.
@available(iOS 16.0, *) // Siri and Shortcuts (App Intents) are iOS 16+
struct OpenBarsShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: StartSharingIntent(),
            phrases: [
                "Start sharing with \(.applicationName)",
                "Share my connection with \(.applicationName)",
            ],
            shortTitle: "Start Sharing",
            systemImageName: "personalhotspot"
        )
        AppShortcut(
            intent: ShareWithHotspotIntent(),
            phrases: ["Share with hotspot in \(.applicationName)"],
            shortTitle: "Share with Hotspot",
            systemImageName: "personalhotspot"
        )
        AppShortcut(
            intent: StopSharingIntent(),
            phrases: ["Stop sharing with \(.applicationName)"],
            shortTitle: "Stop Sharing",
            systemImageName: "xmark.circle"
        )
    }
}
