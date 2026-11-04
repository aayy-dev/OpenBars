import Foundation
import WidgetKit

/// State shared through the app group by the app, the background-sharing
/// extension, and the widgets and Control Center control.
enum SharedState {
    static let group = "group.dev.aayy.openbars"
    /// Kind of the Control Center / Lock Screen control.
    static let controlKind = "dev.aayy.openbars.sharing-control"
    /// Kind of the Home Screen widgets.
    static let widgetKind = "dev.aayy.openbars.sharing-widget"

    static var defaults: UserDefaults { UserDefaults(suiteName: group) ?? .standard }

    /// Engine config for background sharing, written by the app whenever
    /// devices or settings change, so Shortcuts and controls can start
    /// sharing without opening the app.
    static var backgroundConfig: String? {
        get { defaults.string(forKey: "backgroundConfig") }
        set { defaults.set(newValue, forKey: "backgroundConfig") }
    }

    /// Whether sharing is on (in the app or in the background).
    static var isSharing: Bool { defaults.bool(forKey: "sharing") }

    static func setSharing(_ on: Bool) {
        if defaults.object(forKey: "sharing") as? Bool == on { return }
        defaults.set(on, forKey: "sharing")
        reloadWidgets()
    }

    /// Devices online and the current download speed, for the widget.
    static var summary: (online: Int, down: Int64) {
        (defaults.integer(forKey: "online"), Int64(defaults.integer(forKey: "down")))
    }

    static func setSummary(online: Int, down: Int64) {
        let changed = defaults.integer(forKey: "online") != online
        defaults.set(online, forKey: "online")
        defaults.set(Int(down), forKey: "down")
        if changed { WidgetCenter.shared.reloadTimelines(ofKind: widgetKind) }
    }

    /// The “Open Bars + Hotspot” control (and Share with Hotspot) asks the
    /// app to turn Personal Hotspot and sharing on or off together; the app
    /// takes the request when it comes on screen.
    static func requestHotspotAndSharing(on: Bool) {
        defaults.set(Date().timeIntervalSince1970, forKey: "hotspotRequestTime")
        defaults.set(on, forKey: "hotspotRequestOn")
    }

    /// The pending request (true = on, false = off) if made in the last
    /// minute; nil otherwise. Taking it clears it.
    static func takeHotspotAndSharingRequest() -> Bool? {
        let t = defaults.double(forKey: "hotspotRequestTime")
        guard t > 0 else { return nil }
        defaults.removeObject(forKey: "hotspotRequestTime")
        guard Date().timeIntervalSince1970 - t < 60 else { return nil }
        return defaults.bool(forKey: "hotspotRequestOn")
    }

    // MARK: Notifications (read by the background-sharing extension)

    /// Which notifications are on, written by the app from its settings.
    static var notificationPrefs: NotificationPrefs {
        get {
            defaults.data(forKey: "notificationPrefs").flatMap { try? JSONDecoder().decode(NotificationPrefs.self, from: $0) }
                ?? NotificationPrefs()
        }
        set { defaults.set(try? JSONEncoder().encode(newValue), forKey: "notificationPrefs") }
    }

    /// Some device or the all-devices cap has a limit: then a pairing
    /// notification's Allow opens the app to choose the new device's limit.
    static var limitsInUse: Bool {
        get { defaults.bool(forKey: "limitsInUse") }
        set { defaults.set(newValue, forKey: "limitsInUse") }
    }

    /// When background sharing last stopped without being asked to (unix
    /// seconds; 0 = it didn't), so a restart soon after can say it's back.
    static var unexpectedStop: Double {
        get { defaults.double(forKey: "unexpectedStop") }
        set { defaults.set(newValue, forKey: "unexpectedStop") }
    }

    /// Background sharing's last sign of life (unix seconds), written by
    /// its Watchdog; 0 once it stops the normal way.
    static var watchdogAlive: Double {
        get { defaults.double(forKey: "watchdogAlive") }
        set { defaults.set(newValue, forKey: "watchdogAlive") }
    }

    /// When the Watchdog's "Sharing stopped" notice shows if nothing pushes
    /// it back (unix seconds; 0 = none scheduled).
    static var watchdogDue: Double {
        get { defaults.double(forKey: "watchdogDue") }
        set { defaults.set(newValue, forKey: "watchdogDue") }
    }

    static func reloadWidgets() {
        if #available(iOS 18.0, *) {
            ControlCenter.shared.reloadControls(ofKind: controlKind)
            ControlCenter.shared.reloadControls(ofKind: controlKind + ".menu")
        }
        WidgetCenter.shared.reloadTimelines(ofKind: widgetKind)
    }
}
