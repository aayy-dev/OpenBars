import CryptoKit
import Foundation
import Network
import os
import NetworkExtension
#if os(iOS)
import UIKit
#else
import AppKit
#endif

/// Open Bars Connect: pairs this device with Open Bars on a hotspot iPhone,
/// then installs a WireGuard VPN (our own OpenBarsConnect extension) that
/// turns itself on only while this device is on that hotspot, the way
/// Tailscale's on-demand works.
@MainActor
final class ConnectController: ObservableObject {
    static let providerBundleID = "dev.aayy.openbars.connect"
    nonisolated static let phoneHost = "172.20.10.1"
    nonisolated static let webPort = 8080
    private static let keyItem = "connectPrivateKey"

    /// What the iPhone sent back when it approved this device.
    struct Pairing: Codable, Equatable {
        var serverPublicKey: String
        var endpoint: String // 172.20.10.1:51820
        var address: String
        var address6: String
        var dns: String
        var mtu: Int
        var hotspotName: String
        var deviceName: String

        enum CodingKeys: String, CodingKey {
            case serverPublicKey = "server_public_key", endpoint, address, address6, dns, mtu
            case hotspotName = "hotspot_name", deviceName = "device_name"
        }

        /// Same tunnel settings (names aside).
        func sameTunnel(as o: Pairing) -> Bool {
            serverPublicKey == o.serverPublicKey && endpoint == o.endpoint && address == o.address
                && address6 == o.address6 && dns == o.dns && mtu == o.mtu
        }
    }

    /// Reported by the running extension.
    struct TunnelStatus: Decodable {
        var running: Bool
        var endpoint: String
        var lastHandshake: Int64
        var rxBytes: Int64
        var txBytes: Int64
        var uptime: Int64
    }

    @Published var hotspotName: String
    @Published var useOnUSB: Bool
    @Published private(set) var pairing: Pairing?
    @Published private(set) var installed = false
    @Published private(set) var autoConnect = false
    @Published private(set) var status: NEVPNStatus = .invalid
    @Published private(set) var tunnel: TunnelStatus?
    /// Per-second speed for the last 5 minutes, from the tunnel extension.
    @Published private(set) var samples: [TrafficSample] = []
    @Published private(set) var busy = false
    @Published private(set) var waitingForApproval = false
    /// This device has a 172.20.10.x address (on an iPhone hotspot).
    @Published private(set) var onHotspot = false
    /// The system blocks Open Bars from the local network (privacy setting).
    @Published private(set) var localNetworkBlocked = false
    /// Results of the last Diagnose run, in order.
    @Published private(set) var diagnostics: [DiagnosticCheck] = []
    @Published private(set) var diagnosing = false
    /// Show Settings in the Connect window (the Mac menu bar's gear and ⌘,
    /// open the window straight to it).
    @Published var showSettings = false
    /// Last result shown to the user (see report(_:)).
    @Published var message: String?

    private var manager: NETunnelProviderManager?
    private let defaults = UserDefaults.standard
    private var log: LogBook { .connect }
    private var timer: Timer?
    private var observer: Any?
    private let pathMonitor = NWPathMonitor()
    /// Last network summary logged, to log only changes.
    private var lastNetwork = ""

