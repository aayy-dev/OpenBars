import Foundation
import NetworkExtension

// The log's vocabulary, shared by both apps (iOS and Mac) and both tunnel
// extensions: entry model, levels, sources, the app <-> extension messages
// and what a log says about this device. Foundation only, no UI.

/// How serious an entry is. Unknown levels (from a newer Go side) read as info.
enum LogLevel: String, Codable, CaseIterable, Comparable, Sendable {
    case debug, info, warn, error

    init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = LogLevel(rawValue: raw.lowercased()) ?? .info
    }

    /// Upper case, padded to 5 for the text format.
    var label: String {
        switch self {
        case .debug: return "DEBUG"
        case .info: return "INFO "
        case .warn: return "WARN "
        case .error: return "ERROR"
        }
    }

    static func < (a: LogLevel, b: LogLevel) -> Bool {
        allCases.firstIndex(of: a)! < allCases.firstIndex(of: b)!
    }
}

/// The component an entry comes from: a short lowercase name. Open-ended,
/// because the Go side may add sources.
struct LogSource: RawRepresentable, Codable, Hashable, Sendable, ExpressibleByStringLiteral {
    let rawValue: String
    init(rawValue: String) { self.rawValue = rawValue }
    init(stringLiteral value: String) { rawValue = value }

    // Both sides
    static let app: LogSource = "app"
    static let wg: LogSource = "wg"
    static let pair: LogSource = "pair"
    static let ext: LogSource = "ext"
    // Share (engine, app, background extension)
    static let share: LogSource = "share"
    static let tcp: LogSource = "tcp"
    static let udp: LogSource = "udp"
    static let dns: LogSource = "dns"
    static let icmp: LogSource = "icmp"
    static let egress: LogSource = "egress"
    static let nat64: LogSource = "nat64"
    static let web: LogSource = "web"
    static let usage: LogSource = "usage"
    static let diag: LogSource = "diag"
    // Connect
    static let vpn: LogSource = "vpn"
    static let net: LogSource = "net"
}

/// One log entry. On disk (JSON Lines) and from the Go side's LogsJSON it uses
/// the same keys: seq, t (Unix milliseconds), lvl, src, msg, hint, rep.
struct LogEntry: Codable, Identifiable, Hashable, Sendable {
    /// Unique within its log. (A Go ring's own numbers are only used to know
    /// what has been fetched; entries get a new number when stored.)
    var seq: Int64
    var date: Date
    var level: LogLevel
    var source: LogSource
    var message: String
    /// What to try, on warnings and errors.
    var hint: String?
    /// > 0 only on a repeat summary: this many more identical occurrences
    /// since the previous identical entry (shown as "(×N more)"). Entries are
    /// never changed after being added; repeats get a summary entry instead.
    var repeats: Int

    var id: Int64 { seq }

    init(seq: Int64 = 0, date: Date = Date(), level: LogLevel, source: LogSource,
         message: String, hint: String? = nil, repeats: Int = 0) {
        self.seq = seq
        self.date = date
        self.level = level
        self.source = source
        self.message = message
        self.hint = (hint?.isEmpty ?? true) ? nil : hint
        self.repeats = repeats
    }

    /// The instant in whole milliseconds, as the Go side stores it.
    var millis: Int64 { Int64((date.timeIntervalSince1970 * 1000).rounded()) }

    enum CodingKeys: String, CodingKey {
        case seq, date = "t", level = "lvl", source = "src", message = "msg", hint, repeats = "rep"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decodeIfPresent(Int64.self, forKey: .seq) ?? 0
        let ms = try c.decodeIfPresent(Int64.self, forKey: .date) ?? 0
        date = Date(timeIntervalSince1970: Double(ms) / 1000)
        level = try c.decodeIfPresent(LogLevel.self, forKey: .level) ?? .info
        source = try c.decodeIfPresent(LogSource.self, forKey: .source) ?? .app
        message = try c.decodeIfPresent(String.self, forKey: .message) ?? ""
        let h = try c.decodeIfPresent(String.self, forKey: .hint) ?? ""
        hint = h.isEmpty ? nil : h
        repeats = try c.decodeIfPresent(Int.self, forKey: .repeats) ?? 0
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(seq, forKey: .seq)
        try c.encode(millis, forKey: .date)
        try c.encode(level, forKey: .level)
        try c.encode(source, forKey: .source)
        try c.encode(message, forKey: .message)
        if let hint, !hint.isEmpty { try c.encode(hint, forKey: .hint) }
        if repeats > 0 { try c.encode(repeats, forKey: .repeats) }
    }
}

