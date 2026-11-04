import SwiftUI

/// Share tab: this iPhone shares its connection. Status and live speed first,
/// then anything that needs attention, the devices, and usage over time.
/// Technical details live one tap away (Network details).
struct ShareView: View {
    /// What's presented over the Share tab.
    private enum Sheet: Identifiable {
        case settings, log, addDevice
        var id: Self { self }
    }

    @EnvironmentObject var store: Store
    @State private var sheet: Sheet?
    @State private var usageRange: UsageRange = .today

    var body: some View {
        NavStack {
            ScrollViewReader { scroller in
            List {
                Section {
                    StatusCard()
                } footer: {
                    if !store.running {
                        PrivacyNote(text: "Open Bars collects no data. Your devices' traffic goes from this iPhone straight to the internet. Their names, usage and the log stay on this iPhone.")
                    }
                }
                attentionSection
                devicesSection
                usageSection
            }
            #if DEBUG
            .onAppear { showDemoScreen(scroller) }
            #endif
            }
            .navigationTitle("Open Bars")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Log", systemImage: "doc.text.magnifyingglass") { sheet = .log }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Settings", systemImage: "gearshape") { sheet = .settings }
                }
            }
            .sheet(item: $sheet) { which in
                switch which {
                case .settings: SettingsView(settings: store.state.settings)
                case .log: NavStack { LogBookView(book: .share) }
                case .addDevice: AddDeviceView()
                }
            }
            .alert(
                "Allow “\(store.nextPairingRequest?.name ?? "device")” to connect?",
                isPresented: Binding(get: { store.nextPairingRequest != nil && sheet != .addDevice }, set: { _ in }),
                presenting: store.nextPairingRequest
            ) { r in
                Button("Allow") { store.approvePairing(r) }
                Button("Don't Allow", role: .cancel) { store.denyPairing(r) }
            } message: { r in
                Text("A device on your hotspot (\(r.from)) wants to use this iPhone's connection through Open Bars. Only allow devices you know.")
            }
            .onAppear { store.refreshUsageSoon() }
        }
    }

    #if DEBUG
    /// Screenshot mode (see DemoMode): `-showAddDevice` opens Add Device,
    /// `-showUsage` scrolls to the last 7 days of usage.
    private func showDemoScreen(_ scroller: ScrollViewProxy) {
        guard DemoMode.on else { return }
        let args = ProcessInfo.processInfo.arguments
        if args.contains("-showAddDevice") { sheet = .addDevice }
        if args.contains("-showUsage") {
            usageRange = .week
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
                withAnimation(nil) { scroller.scrollTo("usageChart", anchor: .center) }
            }
        }
    }
    #endif

    // MARK: Sections

    @ViewBuilder private var attentionSection: some View {
        let warnings = store.warnings
        if !warnings.isEmpty {
            Section {
                ForEach(warnings) { w in
                    Label {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(w.text).font(.subheadline)
                            if let a = w.action {
                                Button(a.title, action: a.run).buttonStyle(.bordered).controlSize(.small)
                            }
                        }
                    } icon: { Image(systemName: w.symbol).foregroundStyle(w.tint) }
                }
            }
        }
    }

    @ViewBuilder private var devicesSection: some View {
        if store.state.devices.isEmpty {
            Section {
                GetStartedSteps { sheet = .addDevice }
            } header: {
                Text("Get Started")
            }
        } else {
            devicesList
        }
    }

    private var devicesList: some View {
        Section {
            ForEach(store.state.devices) { d in
                NavigationLink { DeviceDetailView(deviceID: d.id) } label: { DeviceRow(device: d) }
            }
            .onDelete { store.removeDevices(at: $0) }
        } header: {
            HStack {
                Text("Devices")
                Spacer()
                Button("Add", systemImage: "plus") { sheet = .addDevice }
                    .font(.subheadline.weight(.semibold))
                    .textCase(nil)
            }
        }
    }

    private var usageSection: some View {
        Section {
            Picker("Range", selection: $usageRange) {
                ForEach(UsageRange.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .listRowSeparator(.hidden)
            if let u = store.usage, u.total(in: usageRange) > 0 {
                let names = store.deviceNames
                let bars = u.bars(in: usageRange, names: names)
                let legend = legendOrder(bars: bars, names: names)
                VStack(alignment: .leading, spacing: 8) {
                    Text(ByteCountFormatter.string(fromByteCount: u.total(in: usageRange), countStyle: .decimal))
                        .font(.title2.weight(.semibold))
                    UsageBarChart(bars: bars, range: usageRange, devices: legend)
                }
                .id("usageChart")
                .padding(.vertical, 4)
            } else {
                Text(usageRange == .today ? "No data used through Open Bars today." : "No data used through Open Bars in this period.")
                    .font(.subheadline).foregroundStyle(.secondary)
            }
        } header: {
            Text("Usage")
        } footer: {
            Text("Data your devices used through this iPhone, counted by Open Bars. Your carrier may count slightly more (connection overhead).")
        }
    }

    /// Current devices in list order, then removed ones that still have usage.
    private func legendOrder(bars: [UsageBar], names: [String: String]) -> [String] {
        var order = store.state.devices.map(\.name)
        for b in bars where !order.contains(b.device) { order.append(b.device) }
        return order.filter { n in bars.contains { $0.device == n } }
    }
}

// MARK: - Status card

private struct StatusCard: View {
    @EnvironmentObject var store: Store

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .center, spacing: 12) {
                StatusDot(on: store.running, active: store.onlineCount > 0)
                VStack(alignment: .leading, spacing: 2) {
                    Text(store.statusTitle).font(.title3.weight(.semibold))
                    Text(store.statusSubtitle).font(.subheadline).foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                if store.busy {
                    ProgressView()
                } else {
                    Button(store.running ? "Stop" : "Start") {
                        if store.running { store.stop() } else { store.start() }
                    }
                    .buttonStyle(.borderedProminent)
                    .buttonBorderShape(.capsule)
                    .tint(store.running ? .red : .green)
                }
            }
            if store.running {
                let rate = store.currentRate()
                HStack(spacing: 24) {
                    RateLabel(symbol: "arrow.down", tint: TrafficColor.down, value: rate.down, caption: "Download")
                    RateLabel(symbol: "arrow.up", tint: TrafficColor.up, value: rate.up, caption: "Upload")
                    Spacer()
                }
                ThroughputChart(samples: store.samples)
                HStack {
                    Text("\(ByteCountFormatter.string(fromByteCount: store.sessionTotal, countStyle: .decimal)) since Start")
                    Spacer()
                    NavigationLink("Network details") { NetworkDetailsView() }
                        .fixedSize()
                }
                .font(.footnote)
                .foregroundStyle(.secondary)
            } else {
                NavigationLink { NetworkDetailsView() } label: {
                    Text("Network details").font(.footnote).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.vertical, 6)
    }
}

// MARK: - Rows

struct PairingRequestRow: View {
    @EnvironmentObject var store: Store
    var request: PairRequest

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label {
                VStack(alignment: .leading, spacing: 2) {
                    Text("“\(request.name)” wants to connect").font(.subheadline.weight(.semibold))
                    Text("From \(request.from) on your hotspot").font(.caption).foregroundStyle(.secondary)
                }
            } icon: {
                Image(systemName: (DeviceKind(rawValue: request.kind) ?? .other).symbol).foregroundStyle(.blue)
            }
            HStack {
                Button("Don't Allow", role: .destructive) { store.denyPairing(request) }
                    .buttonStyle(.bordered)
                Button("Allow") { store.approvePairing(request) }
                    .buttonStyle(.borderedProminent)
            }
            .controlSize(.small)
        }
        .padding(.vertical, 4)
    }
}

