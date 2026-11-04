import Foundation
import NetworkExtension
import SwiftUI
import UIKit
import WGCore

// MARK: - Saved state (openbars.json)
//
// Encoded with a plain JSONEncoder, so property names are the JSON keys.
// Existing installs have files in this format: don't rename or reorder.

/// A device that may use this iPhone's connection: one WireGuard peer.
struct Device: Codable, Identifiable, Hashable {
    var id = UUID()
    var name: String
    /// Empty for devices paired from Open Bars: their key never leaves them.
    var privateKey: String
    var publicKey: String
    /// Tunnel address, 10.66.66.N.
    var address: String
    /// DeviceKind raw value, for the icon ("mac", "laptop", ...).
    var kind: String? = nil
    /// Data limits; nil = none.
    var limit: DeviceLimit? = nil
    /// WireGuard-app devices: the config page offers the .conf once, until
    /// this time (unix seconds).
    var confUntil: Int64? = nil
    /// When it was removed (only set on records kept in removedDevices).
    var removedAt: Int64? = nil

    /// Set up with the WireGuard app (QR/.conf) rather than paired from Open Bars.
    var usesWireGuardApp: Bool { !privateKey.isEmpty }
    var deviceKind: DeviceKind {
        kind.flatMap(DeviceKind.init(rawValue:)) ?? (usesWireGuardApp ? .other : .mac)
    }
}

/// Share settings, edited as a whole in Settings.
struct AppSettings: Codable, Equatable {
    /// WireGuard's UDP port.
    var listenPort: Int = 51820
    /// Send devices' traffic out over cellular data only.
    var bindCellular: Bool = true
    /// Upstream DNS servers, separated by commas or spaces.
    var dns: String = "1.1.1.1, 8.8.8.8"
    /// Serve the config page (which also carries the pairing API) on webPort.
    var webPageEnabled: Bool = true
    var webPort: Int = 8080
    /// Start sharing whenever the app comes on screen.
    var autoStart: Bool = false
    /// Run the engine in the OpenBarsTunnel Network Extension so it keeps
    /// going when the phone locks (instead of only while the app is open).
    var backgroundSharing: Bool = false
    /// The hotspot's Wi-Fi name (this iPhone's name), given to paired devices.
    var hotspotName: String = ""
    /// Run a shortcut that turns on Personal Hotspot when sharing starts from
    /// the app (apps can't switch it on; Shortcuts can).
    var hotspotShortcut: Bool = false
    var hotspotShortcutName: String = "Open Bars Hotspot"

    // Data limits (Settings › Data Limits).
    /// What happens when a device reaches a limit, unless it has its own.
    var limitAction: LimitAction = .block
    /// The speed for "Slow down", in kbps.
    var slowKbps: Int = 1000
    /// Limits a newly allowed device starts with (empty = no limit).
    var newDeviceRules: [LimitRule] = []
    /// Limits for all devices together.
    var capRules: [LimitRule] = []
    /// What happens when all devices together reach a limit.
    var capAction: LimitAction = .block
    /// Day of the month monthly limits start over (your billing day).
    var billingDay: Int = 1
    /// Which notifications to post.
    var notifications = NotificationPrefs()

    /// The settings that change what the engine does when it starts (the
    /// rest apply without restarting sharing).
    var restartRelevant: AppSettings {
        var s = self
        let d = AppSettings()
        s.limitAction = d.limitAction
        s.slowKbps = d.slowKbps
        s.newDeviceRules = d.newDeviceRules
        s.capRules = d.capRules
        s.capAction = d.capAction
        s.billingDay = d.billingDay
        s.notifications = d.notifications
        return s
    }
}

// Decode field by field so settings saved by older versions (missing the
// newer keys) still load instead of resetting the whole app state.
extension AppSettings {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = AppSettings()
        listenPort = try c.decodeIfPresent(Int.self, forKey: .listenPort) ?? d.listenPort
        bindCellular = try c.decodeIfPresent(Bool.self, forKey: .bindCellular) ?? d.bindCellular
        dns = try c.decodeIfPresent(String.self, forKey: .dns) ?? d.dns
        webPageEnabled = try c.decodeIfPresent(Bool.self, forKey: .webPageEnabled) ?? d.webPageEnabled
        webPort = try c.decodeIfPresent(Int.self, forKey: .webPort) ?? d.webPort
        autoStart = try c.decodeIfPresent(Bool.self, forKey: .autoStart) ?? d.autoStart
        backgroundSharing = try c.decodeIfPresent(Bool.self, forKey: .backgroundSharing) ?? d.backgroundSharing
        hotspotName = try c.decodeIfPresent(String.self, forKey: .hotspotName) ?? d.hotspotName
        hotspotShortcut = try c.decodeIfPresent(Bool.self, forKey: .hotspotShortcut) ?? d.hotspotShortcut
        hotspotShortcutName = try c.decodeIfPresent(String.self, forKey: .hotspotShortcutName) ?? d.hotspotShortcutName
        limitAction = try c.decodeIfPresent(LimitAction.self, forKey: .limitAction) ?? d.limitAction
        slowKbps = try c.decodeIfPresent(Int.self, forKey: .slowKbps) ?? d.slowKbps
        newDeviceRules = try c.decodeIfPresent([LimitRule].self, forKey: .newDeviceRules) ?? d.newDeviceRules
        capRules = try c.decodeIfPresent([LimitRule].self, forKey: .capRules) ?? d.capRules
        capAction = try c.decodeIfPresent(LimitAction.self, forKey: .capAction) ?? d.capAction
        billingDay = try c.decodeIfPresent(Int.self, forKey: .billingDay) ?? d.billingDay
        notifications = try c.decodeIfPresent(NotificationPrefs.self, forKey: .notifications) ?? d.notifications
    }
}

/// Everything in openbars.json.
struct PersistedState: Codable {
    /// This iPhone's WireGuard key (base64).
    var serverPrivateKey: String
    var devices: [Device] = []
    var settings = AppSettings()
    /// Removed devices, kept so their limits and usage carry on if they
    /// pair again (newest last). Optional: older files don't have it.
    var removedDevices: [Device]? = nil
}

// MARK: - Engine status (StatusJSON, decoded with .convertFromSnakeCase)

/// Which interfaces the phone has right now.
struct NetInfo: Codable {
    var hotspotIp: String = ""
    var cellularIf: String = ""
    var cellularIp: String = ""
    var wifiIp: String = ""
    var vpnActive: Bool = false
    var vpnIf: String = ""
    var defaultIf: String = ""
}

