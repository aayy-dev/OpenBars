import AppIntents
import SwiftUI
import WidgetKit

@main
struct OpenBarsWidgets: WidgetBundle {
    var body: some Widget {
        SharingControl()
        HotspotAndSharingControl()
        SharingWidget()
        if #available(iOS 27.0, *) {
            HotspotAndSharingWidget()
        }
    }
}

// MARK: - Control Center / Lock Screen / Action button

/// One tap to start or stop sharing. Sits next to Apple's own Personal
/// Hotspot control (Control Center › Connectivity).
struct SharingControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: SharedState.controlKind, provider: SharingValueProvider()) { isOn in
            ControlWidgetToggle("Open Bars", isOn: isOn, action: SetSharingIntent()) { on in
                Label(on ? "Sharing" : "Off", systemImage: "cellularbars")
            }
            .tint(.green)
        }
        .displayName("Open Bars Sharing")
        .description("Start or stop sharing this iPhone's connection with your devices.")
    }
}

/// Highlighted while sharing. Switching it on turns on Personal Hotspot and
/// sharing together; off turns both off. Opens Open Bars, which runs the
/// hotspot shortcut (a quick hop to Shortcuts and back). Keeps the kind of
/// the earlier “Choose” control so a placed control carries over.
struct HotspotAndSharingControl: ControlWidget {
    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: SharedState.controlKind + ".menu", provider: SharingValueProvider()) { isOn in
            ControlWidgetToggle("Open Bars", isOn: isOn, action: HotspotAndSharingIntent()) { on in
                Label(on ? "Hotspot + Sharing" : "Off", systemImage: "personalhotspot")
            }
            .tint(.green)
        }
        .displayName("Open Bars + Hotspot")
        .description("Turns Personal Hotspot and sharing on or off together (uses your “Open Bars Hotspot” shortcut; opens Open Bars). Highlighted while sharing.")
    }
}

struct SharingValueProvider: ControlValueProvider {
    var previewValue: Bool { false }
    func currentValue() async throws -> Bool { SharedState.isSharing }
}

// MARK: - Home Screen widget

struct SharingEntry: TimelineEntry {
    var date: Date
    var sharing: Bool
    var online: Int
}

struct SharingProvider: TimelineProvider {
    func placeholder(in context: Context) -> SharingEntry { SharingEntry(date: .now, sharing: true, online: 1) }
    func getSnapshot(in context: Context, completion: @escaping (SharingEntry) -> Void) { completion(current()) }
    func getTimeline(in context: Context, completion: @escaping (Timeline<SharingEntry>) -> Void) {
        completion(Timeline(entries: [current()], policy: .after(.now.addingTimeInterval(15 * 60))))
    }

    private func current() -> SharingEntry {
        SharingEntry(date: .now, sharing: SharedState.isSharing, online: SharedState.summary.online)
    }
}

struct SharingWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: SharedState.widgetKind, provider: SharingProvider()) { entry in
            SharingWidgetView(sharing: entry.sharing, online: entry.online)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Open Bars")
        .description("See whether this iPhone is sharing, and turn sharing on or off.")
        .supportedFamilies([.systemSmall])
    }
}

struct SharingWidgetView: View {
    var sharing: Bool
    var online: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: "cellularbars", variableValue: sharing ? 1 : 0)
                    .font(.title2)
                    .foregroundStyle(sharing ? .green : .secondary)
                Spacer()
                Toggle(isOn: sharing, intent: SetSharingIntent()) { EmptyView() }
                    .labelsHidden()
                    .tint(.green)
            }
            Spacer(minLength: 0)
            Text("Open Bars").font(.headline)
            Text(statusLine).font(.caption).foregroundStyle(.secondary)
        }
    }

    private var statusLine: String {
        guard sharing else { return "Not sharing" }
        return online == 0 ? "Sharing · no devices yet" : "Sharing · \(online) online"
    }
}

// MARK: - iOS 27: widget with a hotspot button

/// Apps can't turn on Personal Hotspot, but on iOS 27 a widget button can run
/// an action the person picks when editing the widget: for example
/// Shortcuts' Set Personal Hotspot, or their "Share with Open Bars" shortcut.
@available(iOS 27.0, *)
struct HotspotButtonConfiguration: WidgetConfigurationIntent {
    static let title: LocalizedStringResource = "Hotspot Button"
    static let description = IntentDescription(
        "Choose what the left button runs: for example Set Personal Hotspot, or a shortcut that turns on the hotspot and starts sharing.")

    @Parameter(title: "Button")
    var shortcut: SystemShortcut?
}

@available(iOS 27.0, *)
struct HotspotEntry: TimelineEntry {
    var date: Date
    var sharing: Bool
    var online: Int
    var configuration: HotspotButtonConfiguration
}

@available(iOS 27.0, *)
struct HotspotProvider: AppIntentTimelineProvider {
    func placeholder(in context: Context) -> HotspotEntry {
        HotspotEntry(date: .now, sharing: true, online: 1, configuration: HotspotButtonConfiguration())
    }
    func snapshot(for configuration: HotspotButtonConfiguration, in context: Context) async -> HotspotEntry {
        entry(configuration)
    }
    func timeline(for configuration: HotspotButtonConfiguration, in context: Context) async -> Timeline<HotspotEntry> {
        Timeline(entries: [entry(configuration)], policy: .after(.now.addingTimeInterval(15 * 60)))
    }
    private func entry(_ c: HotspotButtonConfiguration) -> HotspotEntry {
        HotspotEntry(date: .now, sharing: SharedState.isSharing, online: SharedState.summary.online, configuration: c)
    }
}

@available(iOS 27.0, *)
struct HotspotAndSharingWidget: Widget {
    var body: some WidgetConfiguration {
        AppIntentConfiguration(kind: SharedState.widgetKind + ".hotspot", intent: HotspotButtonConfiguration.self,
                               provider: HotspotProvider()) { entry in
            HStack(spacing: 12) {
                if let shortcut = entry.configuration.shortcut {
                    Button(intent: RunSystemShortcutIntent(shortcut: shortcut)) {
                        VStack(spacing: 6) {
                            Image(systemName: "personalhotspot").font(.title2)
                            Text(shortcut.displayRepresentation.title)
                                .font(.caption2).lineLimit(2).multilineTextAlignment(.center)
                        }
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                    .buttonStyle(.plain)
                } else {
                    VStack(spacing: 6) {
                        Image(systemName: "personalhotspot").font(.title2).foregroundStyle(.secondary)
                        Text("Edit the widget to choose Set Personal Hotspot")
                            .font(.caption2).foregroundStyle(.secondary).multilineTextAlignment(.center)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
                Divider()
                SharingWidgetView(sharing: entry.sharing, online: entry.online)
            }
            .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Open Bars + Hotspot")
        .description("Turn on Personal Hotspot (pick the action when editing the widget) and share with Open Bars.")
        .supportedFamilies([.systemMedium])
    }
}