struct DeviceRow: View {
    @EnvironmentObject var store: Store
    let device: Device

    var body: some View {
        let online = store.isOnline(device)
        let rate = store.currentRate(for: device.publicKey)
        HStack(spacing: 12) {
            ZStack(alignment: .bottomTrailing) {
                Image(systemName: device.deviceKind.symbol)
                    .font(.title3)
                    .frame(width: 36, height: 36)
                    .foregroundStyle(online ? .primary : .secondary)
                Circle()
                    .fill(online ? Color.green : Color.secondary.opacity(0.5))
                    .frame(width: 9, height: 9)
                    .overlay(Circle().stroke(Color(.secondarySystemGroupedBackground), lineWidth: 2))
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(device.name).font(.body.weight(.medium)).lineLimit(1)
                HStack(spacing: 6) {
                    if device.usesWireGuardApp { WireGuardAppTag() }
                    Text(statusLine(online: online, rate: rate))
                        .font(.caption).foregroundStyle(.secondary).monospacedDigit()
                        .lineLimit(1)
                }
            }
            Spacer(minLength: 4)
            VStack(alignment: .trailing, spacing: 2) {
                Text(ByteCountFormatter.string(fromByteCount: store.usageToday(for: device), countStyle: .decimal))
                    .font(.subheadline).monospacedDigit()
                Text("today").font(.caption2).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }

    private func statusLine(online: Bool, rate: (down: Int64, up: Int64)) -> String {
        if online {
            return rate.down + rate.up > 0 ? "↓ \(formatRate(rate.down))  ↑ \(formatRate(rate.up))" : "Connected"
        }
        if let seen = store.lastSeen(device) {
            return "Last seen \(formatAgo(seen))"
        }
        return store.running ? "Not connected" : "Offline"
    }
}

/// Marks devices that connect with the WireGuard app (not paired from Open Bars).
struct WireGuardAppTag: View {
    var body: some View {
        Text("WireGuard app")
            .font(.caption2.weight(.semibold))
            .lineLimit(1)
            .fixedSize()
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Capsule().fill(Color.secondary.opacity(0.15)))
            .foregroundStyle(.secondary)
            .accessibilityLabel("Uses the WireGuard app")
    }
}