/// One device as WireGuard sees it.
struct PeerStatus: Codable {
    var name: String = ""
    var publicKey: String = ""
    var address: String = ""
    var endpoint: String = ""
    /// Unix seconds, 0 = never.
    var lastHandshake: Int64 = 0
    var rxBytes: Int64 = 0
    var txBytes: Int64 = 0
    /// When the device last sent anything (unix seconds; kept across restarts).
    var lastSeen: Int64?
    /// Its data limits as of now.
    var limit: LimitStatus? = nil
}

/// The engine's report: whether it runs, counters, devices and pairing requests.
struct CoreStatus: Codable {
    var running: Bool = false
    var listenPort: Int = 0
    var net: NetInfo = NetInfo()
    var egressIf: String = ""
    var tcpActive: Int64 = 0
    var tcpTotal: Int64 = 0
    var udpFlows: Int = 0
    var bytesUp: Int64 = 0
    var bytesDown: Int64 = 0
    var uptime: Int64 = 0
    /// Why the config page couldn't start, if it couldn't.
    var webError: String = ""
    var peers: [PeerStatus]? = nil
    var pairing: [PairRequest]? = nil
    /// The all-devices limit as of now.
    var cap: [RuleStatus]? = nil
    /// Devices' unanswered requests for more time or data.
    var asks: [AskRequest]? = nil
}

/// Limits worked out from saved usage while sharing is off (ReadLimitsJSON).
struct OfflineLimits: Codable {
    var peers: [String: LimitStatus]? = nil
    var cap: [RuleStatus]? = nil
}

// MARK: - Store

/// The Share tab's model: the saved devices and settings, the engine that
/// serves them (in the app, or in the OpenBarsTunnel extension in background
/// mode) and what it reports, polled every 2 seconds.
@MainActor
final class Store: ObservableObject {
    /// The one Store: the app's screens and notification buttons share it.
    static let shared = Store()

    @Published var state: PersistedState
    @Published var status = CoreStatus()
    /// Sharing is on: the in-app engine runs, or the background tunnel is up.
    @Published var running = false
    /// A start is under way.
    @Published var busy = false
    /// Shown as a banner on the Share tab.
    @Published var lastError: String?
    /// Per-second traffic for the last 5 minutes (from the engine).
    @Published private(set) var samples: [TrafficSample] = []
    /// Hourly (7 days) and daily usage, saved by the engine.
    @Published private(set) var usage: UsageSummary?
    /// Limits from saved usage, while sharing is off.
    @Published private(set) var offlineLimits: OfflineLimits?
    /// The last device list push to the background engine (notification
    /// buttons wait for it before the app is suspended again).
    var lastPush: Task<Void, Never>?
    private var usageFetched = Date.distantPast
    /// Pairing requests already answered here (the engine drops them a
    /// moment later; this keeps the prompt from showing twice).
    @Published private var answeredPairings: Set<String> = []

    let tunnel = TunnelController()
    /// True when the engine runs in the Network Extension (background mode).
    @Published private(set) var runningInBackground = false

    /// "Start when app opens" waits until the app is actually on screen, so a
    /// launch in the background (Shortcuts, Control Center) doesn't start the
    /// in-app engine.
    private var autoStartPending = false
    /// The background tunnel's state has been read once (so we know whether
    /// sharing is already running before auto-starting).
    private var tunnelLoaded = false
    /// Right after Stop the tunnel can still look connected for a moment;
    /// don't tell the control it's on again meanwhile.
    private var stoppingUntil: Date?

    private var pollTimer: Timer?
    /// Published on the first start: it's what makes iOS ask for Local
    /// Network access, which hotspot devices need to reach this iPhone.
    private var bonjour: NetService?
    /// The extra time iOS gives sharing in the app after Open Bars leaves
    /// the screen (a standard background task).
    private var graceTask: UIBackgroundTaskIdentifier = .invalid
    /// When the scheduled "Sharing paused" notification shows.
    private var pausedNoticeAt: Date?
    /// iOS paused Open Bars since it left the screen.
    private var pausedByIOS = false
    /// The background tunnel's status at the last refresh, to log changes.
    private var lastTunnelStatus: NEVPNStatus?

    private var log: LogBook { .share }
    /// Log rings read into the Share log: the engine in this process, and the
    /// background extension's.
    private static let appLogs = "app"
    private static let extensionLogs = "extension"

    /// Devices and settings live in Application Support/openbars.json.
    private static let fileURL: URL = {
        FileManager.appSupportFolder.appendingPathComponent("openbars.json")
    }()

