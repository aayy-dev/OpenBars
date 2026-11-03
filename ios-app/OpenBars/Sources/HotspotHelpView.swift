import SwiftUI

/// The bundled, signed .shortcut files (import by sharing to Shortcuts).
enum HotspotShortcutFiles {
    /// Set Personal Hotspot On (used by the in-app toggle).
    static var hotspotOnly: URL? { Bundle.main.url(forResource: "Open Bars Hotspot", withExtension: "shortcut") }
    /// Set Personal Hotspot On + Start Sharing (for Control Center, the
    /// Action button and automations).
    static var hotspotAndShare: URL? { Bundle.main.url(forResource: "Share with Open Bars", withExtension: "shortcut") }
}

/// Every way to get Personal Hotspot on together with sharing, since apps
/// can't switch the hotspot on themselves. Pick whichever fits.
struct HotspotHelpView: View {
    var body: some View {
        List {
            Section {
                Text("iOS doesn't let apps switch on Personal Hotspot. These are the ways to do it with Open Bars, from no setup to fully automatic.")
                    .font(.subheadline).foregroundStyle(.secondary)
            }

            way(
                "One small Control Center button", symbol: "square.grid.3x3.square",
                steps: [
                    "Get the “Open Bars Hotspot” shortcut (Settings › Personal Hotspot).",
                    "Control Center: tap +, Add a Control, then Open Bars + Hotspot.",
                    "It's highlighted while sharing. Tap it to turn the hotspot and sharing on together, or off together: it opens Open Bars, which hops to Shortcuts for a moment and back.",
                ],
                footer: "Controls can't run Shortcuts actions by themselves, so this one goes through the app. It also works on the Lock Screen and the Action button.")

            way(
                "Two taps in Control Center, no app", symbol: "switch.2",
                steps: [
                    "Open Control Center, tap +, then Add a Control.",
                    "Add Personal Hotspot (under Connectivity) and Open Bars Sharing.",
                    "Tap both when you want to share. Open Bars Sharing also shows whether it's on.",
                ])

            Section {
                stepList([
                    "Get the shortcut below, then tap Add Shortcut in Shortcuts.",
                    "Control Center: tap +, Add a Control, Shortcut, then choose “Share with Open Bars”.",
                    "Or Action button: Settings › Action Button › Shortcut › “Share with Open Bars”.",
                ])
                if let url = HotspotShortcutFiles.hotspotAndShare {
                    ShareLink(item: url) { Label("Get “Share with Open Bars”", systemImage: "square.and.arrow.down") }
                }
            } header: {
                Label("One tap: hotspot and sharing", systemImage: "bolt.circle")
            } footer: {
                Text("The shortcut turns on Personal Hotspot and starts sharing in the background, without opening any app.")
            }

            way(
                "Whenever you open Open Bars", symbol: "app.badge",
                steps: [
                    "Shortcuts › Automation › + › App.",
                    "Choose Open Bars, Is Opened, Run Immediately.",
                    "Add the action Set Personal Hotspot, set to On.",
                    "In Open Bars Settings, turn on “Start when app opens”.",
                ],
                footer: "No switching apps. Or use “Turn on Personal Hotspot when starting” in Settings, which runs a shortcut from inside Open Bars.")

            way(
                "With Siri", symbol: "mic",
                steps: [
                    "“Turn on Personal Hotspot”, then “Start sharing with Open Bars”.",
                    "To stop: “Stop sharing with Open Bars”.",
                ])

            way(
                "On the Home Screen", symbol: "square.grid.2x2",
                steps: [
                    "Add the Open Bars widget to turn sharing on or off.",
                    "On iOS 27, the Open Bars + Hotspot widget adds a second button: edit the widget and pick Set Personal Hotspot (or the “Share with Open Bars” shortcut).",
                ])

            way(
                "Automatically, on a trigger", symbol: "car",
                steps: [
                    "Shortcuts › Automation › + › CarPlay, Bluetooth, Wi‑Fi, Arrive and more.",
                    "Add Set Personal Hotspot (On) and Open Bars › Start Sharing, and choose Run Immediately.",
                ])

            way(
                "Macs and iPads on your Apple Account", symbol: "laptopcomputer.and.iphone",
                steps: [
                    "Pick this iPhone in their Wi‑Fi menu: it turns on the hotspot by itself (Instant Hotspot), even with Allow Others to Join off.",
                    "Keep “Keep sharing in the background” on here, and their Open Bars VPN connects by itself.",
                ],
                footer: "Works for the same Apple Account or your Family Sharing group. Other devices need Personal Hotspot on with Allow Others to Join.")
        }
        .navigationTitle("Turning On the Hotspot")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func way(_ title: String, symbol: String, steps: [String], footer: String? = nil) -> some View {
        Section {
            stepList(steps)
        } header: {
            Label(title, systemImage: symbol)
        } footer: {
            if let footer { Text(footer) }
        }
    }

    private func stepList(_ steps: [String]) -> some View {
        ForEach(Array(steps.enumerated()), id: \.offset) { i, step in
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("\(i + 1)").font(.caption.weight(.bold)).foregroundStyle(.white)
                    .frame(width: 20, height: 20).background(Circle().fill(Color.green))
                Text(step).font(.subheadline)
            }
        }
    }
}
