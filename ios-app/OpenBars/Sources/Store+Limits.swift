import Foundation
import UserNotifications
import WGCore

/// Data limits: changing a device's limits, giving more, answering
/// requests, and the notification buttons.
extension Store {
    // MARK: Reading

    /// A device's limits as of now: from the engine while sharing, from
    /// saved usage otherwise.
    func limitStatus(for d: Device) -> LimitStatus? {
        if running { return peerStatus(for: d)?.limit }
        return offlineLimits?.peers?[d.publicKey]
    }

    /// The all-devices limit as of now.
    var capStatus: [RuleStatus] {
        (running ? status.cap : offlineLimits?.cap) ?? []
    }

    /// Requests for more time or data nobody has answered.
    var pendingAsks: [AskRequest] { running ? (status.asks ?? []) : [] }

    // MARK: Changing a device's limits

    /// Applies a change to a device's limits, saves, and tells the engine.
    func updateLimit(of deviceID: UUID, _ change: (inout DeviceLimit) -> Void) {
        guard let i = state.devices.firstIndex(where: { $0.id == deviceID }) else { return }
        var l = state.devices[i].limit ?? DeviceLimit()
        change(&l)
        l.rules = l.rules.map(Store.starting)
        state.devices[i].limit = l.isEmpty ? nil : l
        pushPeers()
        refreshUsageSoon()
    }

    func addRule(_ r: LimitRule, to d: Device) {
        LogBook.share.add(.info, .app, "“\(d.name)”: limit \(r.summary)")
        updateLimit(of: d.id) { l in
            l.rules.removeAll { $0.period == r.period }
            l.rules.append(r)
            l.rules.sort { LimitPeriod.allCases.firstIndex(of: $0.period)! < LimitPeriod.allCases.firstIndex(of: $1.period)! }
        }
    }

    func removeRule(_ r: LimitRule, from d: Device) {
        LogBook.share.add(.info, .app, "“\(d.name)”: removed the limit of \(r.summary)")
        updateLimit(of: d.id) { $0.rules.removeAll { $0.id == r.id } }
    }

    func setAction(_ a: LimitAction?, slowKbps: Int?, for d: Device) {
        updateLimit(of: d.id) {
            $0.action = a
            $0.slowKbps = a == .slow ? slowKbps : nil
        }
    }

    func setPaused(_ paused: Bool, _ d: Device) {
        LogBook.share.add(.info, .app, paused ? "Paused internet for “\(d.name)”" : "Resumed internet for “\(d.name)”")
        updateLimit(of: d.id) { $0.paused = paused }
    }

    /// More time or data for a device. Grants older than a week go.
    func give(_ g: LimitGrant, to d: Device) {
        LogBook.share.add(.info, .app, "Gave “\(d.name)” " + Store.describe(g))
        updateLimit(of: d.id) { l in
            let weekAgo = Int64(Date().timeIntervalSince1970) - 7 * 24 * 3600
            l.grants.removeAll { $0.given < weekAgo }
            l.grants.append(g)
        }
    }

    /// Usage before now stops counting toward this device's limits.
    func resetUsage(_ d: Device) {
        LogBook.share.add(.info, .app, "Reset the limits' usage for “\(d.name)”")
        updateLimit(of: d.id) { $0.reset = Int64(Date().timeIntervalSince1970) }
    }

    /// Lets the config page offer a WireGuard-app device's .conf once more,
    /// for a few minutes.
    func allowDownload(_ d: Device) {
        guard let i = state.devices.firstIndex(where: { $0.id == d.id }) else { return }
        state.devices[i].confUntil = Int64(Date().timeIntervalSince1970) + Store.confWindow
        LogBook.share.add(.info, .app, "The config page offers “\(d.name).conf” for 15 minutes (one download)")
        pushPeers()
    }

    /// Limits for a new key that's taken to be `old` again: its limits, and
    /// its usage counts, so a new key doesn't mean a fresh start.
    func limitLike(_ old: Device) -> DeviceLimit {
        var l = old.limit ?? DeviceLimit()
        l.grants = []
        l.paused = false
        l.keys = [old.publicKey] + (l.keys ?? [])
        return l
    }

    /// The config page still offers this device's .conf.
    func downloadOpen(_ d: Device) -> Bool {
        (d.confUntil ?? 0) > Int64(Date().timeIntervalSince1970)
    }