    init() {
        #if DEBUG
        if DemoMode.on {
            state = PersistedState(serverPrivateKey: WgcoreGeneratePrivateKey())
            showDemo()
            return
        }
        #endif
        state = Store.loadState() ?? PersistedState(serverPrivateKey: WgcoreGeneratePrivateKey())
        // The hotspot's Wi‑Fi name is the iPhone's name, so fill it in when iOS
        // tells us (needs Apple's device-name entitlement; see deviceName).
        if state.settings.hotspotName.trimmingCharacters(in: .whitespaces).isEmpty,
           let name = Store.deviceName {
            state.settings.hotspotName = name
            save()
        }
        // Intents running in this process can see and stop the in-app engine.
        InAppEngine.isRunning = { WgcoreRunning() }
        InAppEngine.stop = { [weak self] in
            guard WgcoreRunning() else { return }
            WgcoreStop()
            UIApplication.shared.isIdleTimerDisabled = false
            self?.refresh()
        }
        publishShared()
        hookUpLog()
        #if DEBUG
        listenForDebugCommands()
        #endif
        refresh()
        pollTimer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.refresh() }
        }
        autoStartPending = state.settings.autoStart
        Task {
            await tunnel.load() // picks up background sharing left running
            tunnelLoaded = true
            refresh()
            if UIApplication.shared.applicationState == .active { appBecameActive() }
        }
    }

    /// The saved state, if there is one with a usable server key. Otherwise
    /// the app starts fresh (a valid file is never replaced).
    private static func loadState() -> PersistedState? {
        guard let data = try? Data(contentsOf: fileURL),
              let saved = try? JSONDecoder().decode(PersistedState.self, from: data),
              !WgcorePublicKey(saved.serverPrivateKey).isEmpty else { return nil }
        return saved
    }

    /// Turn on Personal Hotspot (through the hotspot shortcut) and share:
    /// the “Open Bars + Hotspot” control. Runs the shortcut exactly once.
    func shareWithHotspot() {
        if running || busy {
            runHotspotShortcut()
            return
        }
        start() // also runs the shortcut when that setting is on
        if !state.settings.hotspotShortcut { runHotspotShortcut() }
    }

    /// Stop sharing and turn Personal Hotspot off (through the hotspot
    /// shortcut, with input "off").
    func stopWithHotspot() {
        stop(reason: "turned off together with Personal Hotspot")
        runHotspotShortcut(on: false)
    }

    /// A pending “Open Bars + Hotspot” request, once we know whether
    /// background sharing is already running.
    private func takePendingRequests() {
        guard tunnelLoaded, UIApplication.shared.applicationState == .active else { return }
        if let on = SharedState.takeHotspotAndSharingRequest() {
            autoStartPending = false
            if on { shareWithHotspot() } else { stopWithHotspot() }
        }
    }

    /// Called when the app comes on screen.
    func appBecameActive() {
        endGrace()
        if pausedByIOS {
            pausedByIOS = false
            if running && !runningInBackground {
                log.add(.info, .share, "Open Bars is back on screen, so sharing in the app carries on")
            }
        }
        refresh()
        takePendingRequests()
        guard autoStartPending, tunnelLoaded else { return }
        autoStartPending = false
        if !running && !busy { start() }
    }

    /// Called when the app leaves the screen. Sharing in the app keeps going
    /// for as long as iOS allows (a standard background task, usually about
    /// 30 seconds), then pauses with the app until it's back on screen.
    /// Background sharing carries on regardless.
    func appWentToBackground() {
        guard running, !runningInBackground, graceTask == .invalid else { return }
        graceTask = UIApplication.shared.beginBackgroundTask(withName: "Sharing in the app") { [weak self] in
            // Time's up: iOS suspends Open Bars when this returns.
            self?.notePaused()
            self?.endGraceTask()
        }
        guard graceTask != .invalid else {
            notePaused()
            return
        }
        log.add(.info, .share, "Open Bars left the screen. Sharing in the app keeps going for as long as iOS allows, usually about 30 seconds")
        // Scheduled for just after the time should run out, in case iOS ends
        // Open Bars before saying so; taken back if it comes back first.
        schedulePausedNotice(after: 35)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_000_000_000)
            guard let self, self.graceTask != .invalid else { return }
            let left = UIApplication.shared.backgroundTimeRemaining
            if left < 600 { self.schedulePausedNotice(after: max(1, left + 2)) }
        }
    }

    private static let pausedTitle = "Sharing paused"
    private static let pausedBody = "iOS paused Open Bars after it left the screen, so your devices lost the connection. Open it to keep sharing, or turn on Settings › Keep sharing in the background."

    private func schedulePausedNotice(after seconds: TimeInterval) {
        guard state.settings.notifications.paused else { return }
        pausedNoticeAt = Date().addingTimeInterval(seconds)
        Notifier.postSimple(id: "paused", title: Store.pausedTitle, body: Store.pausedBody, after: seconds)
    }

    /// Sharing in the app is pausing with the app.
    private func notePaused() {
        guard !pausedByIOS else { return }
        pausedByIOS = true
        log.add(.warn, .share, "iOS paused Open Bars, so sharing in the app is paused until you come back",
                hint: "To keep sharing with the screen locked or in other apps, turn on Settings › Keep sharing in the background.")
        guard state.settings.notifications.paused else { return }
        if let at = pausedNoticeAt, at <= Date() { return } // the scheduled one has shown
        // Show it now instead of the scheduled one (posting one with the
        // same id doesn't cancel a scheduled one). The app is about to be
        // suspended, so wait briefly for iOS to take it.
        pausedNoticeAt = Date()
        Notifier.removePending(id: "paused")
        let handed = DispatchSemaphore(value: 0)
        Notifier.postSimple(id: "paused", title: Store.pausedTitle, body: Store.pausedBody) { handed.signal() }
        _ = handed.wait(timeout: .now() + 0.5)
    }

    /// Back on screen, or sharing stopped: no more background time, and no
    /// "Sharing paused" notification.
    private func endGrace() {
        endGraceTask()
        pausedNoticeAt = nil
        Notifier.remove(id: "paused")
    }

    private func endGraceTask() {
        guard graceTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(graceTask)
        graceTask = .invalid
    }

    /// Notifications: asked the first time sharing starts, when they start
    /// to matter (pairing requests, limits, sharing stopping).
    private func askForNotificationsOnce() {
        let key = "notificationsAsked"
        guard !UserDefaults.standard.bool(forKey: key) else { return }
        UserDefaults.standard.set(true, forKey: key)
        Notifier.requestAuthorization()
    }

    /// Shortcuts, widgets and the control start sharing from the config the
    /// app last published (they can't run the app's screens).
    private func publishShared() {
        SharedState.backgroundConfig = coreConfigJSON(lowMemory: true)
        SharedState.notificationPrefs = state.settings.notifications
        SharedState.limitsInUse = limitsInUse
    }

    /// Some device, new devices, or all devices together have a limit.
    var limitsInUse: Bool {
        let s = state.settings
        return !s.capRules.isEmpty || !s.newDeviceRules.isEmpty
            || state.devices.contains { !($0.limit?.rules.isEmpty ?? true) || $0.limit?.paused == true }
    }

    // MARK: Personal Hotspot via Shortcuts

    /// Runs the user's hotspot shortcut through Shortcuts, which switches
    /// apps briefly and comes back here (openbars://hotspot).
    /// on: false passes "off" as the shortcut's input, which the bundled
    /// shortcut uses to turn Personal Hotspot off instead.
    func runHotspotShortcut(on: Bool = true) {
        let name = state.settings.hotspotShortcutName.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty, var c = URLComponents(string: "shortcuts://x-callback-url/run-shortcut") else { return }
        c.queryItems = [
            URLQueryItem(name: "name", value: name),
            URLQueryItem(name: "input", value: "text"),
            URLQueryItem(name: "text", value: on ? "on" : "off"),
            URLQueryItem(name: "x-success", value: "openbars://hotspot?result=done"),
            URLQueryItem(name: "x-cancel", value: "openbars://hotspot?result=cancel"),
            URLQueryItem(name: "x-error", value: "openbars://hotspot?result=error"),
        ]
        if let url = c.url { UIApplication.shared.open(url) }
    }

    /// The name from Settings → General → About → Name, which is also the
    /// Personal Hotspot's Wi‑Fi name. On iOS 16+ UIDevice.name only returns it
    /// with the com.apple.developer.device-information.user-assigned-device-name
    /// entitlement (granted by Apple on request); without it iOS returns the
    /// generic "iPhone" and this is nil, so the user types the name instead.
    static var deviceName: String? {
        let d = UIDevice.current
        let name = d.name.trimmingCharacters(in: .whitespaces)
        let generic: Set<String> = [d.model, d.localizedModel, "iPhone", "iPad"]
        return name.isEmpty || generic.contains(name) ? nil : name
    }

    // MARK: Keys and config

    var serverPublicKey: String { WgcorePublicKey(state.serverPrivateKey) }

    /// The phone's address on the hotspot, where devices reach it.
    var endpointHost: String {
        status.net.hotspotIp.isEmpty ? "172.20.10.1" : status.net.hotspotIp
    }

    func save() {
        #if DEBUG
        if DemoMode.on { return } // samples are never saved
        #endif
        if let data = try? JSONEncoder().encode(state) {
            try? data.write(to: Store.fileURL, options: .atomic)
        }
        publishShared()
    }

    /// A device as the engine's PeerConfig.
    private static func peerConfig(_ d: Device) -> [String: Any] {
        var p: [String: Any] = ["name": d.name, "public_key": d.publicKey, "private_key": d.privateKey, "address": d.address]
        if let l = d.limit, !l.isEmpty { p["limit"] = limitConfig(l) }
        if let c = d.confUntil { p["conf_until"] = c }
        return p
    }

    private static func ruleConfig(_ r: LimitRule) -> [String: Any] {
        var d: [String: Any] = ["period": r.period.rawValue, "bytes": r.bytes]
        if let s = r.start { d["start"] = s }
        return d
    }

    /// A device's limits as the engine's DeviceLimit.
    private static func limitConfig(_ l: DeviceLimit) -> [String: Any] {
        var d: [String: Any] = ["rules": l.rules.map(ruleConfig), "paused": l.paused]
        if let a = l.action { d["action"] = a.rawValue }
        if let k = l.slowKbps { d["slow_kbps"] = k }
        if let r = l.reset { d["reset"] = r }
        if let k = l.keys, !k.isEmpty { d["keys"] = k }
        d["grants"] = l.grants.map { g -> [String: Any] in
            var x: [String: Any] = ["id": g.id, "given": g.given]
            if let b = g.bytes { x["bytes"] = b }
            if let u = g.until { x["until"] = u }
            if let a = g.ask { x["ask"] = a }
            return x
        }
        return d
    }

    /// The app-wide limit settings as the engine's LimitSettings.
    var limitSettingsConfig: [String: Any] {
        let s = state.settings
        return [
            "action": s.limitAction.rawValue,
            "slow_kbps": s.slowKbps,
            "month_start": s.billingDay,
            "week_start": Calendar.current.firstWeekday - 1,
            "cap": s.capRules.map(Store.ruleConfig),
            "cap_action": s.capAction.rawValue,
        ]
    }

    /// The engine's Config JSON. lowMemory: for the Network Extension.
    func coreConfigJSON(lowMemory: Bool = false) -> String {
        let s = state.settings
        let config: [String: Any] = [
            "private_key": state.serverPrivateKey,
            "listen_port": s.listenPort,
            "bind_cellular": s.bindCellular,
            "dns": s.dns.split(whereSeparator: { $0 == "," || $0 == " " }).map(String.init),
            "web_port": s.webPageEnabled ? s.webPort : 0,
            "peers": state.devices.map(Store.peerConfig),
            "hotspot_ssid": s.hotspotName.trimmingCharacters(in: .whitespaces),
            "low_memory": lowMemory,
            // The extension can only write to the shared group folder or its
            // own; "" lets it pick its own when there's no group.
            "usage_file": lowMemory ? (Store.sharedUsageURL?.path ?? "") : Store.usageURL.path,
            "time_zone": TimeZone.current.identifier,
            "limits": limitSettingsConfig,
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: config),
              let json = String(data: data, encoding: .utf8) else { return "{}" }
        return json
    }

    // MARK: Start and stop

    /// Start sharing. runHotspot: also run the hotspot shortcut when that
    /// setting is on (false for the plain “Start Sharing” choice).
    func start(runHotspot: Bool = true) {
        guard !busy else { return }
        // Already sharing in the background (e.g. started from a shortcut
        // or Control Center): don't start a second engine in the app.
        if tunnel.isActive { refresh(); return }
        busy = true
        lastError = nil
        stoppingUntil = nil
        advertise() // triggers the Local Network permission prompt
        askForNotificationsOnce()
        if runHotspot && state.settings.hotspotShortcut { runHotspotShortcut() }
        if state.settings.backgroundSharing {
            // Background: the engine runs in the Network Extension, which iOS
            // keeps alive while it's on, so the phone can lock.
            let json = coreConfigJSON(lowMemory: true)
            Task {
                do {
                    try await tunnel.install(config: json, onDemand: true)
                    try tunnel.start()
                    SharedState.setSharing(true)
                } catch {
                    lastError = "Couldn't start background sharing: \(error.localizedDescription)"
                    log.add(.error, .share, "Couldn't start background sharing: \(error.localizedDescription)",
                            hint: "Allow Open Bars to add a VPN configuration when iOS asks. If it keeps failing, turn off Settings › Keep sharing in the background to share from the app.")
                }
                busy = false
                refresh()
            }
            return
        }
        // In the app: works only while the app is on screen, so keep the
        // display from auto-locking while sharing.
        let json = coreConfigJSON()
        WgcoreLog(LogLevel.info.rawValue, LogSource.share.rawValue,
                  LogText.shareSessionStart(mode: "in the app (only while it's on screen)"), "")
        DispatchQueue.global(qos: .userInitiated).async {
            var err: NSError?
            let ok = WgcoreStart(json, &err)
            DispatchQueue.main.async {
                self.busy = false
                if ok {
                    SharedState.setSharing(true)
                    UIApplication.shared.isIdleTimerDisabled = true
                } else {
                    // The engine logs why, with a hint.
                    self.lastError = err?.localizedDescription ?? "Sharing didn't start."
                }
                self.refresh()
            }
        }
    }

    /// Stop sharing (in the app, in the background, or both).
    func stop(reason: String = "you tapped Stop") {
        if running || busy || tunnel.isActive {
            log.add(.info, .share, "Stopping sharing: \(reason)")
        }
        SharedState.setSharing(false)
        stoppingUntil = Date().addingTimeInterval(10)
        if tunnel.isActive || tunnel.isOnDemandEnabled {
            Task {
                try? await tunnel.setOnDemand(false)
                tunnel.stop()
            }
        }
        if WgcoreRunning() { WgcoreStop() }
        endGrace()
        UIApplication.shared.isIdleTimerDisabled = false
        bonjour?.stop()
        bonjour = nil
        refresh()
    }

    /// Restarts in whichever mode the settings now ask for.
    func restartIfRunning() {
        guard running else { return }
        stop(reason: "settings changed, restarting")
        Task {
            for _ in 0..<50 where tunnel.isActive || tunnel.status == .disconnecting {
                try? await Task.sleep(nanoseconds: 100_000_000)
            }
            start()
        }
    }

    private func advertise() {
        guard bonjour == nil else { return }
        let svc = NetService(domain: "local.", type: "_openbars._udp.", name: "OpenBars", port: Int32(state.settings.listenPort))
        svc.publish()
        bonjour = svc
    }

    // MARK: Polling

    func refresh() {
        #if DEBUG
        if DemoMode.on { return } // the samples stand in for the engine
        #endif
        takePendingRequests()
        let since = samples.last?.t ?? 0
        let wantUsage = Date().timeIntervalSince(usageFetched) > 30
        if wantUsage { usageFetched = Date() }
        noteTunnelStatus()
        if tunnel.isActive || tunnel.status == .disconnecting {
            runningInBackground = true
            running = tunnel.isActive
            Task {
                if let s = await tunnel.send("status") { apply(statusJSON: s) }
                await pullExtensionLogs()
                if let h = await tunnel.send("history:\(since)") { apply(historyJSON: h) }
                if wantUsage, let u = await tunnel.send("usage:31") { apply(usageJSON: u) }
            }
            return
        }
        runningInBackground = false
        running = WgcoreRunning()
        apply(statusJSON: WgcoreStatusJSON())
        Task {
            await pullFinalExtensionLog()
            await log.pull(Store.appLogs, persist: false) { WgcoreLogsJSON($0) }
        }
        if running {
            apply(historyJSON: WgcoreHistoryJSON(since))
            if wantUsage { apply(usageJSON: WgcoreUsageJSON(31)) }
        } else {
            if !samples.isEmpty { samples = [] }
            if wantUsage {
                apply(usageJSON: WgcoreReadUsageJSON(Store.usageURL.path, TimeZone.current.identifier, 31))
                let json = WgcoreReadLimitsJSON(Store.usageURL.path, TimeZone.current.identifier, coreConfigJSON())
                let d = JSONDecoder()
                d.keyDecodingStrategy = .convertFromSnakeCase
                offlineLimits = json.data(using: .utf8).flatMap { try? d.decode(OfflineLimits.self, from: $0) }
            }
        }
    }

    /// Fetch usage again on the next refresh (e.g. when a chart appears).
    func refreshUsageSoon() { usageFetched = .distantPast }

    private func apply(historyJSON json: String) {
        guard let data = json.data(using: .utf8),
              let new = try? JSONDecoder().decode([TrafficSample].self, from: data), !new.isEmpty else { return }
        var all = samples + new
        let oldest = (all.last?.t ?? 0) - 300
        all.removeAll { $0.t <= oldest }
        samples = all
    }

    private func apply(usageJSON json: String) {
        guard let data = json.data(using: .utf8),
              let u = try? JSONDecoder().decode(UsageSummary.self, from: data) else { return }
        usage = u
    }

    /// Usage file in the app group container, shared with the background
    /// extension. nil until the group (group.dev.aayy.openbars) is added to
    /// both targets' entitlements and registered for the team.
    static var sharedUsageURL: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "group.dev.aayy.openbars")?
            .appendingPathComponent("usage.json")
    }

    /// Where the in-app engine saves usage. Without the app group, the
    /// background extension keeps its own file, which the app sees only
    /// while background sharing runs.
    static var usageURL: URL {
        sharedUsageURL ?? fileURL.deletingLastPathComponent().appendingPathComponent("usage.json")
    }

    private func apply(statusJSON json: String) {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        if let data = json.data(using: .utf8), let new = try? decoder.decode(CoreStatus.self, from: data) {
            status = new
        }
        // Keep the control and widgets in step with what's running.
        if running, let until = stoppingUntil, Date() < until {
            // still winding down after Stop
        } else {
            SharedState.setSharing(running)
        }
        SharedState.setSummary(online: running ? onlineCount : 0, down: running ? currentRate().down : 0)
    }

    // MARK: Log

    /// Hooks the Share log up to the engine in this process and to the
    /// background extension (clear, detailed logging).
    private func hookUpLog() {
        log.onClear = { [weak self] in
            WgcoreClearLogs()
            if let url = LogKeys.shareExtensionFinalLogURL { try? FileManager.default.removeItem(at: url) }
            guard let self, self.tunnel.isActive else { return }
            Task { _ = await self.tunnel.send(LogMessage.clear) }
        }
        log.onDetailedChange = { [weak self] until in
            WgcoreSetDetailedLogging(until != nil)
            guard let self, self.tunnel.isActive else { return }
            Task { _ = await self.tunnel.send(LogMessage.detailed(until: until)) }
        }
        WgcoreSetDetailedLogging(log.isDetailed)
    }

    /// New entries from the running background extension.
    private func pullExtensionLogs() async {
        await pullFinalExtensionLog()
        await log.pull(Store.extensionLogs) { await self.tunnel.send(LogMessage.logs(after: $0)) }
    }

    /// The last entries of a background extension that has stopped: it
    /// saves its log in the app group when it stops.
    private func pullFinalExtensionLog() async {
        guard let url = LogKeys.shareExtensionFinalLogURL,
              let json = try? String(contentsOf: url, encoding: .utf8) else { return }
        try? FileManager.default.removeItem(at: url)
        await log.pull(Store.extensionLogs) { _ in json }
    }

    /// Logs changes of the background tunnel's status, and why it stopped
    /// when the system says.
    private func noteTunnelStatus() {
        let now = tunnel.status
        defer { lastTunnelStatus = now }
        guard tunnelLoaded, let before = lastTunnelStatus, before != now, before != .invalid else { return }
        log.add(.info, .ext, "Background sharing: \(ConnectController.describe(now))")
        guard now == .disconnected, let connection = tunnel.manager?.connection else { return }
        guard #available(iOS 16.0, *) else { return } // fetchLastDisconnectError is iOS 16
        connection.fetchLastDisconnectError { error in
            guard let error else { return }
            Task { @MainActor in
                LogBook.share.add(.error, .ext, "Background sharing stopped: \(error.localizedDescription)",
                                  hint: "Start sharing again. If it keeps stopping, turn off Settings › Keep sharing in the background to share from the app.")
            }
        }
    }

    // MARK: Devices

    /// Saves, then gives the device list to whichever engine runs.
    func pushPeers() {
        save()
        let peers = state.devices.map(Store.peerConfig)
        guard let data = try? JSONSerialization.data(withJSONObject: peers),
              let json = String(data: data, encoding: .utf8) else { return }
        if tunnel.isActive {
            lastPush = Task {
                let reply = await tunnel.send("peers:" + json)
                if let reply, reply != "ok" {
                    lastError = reply
                    log.add(.error, .share, "Couldn't update the background engine's device list: \(reply)",
                            hint: "Stop and start sharing to reload the devices.")
                }
                // Also update the saved configuration, or the device would be
                // missing whenever iOS restarts the extension.
                do {
                    try await tunnel.install(config: coreConfigJSON(lowMemory: true), onDemand: true)
                } catch {
                    lastError = "Couldn't save the device list for background sharing: \(error.localizedDescription)"
                    log.add(.error, .share, "Couldn't save the device list for background sharing: \(error.localizedDescription)",
                            hint: "Stop and start sharing so the background engine has the current devices.")
                }
            }
            return
        }
        guard WgcoreRunning() else { return }
        var err: NSError?
        if !WgcoreSetPeers(json, &err) {
            let text = err?.localizedDescription ?? "the engine refused the device list"
            lastError = text
            log.add(.error, .share, "Couldn't update the device list: \(text)",
                    hint: "Stop and start sharing to reload the devices.")
        }
    }

    /// A new device for the WireGuard app (QR code or .conf file); its key is
    /// made here. nil when every address is taken.
    @discardableResult
    func addDevice(name: String, kind: DeviceKind = .other) -> Device? {
        guard let address = freeAddress() else {
            lastError = Self.noAddressLeft
            log.add(.warn, .app, "Can't add “\(name)”: all 253 device addresses (\(Self.devicePrefix)2–254) are in use",
                    hint: "Remove a device you no longer use, then add the new one.")
            return nil
        }
        let privateKey = WgcoreGeneratePrivateKey()
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        var device = Device(name: trimmed.isEmpty ? Self.defaultName(for: address) : trimmed,
                            privateKey: privateKey, publicKey: WgcorePublicKey(privateKey),
                            address: address, kind: kind.rawValue)
        device.confUntil = Int64(Date().timeIntervalSince1970) + Self.confWindow
        device.limit = newDeviceLimit()
        state.devices.append(device)
        log.add(.info, .app, "Added “\(device.name)” (\(device.address), \(LogText.shortKey(device.publicKey))) for the WireGuard app")
        pushPeers()
        return device
    }

    /// Devices get 10.66.66.2 through .254 inside the tunnel; .1 is this iPhone.
    static let devicePrefix = "10.66.66."
    private static let noAddressLeft = "No free device address: all 253 are in use."

    /// The lowest device address nobody has, or nil when they're all taken.
    private func freeAddress() -> String? {
        var taken = Set<Int>()
        for d in state.devices where d.address.hasPrefix(Self.devicePrefix) {
            if let n = Int(d.address.dropFirst(Self.devicePrefix.count)) { taken.insert(n) }
        }
        return (2...254).lazy.filter { !taken.contains($0) }.first.map { Self.devicePrefix + String($0) }
    }

    /// "device12" for 10.66.66.12, when a device comes without a name.
    private static func defaultName(for address: String) -> String {
        "device" + (address.split(separator: ".").last.map(String.init) ?? "")
    }

    /// The first pairing request nobody has answered yet.
    var nextPairingRequest: PairRequest? {
        status.pairing?.first { !answeredPairings.contains($0.publicKey) }
    }

    /// Allow: the device becomes a normal WireGuard device whose private key
    /// stays on that device. Adding it is what the engine waits for.
    /// limit: the limits to start with; nil = what it had before if it was
    /// removed, otherwise the default for new devices.
    func approvePairing(_ r: PairRequest, limit: DeviceLimit? = nil) {
        answeredPairings.insert(r.publicKey)
        if state.devices.contains(where: { $0.publicKey == r.publicKey }) {
            log.add(.info, .pair, "Allowed “\(r.name)” again (\(LogText.shortKey(r.publicKey)), already added)")
            pushPeers()
            return
        }
        // Removed earlier and back with the same key: same record, limits and usage.
        if var old = removedRecord(for: r.publicKey) {
            state.removedDevices?.removeAll { $0.publicKey == r.publicKey }
            old.removedAt = nil
            if let limit { old.limit = limit }
            if state.devices.contains(where: { $0.address == old.address }) || !old.address.hasPrefix(Self.devicePrefix) {
                guard let address = freeAddress() else {
                    lastError = Self.noAddressLeft
                    return
                }
                old.address = address
            }
            state.devices.append(old)
            log.add(.info, .pair, "Allowed “\(r.name)” back (\(LogText.shortKey(r.publicKey))) with its earlier limits and usage, as \(old.address)")
            pushPeers()
            return
        }
        guard let address = freeAddress() else {
            lastError = Self.noAddressLeft
            log.add(.warn, .pair, "Couldn't add “\(r.name)”: all 253 device addresses are in use",
                    hint: "Remove a device you no longer use, then pair again.")
            return
        }
        let name = r.name.trimmingCharacters(in: .whitespaces)
        state.devices.append(Device(name: name.isEmpty ? Self.defaultName(for: address) : name, privateKey: "",
                                    publicKey: r.publicKey, address: address,
                                    kind: DeviceKind(rawValue: r.kind)?.rawValue,
                                    limit: limit ?? newDeviceLimit()))
        log.add(.info, .pair, "Allowed “\(r.name)” (\(r.kind.isEmpty ? "device" : r.kind) at \(r.from), \(LogText.shortKey(r.publicKey))) as \(address)")
        pushPeers()
    }

    func denyPairing(_ r: PairRequest) {
        answeredPairings.insert(r.publicKey)
        log.add(.info, .pair, "Didn't allow “\(r.name)” (\(r.from), \(LogText.shortKey(r.publicKey)))")
        if tunnel.isActive {
            lastPush = Task { _ = await tunnel.send("deny:" + r.publicKey) }
        } else {
            _ = WgcoreDenyPairing(r.publicKey)
        }
    }

    /// Swipe to delete on the Share tab.
    func removeDevices(at offsets: IndexSet) {
        for d in offsets.map({ state.devices[$0] }) { logRemoved(d); keepRecord(of: d) }
        state.devices.remove(atOffsets: offsets)
        pushPeers()
    }

    func remove(_ device: Device) {
        logRemoved(device)
        keepRecord(of: device)
        state.devices.removeAll { $0.id == device.id }
        pushPeers()
    }

    /// A removed device's record, kept so pairing again doesn't reset its
    /// limits (its usage is kept by key in the usage file anyway).
    private func keepRecord(of d: Device) {
        var r = d
        r.removedAt = Int64(Date().timeIntervalSince1970)
        r.confUntil = nil
        var list = (state.removedDevices ?? []).filter { $0.publicKey != d.publicKey }
        list.append(r)
        state.removedDevices = Array(list.suffix(Self.keepRemoved))
    }

    private static let keepRemoved = 100
    /// How long a new WireGuard-app device's .conf is on the config page.
    static let confWindow: Int64 = 15 * 60

    func removedRecord(for publicKey: String) -> Device? {
        state.removedDevices?.last { $0.publicKey == publicKey }
    }

    /// A removed or current device that looks like this request (same name
    /// and kind) and has limits: a hint that it may be the same device with
    /// a new key. Only a hint: the request could say anything.
    func lookalike(for r: PairRequest) -> Device? {
        guard removedRecord(for: r.publicKey) == nil else { return nil }
        let name = r.name.trimmingCharacters(in: .whitespaces).lowercased()
        let all = state.devices + (state.removedDevices ?? []).reversed()
        return all.first {
            $0.publicKey != r.publicKey && $0.name.lowercased() == name
                && ($0.kind ?? "") == r.kind && !($0.limit?.rules.isEmpty ?? true)
        }
    }

    /// The limits a new device starts with (Settings › Data Limits).
    func newDeviceLimit() -> DeviceLimit? {
        let rules = state.settings.newDeviceRules.map(Self.starting)
        return rules.isEmpty ? nil : DeviceLimit(rules: rules)
    }

    /// A rule ready to save: a total limit counts from now.
    static func starting(_ r: LimitRule) -> LimitRule {
        var r = r
        if r.period == .total, r.start == nil { r.start = Int64(Date().timeIntervalSince1970) }
        return r
    }

    private func logRemoved(_ d: Device) {
        log.add(.info, .app, "Removed “\(d.name)” (\(d.address), \(LogText.shortKey(d.publicKey)))")
    }

    func setKind(_ kind: DeviceKind, for device: Device) {
        guard let i = state.devices.firstIndex(where: { $0.id == device.id }) else { return } // gone already
        state.devices[i].kind = kind.rawValue
        save()
    }

    /// The engine renames a peer without touching its WireGuard session.
    func rename(_ device: Device, to name: String) {
        guard let i = state.devices.firstIndex(where: { $0.id == device.id }) else { return } // gone already
        log.add(.info, .app, "Renamed “\(state.devices[i].name)” to “\(name)”")
        state.devices[i].name = name
        pushPeers()
    }

    /// The device's WireGuard .conf (QR code, share file, copy).
    func clientConfig(for d: Device) -> String {
        WgcoreClientConfig(d.privateKey, d.address, serverPublicKey, endpointHost,
                           state.settings.listenPort, "10.66.66.1", 1420)
    }

    func peerStatus(for d: Device) -> PeerStatus? {
        status.peers?.first { $0.publicKey == d.publicKey }
    }

    /// Saves new settings and, if anything changed, restarts sharing so
    /// they take effect.
    func updateSettings(_ new: AppSettings) {
        var new = new
        new.capRules = new.capRules.map(Self.starting)
        let old = state.settings
        state.settings = new
        save()
        guard old != new else { return }
        log.add(.info, .app, "Settings changed: " + Store.changes(from: old, to: new))
        if old.restartRelevant != new.restartRelevant {
            restartIfRunning()
        } else {
            pushLimits() // data limits and notifications apply as they are
        }
    }

    private static func changes(from a: AppSettings, to b: AppSettings) -> String {
        func onOff(_ on: Bool) -> String { on ? "on" : "off" }
        var out: [String] = []
        if a.listenPort != b.listenPort { out.append("UDP port \(a.listenPort) → \(b.listenPort)") }
        if a.bindCellular != b.bindCellular { out.append("cellular only \(onOff(b.bindCellular))") }
        if a.dns != b.dns { out.append("DNS “\(a.dns)” → “\(b.dns)”") }
        if a.webPageEnabled != b.webPageEnabled { out.append("config page \(onOff(b.webPageEnabled))") }
        if a.webPort != b.webPort { out.append("config page port \(a.webPort) → \(b.webPort)") }
        if a.autoStart != b.autoStart { out.append("start when app opens \(onOff(b.autoStart))") }
        if a.backgroundSharing != b.backgroundSharing { out.append("background sharing \(onOff(b.backgroundSharing))") }
        if a.hotspotName != b.hotspotName { out.append("hotspot name “\(b.hotspotName)”") }
        if a.hotspotShortcut != b.hotspotShortcut { out.append("hotspot shortcut \(onOff(b.hotspotShortcut))") }
        if a.hotspotShortcutName != b.hotspotShortcutName { out.append("shortcut name “\(b.hotspotShortcutName)”") }
        if a.limitAction != b.limitAction { out.append("at a limit: \(b.limitAction.title.lowercased())") }
        if a.slowKbps != b.slowKbps { out.append("slow-down speed \(speedTitle(b.slowKbps))") }
        if a.newDeviceRules != b.newDeviceRules {
            out.append("new devices: " + (b.newDeviceRules.isEmpty ? "no limit" : b.newDeviceRules.map(\.summary).joined(separator: ", ")))
        }
        if a.capRules != b.capRules {
            out.append("all devices: " + (b.capRules.isEmpty ? "no limit" : b.capRules.map(\.summary).joined(separator: ", ")))
        }
        if a.capAction != b.capAction { out.append("all devices at the limit: \(b.capAction.title.lowercased())") }
        if a.billingDay != b.billingDay { out.append("billing day \(b.billingDay)") }
        if a.notifications != b.notifications { out.append("notifications") }
        return out.joined(separator: ", ")
    }
}