/// One answer from a Go ring's LogsJSON(after).
struct GoLogPage: Decodable {
    /// Seq of the last entry returned (or `after` when none).
    var next: Int64
    /// Oldest seq still in the ring.
    var first: Int64
    /// When the ring was created (Unix ms; 0 if not reported): a different
    /// value means its process restarted and numbering started over.
    var boot: Int64
    /// Entries with seq > after, oldest first.
    var entries: [LogEntry]

    enum CodingKeys: String, CodingKey { case next, first, boot, entries }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        next = try c.decodeIfPresent(Int64.self, forKey: .next) ?? 0
        first = try c.decode(Int64.self, forKey: .first) // required: tells this format apart
        boot = try c.decodeIfPresent(Int64.self, forKey: .boot) ?? 0
        entries = try c.decodeIfPresent([LogEntry].self, forKey: .entries) ?? []
    }

    static func decode(_ json: String?) -> GoLogPage? {
        guard let data = json?.data(using: .utf8), !data.isEmpty else { return nil }
        return try? JSONDecoder().decode(GoLogPage.self, from: data)
    }
}

/// Messages the apps send their tunnel extension about logs (over
/// sendProviderMessage), next to "status", "history:" and so on.
enum LogMessage {
    /// "logs:<after>" → the ring's LogsJSON(after).
    static let logsPrefix = "logs:"
    /// Empties the extension's ring.
    static let clear = "clear-logs"
    /// "detailed:<unix seconds>": detailed logging on until then; 0 = off.
    static let detailedPrefix = "detailed:"

    static func logs(after: Int64) -> String { logsPrefix + String(after) }

    static func detailed(until: Date?) -> String {
        detailedPrefix + String(Int64(until?.timeIntervalSince1970 ?? 0))
    }

    /// The "after" of a logs message, or nil if it isn't one.
    static func logsAfter(_ msg: String) -> Int64? {
        guard msg.hasPrefix(logsPrefix) else { return msg == "logs" ? 0 : nil }
        return Int64(msg.dropFirst(logsPrefix.count)) ?? 0
    }

    static func isDetailed(_ msg: String) -> Bool { msg.hasPrefix(detailedPrefix) }

    /// When detailed logging should end, nil for off.
    static func detailedUntil(_ msg: String) -> Date? {
        guard let s = Double(msg.dropFirst(detailedPrefix.count)), s > 0 else { return nil }
        let until = Date(timeIntervalSince1970: s)
        return until > Date() ? until : nil
    }
}

enum LogKeys {
    /// App group shared by the iPhone app and the background-sharing extension.
    static let appGroup = "group.dev.aayy.openbars"
    /// In the app group's defaults: until when the Share side logs in detail
    /// (Unix seconds), so the background extension picks it up when it starts.
    static let shareDetailedUntil = "shareDetailedLogsUntil"
    /// In the app group's container: the background extension's log, written
    /// when it stops so the app can read the last entries.
    static let shareExtensionFinalLog = "share-extension-log.json"

    static func shareDetailedUntil(in defaults: UserDefaults) -> Date? {
        let s = defaults.double(forKey: shareDetailedUntil)
        guard s > 0 else { return nil }
        let until = Date(timeIntervalSince1970: s)
        return until > Date() ? until : nil
    }

    static var shareExtensionFinalLogURL: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup)?
            .appendingPathComponent(shareExtensionFinalLog)
    }
}

