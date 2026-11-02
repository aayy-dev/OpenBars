import Combine
import Foundation

/// One of the app's two logs (Share and Connect): entries from Swift and from
/// the Go library on that side, kept in time order, saved as JSON Lines in
/// Application Support and capped at 3,000 entries.
@MainActor
final class LogBook: ObservableObject {
    enum Kind: String {
        case share, connect
        var title: String { self == .share ? "Share" : "Connect" }
    }

    static let share = LogBook(.share, fileName: "share-log.jsonl", oldFile: "last-session.log",
                               detailDefaults: UserDefaults(suiteName: LogKeys.appGroup) ?? .standard,
                               detailKey: LogKeys.shareDetailedUntil)
    static let connect = LogBook(.connect, fileName: "connect-log.jsonl", oldFile: "connect.log",
                                 detailDefaults: .standard, detailKey: "connectDetailedLogsUntil")

    static let capacity = 3000
    static let detailedDuration: TimeInterval = 30 * 60

    let kind: Kind
    /// Oldest first.
    @Published private(set) var entries: [LogEntry] = []
    /// While set (and in the future), the Go side records debug entries too.
    @Published private(set) var detailedUntil: Date?

    /// Set by whoever owns this side's Go library: empty its ring(s) where
    /// reachable, and switch detailed logging (nil = off).
    var onClear: (() -> Void)?
    var onDetailedChange: ((Date?) -> Void)?

    var isDetailed: Bool { (detailedUntil ?? .distantPast) > Date() }

    private let fileURL: URL
    private let detailDefaults: UserDefaults
    private let detailKey: String
    private let cursorKey: String
    private var nextSeq: Int64 = 1
    private var linesInFile = 0
    private var detailTimer: Timer?
    /// The last entry add(_:) stored, and a run of identical entries after
    /// it that is being counted rather than stored (see add(_:)).
    private var lastAdded: LogEntry?
    private var run: (entry: LogEntry, count: Int)?
    private var runTimer: Timer?
    /// A run is summed up this long after its last repeat (or when something
    /// else is added); a repeat counts if it comes within a minute of the
    /// previous occurrence.
    private static let runQuiet: TimeInterval = 5
    private static let repeatWindow: TimeInterval = 60
    private let io = DispatchQueue(label: "dev.aayy.openbars.logbook")

    /// How far each Go ring has been read: the last seq fetched, and the
    /// ring's boot time (a new one means its process restarted and its
    /// numbers started over).
    private struct Cursor: Codable {
        var seq: Int64 = 0
        var boot: Int64 = 0
        /// The log was cleared since; a gap in the ring's numbers is expected.
        var cleared = false
    }
    private var cursors: [String: Cursor] = [:]
    /// Rings that live only as long as this process (not saved).
    private var volatileChannels: Set<String> = []
    private var pulling: Set<String> = []

    private init(_ kind: Kind, fileName: String, oldFile: String, detailDefaults: UserDefaults, detailKey: String) {
        self.kind = kind
        let folder = URL.applicationSupportDirectory
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        fileURL = folder.appending(path: fileName)
        self.detailDefaults = detailDefaults
        self.detailKey = detailKey
        cursorKey = "logCursors.\(kind.rawValue)"
        // The text logs of earlier versions are replaced by this one.
        try? FileManager.default.removeItem(at: folder.appending(path: oldFile))

        if let data = UserDefaults.standard.data(forKey: cursorKey),
           let saved = try? JSONDecoder().decode([String: Cursor].self, from: data) {
            cursors = saved
        }
        load()
        let until = detailDefaults.double(forKey: detailKey)
        if until > 0 { detailedUntil = Date(timeIntervalSince1970: until) }
        checkDetailed()
    }

    // MARK: Adding

    /// Adds an entry from Swift. Debug entries are kept only while detailed
    /// logging is on. Entries are never changed once added, so repeats work
    /// as on the Go side: an entry identical to the last one added, within a
    /// minute of the previous occurrence, is only counted, and when the run
    /// ends one summary entry ("×N more", at the last repeat's time) follows.
    func add(_ level: LogLevel, _ source: LogSource, _ message: String, hint: String? = nil) {
        add(LogEntry(date: Date(), level: level, source: source, message: message, hint: hint))
    }