// MARK: - Shortcuts callbacks

extension Store {
    /// openbars://hotspot?result=…: Shortcuts reports back after running the
    /// hotspot shortcut. Only a failure needs anything from us.
    func handleURL(_ url: URL) {
        #if DEBUG
        if url.scheme == "openbars", url.host == "debug" { handleDebugURL(url); return }
        #endif
        guard url.scheme == "openbars", url.host == "hotspot" else { return }
        let query = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        func value(_ name: String) -> String? { query.first { $0.name == name }?.value }
        guard value("result") == "error" else { return }
        let detail = value("errorMessage") ?? ""
        let shortcut = state.settings.hotspotShortcutName
        let suffix = detail.isEmpty ? "" : " (Shortcuts: \(detail))"
        lastError = "Couldn't turn on Personal Hotspot: Shortcuts has no shortcut named “\(shortcut)”. Open Bars finds it by its exact name, so if you renamed it, change it back. If you deleted it, add it again from Settings › Personal Hotspot." + suffix
        log.add(.warn, .app, "The hotspot shortcut “\(shortcut)” didn't run" + (detail.isEmpty ? "" : ": \(detail)"),
                hint: "Check the shortcut name in Settings › Personal Hotspot, or add the shortcut again from there.")
    }
}

#if DEBUG
// MARK: - Screenshot mode

