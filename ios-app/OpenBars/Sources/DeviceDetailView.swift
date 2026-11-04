import SwiftUI
import UIKit

/// One device: live speed, usage, details, and (for WireGuard-app devices)
/// its QR code and config.
struct DeviceDetailView: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    let deviceID: UUID
    var startWithQR = false

    @State private var renaming = false
    @State private var newName = ""
    @State private var confirmRemove = false
    @State private var showQR = false
    @State private var copied = false

    var body: some View {
        if let d = store.state.devices.first(where: { $0.id == deviceID }) {
            content(d)
        } else {
            EmptyState(title: "Device removed", systemImage: "trash")
        }
    }

    private func content(_ d: Device) -> some View {
        let online = store.isOnline(d)
        let ps = store.peerStatus(for: d)
        return ScrollViewReader { scroller in List {
            Section {
                HStack(spacing: 14) {
                    Image(systemName: d.deviceKind.symbol)
                        .font(.system(size: 30))
                        .frame(width: 56, height: 56)
                        .background(RoundedRectangle(cornerRadius: 14, style: .continuous).fill(Color.secondary.opacity(0.12)))
                    VStack(alignment: .leading, spacing: 4) {
                        Text(d.name).font(.title3.weight(.semibold))
                        HStack(spacing: 6) {
                            Circle().fill(online ? Color.green : Color.secondary.opacity(0.5)).frame(width: 8, height: 8)
                            Text(online ? "Connected" : store.lastSeen(d).map { "Last seen \(formatAgo($0))" } ?? "Never connected")
                                .font(.subheadline).foregroundStyle(.secondary)
                        }
                        if d.usesWireGuardApp {
                            WireGuardAppTag()
                        } else {
                            Text("Paired from Open Bars").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
                .padding(.vertical, 4)
            }

            if store.running {
                Section("Right now") {
                    let r = store.currentRate(for: d.publicKey)
                    HStack(spacing: 20) {
                        Label(formatRate(r.down), systemImage: "arrow.down").foregroundStyle(TrafficColor.down)
                        Label(formatRate(r.up), systemImage: "arrow.up").foregroundStyle(TrafficColor.up)
                    }
                    .font(.headline.monospacedDigit())
                    ThroughputChart(samples: store.samples, device: d.publicKey, height: 90)
                }
            }

            Section("Usage") {
                if let u = store.usage {
                    LabeledRow("Today", value: bytes(u.total(in: .today, device: d.publicKey)))
                    LabeledRow("Last 7 days", value: bytes(u.total(in: .week, device: d.publicKey)))
                    LabeledRow("Last 30 days", value: bytes(u.total(in: .month, device: d.publicKey)))
                    let bars = u.bars(in: .week, names: store.deviceNames, only: d.publicKey)
                    if !bars.isEmpty {
                        UsageBarChart(bars: bars, range: .week, devices: [d.name], height: 110)
                            .padding(.vertical, 4)
                    }
                }
                if store.running {
                    LabeledRow("Since Start", value: bytes((ps?.rxBytes ?? 0) + (ps?.txBytes ?? 0)))
                }
            }

            DeviceLimitSection(device: d)
                .id("limits")

            Section("Details") {
                LabeledRow("Connects with", value: d.usesWireGuardApp ? "WireGuard app" : "Open Bars")
                if d.usesWireGuardApp {
                    Picker("Type", selection: Binding(get: { d.deviceKind }, set: { store.setKind($0, for: d) })) {
                        ForEach(DeviceKind.wireGuardChoices) { k in
                            Label(k.title, systemImage: k.symbol).tag(k)
                        }
                    }
                }
                LabeledRow("Tunnel address", value: d.address)
                if let v6 = tunnelIPv6(for: d.address) {
                    LabeledRow("Tunnel IPv6", value: v6)
                }
                if let ep = ps?.endpoint, !ep.isEmpty {
                    LabeledRow("Connected from", value: ep)
                }
            }

            if d.usesWireGuardApp { setupSection(d) }

            Section {
                Button("Remove Device", role: .destructive) { confirmRemove = true }
            }
        }
        #if DEBUG
        .onAppear {
            // Screenshot mode: `-showDevice N -showDeviceLimits`.
            if DemoMode.on, ProcessInfo.processInfo.arguments.contains("-showDeviceLimits") {
                DispatchQueue.main.asyncAfter(deadline: .now() + 1) { scroller.scrollTo("limits", anchor: .top) }
            }
        }
        #endif
        }
        .navigationTitle(d.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Rename", systemImage: "pencil") { newName = d.name; renaming = true }
            }
        }
        .alert("Rename Device", isPresented: $renaming) {
            TextField("Name", text: $newName)
            Button("Save") {
                let n = newName.trimmingCharacters(in: .whitespaces)
                if !n.isEmpty { store.rename(d, to: n) }
            }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog("Remove “\(d.name)”?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                store.remove(d)
                dismiss()
            }
        } message: {
            Text(d.usesWireGuardApp ? "Its WireGuard config stops working." : "It will have to pair again to connect.")
        }
        .onAppear {
            if startWithQR { showQR = true }
            store.refreshUsageSoon()
        }
    }

    private func setupSection(_ d: Device) -> some View {
        let conf = store.clientConfig(for: d)
        return Section {
            if conf.isEmpty {
                Label("This device's address isn't valid, so it has no config. Remove it and add it again.", systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.orange)
            }
            DisclosureGroup("QR code", isExpanded: $showQR) {
                ConfigQRCode(config: conf)
            }
            .disabled(conf.isEmpty)
            if !conf.isEmpty, let url = ConfigFile.write(conf, deviceName: d.name) {
                ShareButton(url: url) { Label("Share .conf file", systemImage: "square.and.arrow.up") }
            }
            Button {
                UIPasteboard.general.string = conf
                copied = true
            } label: {
                Label(copied ? "Copied" : "Copy config", systemImage: copied ? "checkmark" : "doc.on.doc")
            }
            .disabled(conf.isEmpty)
            if store.state.settings.webPageEnabled && !conf.isEmpty {
                if store.downloadOpen(d) {
                    Label("On the config page now (one download)", systemImage: "arrow.down.circle")
                        .foregroundStyle(.secondary)
                } else {
                    Button { store.allowDownload(d) } label: {
                        Label("Allow Download from the Config Page", systemImage: "arrow.down.circle")
                    }
                }
            }
        } header: {
            Text("Set up in the WireGuard app")
        } footer: {
            if store.state.settings.webPageEnabled {
                Text("In the WireGuard app: + › Create from QR code. On a laptop, open \(configPageURL) while on the hotspot and download “\(d.name).conf”. The page offers it once, for 15 minutes, so nobody else on your hotspot can take this device's key.")
            } else {
                Text("In the WireGuard app: + › Create from QR code, or import the .conf file.")
            }
        }
    }

    private var configPageURL: String {
        "http://\(store.endpointHost):\(String(store.state.settings.webPort))"
    }

    private func bytes(_ n: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: n, countStyle: .decimal)
    }
}
