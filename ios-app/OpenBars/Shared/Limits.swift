import Foundation

// Data limits: what the app saves (Device.limit, AppSettings) and what the
// engine reports. The engine decides and enforces (core/engine/limits.go);
// these mirror its JSON.

enum LimitPeriod: String, Codable, CaseIterable, Identifiable, Sendable {
    case hour, day, week, month, total
    var id: String { rawValue }

    /// "Per day" in a picker.
    var title: String {
        switch self {
        case .hour: return "Per hour"
        case .day: return "Per day"
        case .week: return "Per week"
        case .month: return "Per month"
        case .total: return "In total"
        }
    }

    /// "daily" in a sentence.
    var adjective: String {
        switch self {
        case .hour: return "hourly"
        case .day: return "daily"
        case .week: return "weekly"
        case .month: return "monthly"
        case .total: return "total"
        }
    }

    /// "Today" above a usage bar.
    var current: String {
        switch self {
        case .hour: return "This hour"
        case .day: return "Today"
        case .week: return "This week"
        case .month: return "This month"
        case .total: return "In total"
        }
    }

    /// What a limit like this does over time.
    var resetNote: String {
        switch self {
        case .hour: return "Starts over every hour."
        case .day: return "Starts over at midnight."
        case .week: return "Starts over at the start of each week."
        case .month: return "Starts over on the billing day in Settings › Data Limits."
        case .total: return "Never starts over by itself. Reset it or give more when it's used up."
        }
    }
}

enum LimitAction: String, Codable, CaseIterable, Identifiable, Sendable {
    case block, slow, notify
    var id: String { rawValue }

    var title: String {
        switch self {
        case .block: return "Block"
        case .slow: return "Slow down"
        case .notify: return "Only notify"
        }
    }

    var detail: String {
        switch self {
        case .block: return "No internet until the limit starts over or you give more."
        case .slow: return "Internet keeps working, but slowly."
        case .notify: return "Nothing changes for the device. You get a notification."
        }
    }
}

/// One allowance: bytes per period.
struct LimitRule: Codable, Hashable, Identifiable, Sendable {
    var period: LimitPeriod
    var bytes: Int64
    /// A total limit counts from here (unix seconds).
    var start: Int64? = nil

    var id: String { "\(period.rawValue)-\(bytes)-\(start ?? 0)" }

    /// "1 GB per day".
    var summary: String {
        let amount = ByteCountFormatter.string(fromByteCount: bytes, countStyle: .decimal)
        switch period {
        case .total: return "\(amount) in total"
        default: return "\(amount) per \(period.rawValue)"
        }
    }
}

/// More time or data for a device past its limits.
struct LimitGrant: Codable, Hashable, Identifiable, Sendable {
    var id: String = UUID().uuidString
    /// This much more data from `given`.
    var bytes: Int64? = nil
    /// No limit until this time (unix seconds).
    var until: Int64? = nil
    var given: Int64 = Int64(Date().timeIntervalSince1970)
    /// The device's request this answers.
    var ask: String? = nil
}

/// Everything limiting one device (saved with the device).
struct DeviceLimit: Codable, Hashable, Sendable {
    var rules: [LimitRule] = []
    /// nil = the default in Settings.
    var action: LimitAction? = nil
    var slowKbps: Int? = nil
    var paused: Bool = false
    var grants: [LimitGrant] = []
    /// Usage before this (unix seconds) doesn't count.
    var reset: Int64? = nil
    /// Earlier keys of the same device: their usage counts too.
    var keys: [String]? = nil

    var isEmpty: Bool { rules.isEmpty && !paused && grants.isEmpty && action == nil }
}

/// Speeds offered for "Slow down".
let slowSpeeds: [Int] = [256, 512, 1000, 2000, 5000]

func speedTitle(_ kbps: Int) -> String {
    kbps >= 1000 ? "\(kbps / 1000) Mbps" : "\(kbps) kbps"
}

// MARK: - From the engine (decoded with .convertFromSnakeCase)

struct RuleStatus: Codable, Hashable, Sendable {
    var period: String = ""
    var bytes: Int64 = 0
    var used: Int64 = 0
    /// Unix seconds when it starts over; 0 = never.
    var resets: Int64 = 0

    var limitPeriod: LimitPeriod { LimitPeriod(rawValue: period) ?? .total }
    var isOver: Bool { bytes > 0 && used >= bytes }
    var fraction: Double { bytes > 0 ? min(1, Double(used) / Double(bytes)) : 0 }
}

