import SwiftUI

/// Connect's status at a glance: state, Connect/Disconnect, and while
/// connected the live speed and chart. Used in the Connect tab and (compact)
/// in the Mac menu bar.
struct ConnectStatusCard: View {
    @EnvironmentObject var connect: ConnectController
    var compact = false

    var body: some View {
        VStack(alignment: .leading, spacing: compact ? 10 : 14) {
            HStack(spacing: compact ? 10 : 12) {
                StatusDot(on: connect.isConnected || (connect.installed && connect.autoConnect),
                          active: connect.isConnected)
                VStack(alignment: .leading, spacing: 2) {
                    Text(connect.statusTitle)
                        .font(compact ? .headline : .title3.weight(.semibold))
                        .lineLimit(compact ? 1 : 2) // “Connected to “Name's iPhone”” needs two lines on a phone
                    Text(connect.statusSubtitle)
                        .font(compact ? .caption : .subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                if connect.isSetUp { ConnectButton(compact: compact) }
            }
            if connect.isConnected {
                let r = connect.currentRate
                HStack(spacing: compact ? 16 : 24) {
                    RateLabel(symbol: "arrow.down", tint: TrafficColor.down, value: r.down, caption: "Download", compact: compact)
                    RateLabel(symbol: "arrow.up", tint: TrafficColor.up, value: r.up, caption: "Upload", compact: compact)
                    Spacer()
                }
                ThroughputChart(samples: connect.samples, height: compact ? 70 : 110)
                if let t = connect.tunnel {
                    HStack {
                        Text("\(ByteCountFormatter.string(fromByteCount: t.rxBytes + t.txBytes, countStyle: .decimal)) since connected")
                        Spacer()
                        Text("Handshake \(formatAgo(t.lastHandshake))")
                    }
                    .font(compact ? .caption2 : .footnote)
                    .foregroundStyle(.secondary)
                }
            }
        }
    }
}

/// Connect / Disconnect. Disconnecting by hand also turns automatic connect
/// off, so the on-demand rule doesn't bring it straight back.
struct ConnectButton: View {
    @EnvironmentObject var connect: ConnectController
    var compact = false

    var body: some View {
        let on = connect.isConnected || connect.status == .connecting
        Button(on ? "Disconnect" : "Connect") {
            Task {
                if on { await connect.disconnect() } else { connect.connectNow() }
            }
        }
        .buttonStyle(.borderedProminent)
        .capsuleButtonShape()
        .controlSize(compact ? .small : .regular)
        .tint(on ? .red : .green)
        .disabled(connect.busy || (!on && !connect.onHotspot))
        .help(!on && !connect.onHotspot ? "Join the iPhone's hotspot first" : "")
    }
}

/// Toggle for automatic connect on the hotspot.
struct AutoConnectToggle: View {
    @EnvironmentObject var connect: ConnectController
    var label: String? = nil

    var body: some View {
        Toggle(label ?? "Connect automatically on \(connect.hotspotLabel)", isOn: Binding(
            get: { connect.autoConnect },
            set: { on in Task { await connect.setAutoConnect(on) } }
        ))
        .disabled(connect.busy || !connect.installed)
    }
}