    private func add(_ entry: LogEntry) {
        if entry.level == .debug && !isDetailed { return }
        let previous = run?.entry ?? lastAdded
        if let previous, previous.level == entry.level, previous.source == entry.source,
           previous.message == entry.message, previous.hint == entry.hint,
           entry.date >= previous.date, entry.date.timeIntervalSince(previous.date) < Self.repeatWindow {
            run = (entry, (run?.count ?? 0) + 1)
            armRunTimer()
            return
        }
        endRun()
        lastAdded = entry
        append(contentsOf: [entry])
    }

    /// Appends the summary of a pending run of repeats, if any.
    private func endRun() {
        runTimer?.invalidate()
        runTimer = nil
        guard let pending = run else { return }
        run = nil
        var summary = pending.entry
        summary.repeats = pending.count
        lastAdded = summary
        append(contentsOf: [summary])
    }

    private func armRunTimer() {
        runTimer?.invalidate()
        let t = Timer(timeInterval: Self.runQuiet, repeats: false) { [weak self] _ in
            Task { @MainActor in self?.endRun() }
        }
        RunLoop.main.add(t, forMode: .common)
        runTimer = t
    }

    /// Stores entries (e.g. fetched from Go), each under a new number, in
    /// time order.
    func append(contentsOf new: [LogEntry]) {
        guard !new.isEmpty else { return }
        var list = entries
        var stored: [LogEntry] = []
        for var e in new {
            e.seq = nextSeq
            nextSeq += 1
            if let last = list.last, e.date < last.date {
                let i = list.lastIndex(where: { $0.date <= e.date }).map { $0 + 1 } ?? 0
                list.insert(e, at: i)
            } else {
                list.append(e)
            }
            stored.append(e)
        }
        if list.count > Self.capacity { list.removeFirst(list.count - Self.capacity) }
        entries = list
        write(stored)
    }

    /// Empties the log, here and on the Go side where it can be reached.
    func clear() {
        runTimer?.invalidate()
        runTimer = nil
        run = nil
        lastAdded = nil
        entries = []
        for key in cursors.keys { cursors[key]?.cleared = true }
        saveCursors()
        rewriteFile()
        onClear?()
    }

    // MARK: Fetching from Go

    /// Reads new entries from a Go log ring. `fetch(after)` returns that
    /// ring's LogsJSON(after), or nil when it can't be reached right now.
    /// `channel` names the ring; `persist: false` for a ring in this process,
    /// which starts over whenever the app does.
    func pull(_ channel: String, persist: Bool = true, fetch: (Int64) async -> String?) async {
        guard !pulling.contains(channel) else { return }
        pulling.insert(channel)
        defer { pulling.remove(channel) }
        if !persist { volatileChannels.insert(channel) }
        let cur = cursors[channel] ?? Cursor()

        guard var page = GoLogPage.decode(await fetch(cur.seq)) else { return }
        var after = cur.seq
        if page.boot != 0 && page.boot != cur.boot {
            // Not the ring read before (its process restarted, or first
            // read): its numbers start over, so everything in it is new.
            after = 0
            if cur.seq > 0 {
                guard let all = GoLogPage.decode(await fetch(0)) else { return }
                page = all
            }
        }
        let fresh = page.entries.filter { $0.seq > after } // a saved page isn't filtered
        let missed = page.first - after - 1
        if missed > 0 && !cur.cleared, let from = fresh.first?.date {
            append(contentsOf: [LogEntry(date: from.addingTimeInterval(-0.001), level: .warn, source: .app,
                                         message: "\(missed) earlier \(missed == 1 ? "entry was" : "entries were") dropped before Open Bars could read \(missed == 1 ? "it" : "them")")])
        }
        append(contentsOf: fresh)
        if let last = fresh.last {
            cursors[channel] = Cursor(seq: last.seq, boot: page.boot, cleared: false)
        } else if after != cur.seq {
            cursors[channel] = Cursor(seq: 0, boot: page.boot, cleared: cur.cleared)
        } else {
            return
        }
        saveCursors()
    }

