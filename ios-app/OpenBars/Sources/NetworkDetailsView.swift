import SwiftUI

/// The technical side: interfaces, ports, routes and connection counts.
/// Problems also show up as banners on the Share screen.
struct NetworkDetailsView: View {
    @EnvironmentObject var store: Store

    var body: some View {
        let net = store.status.net
        let s = store.state.settings
        List {
            Section("Personal Hotspot") {
                LabeledRow("Address", value: net.hotspotIp.isEmpty ? "172.20.10.1" : net.hotspotIp)
                LabeledRow("Hotspot interface", value: net.hotspotIp.isEmpty ? "Not active yet" : "Active")
                LabeledRow("Wi‑Fi name", value: s.hotspotName.isEmpty ? "Not set" : s.hotspotName)
            }
            Section("This iPhone's Connection") {
                LabeledRow("Cellular", value: net.cellularIf.isEmpty ? "Not found" : "\(net.cellularIf) · \(net.cellularIp)")
                LabeledRow("Internet route", value: net.defaultIf.isEmpty ? "Unknown" : net.defaultIf)
                LabeledRow("VPN on this iPhone", value: net.vpnActive ? net.vpnIf : "Off")
                if store.running {
                    LabeledRow("Traffic goes out via", value: store.status.egressIf)
                }
            }
            Section("Open Bars") {
                LabeledRow("Mode", value: store.running ? (store.runningInBackground ? "Background" : "In the app") : "Stopped")
                LabeledRow("Tunnel endpoint", value: "\(store.endpointHost):\(s.listenPort) UDP")
                if s.webPageEnabled {
                    LabeledRow("Config page") {
                        Text(verbatim: "http://\(store.endpointHost):\(s.webPort)").textSelection(.enabled)
                    }
                }
                if store.running {
                    LabeledRow("Running for", value: duration(store.status.uptime))
                }
            }
            if store.running {
                Section("Connections") {
                    LabeledRow("TCP", value: "\(store.status.tcpActive) open · \(store.status.tcpTotal) total")
                    LabeledRow("UDP flows", value: "\(store.status.udpFlows)")
                    LabeledRow("Forwarded",
                                   value: "↓ \(formatBytes(store.status.bytesDown))  ↑ \(formatBytes(store.status.bytesUp))")
                }
            }
        }
        .navigationTitle("Network Details")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func duration(_ seconds: Int64) -> String {
        let f = DateComponentsFormatter()
        f.allowedUnits = seconds >= 3600 ? [.hour, .minute] : [.minute, .second]
        f.unitsStyle = .abbreviated
        return f.string(from: TimeInterval(seconds)) ?? "\(seconds)s"
    }
}