struct GrantStatus: Codable, Hashable, Sendable {
    var id: String = ""
    var bytes: Int64? = nil
    var left: Int64? = nil
    var until: Int64? = nil
    var active: Bool = false
}

/// A device's limits as of now.
struct LimitStatus: Codable, Hashable, Sendable {
    /// "", warn, limited, extended or paused.
    var state: String = ""
    var action: String? = nil
    var rules: [RuleStatus]? = nil
    var grants: [GrantStatus]? = nil
    /// The all-devices limit is what's limiting it.
    var cap: Bool? = nil

    var isLimited: Bool { state == "limited" }
    var isPaused: Bool { state == "paused" }
    var limitAction: LimitAction? { action.flatMap(LimitAction.init(rawValue:)) }

    /// A short word for the device list, nil when nothing's going on.
    var badge: String? {
        switch state {
        case "paused": return "Paused"
        case "limited":
            switch limitAction {
            case .slow: return "Slowed"
            case .notify: return "Over limit"
            default: return "Blocked"
            }
        case "extended": return "Extra time"
        case "warn": return "Near limit"
        default: return nil
        }
    }
}

/// A device asking for more time or data (from the status page).
struct AskRequest: Codable, Hashable, Identifiable, Sendable {
    var id: String = ""
    var key: String = ""
    var name: String = ""
    var minutes: Int? = nil
    var today: Bool? = nil
    var bytes: Int64? = nil
    var message: String? = nil
    var since: Int64 = 0
    var state: String = ""
    var answered: Int64? = nil

    /// "15 more minutes", "the rest of today", "1 GB more".
    var what: String {
        if today == true { return "the rest of today" }
        if let m = minutes, m > 0 { return m == 60 ? "1 more hour" : "\(m) more minutes" }
        if let b = bytes, b > 0 { return ByteCountFormatter.string(fromByteCount: b, countStyle: .decimal) + " more" }
        return "more"
    }

    /// The grant that gives what was asked for.
    func grant(now: Date = Date()) -> LimitGrant {
        let t = Int64(now.timeIntervalSince1970)
        var g = LimitGrant(given: t, ask: id)
        if today == true {
            g.until = endOfToday(now)
        } else if let m = minutes, m > 0 {
            g.until = t + Int64(m) * 60
        } else if let b = bytes, b > 0 {
            g.bytes = b
        }
        return g
    }
}

/// Midnight tonight (unix seconds), local time.
func endOfToday(_ now: Date = Date()) -> Int64 {
    let cal = Calendar.current
    let midnight = cal.startOfDay(for: cal.date(byAdding: .day, value: 1, to: now) ?? now)
    return Int64(midnight.timeIntervalSince1970)
}

/// A device asking to pair.
struct PairRequest: Codable, Hashable, Identifiable, Sendable {
    var name: String = ""
    var kind: String = "" // mac, ipad, iphone
    var publicKey: String = ""
    var from: String = ""
    var since: Int64 = 0
    var id: String { publicKey }
}

/// Something worth a notification, from the engine's EventsJSON.
struct EngineEvent: Codable, Sendable {
    var seq: Int64 = 0
    var t: Int64 = 0
    var kind: String = ""
    var key: String? = nil
    var name: String? = nil
    var period: String? = nil
    var bytes: Int64? = nil
    var used: Int64? = nil
    var action: String? = nil
    var pair: PairRequest? = nil
    var ask: AskRequest? = nil
}

struct EngineEvents: Codable, Sendable {
    var next: Int64 = 0
    var events: [EngineEvent] = []

    static func decode(_ json: String) -> EngineEvents? {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return json.data(using: .utf8).flatMap { try? d.decode(EngineEvents.self, from: $0) }
    }
}

// MARK: - Notification settings

/// Which notifications Open Bars posts on the sharing iPhone.
struct NotificationPrefs: Codable, Equatable, Sendable {
    var pairing = true
    var limits = true
    var asks = true
    var stopped = true
    var errors = true
    var paused = true

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = NotificationPrefs()
        pairing = try c.decodeIfPresent(Bool.self, forKey: .pairing) ?? d.pairing
        limits = try c.decodeIfPresent(Bool.self, forKey: .limits) ?? d.limits
        asks = try c.decodeIfPresent(Bool.self, forKey: .asks) ?? d.asks
        stopped = try c.decodeIfPresent(Bool.self, forKey: .stopped) ?? d.stopped
        errors = try c.decodeIfPresent(Bool.self, forKey: .errors) ?? d.errors
        paused = try c.decodeIfPresent(Bool.self, forKey: .paused) ?? d.paused
    }
}
