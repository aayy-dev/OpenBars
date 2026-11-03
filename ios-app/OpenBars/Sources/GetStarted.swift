import SwiftUI

/// Where people get Open Bars and the WireGuard app.
enum Links {
    /// The public TestFlight beta: iPhone, iPad and Mac.
    static let testFlight = URL(string: "https://testflight.apple.com/join/Xy7czFXJ")!
    /// Open Bars on the App Store (iPhone, iPad and Mac share one listing).
    static let appStore = URL(string: "https://apps.apple.com/app/id6829876797")!
    /// GitHub releases: Open Bars for Windows.
    static let releases = URL(string: "https://github.com/aayy-dev/OpenBars/releases")!
    static let releasesText = "github.com/aayy-dev/OpenBars/releases"
    /// The WireGuard app for every platform.
    static let wireGuardInstall = URL(string: "https://www.wireguard.com/install/")!

    /// Installed from the App Store, not TestFlight, Xcode or sideloaded:
    /// only App Store installs have a production receipt.
    static let isAppStoreInstall: Bool = {
        guard let url = Bundle.main.appStoreReceiptURL else { return false }
        return url.lastPathComponent == "receipt" && FileManager.default.fileExists(atPath: url.path)
    }()

    /// The link that gets Open Bars on another iPhone, iPad or Mac: the App
    /// Store once this copy came from there, TestFlight until then.
    static var getOpenBars: URL { isAppStoreInstall ? appStore : testFlight }

    /// What the link opens, for the steps that send it.
    static var getOpenBarsNote: String {
        isAppStoreInstall
            ? "The link opens Open Bars on the App Store."
            : "The link opens TestFlight, Apple's app for trying apps before they're on the App Store. Install TestFlight if asked, then install Open Bars from it."
    }
}

// MARK: - First launch

/// First launch on iPhone and iPad: one question, so Open Bars opens on the
/// tab this device needs. Shown once, and never to people who already
/// shared or connected.
struct WelcomeView: View {
    /// "share" or "connect": the tab to open.
    var choose: (String) -> Void

    private var model: String { UIDevice.current.localizedModel } // iPhone, iPad

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 10) {
                    Image(systemName: "personalhotspot")
                        .font(.system(size: 44, weight: .semibold))
                        .foregroundStyle(.green)
                        .accessibilityHidden(true)
                    Text("Welcome to Open Bars").font(.largeTitle.bold())
                    Text("Use an iPhone's cellular data on your other devices, through an encrypted tunnel.")
                        .font(.title3).foregroundStyle(.secondary)
                }
                .padding(.top, 24)

                VStack(alignment: .leading, spacing: 12) {
                    Text("What do you want to do on this \(model)?").font(.headline)
                    choice(symbol: "personalhotspot", tint: .green,
                           title: "Share this \(model)'s data",
                           detail: "Your other devices use its cellular connection, through its Personal Hotspot.") {
                        choose("share")
                    }
                    choice(symbol: "lock.shield", tint: .blue,
                           title: "Use another iPhone's data",
                           detail: "This \(model) connects through an iPhone that shares with Open Bars.") {
                        choose("connect")
                    }
                }

                VStack(alignment: .leading, spacing: 6) {
                    Text("You can switch any time with the Share and Connect tabs.")
                        .foregroundStyle(.secondary)
                    Text("Open Bars collects no data.")
                        .foregroundStyle(.secondary)
                    Link("Privacy Policy", destination: privacyPolicyURL)
                }
                .font(.footnote)
            }
            .padding(24)
            .frame(maxWidth: 560, alignment: .leading)
            .frame(maxWidth: .infinity)
        }
        .background(Color(.systemGroupedBackground))
    }

    private func choice(symbol: String, tint: Color, title: String, detail: String,
                        action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 14) {
                Image(systemName: symbol)
                    .font(.title2)
                    .foregroundStyle(tint)
                    .frame(width: 36)
                VStack(alignment: .leading, spacing: 3) {
                    Text(title).font(.body.weight(.semibold)).foregroundStyle(.primary)
                    Text(detail).font(.subheadline).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 4)
                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            }
            .multilineTextAlignment(.leading)
            .padding(16)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

// MARK: - Share tab, before any device

/// The Share tab before the first device: what to do next, ticked off as it
/// happens. It's replaced by the device list once a device is added.
struct GetStartedSteps: View {
    @EnvironmentObject var store: Store
    var chooseDevice: () -> Void

    var body: some View {
        let requests = store.running ? (store.status.pairing ?? []) : []
        let joined = store.running && !store.status.net.hotspotIp.isEmpty
        SetupStep(number: 1, title: "Turn on Personal Hotspot and tap Start",
                  detail: store.running
                      ? "Sharing is on."
                      : "On this iPhone, open Settings › Personal Hotspot and turn on Allow Others to Join. Then tap Start above.",
                  state: store.running ? .done : .current)
        SetupStep(number: 2, title: "Set up your other device",
                  detail: "Choose what you want to connect, and Open Bars shows you how.",
                  state: joined || !requests.isEmpty ? .done : .current) {
            Button("Choose Device", action: chooseDevice)
                .buttonStyle(.borderedProminent)
        }
        SetupStep(number: 3, title: "Tap Allow when it asks",
                  detail: requests.isEmpty
                      ? "A device with Open Bars asks this iPhone to pair, and you allow it here. One added for the WireGuard app shows up right away."
                      : "Only allow devices you know.",
                  state: requests.isEmpty ? .todo : .current) {
            ForEach(requests) { PairingRequestRow(request: $0) }
        }
        if store.running && !store.state.settings.backgroundSharing {
            Toggle(isOn: backgroundSharing) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Keep sharing when this iPhone locks")
                    Text("Recommended. Without it, sharing pauses when you lock this iPhone or leave Open Bars. iOS asks once to add a VPN configuration for it.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }

    private var backgroundSharing: Binding<Bool> {
        Binding(
            get: { store.state.settings.backgroundSharing },
            set: { on in
                var s = store.state.settings
                s.backgroundSharing = on
                store.updateSettings(s)
            })
    }
}
