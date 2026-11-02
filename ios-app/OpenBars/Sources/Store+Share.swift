import SwiftUI

/// Derived values the Share screens show.
extension Store {
    /// Current speed, averaged over the last 2 seconds.
    func currentRate(for key: String? = nil) -> (down: Int64, up: Int64) {
        let recent = samples.suffix(2)
        guard !recent.isEmpty else { return (0, 0) }
        let sum = recent.reduce((down: Int64(0), up: Int64(0))) { acc, s in
            let r = s.rates(for: key)
            return (acc.down + r.down, acc.up + r.up)
        }
        return (sum.down / Int64(recent.count), sum.up / Int64(recent.count))
    }

    /// A device counts as connected if it sent anything in the last 40 s:
    /// every client sends a keepalive at least every 25 s. (WireGuard
    /// handshakes only happen every 2 minutes, so they lag behind.)
    func isOnline(_ d: Device) -> Bool {
        guard running, let seen = lastSeen(d) else { return false }
        return Int64(Date().timeIntervalSince1970) - seen < 40
    }

    /// When the device last sent anything (unix seconds), from the running
    /// engine or, when sharing is off, the saved usage history; nil = never.
    func lastSeen(_ d: Device) -> Int64? {
        let ps = running ? peerStatus(for: d) : nil
        let candidates = [ps?.lastSeen, ps?.lastHandshake, usage?.seen?[d.publicKey]]
        let best = candidates.compactMap { $0 }.max() ?? 0
        return best > 0 ? best : nil
    }

    var onlineCount: Int { state.devices.filter(isOnline).count }

    /// Data moved since sharing started (all devices, both directions).
    var sessionTotal: Int64 {
        (status.peers ?? []).reduce(0) { $0 + $1.rxBytes + $1.txBytes }
    }

    func usageToday(for d: Device) -> Int64 {
        usage?.total(in: .today, device: d.publicKey) ?? 0
    }

    /// Display names by public key, for charts (current devices win over
    /// names saved with old usage).
    var deviceNames: [String: String] {
        Dictionary(state.devices.map { ($0.publicKey, $0.name) }, uniquingKeysWith: { a, _ in a })
    }

    var statusTitle: String {
        guard running else { return "Not sharing" }
        let n = onlineCount
        return n == 0 ? "Waiting for devices" : "Sharing with \(n) device\(n == 1 ? "" : "s")"
    }

    var statusSubtitle: String {
        guard running else { return "Turn on Personal Hotspot, then tap Start." }
        return runningInBackground ? "Runs in the background" : "In the app · keep it open"
    }

    /// Things worth the user's attention right now.
    var warnings: [ShareWarning] {
        var out: [ShareWarning] = []
        if let e = lastError {
            out.append(ShareWarning(id: "error", symbol: "exclamationmark.octagon.fill", tint: .red, text: e))
        }
        guard running else { return out }
        let net = status.net
        if net.vpnActive {
            out.append(ShareWarning(id: "vpn", symbol: "lock.shield", tint: .blue,
                                    text: "A VPN is on (\(net.vpnIf)), so devices' traffic goes through it."))
        } else if net.cellularIf.isEmpty {
            out.append(ShareWarning(id: "cell", symbol: "antenna.radiowaves.left.and.right.slash", tint: .orange,
                                    text: "No cellular connection found. Traffic goes out over \(net.defaultIf.isEmpty ? "the default route" : net.defaultIf)."))
        }
        if net.hotspotIp.isEmpty, status.uptime > 45 {
            out.append(ShareWarning(
                id: "hotspot", symbol: "personalhotspot", tint: .blue,
                text: "No device has joined yet. Is Personal Hotspot on, with Allow Others to Join? Macs and iPads on your Apple Account can turn it on from their Wi‑Fi menu.",
                action: state.settings.hotspotShortcut ? ("Run Hotspot Shortcut", { [weak self] in self?.runHotspotShortcut() }) : nil))
        }
        if !status.webError.isEmpty {
            out.append(ShareWarning(id: "web", symbol: "exclamationmark.triangle.fill", tint: .orange,
                                    text: "Config page couldn't start: \(status.webError). Change its port in Settings."))
        }
        return out
    }
}

struct ShareWarning: Identifiable {
    var id: String
    var symbol: String
    var tint: Color
    var text: String
    var action: (title: String, run: () -> Void)? = nil
}

/// fd0b:a75::/96 + the IPv4 address, as the engine assigns it.
func tunnelIPv6(for v4: String) -> String? {
    let o = v4.split(separator: ".").compactMap { Int($0) }
    guard o.count == 4 else { return nil }
    return String(format: "fd0b:a75::%x:%x", o[0] << 8 | o[1], o[2] << 8 | o[3])
}