    private func saveCursors() {
        let keep = cursors.filter { !volatileChannels.contains($0.key) }
        if let data = try? JSONEncoder().encode(keep) {
            UserDefaults.standard.set(data, forKey: cursorKey)
        }
    }

    // MARK: Detailed logging

    /// Turns detailed logging on for 30 minutes, or off.
    func setDetailed(_ on: Bool) {
        let until = on ? Date().addingTimeInterval(Self.detailedDuration) : nil
        detailedUntil = until
        if let until {
            detailDefaults.set(until.timeIntervalSince1970, forKey: detailKey)
        } else {
            detailDefaults.removeObject(forKey: detailKey)
        }
        armDetailTimer()
        onDetailedChange?(until)
        add(.info, .app, on ? "Detailed logging on for 30 minutes" : "Detailed logging off")
    }

    /// Ends detailed logging once its time is up (logged at the moment it ended).
    private func checkDetailed() {
        guard let until = detailedUntil else { return }
        guard until <= Date() else {
            armDetailTimer()
            return
        }
        detailedUntil = nil
        detailDefaults.removeObject(forKey: detailKey)
        detailTimer?.invalidate()
        detailTimer = nil
        onDetailedChange?(nil)
        append(contentsOf: [LogEntry(date: until, level: .info, source: .app,
                                     message: "Detailed logging turned itself off after 30 minutes")])
    }

    private func armDetailTimer() {
        detailTimer?.invalidate()
        detailTimer = nil
        guard let until = detailedUntil else { return }
        let t = Timer(fire: until.addingTimeInterval(0.5), interval: 0, repeats: false) { [weak self] _ in
            Task { @MainActor in self?.checkDetailed() }
        }
        RunLoop.main.add(t, forMode: .common)
        detailTimer = t
    }

    // MARK: Export

    /// The whole log as text: a header, then one line per entry (plus its
    /// hint), oldest first, in the time zone chosen in the log view.
    func exportText() -> String {
        endRun()
        let utc = LogFormat.useUTC
        var out = LogFormat.header(kind: kind, entries: entries.count, utc: utc)
        out.reserveCapacity(out.count + entries.count * 120)
        for e in entries {
            out += LogFormat.text(e, utc: utc)
            out += "\n"
        }
        return out
    }

    /// "Open Bars Share log 2026-11-02.txt"
    var exportFileName: String {
        "Open Bars \(kind.title) log \(LogFormat.day(Date(), utc: LogFormat.useUTC)).txt"
    }

    // MARK: File (JSON Lines, append-only; rewritten only to drop old entries)

    private func load() {
        guard let data = try? Data(contentsOf: fileURL) else { return }
        let dec = JSONDecoder()
        var bySeq: [Int64: LogEntry] = [:]
        var lines = 0
        for line in data.split(separator: 0x0A) where !line.isEmpty {
            lines += 1
            guard let e = try? dec.decode(LogEntry.self, from: Data(line)) else { continue }
            bySeq[e.seq] = e
        }
        var all = bySeq.values.sorted { ($0.date, $0.seq) < ($1.date, $1.seq) }
        if all.count > Self.capacity { all.removeFirst(all.count - Self.capacity) }
        entries = all
        nextSeq = (bySeq.keys.max() ?? 0) + 1
        linesInFile = lines
        if lines > all.count + Self.capacity / 4 { rewriteFile() }
    }

    private func write(_ batch: [LogEntry]) {
        linesInFile += batch.count
        if linesInFile > entries.count + Self.capacity / 4 {
            rewriteFile()
            return
        }
        let data = Self.encode(batch)
        let url = fileURL
        io.async {
            if let h = try? FileHandle(forWritingTo: url) {
                defer { try? h.close() }
                _ = try? h.seekToEnd()
                try? h.write(contentsOf: data)
            } else {
                try? data.write(to: url, options: .atomic)
            }
        }
    }

