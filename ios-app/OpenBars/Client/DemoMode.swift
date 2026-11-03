#if DEBUG
import Foundation

/// Screenshot mode, in Debug builds only: launched with `-demo`, Share shows
/// sample devices, usage and live speed instead of the engine's, Connect
/// shows a paired, connected device, and nothing is saved.
///
///     xcrun simctl launch booted dev.aayy.openbars -demo -tab share
enum DemoMode {
    static let on = ProcessInfo.processInfo.arguments.contains("-demo")

    /// The sharing iPhone's name in the samples.
    static let iPhoneName = "Alex's iPhone"

    /// A made-up WireGuard key (32 bytes, base64), different for each n.
    static func key(_ n: Int) -> String {
        Data((0..<32).map { UInt8(truncatingIfNeeded: n &* 73 &+ $0 &* 29 &+ 11) }).base64EncodedString()
    }

    /// Per-second speeds for the last `seconds`, ending now, split between
    /// `peers` by weight. Smooth, with a few bursts, like real browsing.
    static func samples(seconds: Int = 300, peers: [(key: String, weight: Double)],
                        downPeak: Double = 7_500_000, upPeak: Double = 900_000,
                        now: Int64 = Int64(Date().timeIntervalSince1970)) -> [TrafficSample] {
        (0..<seconds).map { i in
            let t = now - Int64(seconds - 1 - i)
            return sample(t: t, peers: peers, downPeak: downPeak, upPeak: upPeak)
        }
    }

    static func sample(t: Int64, peers: [(key: String, weight: Double)],
                       downPeak: Double, upPeak: Double) -> TrafficSample {
        let x = Double(t)
        // A slow swell, a medium wave and a little jitter: 0.15...1.
        let wave = 0.55 + 0.25 * sin(x / 37) + 0.15 * sin(x / 9 + 1.3) + 0.05 * sin(x * 1.7)
        let level = max(0.15, min(1, wave))
        let down = downPeak * level
        let up = upPeak * (0.6 * level + 0.4 * (0.5 + 0.5 * sin(x / 13 + 0.4)))
        let total = peers.reduce(0) { $0 + $1.weight }
        var split: [String: [Int64]] = [:]
        for p in peers where total > 0 {
            split[p.key] = [Int64(down * p.weight / total), Int64(up * p.weight / total)]
        }
        return TrafficSample(t: t, down: Int64(down), up: Int64(up), peers: peers.isEmpty ? nil : split)
    }

    /// Usage for today (by hour) and the last 30 days, for `devices`
    /// (key, name, typical bytes per day).
    static func usage(devices: [(key: String, name: String, perDay: Double)],
                      seen: [String: Int64], now: Date = Date()) -> UsageSummary {
        let cal = Calendar.current
        let today = cal.startOfDay(for: now)
        let dayFormat = DateFormatter()
        dayFormat.calendar = Calendar(identifier: .gregorian)
        dayFormat.locale = Locale(identifier: "en_US_POSIX")
        dayFormat.dateFormat = "yyyy-MM-dd"

        func point(scale: Double, seed: Int, start: Int64?, date: String?) -> UsagePoint {
            var peers: [String: [Int64]] = [:]
            var down: Int64 = 0, up: Int64 = 0
            for (i, d) in devices.enumerated() {
                let wobble = 0.55 + 0.45 * (0.5 + 0.5 * sin(Double(seed * 7 + i * 13)))
                let bytes = d.perDay * scale * wobble
                let r = [Int64(bytes * 0.9), Int64(bytes * 0.1)]
                peers[d.key] = r
                down += r[0]; up += r[1]
            }
            return UsagePoint(start: start, date: date, down: down, up: up, peers: peers)
        }

        // Today, hour by hour up to now: quiet at night, busy in the evening.
        let hoursSoFar = max(1, cal.component(.hour, from: now) + 1)
        let hours = (0..<hoursSoFar).map { h -> UsagePoint in
            let shape = [0.1, 0.05, 0.03, 0.03, 0.03, 0.05, 0.2, 0.5, 0.8, 0.9, 0.85, 0.8,
                         0.9, 0.85, 0.8, 0.85, 0.95, 1.1, 1.4, 1.6, 1.5, 1.2, 0.7, 0.3][h % 24]
            let start = Int64(today.addingTimeInterval(TimeInterval(h * 3600)).timeIntervalSince1970)
            return point(scale: shape / 18, seed: h, start: start, date: nil)
        }
        // Today's day total is what the hours add up to; earlier days vary.
        let days = (0..<30).reversed().map { back -> UsagePoint in
            let day = cal.date(byAdding: .day, value: -back, to: today)!
            let date = dayFormat.string(from: day)
            if back == 0 {
                let down = hours.reduce(0) { $0 + $1.down }, up = hours.reduce(0) { $0 + $1.up }
                var peers: [String: [Int64]] = [:]
                for d in devices {
                    let r = hours.map { $0.peers?[d.key] ?? [0, 0] }
                    peers[d.key] = [r.reduce(0) { $0 + $1[0] }, r.reduce(0) { $0 + $1[1] }]
                }
                return UsagePoint(start: nil, date: date, down: down, up: up, peers: peers)
            }
            let weekend = [1, 7].contains(cal.component(.weekday, from: day))
            return point(scale: weekend ? 1.35 : 0.9, seed: back + 100, start: nil, date: date)
        }
        let names = Dictionary(uniqueKeysWithValues: devices.map { ($0.key, $0.name) })
        return UsageSummary(hours: hours, days: days, names: names, seen: seen)
    }
}
#endif
