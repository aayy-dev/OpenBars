import Foundation
import UserNotifications

/// Notifications on the sharing iPhone. The background-sharing extension
/// posts them from the engine's events; the app answers their buttons
/// (AppDelegate). Which ones are on is in SharedState.notificationPrefs.
enum Notifier {
    // Categories (the buttons a notification has).
    static let pairCategory = "pair"
    static let pairReviewCategory = "pair-review"
    static let limitCategory = "limit"
    static let askCategory = "ask"

    // Buttons.
    static let allowAction = "allow"
    static let denyAction = "deny"
    static let give15Action = "give15"
    static let give60Action = "give60"
    static let giveTodayAction = "giveToday"
    static let approveAction = "approve"
    static let declineAction = "decline"

    /// userInfo keys.
    enum Key {
        static let kind = "kind"
        static let device = "device" // public key
        static let pair = "pair"     // PairRequest JSON
        static let ask = "ask"       // AskRequest JSON
    }

    /// Registers the buttons. Both the app and the extension call it.
    static func registerCategories() {
        let allow = UNNotificationAction(identifier: allowAction, title: "Allow", options: [])
        let review = UNNotificationAction(identifier: allowAction, title: "Review…", options: [.foreground])
        let deny = UNNotificationAction(identifier: denyAction, title: "Don't Allow", options: [.destructive])
        let give15 = UNNotificationAction(identifier: give15Action, title: "Give 15 More Minutes", options: [])
        let give60 = UNNotificationAction(identifier: give60Action, title: "Give 1 More Hour", options: [])
        let giveToday = UNNotificationAction(identifier: giveTodayAction, title: "No Limit Until Midnight", options: [])
        let approve = UNNotificationAction(identifier: approveAction, title: "Approve", options: [])
        let decline = UNNotificationAction(identifier: declineAction, title: "Decline", options: [.destructive])
        UNUserNotificationCenter.current().setNotificationCategories([
            UNNotificationCategory(identifier: pairCategory, actions: [allow, deny], intentIdentifiers: []),
            UNNotificationCategory(identifier: pairReviewCategory, actions: [review, deny], intentIdentifiers: []),
            UNNotificationCategory(identifier: limitCategory, actions: [give15, give60, giveToday], intentIdentifiers: []),
            UNNotificationCategory(identifier: askCategory, actions: [approve, decline], intentIdentifiers: []),
        ])
    }

    /// Asks once for permission (no-op after the first answer).
    static func requestAuthorization(_ done: ((Bool) -> Void)? = nil) {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { ok, _ in
            done?(ok)
        }
    }

