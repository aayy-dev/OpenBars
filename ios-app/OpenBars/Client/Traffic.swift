import Charts
import SwiftUI

// Traffic data from the engine and the charts that draw it. Shared by iOS
// and macOS so Connect can reuse them.

/// One second of traffic. down = to devices, up = from them; peers maps a
/// device's public key to [down, up] (only devices that moved data).
struct TrafficSample: Decodable, Identifiable, Equatable {
    var t: Int64
    var down: Int64
    var up: Int64
    var peers: [String: [Int64]]?
    var id: Int64 { t }

    func rates(for key: String?) -> (down: Int64, up: Int64) {
        guard let key else { return (down, up) }
        guard let r = peers?[key], r.count == 2 else { return (0, 0) }
        return (r[0], r[1])
    }
}

/// One hour (start) or day (date, local yyyy-MM-dd) of usage.
struct UsagePoint: Decodable {
    var start: Int64?
    var date: String?
    var down: Int64
    var up: Int64
    var peers: [String: [Int64]]?

    func bytes(for key: String) -> Int64 {
        guard let r = peers?[key], r.count == 2 else { return 0 }
        return r[0] + r[1]
    }
}

struct UsageSummary: Decodable {
    var hours: [UsagePoint]
    var days: [UsagePoint]
    var names: [String: String]
    /// Public key -> when the device last sent anything (unix seconds).
    var seen: [String: Int64]?
}

/// What a device is, for its icon.
enum DeviceKind: String, CaseIterable, Identifiable {
    // Paired from Open Bars (sent by the device when pairing).
    case mac, ipad, iphone, windows, android
    // WireGuard-app devices (picked by the user).
    case laptop, desktop, phone, tablet, console, tv, other

    var id: String { rawValue }

    static let wireGuardChoices: [DeviceKind] = [.laptop, .desktop, .phone, .tablet, .console, .tv, .other]

    var title: String {
        switch self {
        case .mac: return "Mac"
        case .ipad: return "iPad"
        case .iphone: return "iPhone"
        case .windows: return "Windows PC"
        case .android: return "Android"
        case .laptop: return "Laptop"
        case .desktop: return "Desktop"
        case .phone: return "Phone"
        case .tablet: return "Tablet"
        case .console: return "Game console"
        case .tv: return "TV or streaming box"
        case .other: return "Other"
        }
    }

    var symbol: String {
        switch self {
        case .mac, .laptop: return "laptopcomputer"
        case .ipad: return "ipad"
        case .iphone: return "iphone"
        case .windows, .desktop: return "desktopcomputer"
        case .android, .phone: return "smartphone"
        case .tablet: return "ipad.landscape"
        case .console: return "gamecontroller"
        case .tv: return "tv"
        case .other: return "network"
        }
    }
}

// MARK: - Formatting

/// "12.4 MB/s"
func formatRate(_ bytesPerSecond: Int64) -> String {
    bytesPerSecond <= 0 ? "0 KB/s" : ByteCountFormatter.string(fromByteCount: bytesPerSecond, countStyle: .decimal) + "/s"
}

/// Chart colors: down in the app's green, up in blue.
enum TrafficColor {
    static let down = Color.green
    static let up = Color.blue
    /// Per-device colors for stacked usage bars.
    static let palette: [Color] = [.green, .blue, .orange, .purple, .pink, .teal, .yellow, .indigo, .mint, .brown]
}

// MARK: - Live chart

/// Down (filled) and up (line) speed over the last 5 minutes, for everything
/// or one device.
struct ThroughputChart: View {
    var samples: [TrafficSample]
    var device: String? = nil
    var height: CGFloat = 110

    private struct Point: Identifiable {
        var date: Date
        var down: Double
        var up: Double
        var id: Date { date }
    }

    private var points: [Point] {
        // 2-second averages: per-second data is too spiky to read.
        var out: [Point] = []
        var i = 0
        while i < samples.count {
            let a = samples[i].rates(for: device)
            var down = Double(a.down), up = Double(a.up), n = 1.0
            if i + 1 < samples.count {
                let b = samples[i + 1].rates(for: device)
                down += Double(b.down); up += Double(b.up); n += 1
            }
            out.append(Point(date: Date(timeIntervalSince1970: TimeInterval(samples[i].t)), down: down / n, up: up / n))
            i += 2
        }
        return out
    }