    init() {
        hotspotName = defaults.string(forKey: "hotspotName") ?? ""
        #if os(macOS)
        useOnUSB = defaults.object(forKey: "useOnUSB") as? Bool ?? true
        #else
        useOnUSB = false
        #endif
        if let data = defaults.data(forKey: "pairing") {
            pairing = try? JSONDecoder().decode(Pairing.self, from: data)
        }
        defaults.removeObject(forKey: "tunnelLogSeq") // the old text log's position
        log.add(.info, .app, "Open Bars Connect started: \(AppInfo.summary)")
        hookUpLog()
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange, object: nil, queue: .main
        ) { [weak self] note in
            let conn = note.object as? NEVPNConnection
            Task { @MainActor in await self?.statusChanged(conn) }
        }
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.poll() }
        }
        pathMonitor.pathUpdateHandler = { [weak self] path in
            let summary = NetworkSummary.describe(path)
            Task { @MainActor in
                self?.logNetwork(summary)
                await self?.networkChanged()
            }
        }
        pathMonitor.start(queue: DispatchQueue(label: "dev.aayy.openbars.connect.hotspot"))
        Task { await reload() }
    }

    /// On joining a hotspot, ask for Local Network access right away, so the
    /// system prompt appears now rather than failing the first Pair.
    private func networkChanged() async {
        let now = Self.hotspotAddress() != nil
        guard now != onHotspot else { return }
        onHotspot = now
        if now {
            localNetworkBlocked = await Self.localNetworkDenied()
            if localNetworkBlocked {
                log.add(.warn, .net, "Local Network access is turned off for Open Bars", hint: Self.localNetworkHint)
            }
        }
    }

    private func logNetwork(_ summary: String) {
        guard summary != lastNetwork else { return }
        lastNetwork = summary
        log.add(.info, .net, "Network: \(summary)")
    }

    // MARK: Log

    /// Shows a result to the user and records it in the Connect log.
    private func report(_ text: String, _ level: LogLevel = .info, _ source: LogSource = .app, hint: String? = nil) {
        message = text
        log.add(level, source, text, hint: hint)
    }

    /// The Connect log's Clear and Detailed logging reach the tunnel
    /// extension while it runs.
    private func hookUpLog() {
        log.onClear = { [weak self] in
            Task { _ = await self?.send(LogMessage.clear) }
        }
        log.onDetailedChange = { [weak self] until in
            Task { _ = await self?.send(LogMessage.detailed(until: until)) }
        }
    }

    static var localNetworkHint: String {
        #if os(macOS)
        return "Turn on System Settings › Privacy & Security › Local Network › Open Bars."
        #else
        return "Turn on Settings › Privacy & Security › Local Network › Open Bars."
        #endif
    }

    /// This device's address on an iPhone hotspot (172.20.10.x), if any.
    nonisolated static func hotspotAddress() -> String? {
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let first = head else { return nil }
        defer { freeifaddrs(head) }
        for p in sequence(first: first, next: { $0.pointee.ifa_next }) {
            guard let sa = p.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET) else { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(sa, socklen_t(sa.pointee.sa_len), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                let a = String(cString: host)
                if a.hasPrefix("172.20.10."), a != phoneHost { return a }
            }
        }
        return nil
    }

    // MARK: State for the UI (Connect tab and the Mac menu bar)

    var isSetUp: Bool { pairing != nil && installed }
    var isConnected: Bool { status == .connected || status == .reasserting }

    var hotspotLabel: String {
        let name = hotspotName.trimmingCharacters(in: .whitespaces)
        return name.isEmpty ? "the hotspot" : "“\(name)”"
    }

    var statusTitle: String {
        guard pairing != nil else { return "Not set up" }
        guard installed else { return "VPN not added yet" }
        switch status {
        case .connected: return "Connected to \(hotspotLabel)"
        case .connecting: return "Connecting…"
        case .reasserting: return "Reconnecting…"
        case .disconnecting: return "Disconnecting…"
        default: return autoConnect ? "Waiting for \(hotspotLabel)" : "Off"
        }
    }

    var statusSubtitle: String {
        guard isSetUp else { return "Finish setup to use an iPhone's hotspot." }
        if isConnected { return "Traffic goes through the iPhone" }
        return autoConnect ? "Turns on by itself when you join it" : "Automatic connect is off"
    }

    func openLocalNetworkSettings() {
        #if os(macOS)
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_LocalNetwork") {
            NSWorkspace.shared.open(url)
        }
        #else
        if let url = URL(string: UIApplication.openSettingsURLString) {
            UIApplication.shared.open(url)
        }
        #endif
    }

    // MARK: Keys and names

    /// This device's WireGuard key: made here, kept in the Keychain. Only the
    /// public half is ever sent to the iPhone.
    private lazy var privateKey: String = {
        if let k = KeychainStore.get(Self.keyItem), Data(base64Encoded: k)?.count == 32 { return k }
        var bytes = [UInt8](repeating: 0, count: 32)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        bytes[0] &= 248
        bytes[31] = (bytes[31] & 127) | 64
        let k = Data(bytes).base64EncodedString()
        KeychainStore.set(Self.keyItem, k)
        return k
    }()

    var publicKey: String? {
        guard let raw = Data(base64Encoded: privateKey),
              let key = try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw) else { return nil }
        return key.publicKey.rawRepresentation.base64EncodedString()
    }

    /// Shown on the iPhone when asking to pair.
    static var deviceName: String {
        #if os(macOS)
        return Host.current().localizedName ?? "Mac"
        #else
        return UIDevice.current.name
        #endif
    }

    /// Sent when pairing so the iPhone can show the right icon.
    static var deviceKind: String {
        #if os(macOS)
        return "mac"
        #else
        return UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
        #endif
    }

    /// iPhone names use ’ (U+2019) but people type ', and SSIDs must match exactly.
    static func ssidVariants(_ name: String) -> [String] {
        var out = [name]
        for v in [name.replacingOccurrences(of: "'", with: "\u{2019}"),
                  name.replacingOccurrences(of: "\u{2019}", with: "'")] where !out.contains(v) {
            out.append(v)
        }
        return out
    }

    private func persist() {
        defaults.set(hotspotName, forKey: "hotspotName")
        defaults.set(useOnUSB, forKey: "useOnUSB")
        if let p = pairing, let data = try? JSONEncoder().encode(p) {
            defaults.set(data, forKey: "pairing")
        } else {
            defaults.removeObject(forKey: "pairing")
        }
    }

    // MARK: VPN configuration

    func reload() async {
        let all = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        manager = all.first {
            ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.providerBundleID
        }
        installed = manager != nil
        autoConnect = manager?.isOnDemandEnabled ?? false
        status = manager?.connection.status ?? .invalid
    }

    func onDemandRules() -> [NEOnDemandRule] {
        var rules: [NEOnDemandRule] = []
        let name = hotspotName.trimmingCharacters(in: .whitespaces)
        if !name.isEmpty {
            let wifi = NEOnDemandRuleConnect()
            wifi.interfaceTypeMatch = .wiFi
            wifi.ssidMatch = Self.ssidVariants(name)
            rules.append(wifi)
        }
        #if os(macOS)
        if useOnUSB {
            // A USB hotspot has no SSID: identify it by asking Open Bars on the phone.
            let usb = NEOnDemandRuleConnect()
            usb.interfaceTypeMatch = .ethernet
            usb.probeURL = URL(string: "http://\(Self.phoneHost):\(Self.webPort)/hotspot-check")
            rules.append(usb)
        }
        #endif
        let elsewhere = NEOnDemandRuleDisconnect()
        elsewhere.interfaceTypeMatch = .any
        rules.append(elsewhere)
        return rules
    }

    private func providerConfiguration(_ p: Pairing) -> [String: Any] {
        let wg: [String: Any] = ["private_key": privateKey, "server_public_key": p.serverPublicKey,
                                 "endpoint": p.endpoint, "keepalive": 25]
        let json = (try? JSONSerialization.data(withJSONObject: wg, options: [.sortedKeys]))
            .flatMap { String(data: $0, encoding: .utf8) } ?? "{}"
        let host = p.endpoint.split(separator: ":").first.map(String.init) ?? Self.phoneHost
        return [
            "wireguard": json, "address": p.address, "address6": p.address6, "dns": p.dns,
            "mtu": p.mtu, "endpoint_host": host,
            // Not read by the extension: makes rule changes count as a change below.
            "on_demand": "\(Self.ssidVariants(hotspotName.trimmingCharacters(in: .whitespaces)))|usb=\(useOnUSB)",
        ]
    }

    /// Creates or updates the VPN. Saves only when something changed, so the
    /// system doesn't ask to add the VPN again.
    func applyVPN(autoConnect wantAuto: Bool? = nil) async {
        guard let p = pairing else {
            report("Pair with your iPhone first.", .warn, .vpn)
            return
        }
        persist()
        let name = hotspotName.trimmingCharacters(in: .whitespaces)
        if name.isEmpty && !useOnUSB {
            report("Enter your hotspot name first.", .warn, .vpn,
                   hint: "The VPN turns on by itself only on the Wi‑Fi network with this name.")
            return
        }
        busy = true
        defer { busy = false }
        let m = manager ?? NETunnelProviderManager()
        if manager != nil { try? await m.loadFromPreferences() }
        let conf = providerConfiguration(p)
        let auto = wantAuto ?? (manager == nil ? true : m.isOnDemandEnabled)
        let old = (m.protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration ?? [:]
        if manager != nil, NSDictionary(dictionary: old).isEqual(to: conf), m.isOnDemandEnabled == auto, m.isEnabled {
            report("VPN settings are already up to date.", .info, .vpn)
            return
        }
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.providerBundleID
        proto.serverAddress = "\(name.isEmpty ? "iPhone" : name) · \(p.endpoint)"
        proto.providerConfiguration = conf
        proto.disconnectOnSleep = false
        m.protocolConfiguration = proto
        m.localizedDescription = "Open Bars"
        m.onDemandRules = onDemandRules()
        m.isOnDemandEnabled = auto
        m.isEnabled = true
        do {
            try await m.saveToPreferences()
            try await m.loadFromPreferences()
            manager = m
            log.add(.info, .vpn, "VPN saved: automatic \(auto ? "on" : "off"), hotspot “\(name)”\(useOnUSB ? " or USB" : ""), \(p.address) via \(p.endpoint)")
            message = auto
                ? "VPN ready. It turns on by itself whenever this device is on \(name.isEmpty ? "the hotspot" : "“\(name)”")."
                : "VPN saved. Automatic connect is off."
        } catch {
            report("Couldn't save the VPN: \(error.localizedDescription)", .error, .vpn,
                   hint: "Allow adding the VPN configuration when asked, then try again.")
        }
        await reload()
    }

    func setAutoConnect(_ on: Bool) async {
        await applyVPN(autoConnect: on)
    }

    func connectNow() {
        do {
            log.add(.info, .vpn, "Connecting (asked from the app)")
            try manager?.connection.startVPNTunnel(options: ["source": "the app" as NSString])
        } catch {
            report("Couldn't connect: \(error.localizedDescription)", .error, .vpn,
                   hint: "Check that the Open Bars VPN is still in the system's VPN settings, or set it up again with Diagnose Connection.")
        }
    }

    /// Turning it off by hand also turns off automatic connect; otherwise the
    /// on-demand rule would bring it straight back up on the hotspot.
    func disconnect() async {
        log.add(.info, .vpn, "Turning the VPN off by hand\(autoConnect ? " (automatic connect off too)" : "")")
        if autoConnect { await applyVPN(autoConnect: false) }
        manager?.connection.stopVPNTunnel()
    }

    /// Removes the VPN and the pairing (the key stays, so pairing again with
    /// the same iPhone is instant if it still has this device).
    func forget() async {
        busy = true
        defer { busy = false }
        if let m = manager {
            do {
                try await m.removeFromPreferences()
            } catch {
                report("Couldn't remove the VPN: \(error.localizedDescription)", .error, .vpn,
                       hint: "Remove the “Open Bars” VPN in the system's VPN settings instead.")
                return
            }
        }
        pairing = nil
        persist()
        report("Forgot the pairing and removed the VPN.", .info, .vpn)
        await reload()
    }

    // MARK: Status

    private func statusChanged(_ conn: NEVPNConnection?) async {
        guard let mine = manager?.connection, conn == nil || conn === mine else { return }
        let new = mine.status
        guard new != status else { return }
        status = new
        log.add(.info, .vpn, "VPN: \(Self.describe(new))")
        if new == .disconnected, #available(iOS 16.0, macOS 13.0, *) {
            mine.fetchLastDisconnectError { err in
                guard let err else { return }
                Task { @MainActor in
                    LogBook.connect.add(.error, .vpn, "VPN stopped: \(err.localizedDescription)",
                                        hint: "Run Diagnose Connection to find the step that fails.")
                }
            }
        }
        if new == .connected {
            // A freshly started extension logs only the basics until told otherwise.
            if log.isDetailed { _ = await send(LogMessage.detailed(until: log.detailedUntil)) }
            await poll()
        } else {
            tunnel = nil
            samples = []
        }
    }

    static func describe(_ s: NEVPNStatus) -> String {
        switch s {
        case .connected: return "connected"
        case .connecting: return "connecting"
        case .reasserting: return "reconnecting"
        case .disconnecting: return "disconnecting"
        case .disconnected: return "disconnected"
        case .invalid: return "not set up"
        @unknown default: return "status \(s.rawValue)"
        }
    }

    /// Current speed, averaged over the last 2 seconds.
    var currentRate: (down: Int64, up: Int64) {
        let recent = samples.suffix(2)
        guard !recent.isEmpty else { return (0, 0) }
        let n = Int64(recent.count)
        return (recent.reduce(0) { $0 + $1.down } / n, recent.reduce(0) { $0 + $1.up } / n)
    }

    /// While connected: tunnel stats, and the extension's new log entries
    /// added to the Connect log.
    private func poll() async {
        guard status == .connected || status == .reasserting else { return }
        if let s = await send("status"), let data = s.data(using: .utf8) {
            let dec = JSONDecoder()
            dec.keyDecodingStrategy = .convertFromSnakeCase
            tunnel = try? dec.decode(TunnelStatus.self, from: data)
        }
        if let h = await send("history:\(samples.last?.t ?? 0)"), let data = h.data(using: .utf8),
           let new = try? JSONDecoder().decode([TrafficSample].self, from: data), !new.isEmpty {
            var all = samples + new
            let oldest = (all.last?.t ?? 0) - 300
            all.removeAll { $0.t <= oldest }
            samples = all
        }
        await log.pull("extension") { await send(LogMessage.logs(after: $0)) }
    }

    private func send(_ message: String) async -> String? {
        guard let session = manager?.connection as? NETunnelProviderSession,
              session.status == .connected || session.status == .reasserting else { return nil }
        return await withCheckedContinuation { cont in
            do {
                try session.sendProviderMessage(Data(message.utf8)) { data in
                    cont.resume(returning: data.map { String(decoding: $0, as: UTF8.self) })
                }
            } catch {
                cont.resume(returning: nil)
            }
        }
    }

    // MARK: Talking to the iPhone

    struct PhoneInfo: Decodable {
        var hotspot_name: String?
        var pairing: Bool?
        var wireguard_port: Int?
    }

    private func phoneURL(_ path: String) -> URL {
        URL(string: "http://\(Self.phoneHost):\(Self.webPort)\(path)")!
    }

    /// Asks Open Bars on the phone for its settings (must be on its hotspot).
    private func fetchPhoneInfo() async throws -> PhoneInfo {
        var req = URLRequest(url: phoneURL("/client.json"))
        req.timeoutInterval = 8
        log.add(.info, .pair, "Asking the iPhone for its settings: \(req.url!.absoluteString)")
        let (data, _) = try await URLSession.shared.data(for: req)
        let info = try JSONDecoder().decode(PhoneInfo.self, from: data)
        log.add(.info, .pair, "iPhone says: hotspot “\(info.hotspot_name ?? "")”, tunnel port \(info.wireguard_port ?? 0), pairing \(info.pairing == true ? "supported" : "not supported")")
        return info
    }

    /// Error text with its codes, for the log ("Could not connect to the
    /// server (NSURLErrorDomain -1004, kCFErrorDomainCFNetwork -1004, stream 61)").
    static func describe(_ error: Error) -> String {
        let e = error as NSError
        var s = "\(e.localizedDescription) (\(e.domain) \(e.code)"
        if let u = e.userInfo[NSUnderlyingErrorKey] as? NSError { s += ", \(u.domain) \(u.code)" }
        if let c = e.userInfo["_kCFStreamErrorCodeKey"] as? Int { s += ", stream \(c)" }
        return s + ")"
    }

    /// True when the system blocks Open Bars from the local network (Local
    /// Network privacy). Every request to the iPhone then fails at once,
    /// which otherwise looks like "nothing is listening".
    static func localNetworkDenied() async -> Bool {
        await withCheckedContinuation { cont in
            let q = DispatchQueue(label: "dev.aayy.openbars.localnet")
            let conn = NWConnection(host: NWEndpoint.Host(phoneHost),
                                    port: NWEndpoint.Port(rawValue: UInt16(webPort)) ?? 8080, using: .tcp)
            let finished = OSAllocatedUnfairLock(initialState: false)
            let finish: @Sendable () -> Void = {
                // The state handler and the timeout can both fire; answer once.
                guard finished.withLock({ done in defer { done = true }; return !done }) else { return }
                let denied = conn.currentPath?.unsatisfiedReason == .localNetworkDenied
                conn.cancel()
                cont.resume(returning: denied)
            }
            conn.stateUpdateHandler = { state in
                switch state {
                case .ready, .waiting, .failed: finish()
                default: break
                }
            }
            conn.start(queue: q)
            q.asyncAfter(deadline: .now() + 3) { finish() }
        }
    }

    /// Logs a failed request to the phone (with its URL and error) and
    /// returns what the user should do next, which is also the entry's hint.
    private func phoneRequestFailed(_ method: String, _ url: URL?, _ error: Error) async -> String {
        let advice = await phoneUnreachableMessage(error)
        log.add(.error, .pair, "No answer from the iPhone: \(method) \(Self.loggable(url)): \(Self.describe(error))", hint: advice)
        return advice
    }

    /// Logs an HTTP error answer from the phone.
    private func phoneAnswered(_ method: String, _ url: URL?, _ code: Int, _ level: LogLevel, hint: String) {
        log.add(level, .pair, "\(method) \(Self.loggable(url)): HTTP \(code)", hint: hint)
    }

    /// A URL for the log: public keys in the query shortened.
    private static func loggable(_ url: URL?) -> String {
        guard let url, var c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return "?" }
        let items = c.queryItems ?? []
        c.query = nil
        let base = c.string ?? url.absoluteString
        guard !items.isEmpty else { return base }
        return base + "?" + items.map { "\($0.name)=\(LogText.shortKey($0.value ?? ""))" }.joined(separator: "&")
    }

    /// Turns a failed request to the phone into what the user should do next.
    private func phoneUnreachableMessage(_ error: Error) async -> String {
        if await Self.localNetworkDenied() {
            log.add(.error, .pair, "Local Network access is turned off for Open Bars", hint: Self.localNetworkHint)
            #if os(macOS)
            return "Open Bars isn't allowed to reach devices on this network. Turn on System Settings › Privacy & Security › Local Network › Open Bars, then try again."
            #else
            return "Open Bars isn't allowed to reach devices on this network. Turn on Settings › Privacy & Security › Local Network › Open Bars, then try again."
            #endif
        }
        switch (error as? URLError)?.code {
        case .timedOut?:
            return "The iPhone didn't answer. Open Open Bars on the iPhone, make sure it's sharing, then try again."
        case .cannotConnectToHost?:
            return "The iPhone is there, but Open Bars isn't sharing. Tap Start in Open Bars on the iPhone."
        default:
            return "Couldn't reach the iPhone at \(Self.phoneHost). Join its hotspot (Wi‑Fi or USB cable) first."
        }
    }

    /// Asks the iPhone to add this device; someone has to tap Allow there.
    func pair() async {
        guard let pub = publicKey else {
            report("Couldn't create this device's key.", .error, .pair,
                   hint: "Quit and reopen Open Bars, then try again.")
            return
        }
        busy = true
        defer { busy = false }
        let info: PhoneInfo
        do {
            info = try await fetchPhoneInfo()
        } catch {
            message = await phoneRequestFailed("GET", phoneURL("/client.json"), error)
            return
        }
        guard info.pairing == true else {
            report("Open Bars on the iPhone is too old to pair. Update it there first.", .error, .pair)
            return
        }
        if let n = info.hotspot_name, !n.isEmpty { hotspotName = n }

        waitingForApproval = true
        defer { waitingForApproval = false }
        report("Tap Allow on the iPhone to let “\(Self.deviceName)” connect.", .info, .pair)
        var req = URLRequest(url: phoneURL("/api/pair"))
        req.httpMethod = "POST"
        req.timeoutInterval = 100 // the iPhone waits up to 90 s for Allow
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["name": Self.deviceName, "kind": Self.deviceKind, "public_key": pub])
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
            switch code {
            case 200:
                let p = try JSONDecoder().decode(Pairing.self, from: data)
                pairing = p
                if !p.hotspotName.isEmpty { hotspotName = p.hotspotName }
                persist()
                log.add(.info, .pair, "Paired as “\(p.deviceName)”: \(p.address)\(p.address6.isEmpty ? "" : ", \(p.address6)"), iPhone at \(p.endpoint)")
                await applyVPN(autoConnect: true)
            case 403:
                message = "The iPhone didn't allow this device."
                phoneAnswered("POST", req.url, code, .warn, hint: "Pair again and tap Allow on the iPhone.")
            case 408:
                message = "Nobody tapped Allow on the iPhone in time. Keep Open Bars open on the iPhone and try again."
                phoneAnswered("POST", req.url, code, .warn, hint: "Keep Open Bars open on the iPhone and tap Allow within 90 seconds.")
            default:
                message = "Pairing failed: the iPhone answered HTTP \(code)."
                phoneAnswered("POST", req.url, code, .error, hint: "Update Open Bars on the iPhone and on this device, then pair again.")
            }
        } catch {
            message = await phoneRequestFailed("POST", req.url, error)
        }
    }

    /// Diagnose: checks each link the tunnel depends on, in order, and stops
    /// at the first one that's broken (later ones can't be checked yet).
    func runDiagnostics() async {
        guard !diagnosing else { return }
        diagnosing = true
        defer { diagnosing = false }
        var out: [DiagnosticCheck] = []
        func add(_ c: DiagnosticCheck) {
            out.append(c)
            diagnostics = out
            let level: LogLevel = c.state == .failed ? .error : (c.state == .warning ? .warn : .info)
            log.add(level, .app, "Diagnose: \(c.title) — \(c.state) — \(c.detail)",
                    hint: c.fix?.title.map { "Tap “\($0)” in Diagnose Connection." })
        }
        diagnostics = []
        #if os(macOS)
        let join = "Join the iPhone's hotspot over Wi‑Fi or a USB cable."
        #else
        let join = "Join the iPhone's hotspot in Wi‑Fi settings."
        #endif

        guard let addr = Self.hotspotAddress() else {
            add(.init(id: "hotspot", title: "On an iPhone hotspot", state: .failed, detail: join, fix: .joinHotspot))
            return
        }
        add(.init(id: "hotspot", title: "On an iPhone hotspot", state: .ok, detail: "This device is \(addr)."))

        localNetworkBlocked = await Self.localNetworkDenied()
        guard !localNetworkBlocked else {
            add(.init(id: "localnet", title: "Local Network access", state: .failed,
                      detail: "Open Bars isn't allowed to reach devices on this network.", fix: .openLocalNetwork))
            return
        }
        add(.init(id: "localnet", title: "Local Network access", state: .ok, detail: "Allowed."))

        let info: PhoneInfo
        do {
            info = try await fetchPhoneInfo()
        } catch {
            add(.init(id: "sharing", title: "Open Bars sharing on the iPhone", state: .failed,
                      detail: await phoneRequestFailed("GET", phoneURL("/client.json"), error), fix: .startSharing))
            return
        }
        let phoneName = info.hotspot_name ?? ""
        add(.init(id: "sharing", title: "Open Bars sharing on the iPhone", state: .ok,
                  detail: phoneName.isEmpty ? "It's answering." : "Sharing as “\(phoneName)”."))

        guard let p = pairing, let pub = publicKey else {
            add(.init(id: "paired", title: "Paired with this iPhone", state: .failed,
                      detail: "This device isn't paired yet.", fix: .pair))
            return
        }
        var comps = URLComponents(url: phoneURL("/api/pair"), resolvingAgainstBaseURL: false)!
        comps.queryItems = [URLQueryItem(name: "public_key", value: pub)]
        var req = URLRequest(url: comps.url!)
        req.timeoutInterval = 8
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            switch (resp as? HTTPURLResponse)?.statusCode ?? 0 {
            case 200:
                let phone = try JSONDecoder().decode(Pairing.self, from: data)
                if !phone.sameTunnel(as: p) {
                    log.add(.info, .pair, "The iPhone's settings changed since pairing; updating the VPN")
                    pairing = phone
                    persist()
                    await applyVPN()
                }
                add(.init(id: "paired", title: "Paired with this iPhone", state: .ok,
                          detail: "Known as “\(phone.deviceName)”, \(phone.address)."))
            case 404:
                phoneAnswered("GET", req.url, 404, .warn, hint: "Pair again: the iPhone doesn't know this device.")
                add(.init(id: "paired", title: "Paired with this iPhone", state: .failed,
                          detail: "The iPhone doesn't have this device (removed there, or a different iPhone).", fix: .pair))
                return
            case let code:
                phoneAnswered("GET", req.url, code, .error, hint: "Update Open Bars on the iPhone and on this device.")
                add(.init(id: "paired", title: "Paired with this iPhone", state: .failed,
                          detail: "The iPhone answered HTTP \(code). Is Open Bars up to date there?"))
                return
            }
        } catch {
            log.add(.error, .pair, "No answer from the iPhone: GET \(Self.loggable(req.url)): \(Self.describe(error))",
                    hint: "Is Open Bars sharing on the iPhone, and is this device on its hotspot?")
            add(.init(id: "paired", title: "Paired with this iPhone", state: .failed, detail: Self.describe(error)))
            return
        }

        await reload()
        guard installed else {
            add(.init(id: "vpn", title: "VPN added", state: .failed, detail: "The Open Bars VPN isn't on this device.", fix: .installVPN))
            return
        }
        add(.init(id: "vpn", title: "VPN added", state: .ok, detail: "The “Open Bars” VPN is installed."))

        let name = hotspotName.trimmingCharacters(in: .whitespaces)
        if !phoneName.isEmpty, !Self.ssidVariants(name).contains(phoneName) {
            add(.init(id: "name", title: "Hotspot name", state: .warning,
                      detail: "The VPN waits for “\(name)”, but this iPhone's hotspot is “\(phoneName)”.", fix: .useHotspotName(phoneName)))
        } else {
            add(.init(id: "name", title: "Hotspot name", state: .ok, detail: "Matches “\(phoneName.isEmpty ? name : phoneName)”."))
        }

        if autoConnect {
            add(.init(id: "auto", title: "Connects automatically", state: .ok, detail: "On \(hotspotLabel)."))
        } else {
            add(.init(id: "auto", title: "Connects automatically", state: .warning, detail: "Automatic connect is off.", fix: .turnOnAuto))
        }

        if isConnected {
            await poll()
            let now = Int64(Date().timeIntervalSince1970)
            if let t = tunnel, t.lastHandshake > 0, now - t.lastHandshake < 180 {
                add(.init(id: "tunnel", title: "Tunnel working", state: .ok,
                          detail: "Last handshake \(formatAgo(t.lastHandshake)); \(formatBytes(t.rxBytes + t.txBytes)) so far."))
            } else {
                add(.init(id: "tunnel", title: "Tunnel working", state: .failed,
                          detail: "The VPN is on, but the iPhone isn't answering on \(p.endpoint)."))
            }
        } else {
            add(.init(id: "tunnel", title: "Tunnel working", state: .warning, detail: "The VPN isn't connected right now.", fix: .connect))
        }
    }

    /// Runs a Diagnose fix, then checks again.
    func apply(_ fix: DiagnosticCheck.Fix) async {
        switch fix {
        case .joinHotspot, .startSharing: break
        case .openLocalNetwork: openLocalNetworkSettings(); return
        case .pair: await pair()
        case .installVPN: await applyVPN(autoConnect: true)
        case .useHotspotName(let n):
            hotspotName = n
            await applyVPN()
        case .turnOnAuto: await setAutoConnect(true)
        case .connect:
            connectNow()
            try? await Task.sleep(nanoseconds: 3_000_000_000)
        }
        await runDiagnostics()
    }
}

/// One line of the Diagnose checklist.
struct DiagnosticCheck: Identifiable, Equatable {
    enum State: String { case ok, warning, failed }
    enum Fix: Equatable {
        case joinHotspot, openLocalNetwork, startSharing, pair, installVPN, turnOnAuto, connect
        case useHotspotName(String)

        /// Button title, or nil when the user has to do it elsewhere.
        var title: String? {
            switch self {
            case .joinHotspot, .startSharing: return nil
            case .openLocalNetwork: return "Open Settings"
            case .pair: return "Pair Again"
            case .installVPN: return "Add VPN"
            case .turnOnAuto: return "Turn On"
            case .connect: return "Connect Now"
            case .useHotspotName(let n): return "Use “\(n)”"
            }
        }
    }

    var id: String
    var title: String
    var state: State
    var detail: String
    var fix: Fix? = nil
}
