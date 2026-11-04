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
    // Keepalives alone are tens of bytes a second: "74 bytes/s" is too wide.
    bytesPerSecond <= 0 ? "0 KB/s"
        : bytesPerSecond < 1000 ? "<1 KB/s"
        : ByteCountFormatter.string(fromByteCount: bytesPerSecond, countStyle: .decimal) + "/s"
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
        Group {
            if #available(iOS 16.0, macOS 13.0, *) {
                swiftChart(pts, start: start, end: end, peak: peak)
            } else {
                SpeedGraphFallback(points: pts.map { ($0.date, $0.down, $0.up) }, start: start, end: end, top: peak * 1.15)
            }
        }
        .frame(height: height)
        .accessibilityLabel("Speed over the last 5 minutes")
    }

    @available(iOS 16.0, macOS 13.0, *)
    private func swiftChart(_ pts: [Point], start: Date, end: Date, peak: Double) -> some View {
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
        Group {
            if #available(iOS 16.0, macOS 13.0, *) {
                swiftChart(unit: unit, domain: domain)
            } else {
                UsageBarsFallback(bars: bars, devices: devices, domain: domain, unit: unit)
            }
        }
        .frame(height: height)
    }

    @available(iOS 16.0, macOS 13.0, *)
    private func swiftChart(unit: Calendar.Component, domain: ClosedRange<Date>) -> some View {
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
                    .numericTextTransition()
            } icon: {
                Image(systemName: symbol).foregroundStyle(tint).font(compact ? .subheadline : .headline)
            }
            Text(caption).font(compact ? .caption2 : .caption).foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }
}

// MARK: - Graphs for iOS 15 (Swift Charts is iOS 16 / macOS 13)

/// The speed graph drawn by hand: download as a filled area, upload as a line,
/// over the same time window and scale as the Swift Charts version.
private struct SpeedGraphFallback: View {
    var points: [(date: Date, down: Double, up: Double)]
    var start: Date
    var end: Date
    var top: Double

    var body: some View {
        GeometryReader { geo in
            let w = max(geo.size.width - 60, 1) // room for the scale on the right
            let h = geo.size.height
            ZStack(alignment: .topLeading) {
                ForEach([0.0, 0.5, 1.0], id: \.self) { f in
                    Path { p in
                        let y = h - CGFloat(f) * h
                        p.move(to: CGPoint(x: 0, y: y)); p.addLine(to: CGPoint(x: w, y: y))
                    }
                    .stroke(Color.secondary.opacity(0.2), lineWidth: 0.5)
                }
                Path { p in
                    let pts = plotted(\.down, w: w, h: h)
                    guard let first = pts.first, let last = pts.last else { return }
                    p.move(to: CGPoint(x: first.x, y: h))
                    pts.forEach { p.addLine(to: $0) }
                    p.addLine(to: CGPoint(x: last.x, y: h))
                    p.closeSubpath()
                }
                .fill(LinearGradient(colors: [TrafficColor.down.opacity(0.45), TrafficColor.down.opacity(0.05)],
                                     startPoint: .top, endPoint: .bottom))
                line(plotted(\.down, w: w, h: h)).stroke(TrafficColor.down, lineWidth: 1.5)
                line(plotted(\.up, w: w, h: h)).stroke(TrafficColor.up, lineWidth: 1.5)
                VStack(alignment: .trailing) {
                    Text(formatRate(Int64(top)))
                    Spacer()
                    Text(formatRate(Int64(top / 2)))
                    Spacer()
                    Text("0 KB/s")
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
                .frame(width: 56, height: h, alignment: .trailing)
                .offset(x: w + 4)
            }
        }
    }

    private typealias Point = (date: Date, down: Double, up: Double)

    private func plotted(_ value: KeyPath<Point, Double>, w: CGFloat, h: CGFloat) -> [CGPoint] {
        let span = max(end.timeIntervalSince(start), 1)
        return points.filter { $0.date >= start && $0.date <= end }.map { pt in
            CGPoint(x: CGFloat(pt.date.timeIntervalSince(start) / span) * w,
                    y: h - CGFloat(min(pt[keyPath: value] / max(top, 1), 1)) * h)
        }
    }

    private func line(_ pts: [CGPoint]) -> Path {
        Path { p in
            guard let first = pts.first else { return }
            p.move(to: first)
            pts.dropFirst().forEach { p.addLine(to: $0) }
        }
    }
}

/// The usage bars drawn by hand: one column per hour (today) or day, stacked
/// by device in the same colors, with a legend and the top of the scale.
private struct UsageBarsFallback: View {
    var bars: [UsageBar]
    var devices: [String]
    var domain: ClosedRange<Date>
    var unit: Calendar.Component

    var body: some View {
        let columns = self.columns
        let top = max(columns.map { $0.reduce(0) { $0 + $1.bytes } }.max() ?? 0, 1)
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top, spacing: 4) {
                GeometryReader { geo in
                    HStack(alignment: .bottom, spacing: columns.count > 12 ? 1 : 3) {
                        ForEach(columns.indices, id: \.self) { i in
                            VStack(spacing: 0) {
                                Spacer(minLength: 0)
                                ForEach(columns[i].indices.reversed(), id: \.self) { j in
                                    Rectangle()
                                        .fill(color(columns[i][j].device))
                                        .frame(height: geo.size.height * CGFloat(columns[i][j].bytes) / CGFloat(top))
                                }
                            }
                            .frame(maxWidth: .infinity)
                        }
                    }
                }
                Text(ByteCountFormatter.string(fromByteCount: top, countStyle: .decimal))
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            if devices.count > 1 {
                HStack(spacing: 10) {
                    ForEach(devices, id: \.self) { d in
                        HStack(spacing: 4) {
                            Circle().fill(color(d)).frame(width: 7, height: 7)
                            Text(d).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                }
            }
        }
    }

    /// Each hour or day in the range, with its device segments in legend order.
    private var columns: [[(device: String, bytes: Int64)]] {
        let cal = Calendar.current
        var count = 0
        var d = domain.lowerBound
        while d < domain.upperBound, let next = cal.date(byAdding: unit, value: 1, to: d) {
            count += 1
            d = next
        }
        var out = Array(repeating: [String: Int64](), count: max(count, 1))
        for b in bars {
            guard let i = cal.dateComponents([unit], from: domain.lowerBound, to: b.date).value(for: unit),
                  out.indices.contains(i) else { continue }
            out[i][b.device, default: 0] += b.bytes
        }
        return out.map { col in devices.compactMap { name in col[name].map { (name, $0) } } }
    }

    private func color(_ device: String) -> Color {
        let i = devices.firstIndex(of: device) ?? 0
        return TrafficColor.palette[i % TrafficColor.palette.count]
    }
}
