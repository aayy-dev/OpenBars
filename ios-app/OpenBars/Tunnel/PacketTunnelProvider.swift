import Foundation
import NetworkExtension
import WGCore

/// Runs the sharing engine in the background. The tunnel excludes local
/// networks so the Personal Hotspot bridge (172.20.10.0/28) stays up and
/// hotspot devices can still reach the engine's ports. No routes are
/// included, so none of this iPhone's own traffic enters the tunnel.
final class PacketTunnelProvider: NEPacketTunnelProvider {
    /// Detailed logging, on while the app's Share log asks for it.
    private let detailed = DetailedLoggingTimer { WgcoreSetDetailedLogging($0) }

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        detailed.set(until: LogKeys.shareDetailedUntil(in: SharedState.defaults))
        guard let proto = protocolConfiguration as? NETunnelProviderProtocol,
              let config = proto.providerConfiguration?["config"] as? String else {
            log(.error, .ext, "Background sharing has no configuration, so it can't start",
                hint: "Open Open Bars and start sharing again.")
            saveLogForApp()
            completionHandler(NSError(domain: "OpenBars", code: 1,
                                      userInfo: [NSLocalizedDescriptionKey: "No sharing configuration. Open the app and start sharing again."]))
            return
        }
        log(.info, .share, LogText.shareSessionStart(mode: "in the background (Network Extension)"))
        var err: NSError?
        guard WgcoreStart(config, &err) else {
            // The engine logs why (with a hint) itself.
            saveLogForApp()
            completionHandler(err ?? NSError(domain: "OpenBars", code: 2,
                                             userInfo: [NSLocalizedDescriptionKey: "The sharing engine didn't start."]))
            return
        }

        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "198.18.0.1")

        // IPv4: give the tunnel an address but claim NO routes, so the
        // phone's own traffic is completely unaffected.
        let v4 = NEIPv4Settings(addresses: ["198.18.0.1"], subnetMasks: ["255.255.255.255"])
        v4.includedRoutes = []   // no traffic enters the tunnel
        v4.excludedRoutes = [NEIPv4Route.default()]  // everything stays outside
        settings.ipv4Settings = v4

        // Same for IPv6.
        let v6 = NEIPv6Settings(addresses: ["fd00::1"], networkPrefixLengths: [128])
        v6.includedRoutes = []
        v6.excludedRoutes = [NEIPv6Route.default()]
        settings.ipv6Settings = v6

        // Don't touch DNS — the phone keeps its normal resolvers.
        settings.dnsSettings = nil

        setTunnelNetworkSettings(settings) { error in
            if let error {
                self.log(.error, .ext, "Couldn't apply the background tunnel's settings: \(error.localizedDescription)",
                         hint: "Start sharing again. If it keeps failing, turn off Settings › Keep sharing in the background.")
                WgcoreStop()
                self.saveLogForApp()
            } else {
                self.log(.info, .ext, "Background sharing is running")
            }
            // Control Center control and widgets show whether sharing is on.
            SharedState.setSharing(error == nil)
            completionHandler(error)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        log(.info, .ext, "Stopping background sharing: \(reason.logDescription)")
        WgcoreStop()
        SharedState.setSharing(false)
        saveLogForApp()
        completionHandler()
    }

    /// The app asks for status/logs and pushes device-list changes.
    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        let msg = String(decoding: messageData, as: UTF8.self)
        var reply = ""
        switch msg {
        case "status":
            reply = WgcoreStatusJSON()
        case LogMessage.clear:
            WgcoreClearLogs()
            reply = "ok"
        default:
            if let after = LogMessage.logsAfter(msg) {
                reply = WgcoreLogsJSON(after)
            } else if LogMessage.isDetailed(msg) {
                detailed.set(until: LogMessage.detailedUntil(msg))
                reply = "ok"
            } else if msg.hasPrefix("peers:") {
                var err: NSError?
                reply = WgcoreSetPeers(String(msg.dropFirst(6)), &err) ? "ok" : (err?.localizedDescription ?? "error")
            } else if msg.hasPrefix("deny:") {
                reply = WgcoreDenyPairing(String(msg.dropFirst(5))) ? "ok" : "not found"
            } else if msg.hasPrefix("history:") {
                reply = WgcoreHistoryJSON(Int64(msg.dropFirst(8)) ?? 0)
            } else if msg.hasPrefix("usage:") {
                reply = WgcoreUsageJSON(Int(msg.dropFirst(6)) ?? 31)
            }
        }
        completionHandler?(Data(reply.utf8))
    }

    private func log(_ level: LogLevel, _ source: LogSource, _ message: String, hint: String = "") {
        WgcoreLog(level.rawValue, source.rawValue, message, hint)
    }

    /// The app can't ask a stopped extension for its log, so leave it in the
    /// app group, where the app reads its last entries.
    private func saveLogForApp() {
        guard let url = LogKeys.shareExtensionFinalLogURL else { return }
        try? WgcoreLogsJSON(0).write(to: url, atomically: true, encoding: .utf8)
    }
}