/// Detailed (debug) logging inside a tunnel extension: on until the time the
/// app chose, then off by itself, even if the app isn't running by then.
final class DetailedLoggingTimer {
    private let apply: (Bool) -> Void
    private let queue = DispatchQueue(label: "dev.aayy.openbars.detailed-logging")
    private var work: DispatchWorkItem?

    init(apply: @escaping (Bool) -> Void) {
        self.apply = apply
    }

    func set(until: Date?) {
        queue.async { [self] in
            work?.cancel()
            work = nil
            guard let until, until > Date() else {
                apply(false)
                return
            }
            apply(true)
            let w = DispatchWorkItem { [weak self] in self?.apply(false) }
            work = w
            queue.asyncAfter(deadline: .now() + until.timeIntervalSinceNow, execute: w)
        }
    }
}

/// What a log says about this device and app (session entries and export headers).
enum AppInfo {
    /// "1.0.0 (1)"
    static var version: String {
        let info = Bundle.main.infoDictionary ?? [:]
        let short = info["CFBundleShortVersionString"] as? String ?? "?"
        let build = info["CFBundleVersion"] as? String ?? "?"
        return "\(short) (\(build))"
    }

    /// "iOS 26.1", "iPadOS 26.1" or "macOS 15.5"
    static var system: String {
        let v = ProcessInfo.processInfo.operatingSystemVersion
        let number = v.patchVersion > 0
            ? "\(v.majorVersion).\(v.minorVersion).\(v.patchVersion)"
            : "\(v.majorVersion).\(v.minorVersion)"
        #if os(macOS)
        return "macOS \(number)"
        #else
        return (model.hasPrefix("iPad") ? "iPadOS " : "iOS ") + number
        #endif
    }

    /// Model identifier, e.g. "iPhone17,1" or "Mac14,2".
    static var model: String {
        #if os(macOS)
        var size = 0
        sysctlbyname("hw.model", nil, &size, nil, 0)
        guard size > 0 else { return "Mac" }
        var bytes = [CChar](repeating: 0, count: size)
        sysctlbyname("hw.model", &bytes, &size, nil, 0)
        return String(cString: bytes)
        #else
        if let sim = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"] {
            return sim + " (Simulator)"
        }
        var u = utsname()
        uname(&u)
        return withUnsafeBytes(of: &u.machine) { raw in
            String(decoding: raw.prefix { $0 != 0 }, as: UTF8.self)
        }
        #endif
    }

    /// "Open Bars 1.0.0 (1), iOS 26.1, iPhone17,1"
    static var summary: String { "Open Bars \(version), \(system), \(model)" }
}

/// Wording shared by the app and the extensions.
enum LogText {
    /// A public key shortened for the log ("AbCdEfGh…"). Private keys are never logged.
    static func shortKey(_ key: String) -> String {
        key.count > 8 ? String(key.prefix(8)) + "…" : key
    }

    /// The Share session entry, logged just before the engine starts. It
    /// carries what the engine can't know (app version, how sharing runs);
    /// the engine's own start entry follows with the settings, egress and VPN.
    static func shareSessionStart(mode: String) -> String {
        "Starting sharing \(mode): \(AppInfo.summary)"
    }
}

extension NEProviderStopReason {
    /// Why the system stopped a tunnel extension, for the log.
    var logDescription: String {
        switch self {
        case .userInitiated: return "turned off by the user"
        case .noNetworkAvailable: return "no network"
        case .unrecoverableNetworkChange: return "network changed"
        case .configurationDisabled: return "VPN configuration disabled"
        case .configurationRemoved: return "VPN configuration removed"
        case .superceded: return "another VPN started"
        case .userLogout: return "user logged out"
        case .userSwitch: return "user switched"
        case .connectionFailed: return "connection failed"
        case .sleep: return "sleep"
        case .appUpdate: return "app update"
        case .idleTimeout: return "idle"
        case .configurationFailed: return "configuration failed"
        case .providerFailed: return "provider failed"
        case .authenticationCanceled: return "authentication canceled"
        case .none: return "no reason given"
        default: return "reason \(rawValue)"
        }
    }
}