    /// Turns an engine event into a notification, if that kind is on.
    /// delay: seconds from now (testing).
    static func post(_ e: EngineEvent, delay: TimeInterval = 0) {
        let prefs = SharedState.notificationPrefs
        let name = e.name ?? "A device"
        let content = UNMutableNotificationContent()
        content.sound = .default
        content.userInfo[Key.kind] = e.kind
        if let key = e.key { content.userInfo[Key.device] = key }
        var id = "\(e.kind)-\(e.seq)"
        let period = e.period.flatMap(LimitPeriod.init(rawValue:))
        let amount = ByteCountFormatter.string(fromByteCount: e.bytes ?? 0, countStyle: .decimal)
        let used = ByteCountFormatter.string(fromByteCount: e.used ?? 0, countStyle: .decimal)

        switch e.kind {
        case "pair":
            guard prefs.pairing, let r = e.pair else { return }
            content.title = "“\(r.name)” wants to connect"
            content.body = "A device on your hotspot (\(r.from)) wants to use this iPhone's connection through Open Bars. Only allow devices you know."
            // With limits in use, Allow opens the app to choose this device's limit.
            content.categoryIdentifier = SharedState.limitsInUse ? pairReviewCategory : pairCategory
            content.userInfo[Key.pair] = encode(r)
            id = "pair-\(r.publicKey)"
        case "warn":
            guard prefs.limits, let p = period else { return }
            content.title = "“\(name)” is near its \(p.adjective) limit"
            content.body = "It used \(used) of \(amount)."
            content.categoryIdentifier = limitCategory
        case "limit":
            guard prefs.limits, let p = period else { return }
            content.title = "“\(name)” reached its \(p.adjective) limit"
            content.body = "It used \(amount). " + actionSentence(e.action, subject: "It")
            content.categoryIdentifier = limitCategory
        case "cap_warn":
            guard prefs.limits, let p = period else { return }
            content.title = "Your devices are near the \(p.adjective) limit for all devices"
            content.body = "They used \(used) of \(amount) together."
        case "cap":
            guard prefs.limits, let p = period else { return }
            content.title = "Your devices reached the \(p.adjective) limit for all devices"
            content.body = "They used \(amount) together. " + actionSentence(e.action, subject: "They")
        case "ask":
            guard prefs.asks, let a = e.ask else { return }
            content.title = "“\(a.name)” asks for \(a.what)"
            if let m = a.message, !m.isEmpty {
                content.body = "“\(m)”"
            } else {
                content.body = "From its Open Bars status page."
            }
            content.categoryIdentifier = askCategory
            content.userInfo[Key.ask] = encode(a)
            id = "ask-\(a.id)"
        default:
            return
        }
        let trigger = delay > 0 ? UNTimeIntervalNotificationTrigger(timeInterval: delay, repeats: false) : nil
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: id, content: content, trigger: trigger))
    }

    private static func actionSentence(_ action: String?, subject: String) -> String {
        switch LimitAction(rawValue: action ?? "") {
        case .block: return "\(subject) can't use the internet until the limit starts over or you give more."
        case .slow: return (subject == "It" ? "It's" : "They're") + " slowed down until the limit starts over or you give more."
        default: return "Nothing was changed: this limit only notifies you."
        }
    }

    /// A plain notification (sharing stopped, an error, ...), now or after
    /// `after` seconds. One with the same id that hasn't shown yet is
    /// replaced. Calls done once it's handed to the system.
    static func postSimple(id: String, title: String, body: String, after: TimeInterval = 0, done: (() -> Void)? = nil) {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = .default
        let trigger = after > 0 ? UNTimeIntervalNotificationTrigger(timeInterval: after, repeats: false) : nil
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: id, content: content, trigger: trigger)) { _ in
            done?()
        }
    }

    /// Takes a notification away: from Notification Center, and from the
    /// schedule if it hasn't shown yet.
    static func remove(id: String) {
        removePending(id: id)
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [id])
    }

    /// Cancels a scheduled notification that hasn't shown yet.
    static func removePending(id: String) {
        UNUserNotificationCenter.current().removePendingNotificationRequests(withIdentifiers: [id])
    }

    static func encode<T: Encodable>(_ v: T) -> String {
        (try? JSONEncoder().encode(v)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }

    static func decode<T: Decodable>(_ type: T.Type, _ s: Any?) -> T? {
        guard let s = s as? String, let data = s.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(type, from: data)
    }
}

/// Posts notifications for log errors, at most once per message every half
/// hour, so a repeating problem doesn't flood the lock screen.
final class ErrorNotifier {
    private var lastSeq: Int64 = 0
    private var primed = false
    private var lastPosted: [String: Date] = [:]

    /// entriesJSON is the engine's LogsJSON after `after`. The first call
    /// only notes where the log is, so older errors aren't posted.
    func take(_ entriesJSON: String) {
        struct Page: Decodable {
            var next: Int64
            var entries: [Entry]
        }
        struct Entry: Decodable {
            var seq: Int64
            var lvl: String
            var msg: String
            var hint: String?
        }
        guard let data = entriesJSON.data(using: .utf8),
              let page = try? JSONDecoder().decode(Page.self, from: data) else { return }
        lastSeq = page.next
        guard primed else { primed = true; return }
        guard SharedState.notificationPrefs.errors else { return }
        for e in page.entries where e.lvl == "error" {
            let key = String(e.msg.prefix(40))
            if let t = lastPosted[key], Date().timeIntervalSince(t) < 1800 { continue }
            lastPosted[key] = Date()
            Notifier.postSimple(id: "error-\(e.seq)", title: "Open Bars: \(e.msg)", body: e.hint ?? "Open Open Bars › Log for details.")
        }
    }

    var after: Int64 { lastSeq }
}
