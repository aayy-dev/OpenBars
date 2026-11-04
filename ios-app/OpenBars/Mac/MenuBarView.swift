import AppKit
import SwiftUI

/// The menu bar popover: only the connection. Setup and settings open the
/// window.
struct MenuBarView: View {
    @EnvironmentObject var connect: ConnectController
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ConnectStatusCard(compact: true)
            if connect.isSetUp {
                AutoConnectToggle(label: "Automatic on \(connect.hotspotLabel)")
                    .toggleStyle(.switch)
                    .controlSize(.small)
            } else {
                Button("Set Up Open Bars…") { show("main") }
                    .buttonStyle(.borderedProminent)
            }
            if connect.localNetworkBlocked {
                Button {
                    connect.openLocalNetworkSettings()
                } label: {
                    Label("Local Network access is off", systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                }
                .buttonStyle(.borderless)
                .font(.caption)
            }
            Divider()
            HStack(spacing: 14) {
                Button("Open Bars…") { show("main") }
                Button("Log") { show("connect-log") }
                Spacer()
                Button {
                    connect.showSettings = true
                    show("main")
                } label: { Image(systemName: "gearshape") }
                    .help("Settings")
                Button { NSApp.terminate(nil) } label: { Image(systemName: "power") }
                    .help("Quit Open Bars")
            }
            .buttonStyle(.borderless)
            .font(.callout)
        }
        .padding(14)
        .frame(width: 320)
    }

    private func show(_ id: String) {
        showWindow(id, openWindow: openWindow)
    }
}

/// Opens a window and brings it in front of other apps. Needed because
/// without a Dock icon Open Bars is an accessory app, which macOS doesn't
/// bring forward by itself.
@MainActor
func showWindow(_ id: String, openWindow: OpenWindowAction) {
    openWindow(id: id)
    NSApp.activateCompat()
    DispatchQueue.main.async {
        for w in NSApp.windows where w.identifier?.rawValue.hasPrefix(id) == true {
            w.makeKeyAndOrderFront(nil)
            w.orderFrontRegardless()
        }
    }
}

/// Signal bars: full while connected, faint while waiting for the hotspot,
/// empty when off or not set up.
struct MenuBarIcon: View {
    @ObservedObject var connect: ConnectController

    var body: some View {
        let level: Double = connect.isConnected ? 1 : (connect.isSetUp && connect.autoConnect ? 0.34 : 0)
        Image(systemName: "cellularbars", variableValue: level)
            .accessibilityLabel("Open Bars: \(connect.statusTitle)")
    }
}