    var body: some View {
        let end = Date(timeIntervalSince1970: TimeInterval(samples.last?.t ?? Int64(Date().timeIntervalSince1970)))
        // Up to 5 minutes; just after Start, only as far back as there's data
        // (at least a minute), so the chart isn't mostly empty.
        let first = Date(timeIntervalSince1970: TimeInterval(samples.first?.t ?? 0))
        let start = max(end.addingTimeInterval(-300), min(first, end.addingTimeInterval(-60)))
        let pts = points
        let peak = max(pts.map { max($0.down, $0.up) }.max() ?? 0, 64_000)
        Chart {
            ForEach(pts) { p in
                AreaMark(x: .value("Time", p.date), y: .value("Download", p.down))
                    .foregroundStyle(LinearGradient(colors: [TrafficColor.down.opacity(0.45), TrafficColor.down.opacity(0.05)],
                                                    startPoint: .top, endPoint: .bottom))
                    .interpolationMethod(.monotone)
                LineMark(x: .value("Time", p.date), y: .value("Download", p.down), series: .value("Direction", "down"))
                    .foregroundStyle(TrafficColor.down)
                    .interpolationMethod(.monotone)
                    .lineStyle(StrokeStyle(lineWidth: 1.5))
            }
            ForEach(pts) { p in
                LineMark(x: .value("Time", p.date), y: .value("Upload", p.up), series: .value("Direction", "up"))
                    .foregroundStyle(TrafficColor.up)
                    .interpolationMethod(.monotone)
                    .lineStyle(StrokeStyle(lineWidth: 1.5))
            }
        }
        .chartXScale(domain: start...end)
        .chartYScale(domain: 0...(peak * 1.15))
        .chartXAxis {
            AxisMarks(values: .stride(by: .minute, count: 1)) { _ in
                AxisGridLine().foregroundStyle(.quaternary)
            }
        }
        .chartYAxis {
            AxisMarks(position: .trailing, values: .automatic(desiredCount: 3)) { v in
                AxisGridLine().foregroundStyle(.quaternary)
                AxisValueLabel {
                    if let d = v.as(Double.self) { Text(formatRate(Int64(d))).font(.caption2) }
                }
            }
        }
        .frame(height: height)
        .accessibilityLabel("Speed over the last 5 minutes")
    }
}

// MARK: - Usage bars

enum UsageRange: String, CaseIterable, Identifiable {
    case today = "Today", week = "7 days", month = "30 days"
    var id: String { rawValue }
}

/// One bar segment: a device's usage in an hour or a day.
struct UsageBar: Identifiable {
    var date: Date
    var device: String // display name
    var bytes: Int64
    var id: String { "\(date.timeIntervalSince1970)-\(device)" }
}

struct UsageBarChart: View {
    var bars: [UsageBar]
    var range: UsageRange
    /// Device names in legend order, and their colors.
    var devices: [String]
    var height: CGFloat = 150