extension Store {
    private static let demoDevices: [(name: String, kind: DeviceKind, wireGuardApp: Bool, perDay: Double, weight: Double, online: Bool)] = [
        ("MacBook Air", .mac, false, 2_600_000_000, 0.55, true),
        ("iPad", .ipad, false, 900_000_000, 0.15, true),
        ("Gaming PC", .windows, false, 3_400_000_000, 0.30, true),
        ("Travel Laptop", .laptop, true, 450_000_000, 0, false),
    ]

    /// Screenshot mode (see DemoMode): sharing with three of four sample
    /// devices, with live speed and a month of usage.
    fileprivate func showDemo() {
        let now = Int64(Date().timeIntervalSince1970)
        let all = Self.demoDevices
        state.devices = all.enumerated().map { i, d in
            Device(name: d.name, privateKey: d.wireGuardApp ? DemoMode.key(100 + i) : "",
                   publicKey: DemoMode.key(i + 1), address: Store.devicePrefix + String(i + 2), kind: d.kind.rawValue)
        }
        state.settings.hotspotName = DemoMode.iPhoneName
        state.settings.backgroundSharing = true
        let peers = state.devices.enumerated().map { i, d in
            PeerStatus(name: d.name, publicKey: d.publicKey, address: d.address,
                       endpoint: "172.20.10.\(i + 2):\(51000 + i * 7)",
                       lastHandshake: all[i].online ? now - 30 : now - 3 * 3600 - 600,
                       rxBytes: Int64(all[i].perDay * 0.08), txBytes: Int64(all[i].perDay * 0.6),
                       lastSeen: all[i].online ? now : now - 3 * 3600 - 600)
        }
        running = true
        runningInBackground = true
        status = CoreStatus(running: true, listenPort: 51820,
                            net: NetInfo(hotspotIp: "172.20.10.1", cellularIf: "pdp_ip0", cellularIp: "10.48.112.7", defaultIf: "pdp_ip0"),
                            egressIf: "pdp_ip0", tcpActive: 46, tcpTotal: 1873, udpFlows: 9,
                            bytesUp: peers.reduce(0) { $0 + $1.rxBytes }, bytesDown: peers.reduce(0) { $0 + $1.txBytes },
                            uptime: 2760, peers: peers)
        addDemoLimits(now: now)
        samples = DemoMode.samples(peers: demoWeights)
        usage = DemoMode.usage(
            devices: state.devices.enumerated().map { i, d in (d.publicKey, d.name, all[i].perDay) },
            seen: Dictionary(uniqueKeysWithValues: peers.map { ($0.publicKey, $0.lastSeen ?? 0) }))
        // Keep it live: a new second of traffic every second.
        pollTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.demoTick() }
        }
    }

    /// Limits in the samples: the iPad near its daily limit and asking for
    /// more, the PC with a weekly limit, the laptop paused, and a monthly
    /// limit for all devices. `-showPairing` adds a device asking to connect.
    private func addDemoLimits(now: Int64) {
        let midnight = endOfToday()
        let week = midnight + 3 * 24 * 3600
        let month = midnight + 11 * 24 * 3600
        state.devices[1].limit = DeviceLimit(rules: [LimitRule(period: .day, bytes: 1_000_000_000)])
        state.devices[2].limit = DeviceLimit(rules: [LimitRule(period: .week, bytes: 25_000_000_000)], action: .slow, slowKbps: 2000)
        state.devices[3].limit = DeviceLimit(paused: true)
        state.settings.capRules = [LimitRule(period: .month, bytes: 60_000_000_000)]
        state.settings.billingDay = 15
        var peers = status.peers ?? []
        peers[1].limit = LimitStatus(state: "warn", rules: [RuleStatus(period: "day", bytes: 1_000_000_000, used: 840_000_000, resets: midnight)])
        peers[2].limit = LimitStatus(state: "", rules: [RuleStatus(period: "week", bytes: 25_000_000_000, used: 13_600_000_000, resets: week)])
        peers[3].limit = LimitStatus(state: "paused", action: "block")
        status.peers = peers
        status.cap = [RuleStatus(period: "month", bytes: 60_000_000_000, used: 38_400_000_000, resets: month)]
        status.asks = [AskRequest(id: "demo", key: state.devices[1].publicKey, name: state.devices[1].name, minutes: 60,
                                  message: "Finishing my homework", since: now - 90, state: "pending")]
        if ProcessInfo.processInfo.arguments.contains("-showPairing") {
            status.pairing = [PairRequest(name: "Sam's iPad", kind: "ipad", publicKey: DemoMode.key(50), from: "172.20.10.6", since: now)]
        }
    }

    private var demoWeights: [(key: String, weight: Double)] {
        state.devices.enumerated().compactMap { i, d in
            Self.demoDevices[i].online ? (d.publicKey, Self.demoDevices[i].weight) : nil
        }
    }

    private func demoTick() {
        let now = Int64(Date().timeIntervalSince1970)
        let s = DemoMode.sample(t: now, peers: demoWeights, downPeak: 7_500_000, upPeak: 900_000)
        samples = Array((samples + [s]).suffix(300))
        var peers = status.peers ?? []
        for i in peers.indices where Self.demoDevices[i].online {
            let r = s.rates(for: peers[i].publicKey)
            peers[i].lastSeen = now
            peers[i].txBytes += r.down
            peers[i].rxBytes += r.up
        }
        status.peers = peers
    }
}
#endif
