import Foundation
import Network
import NetworkExtension
import WGClient

/// Open Bars Connect (Mac, iPad, iPhone using someone's hotspot): a
/// WireGuard tunnel to Open Bars on the hotspot iPhone. All of this device's
/// traffic goes in, so it leaves the phone as the phone's own traffic. The
/// app turns it on only on that hotspot (on-demand rules); see
/// ConnectController.
final class ConnectTunnelProvider: NEPacketTunnelProvider {
    private var monitor: NWPathMonitor?
    private var lastPath = ""
    /// Detailed logging, on while the app's Connect log asks for it.
    private let detailed = DetailedLoggingTimer { WgclientSetDetailedLogging($0) }

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        let started = (options?["source"] as? String).map { "by \($0)" } ?? "automatically"
        guard let proto = protocolConfiguration as? NETunnelProviderProtocol,
              let conf = proto.providerConfiguration,
              let wireguard = conf["wireguard"] as? String,
              let address = conf["address"] as? String else {
            log(.info, .app, "Starting the tunnel (\(started)): \(AppInfo.summary)")
            log(.error, .ext, "The VPN configuration has no pairing settings, so the tunnel can't start",
                hint: "Open Open Bars and pair this device with your iPhone again.")
            completionHandler(Self.error("This device isn't paired. Open Open Bars and pair it with your iPhone."))
            return
        }
        let address6 = conf["address6"] as? String ?? ""
        let dns = conf["dns"] as? String ?? "10.66.66.1"
        let mtu = conf["mtu"] as? Int ?? 1420
        let host = conf["endpoint_host"] as? String ?? "172.20.10.1"
        // Session entry: who and what (never the private key).
        let wg = (try? JSONSerialization.jsonObject(with: Data(wireguard.utf8))) as? [String: Any] ?? [:]
        let endpoint = wg["endpoint"] as? String ?? host
        let server = (wg["server_public_key"] as? String).map(LogText.shortKey) ?? "?"
        log(.info, .app, "Starting the tunnel (\(started)): \(AppInfo.summary); iPhone at \(endpoint) (key \(server)), "
            + "address \(address)\(address6.isEmpty ? "" : " and \(address6)"), DNS \(dns), MTU \(mtu)")

        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: host)
        let v4 = NEIPv4Settings(addresses: [address], subnetMasks: ["255.255.255.255"])
        v4.includedRoutes = [NEIPv4Route.default()]
        // The hotspot network itself (the iPhone, its WireGuard port, other
        // devices on it) stays outside the tunnel.
        v4.excludedRoutes = [NEIPv4Route(destinationAddress: "172.20.10.0", subnetMask: "255.255.255.240")]
        settings.ipv4Settings = v4
        if !address6.isEmpty {
            // Take all IPv6 too, or it would leave through the hotspot directly.
            let v6 = NEIPv6Settings(addresses: [address6], networkPrefixLengths: [128])
            v6.includedRoutes = [NEIPv6Route.default()]
            settings.ipv6Settings = v6
        }
        let d = NEDNSSettings(servers: [dns])
        d.matchDomains = [""] // all lookups go to the iPhone
        settings.dnsSettings = d
        settings.mtu = NSNumber(value: mtu)

        setTunnelNetworkSettings(settings) { error in
            if let error {
                self.log(.error, .ext, "Couldn't apply tunnel settings: \(error.localizedDescription)",
                         hint: "Turn the VPN off and on again. If it keeps failing, run Diagnose Connection in Open Bars.")
                completionHandler(error)
                return
            }
            self.log(.info, .ext, "Tunnel settings applied: all traffic through the iPhone except the hotspot network 172.20.10.0/28")
            var err: NSError?
            guard WgclientStart(wireguard, &err) else {
                let msg = err?.localizedDescription ?? "The tunnel didn't start"
                self.log(.error, .ext, "Start failed: \(msg)",
                         hint: "Run Diagnose Connection in Open Bars; pairing again usually fixes bad settings.")
                completionHandler(Self.error(msg))
                return
            }
            self.watchNetwork()
            completionHandler(nil)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        log(.info, .ext, "Stopping: \(reason.logDescription)")
        monitor?.cancel()
        monitor = nil
        WgclientStop()
        completionHandler()
    }

    override func sleep(completionHandler: @escaping () -> Void) {
        completionHandler()
    }

    override func wake() {
        WgclientBump()
    }

    /// The app asks for "status", "history:<since>" and "logs:<after>", and
    /// sets the log's options ("detailed:<until>", "clear-logs").
    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        let msg = String(decoding: messageData, as: UTF8.self)
        var reply = ""
        if msg == "status" {
            reply = WgclientStatusJSON()
        } else if let after = LogMessage.logsAfter(msg) {
            reply = WgclientLogsJSON(after)
        } else if msg.hasPrefix("history:") {
            reply = WgclientHistoryJSON(Int64(msg.dropFirst(8)) ?? 0)
        } else if LogMessage.isDetailed(msg) {
            detailed.set(until: LogMessage.detailedUntil(msg))
            reply = "ok"
        } else if msg == LogMessage.clear {
            WgclientClearLogs()
            reply = "ok"
        }
        completionHandler?(Data(reply.utf8))
    }

    /// After the network changes (Wi‑Fi roam, USB plugged in, wake), rebind
    /// the UDP socket so the tunnel recovers right away.
    private func watchNetwork() {
        let m = NWPathMonitor()
        m.pathUpdateHandler = { [weak self] path in
            guard let self else { return }
            let summary = path.availableInterfaces.map(\.name).joined(separator: ",") + " " + "\(path.status)"
            if summary != self.lastPath {
                if !self.lastPath.isEmpty {
                    self.log(.info, .net, "Network changed: \(summary)")
                    WgclientBump()
                }
                self.lastPath = summary
            }
        }
        m.start(queue: DispatchQueue(label: "dev.aayy.openbars.connect.path"))
        monitor = m
    }

    private func log(_ level: LogLevel, _ source: LogSource, _ message: String, hint: String = "") {
        WgclientLog(level.rawValue, source.rawValue, message, hint)
    }

    private static func error(_ text: String) -> NSError {
        NSError(domain: "OpenBarsConnect", code: 1, userInfo: [NSLocalizedDescriptionKey: text])
    }
}