    var body: some View {
        let unit: Calendar.Component = range == .today ? .hour : .day
        let cal = Calendar.current
        let now = Date()
        let domain: ClosedRange<Date> = {
            switch range {
            case .today:
                let s = cal.startOfDay(for: now)
                return s...cal.date(byAdding: .day, value: 1, to: s)!
            case .week, .month:
                let days = range == .week ? 7 : 30
                let s = cal.date(byAdding: .day, value: -(days - 1), to: cal.startOfDay(for: now))!
                return s...cal.date(byAdding: .day, value: 1, to: cal.startOfDay(for: now))!
            }
        }()
        Chart(bars) { b in
            BarMark(x: .value("Time", b.date, unit: unit), y: .value("Data", b.bytes))
                .foregroundStyle(by: .value("Device", b.device))
        }
        .chartForegroundStyleScale(domain: devices, range: devices.indices.map { TrafficColor.palette[$0 % TrafficColor.palette.count] })
        .chartXScale(domain: domain)
        .chartXAxis {
            switch range {
            case .today:
                AxisMarks(values: .stride(by: .hour, count: 6)) { _ in
                    AxisGridLine().foregroundStyle(.quaternary)
                    AxisValueLabel(format: .dateTime.hour(), centered: false)
                }
            case .week:
                AxisMarks(values: .stride(by: .day, count: 1)) { _ in
                    AxisValueLabel(format: .dateTime.weekday(.narrow), centered: true)
                }
            case .month:
                AxisMarks(values: .stride(by: .day, count: 7)) { _ in
                    AxisGridLine().foregroundStyle(.quaternary)
                    AxisValueLabel(format: .dateTime.month(.abbreviated).day(), centered: false)
                }
            }
        }
        .chartYAxis {
            AxisMarks(position: .trailing, values: .automatic(desiredCount: 3)) { v in
                AxisGridLine().foregroundStyle(.quaternary)
                AxisValueLabel {
                    if let d = v.as(Double.self) { Text(ByteCountFormatter.string(fromByteCount: Int64(d), countStyle: .decimal)).font(.caption2) }
                }
            }
        }
        .chartLegend(devices.count > 1 ? .visible : .hidden)
        .frame(height: height)
    }
}

extension UsageSummary {
    private static let dayFormat: DateFormatter = {
        let f = DateFormatter()
        f.calendar = Calendar(identifier: .gregorian)
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = .current
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()

    /// Hours (today) or days (last 7/30) in the range.
    func points(in range: UsageRange, now: Date = Date()) -> [(date: Date, point: UsagePoint)] {
        let cal = Calendar.current
        switch range {
        case .today:
            let start = cal.startOfDay(for: now).timeIntervalSince1970
            return hours.compactMap { p in
                guard let s = p.start, TimeInterval(s) >= start else { return nil }
                return (Date(timeIntervalSince1970: TimeInterval(s)), p)
            }
        case .week, .month:
            let days = range == .week ? 7 : 30
            let first = cal.date(byAdding: .day, value: -(days - 1), to: cal.startOfDay(for: now))!
            return self.days.compactMap { p in
                guard let d = p.date.flatMap(Self.dayFormat.date(from:)), d >= first else { return nil }
                return (d, p)
            }
        }
    }

    func total(in range: UsageRange, device key: String? = nil) -> Int64 {
        var sum: Int64 = 0
        for (_, p) in points(in: range) {
            sum += key.map { p.bytes(for: $0) } ?? (p.down + p.up)
        }
        return sum
    }

    /// Stacked bars per device. names maps public key to display name (current
    /// devices first); keys not in it use the saved name.
    func bars(in range: UsageRange, names: [String: String], only key: String? = nil) -> [UsageBar] {
        var out: [UsageBar] = []
        for (date, p) in points(in: range) {
            for (k, r) in p.peers ?? [:] where r.count == 2 && (key == nil || key == k) {
                let name = names[k] ?? self.names[k] ?? "Removed device"
                out.append(UsageBar(date: date, device: name, bytes: r[0] + r[1]))
            }
        }
        return out
    }
}

// MARK: - Status pieces (Share and Connect)

/// Green when active, orange when on but idle, gray when off.
struct StatusDot: View {
    var on: Bool
    var active: Bool
    @State private var pulse = false

    var body: some View {
        ZStack {
            Circle().fill(color.opacity(0.25)).frame(width: 28, height: 28)
                .scaleEffect(on && active && pulse ? 1.15 : 1)
            Circle().fill(color).frame(width: 12, height: 12)
        }
        .onAppear {
            withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) { pulse = true }
        }
        .accessibilityHidden(true)
    }

    private var color: Color { on ? (active ? .green : .orange) : .secondary }
}

struct RateLabel: View {
    var symbol: String
    var tint: Color
    var value: Int64
    var caption: String
    var compact = false

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Label {
                Text(formatRate(value)).font((compact ? Font.headline : .title2).weight(.semibold)).monospacedDigit()
                    .contentTransition(.numericText())
            } icon: {
                Image(systemName: symbol).foregroundStyle(tint).font(compact ? .subheadline : .headline)
            }
            Text(caption).font(compact ? .caption2 : .caption).foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }
}
