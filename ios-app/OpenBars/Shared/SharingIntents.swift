import AppIntents

// Actions for Shortcuts, Siri, Spotlight, the Action button, Control Center
// and widgets. Sharing started from these runs in the background.

@available(iOS 16.0, *)
struct StartSharingIntent: AppIntent {
    static let title: LocalizedStringResource = "Start Sharing"
    static let description = IntentDescription(
        "Share your cellular connection with your other devices through Open Bars, in the background. Turn on Personal Hotspot too (Shortcuts: Set Personal Hotspot).")

    #if WIDGET_EXTENSION
    static let isDiscoverable = false // the app's copy is the one in Shortcuts
    #endif

    // No dialog: from Control Center or a shortcut it would show up as a
    // notification. The "Share with Open Bars" shortcut opens the app instead.
    func perform() async throws -> some IntentResult {
        try await SharingActions.start()
        return .result()
    }
}

@available(iOS 16.0, *)
struct StopSharingIntent: AppIntent {
    static let title: LocalizedStringResource = "Stop Sharing"
    static let description = IntentDescription("Stop sharing your cellular connection through Open Bars.")

    #if WIDGET_EXTENSION
    static let isDiscoverable = false // the app's copy is the one in Shortcuts
    #endif

    func perform() async throws -> some IntentResult {
        await SharingActions.stop()
        return .result()
    }
}

/// On/off for the Control Center control and the widget toggle.
@available(iOS 18.0, *)
struct SetSharingIntent: SetValueIntent {
    static let title: LocalizedStringResource = "Set Open Bars Sharing"
    static let description = IntentDescription("Turn Open Bars sharing on or off.")

    #if WIDGET_EXTENSION
    static let isDiscoverable = false // the app's copy is the one in Shortcuts
    #endif

    @Parameter(title: "Sharing")
    var value: Bool

    init() {}

    func perform() async throws -> some IntentResult {
        if value { try await SharingActions.start() } else { await SharingActions.stop() }
        return .result()
    }
}

/// Opens Open Bars, which turns on Personal Hotspot with the user's
/// hotspot shortcut and starts sharing. For a 1×1 control: controls can't
/// run Shortcuts actions themselves, but the app can.
@available(iOS 16.0, *)
struct ShareWithHotspotIntent: AppIntent {
    static let title: LocalizedStringResource = "Share with Hotspot"
    static let description = IntentDescription(
        "Opens Open Bars, turns on Personal Hotspot with your “Open Bars Hotspot” shortcut, and starts sharing.")
    static let openAppWhenRun = true

    #if WIDGET_EXTENSION
    static let isDiscoverable = false // the app's copy is the one in Shortcuts
    #endif

    func perform() async throws -> some IntentResult {
        // Picked up by the app as soon as it's on screen, whichever process
        // runs this.
        SharedState.requestHotspotAndSharing(on: true)
        return .result()
    }
}

/// The “Open Bars + Hotspot” toggle control: highlighted while sharing.
/// Switching it on turns on Personal Hotspot and sharing; off turns both off.
/// It opens Open Bars, which runs the hotspot shortcut (only Shortcuts can
/// switch Personal Hotspot).
@available(iOS 18.0, *)
struct HotspotAndSharingIntent: SetValueIntent {
    static let title: LocalizedStringResource = "Open Bars + Hotspot"
    static let description = IntentDescription("Turns Personal Hotspot and sharing on or off together. Opens Open Bars.")
    static let openAppWhenRun = true
    static let isDiscoverable = false // control-only; Shortcuts has the other actions

    @Parameter(title: "On")
    var value: Bool

    init() {}

    func perform() async throws -> some IntentResult {
        // Picked up by the app as soon as it's on screen.
        SharedState.requestHotspotAndSharing(on: value)
        return .result()
    }
}