    static func describe(_ g: LimitGrant) -> String {
        if let u = g.until {
            if u == endOfToday() { return "no limit until midnight" }
            let mins = (u - g.given) / 60
            return mins == 60 ? "1 more hour" : "\(mins) more minutes"
        }
        return ByteCountFormatter.string(fromByteCount: g.bytes ?? 0, countStyle: .decimal) + " more"
    }

    /// Grants offered in menus and notifications.
    static func quickGrant(_ id: String) -> LimitGrant? {
        let now = Int64(Date().timeIntervalSince1970)
        switch id {
        case "15m", Notifier.give15Action: return LimitGrant(until: now + 15 * 60, given: now)
        case "1h", Notifier.give60Action: return LimitGrant(until: now + 3600, given: now)
        case "today", Notifier.giveTodayAction: return LimitGrant(until: endOfToday(), given: now)
        case "250mb": return LimitGrant(bytes: 250_000_000, given: now)
        case "1gb": return LimitGrant(bytes: 1_000_000_000, given: now)
        default: return nil
        }
    }

    // MARK: Requests from devices

    func approveAsk(_ a: AskRequest) {
        guard let d = state.devices.first(where: { $0.publicKey == a.key }) else { return }
        give(a.grant(), to: d)
    }

    func denyAsk(_ a: AskRequest) {
        LogBook.share.add(.info, .app, "Declined “\(a.name)”'s request for \(a.what)")
        if tunnel.isActive {
            lastPush = Task { _ = await tunnel.send("denyask:" + a.id) }
        } else {
            _ = WgcoreDenyAsk(a.id)
        }
        refresh()
    }

    // MARK: App-wide settings

    /// Hands the engine the app-wide limit settings without restarting, and
    /// saves them for when background sharing restarts.
    func pushLimits() {
        guard let data = try? JSONSerialization.data(withJSONObject: limitSettingsConfig),
              let json = String(data: data, encoding: .utf8) else { return }
        if tunnel.isActive {
            lastPush = Task {
                _ = await tunnel.send("limits:" + json)
                try? await tunnel.install(config: coreConfigJSON(lowMemory: true), onDemand: true)
            }
        } else if WgcoreRunning() {
            var err: NSError?
            _ = WgcoreSetLimits(json, &err)
        }
        refreshUsageSoon()
    }

    // MARK: Notification buttons

    /// A notification's button was tapped. The app may have been launched in
    /// the background for it: this waits until the change reaches the
    /// background engine.
    func handleNotification(action: String, userInfo: [AnyHashable: Any]) async {
        await tunnel.load()
        let key = userInfo[Notifier.Key.device] as? String
        let device = key.flatMap { k in state.devices.first { $0.publicKey == k } }
        switch action {
        case Notifier.allowAction:
            // "Review…" (limits in use) opens the app, where the Allow sheet shows.
            guard !SharedState.limitsInUse,
                  let r = Notifier.decode(PairRequest.self, userInfo[Notifier.Key.pair]) else { break }
            approvePairing(r)
        case Notifier.denyAction:
            guard let r = Notifier.decode(PairRequest.self, userInfo[Notifier.Key.pair]) else { break }
            denyPairing(r)
        case Notifier.give15Action, Notifier.give60Action, Notifier.giveTodayAction:
            if let d = device, let g = Store.quickGrant(action) { give(g, to: d) }
        case Notifier.approveAction:
            if let a = Notifier.decode(AskRequest.self, userInfo[Notifier.Key.ask]) { approveAsk(a) }
        case Notifier.declineAction:
            if let a = Notifier.decode(AskRequest.self, userInfo[Notifier.Key.ask]) { denyAsk(a) }
        default:
            break // opened the app
        }
        await lastPush?.value
    }
}

#if DEBUG
// MARK: - Testing without taps (Debug builds only)

extension Store {
    private static var debugWatchdog: Watchdog?

    /// Commands without the "Open in Open Bars?" prompt that a link gets:
    /// write "openbars://debug/<command>?…" to Application Support/
    /// debug-command.txt in the app's container, then post the Darwin
    /// notification dev.aayy.openbars.debug
    /// (`xcrun simctl spawn booted notifyutil -p dev.aayy.openbars.debug`).
    func listenForDebugCommands() {
        CFNotificationCenterAddObserver(
            CFNotificationCenterGetDarwinNotifyCenter(), Unmanaged.passUnretained(self).toOpaque(),
            { _, observer, _, _, _ in
                guard let observer else { return }
                let store = Unmanaged<Store>.fromOpaque(observer).takeUnretainedValue()
                Task { @MainActor in store.runDebugCommandFile() }
            },
            "dev.aayy.openbars.debug" as CFString, nil, .deliverImmediately)
    }

