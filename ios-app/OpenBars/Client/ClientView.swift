import NetworkExtension
import SwiftUI

/// Connect tab: this device uses an iPhone's Open Bars hotspot through a
/// WireGuard tunnel that only turns on while on that hotspot. Before setup
/// it's a three-step guide; after, the status card and a few controls.
struct ClientView: View {
    @EnvironmentObject var connect: ConnectController
    @State private var showDiagnose = false

    var body: some View {
        NavigationStack {
            Form {
                if connect.isSetUp { readyContent } else { setupContent }
            }
            .formStyle(.grouped)
            .navigationTitle("Connect")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                #if os(iOS)
                // Log on the left, like Share, so the title stays centered.
                ToolbarItem(placement: .topBarLeading) {
                    Button { showLog() } label: { Image(systemName: "doc.text.magnifyingglass") }
                        .accessibilityLabel("Log")
                }
                ToolbarItemGroup(placement: .topBarTrailing) {
                    Button { Task { await connect.reload() } } label: { Image(systemName: "arrow.clockwise") }
                        .accessibilityLabel("Refresh")
                    Button { connect.showSettings = true } label: { Image(systemName: "gearshape") }
                        .accessibilityLabel("Settings")
                }
                #else
                ToolbarItemGroup {
                    Button { showLog() } label: { Image(systemName: "doc.text.magnifyingglass") }
                        .help("Show log")
                    Button { Task { await connect.reload() } } label: { Image(systemName: "arrow.clockwise") }
                        .help("Refresh")
                    Button { connect.showSettings = true } label: { Image(systemName: "gearshape") }
                        .help("Settings")
                }
                #endif
            }
            .navigationDestination(isPresented: $connect.showSettings) { ConnectSettingsView() }
            .sheet(isPresented: $showDiagnose) { DiagnoseView() }
            #if os(iOS)
            .sheet(isPresented: $showingLog) {
                NavigationStack { LogBookView(book: .connect) }
            }
            #endif
        }
    }

    // MARK: After setup

    @ViewBuilder private var readyContent: some View {
        Section {
            ConnectStatusCard().padding(.vertical, 6)
            if let msg = connect.message {
                Text(msg).font(.footnote).foregroundStyle(.secondary)
            }
        } footer: {
            Text("On the iPhone's hotspot, this device's traffic goes through an encrypted tunnel to Open Bars on the iPhone. On any other network the VPN stays off.")
        }
        if connect.localNetworkBlocked { localNetworkSection }
        Section {
            AutoConnectToggle()
            Button { showDiagnose = true } label: {
                Label("Diagnose Connection", systemImage: "stethoscope")
            }
            Button { connect.showSettings = true } label: {
                Label("Settings", systemImage: "gearshape")
            }
        }
    }

    // MARK: Setup

    @ViewBuilder private var setupContent: some View {
        Section {
            SetupStep(number: 1, title: "Join the iPhone's hotspot", detail: joinDetail,
                      state: connect.onHotspot ? .done : .current)
            SetupStep(number: 2, title: "Pair with the iPhone", detail: pairDetail, state: pairState) {
                Button {
                    Task { await connect.pair() }
                } label: {
                    HStack(spacing: 8) {
                        if connect.waitingForApproval { ProgressView().controlSize(.small) }
                        Text(connect.waitingForApproval ? "Tap Allow on the iPhone…" : "Pair with iPhone")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(connect.busy)
            }
            SetupStep(number: 3, title: "Add the VPN", detail: vpnDetail,
                      state: connect.pairing == nil ? .todo : .current) {
                Button("Add VPN") { Task { await connect.applyVPN(autoConnect: true) } }
                    .buttonStyle(.borderedProminent)
                    .disabled(connect.busy)
            }
        } header: {
            Text("Set Up")
        } footer: {
            VStack(alignment: .leading, spacing: 8) {
                Text("On the iPhone, open Open Bars and tap Start. There are no certificates or profiles to install.")
                PrivacyNote(text: "Open Bars collects no data. Pairing gives the iPhone this device's name and public key, and nothing is sent to the developer.")
            }
        }
        if connect.localNetworkBlocked { localNetworkSection }
        if let msg = connect.message {
            Section { Text(msg).font(.footnote) }
        }
        Section {
            Button { showDiagnose = true } label: {
                Label("Having trouble? Diagnose", systemImage: "stethoscope")
            }
        }
    }

    private var localNetworkSection: some View {
        Section {
            Label {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Local Network access is off").font(.subheadline.weight(.semibold))
                    Text("Open Bars needs it to talk to the iPhone on its hotspot.")
                        .font(.footnote).foregroundStyle(.secondary)
                    Button("Open Settings") { connect.openLocalNetworkSettings() }
                        .buttonStyle(.bordered).controlSize(.small)
                }
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
            }
        }
    }

    private var joinDetail: String {
        if connect.onHotspot { return "You're on an iPhone hotspot." }
        #if os(macOS)
        return "Join the iPhone's hotspot over Wi‑Fi (its name is the iPhone's name) or connect the iPhone with a USB cable."
        #else
        return "In Settings › Wi‑Fi, join the iPhone's hotspot. Its name is the iPhone's name."
        #endif
    }

    private var pairState: SetupStepStatus {
        connect.pairing != nil ? .done : (connect.onHotspot ? .current : .todo)
    }

    private var pairDetail: String {
        if let p = connect.pairing { return "Paired as “\(p.deviceName)”." }
        return connect.onHotspot ? "Tap Pair, then Allow on the iPhone." : "Join the hotspot first."
    }

    private var vpnDetail: String {
        connect.pairing == nil
            ? "After pairing."
            : "Allow adding the “Open Bars” VPN when asked. It then turns on by itself on \(connect.hotspotLabel)."
    }

    #if os(macOS)
    @Environment(\.openWindow) private var openWindow
    private func showLog() { openWindow(id: "connect-log") }
    #else
    @State private var showingLog = false
    private func showLog() { showingLog = true }
    #endif
}

/// One numbered step of the setup guide; its action shows while it's current.
enum SetupStepStatus { case done, current, todo }

struct SetupStep<Action: View>: View {
    typealias Status = SetupStepStatus

    var number: Int
    var title: String
    var detail: String
    var state: Status
    @ViewBuilder var action: () -> Action

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            ZStack {
                Circle().fill(circleColor).frame(width: 26, height: 26)
                if state == .done {
                    Image(systemName: "checkmark").font(.caption.weight(.bold)).foregroundStyle(.white)
                } else {
                    Text("\(number)").font(.caption.weight(.bold)).foregroundStyle(state == .todo ? Color.secondary : .white)
                }
            }
            VStack(alignment: .leading, spacing: 6) {
                Text(title).font(.headline).foregroundStyle(state == .todo ? .secondary : .primary)
                Text(detail).font(.subheadline).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if state == .current { action() }
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 4)
    }

    private var circleColor: Color {
        switch state {
        case .done: return .green
        case .current: return .accentColor
        case .todo: return Color.secondary.opacity(0.25)
        }
    }
}

extension SetupStep where Action == AnyView {
    init(number: Int, title: String, detail: String, state: Status) {
        self.init(number: number, title: title, detail: detail, state: state) { AnyView(EmptyView()) }
    }
}