    private func rewriteFile() {
        let snapshot = entries
        let url = fileURL
        linesInFile = snapshot.count
        io.async {
            try? Self.encode(snapshot).write(to: url, options: .atomic)
        }
    }

    nonisolated private static func encode(_ batch: [LogEntry]) -> Data {
        let enc = JSONEncoder()
        enc.outputFormatting = [.withoutEscapingSlashes]
        var data = Data()
        for e in batch {
            guard let line = try? enc.encode(e) else { continue }
            data.append(line)
            data.append(0x0A)
        }
        return data
    }
}

/// The text form of log entries, the same on screen and in exports:
///
///     2026-11-02 13:04:05.123  INFO   egress  Sending traffic out pdp_ip0 (10.12.4.2)
///     2026-11-02 13:04:09.870  WARN   tcp     14 connections failed in the last minute
///                                             ↳ Check that cellular data is on for Open Bars.
///     2026-11-02 13:05:41.520  ERROR  web     Couldn't listen on 172.20.10.1:8080: address in use (×3 more)
enum LogFormat {
    /// UserDefaults key of the "Show times in UTC" option (both logs).
    static let utcKey = "logTimesInUTC"
    static var useUTC: Bool { UserDefaults.standard.bool(forKey: utcKey) }

    private static let utcZone = TimeZone(identifier: "UTC")!
    private static let localStamp = formatter("yyyy-MM-dd HH:mm:ss.SSS", .autoupdatingCurrent)
    private static let utcStamp = formatter("yyyy-MM-dd HH:mm:ss.SSS", utcZone)
    private static let localDay = formatter("yyyy-MM-dd", .autoupdatingCurrent)
    private static let utcDay = formatter("yyyy-MM-dd", utcZone)

    /// Where hint lines start: under the message.
    static let hintIndent = String(repeating: " ", count: 40)

    private static func formatter(_ format: String, _ zone: TimeZone) -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.calendar = Calendar(identifier: .gregorian)
        f.timeZone = zone
        f.dateFormat = format
        return f
    }

    static func timestamp(_ date: Date, utc: Bool) -> String {
        (utc ? utcStamp : localStamp).string(from: date)
    }

    static func day(_ date: Date, utc: Bool) -> String {
        (utc ? utcDay : localDay).string(from: date)
    }

    /// The entry's main line.
    static func line(_ e: LogEntry, utc: Bool) -> String {
        var src = e.source.rawValue
        if src.count < 6 { src += String(repeating: " ", count: 6 - src.count) }
        var msg = e.message.replacingOccurrences(of: "\n", with: " ")
        if e.repeats > 0 { msg += " (×\(e.repeats) more)" }
        return "\(timestamp(e.date, utc: utc))  \(e.level.label)  \(src)  \(msg)"
    }

    /// The main line, plus the hint on its own line when there is one.
    static func text(_ e: LogEntry, utc: Bool) -> String {
        guard let hint = e.hint, !hint.isEmpty else { return line(e, utc: utc) }
        return line(e, utc: utc) + "\n" + hintIndent + "↳ " + hint.replacingOccurrences(of: "\n", with: " ")
    }

    /// "America/Chicago (UTC−06:00)", or "UTC (UTC+00:00)".
    static func zoneDescription(utc: Bool, at date: Date = Date()) -> String {
        let zone = utc ? utcZone : TimeZone.current
        let offset = zone.secondsFromGMT(for: date)
        let sign = offset < 0 ? "−" : "+"
        let minutes = abs(offset) / 60
        return "\(zone.identifier) (UTC\(sign)\(String(format: "%02d:%02d", minutes / 60, minutes % 60)))"
    }

    static func header(kind: LogBook.Kind, entries: Int, utc: Bool) -> String {
        """
        Open Bars \(kind.title) log
        App: Open Bars \(AppInfo.version)
        OS: \(AppInfo.system)
        Device: \(AppInfo.model)
        Time zone: \(zoneDescription(utc: utc))
        Exported: \(timestamp(Date(), utc: utc))
        Entries: \(entries)


        """
    }
}