    func runDebugCommandFile() {
        let file = FileManager.appSupportFolder.appendingPathComponent("debug-command.txt")
        guard let text = try? String(contentsOf: file, encoding: .utf8) else { return }
        try? FileManager.default.removeItem(at: file)
        for line in text.split(separator: "\n") {
            if let url = URL(string: line.trimmingCharacters(in: .whitespaces)) { handleDebugURL(url) }
        }
        refresh()
    }

    /// `xcrun simctl openurl booted "openbars://debug/<command>?…"` runs what
    /// the matching button runs, for testing in the Simulator with
    /// ios-app/core/cmd/fakedevice:
    ///
    ///     approve-ask, decline-ask        the first request for more
    ///     allow[?limit=1gb-day|like], deny  the first pairing request
    ///     give?what=15m|1h|today|250mb|1gb[&device=N]
    ///     pause[?device=N], resume[?device=N], reset[?device=N], remove[?device=N]
    ///     notify?kind=pair|limit|warn|ask&delay=S   a sample notification
    ///     permission                       ask for notification permission
    ///     start, stop                      start or stop sharing
    ///     watchdog?grace=S, watchdog-stop, watchdog-last   background sharing's watchdog, in the app
    func handleDebugURL(_ url: URL) {
        let q = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        func value(_ n: String) -> String? { q.first { $0.name == n }?.value }
        let device: Device? = state.devices.indices.contains(Int(value("device") ?? "0") ?? 0)
            ? state.devices[Int(value("device") ?? "0") ?? 0] : nil
        LogBook.share.add(.debug, .app, "Debug link: \(url.absoluteString)")
        switch url.path {
        case "/approve-ask": if let a = pendingAsks.first { approveAsk(a) }
        case "/decline-ask": if let a = pendingAsks.first { denyAsk(a) }
        case "/allow":
            guard let r = status.pairing?.first else { break }
            var limit: DeviceLimit? = nil
            if value("limit") == "1gb-day" { limit = DeviceLimit(rules: [LimitRule(period: .day, bytes: 1_000_000_000)]) }
            if value("limit") == "like", let old = lookalike(for: r) { limit = limitLike(old) }
            approvePairing(r, limit: limit)
        case "/deny": if let r = status.pairing?.first { denyPairing(r) }
        case "/give": if let d = device, let g = Store.quickGrant(value("what") ?? "15m") { give(g, to: d) }
        case "/pause": if let d = device { setPaused(true, d) }
        case "/resume": if let d = device { setPaused(false, d) }
        case "/reset": if let d = device { resetUsage(d) }
        case "/remove": if let d = device { remove(d) }
        case "/permission":
            Notifier.requestAuthorization()
        case "/start": if !running { start() }
        case "/stop": stop(reason: "debug link")
        case "/watchdog":
            // Background sharing's watchdog, run in the app (the Simulator
            // has no extensions): kill the app and the notice should show.
            Store.debugWatchdog?.stop()
            let grace = TimeInterval(value("grace") ?? "15") ?? 15
            Store.debugWatchdog = Watchdog(every: max(1, grace / 3), grace: grace)
            Store.debugWatchdog?.start()
        case "/watchdog-stop":
            Store.debugWatchdog?.stop()
            Store.debugWatchdog = nil
        case "/watchdog-last":
            let last = Watchdog.lastRun()
            LogBook.share.add(.info, .app, "Watchdog last run: ended without warning \(last.endedWithoutWarning), "
                + "last alive \(last.lastAlive?.description ?? "-"), notice shown \(last.noticeShown?.description ?? "no")")
        case "/notify":
            let key = device?.publicKey
            var e = EngineEvent(seq: Int64(Date().timeIntervalSince1970), kind: value("kind") ?? "limit", key: key,
                                name: device?.name, period: "day", bytes: 1_000_000_000, used: 1_000_000_000, action: "block")
            switch e.kind {
            case "pair": e.pair = PairRequest(name: "Sam's iPad", kind: "ipad", publicKey: DemoMode.key(50), from: "172.20.10.6")
            case "ask": e.ask = AskRequest(id: "debug", key: key ?? "", name: device?.name ?? "Device", minutes: 15, message: "Sample request")
            case "stopped":
                Notifier.postSimple(id: "stopped", title: "Sharing stopped", body: "Background sharing stopped: sample. Open Open Bars to start it again.")
                return
            default: break
            }
            Notifier.post(e, delay: TimeInterval(value("delay") ?? "0") ?? 0)
        default: break
        }
    }
}
#endif
