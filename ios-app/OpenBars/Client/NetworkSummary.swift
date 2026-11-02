import Foundation
import Network

/// The current network in one line, for the Connect log's `net` entries.
enum NetworkSummary {
    /// One line describing the current network, flagging an iPhone hotspot
    /// (its clients always get a 172.20.10.x address).
    static func describe(_ path: NWPath) -> String {
        guard path.status == .satisfied else { return "offline" }
        let addrs = ipv4Addresses()
        let parts = path.availableInterfaces.map { iface -> String in
            let kind: String
            switch iface.type {
            case .wifi: kind = "Wi‑Fi"
            case .wiredEthernet: kind = "Ethernet/USB"
            case .cellular: kind = "Cellular"
            case .loopback: kind = "Loopback"
            default: kind = "Other"
            }
            var s = "\(kind) \(iface.name)"
            if let a = addrs[iface.name] {
                s += " \(a)"
                if a.hasPrefix("172.20.10.") { s += " (iPhone hotspot)" }
            }
            return s
        }
        return parts.isEmpty ? "connected" : parts.joined(separator: ", ")
    }

    /// Interface name → first IPv4 address.
    private static func ipv4Addresses() -> [String: String] {
        var out: [String: String] = [:]
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let first = head else { return out }
        defer { freeifaddrs(head) }
        for p in sequence(first: first, next: { $0.pointee.ifa_next }) {
            guard let sa = p.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET) else { continue }
            let name = String(cString: p.pointee.ifa_name)
            if out[name] != nil { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(sa, socklen_t(sa.pointee.sa_len), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                out[name] = String(cString: host)
            }
        }
        return out
    }
}
