import SwiftUI

/// Add a device: pick what it is, then follow that device's steps. Apple
/// devices and Windows PCs pair with Open Bars; anything else uses the
/// WireGuard app.
struct AddDeviceView: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section {
                    NavigationLink { PhoneGuide() } label: {
                        choice("iPhone or iPad", symbol: "ipad.and.iphone", detail: "With Open Bars")
                    }
                    NavigationLink { MacGuide() } label: {
                        choice("Mac", symbol: "laptopcomputer", detail: "With Open Bars for Mac")
                    }
                    NavigationLink { WindowsGuide() } label: {
                        choice("Windows PC", symbol: DeviceKind.windows.symbol, detail: "With Open Bars for Windows")
                    }
                    NavigationLink {
                        WireGuardGuide(intro: "For Linux, other phones and tablets, and anything else that runs the WireGuard app. Install WireGuard on it first, from its app store or wireguard.com/install.")
                    } label: {
                        choice("Something else", symbol: "ellipsis.circle", detail: "With the WireGuard app")
                    }
                } header: {
                    Text("What do you want to connect?")
                } footer: {
                    Text("Devices with Open Bars turn their tunnel on by themselves whenever they're on this iPhone's hotspot, and off everywhere else.")
                }
                if store.running, let requests = store.status.pairing, !requests.isEmpty {
                    Section("Asking to pair") {
                        ForEach(requests) { PairingRequestRow(request: $0) }
                    }
                }
            }
            .navigationTitle("Add Device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .pinnedTrailing) { Button("Done", systemImage: "checkmark") { dismiss() } }
            }
        }
    }

    private func choice(_ title: String, symbol: String, detail: String) -> some View {
        Label {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.body.weight(.medium))
                Text(detail).font(.caption).foregroundStyle(.secondary)
            }
        } icon: {
            Image(systemName: symbol)
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Guides

/// Another iPhone or iPad: get Open Bars, join the hotspot, pair.
private struct PhoneGuide: View {
    var body: some View {
        List {
            Section {
                ConfigQRCode(config: Links.getOpenBars.absoluteString, label: "QR code of the link to Open Bars", maxSide: 180)
                Text("On the other iPhone or iPad, open the Camera and point it at this code. Or send it the link:")
                    .font(.subheadline)
                ShareLink(item: Links.getOpenBars) { Label("Send Link", systemImage: "square.and.arrow.up") }
            } header: {
                StepHeader(1, "Get Open Bars on it")
            } footer: {
                Text(Links.getOpenBarsNote)
            }
            JoinHotspotSection(number: 2, on: "On that iPhone or iPad, open Settings › Wi‑Fi and choose",
                               footer: "Signed in to the same Apple Account? It's listed under Personal Hotspots, with no password needed.")
            PairSection(number: 3,
                        how: "Open the Open Bars app on it, choose Use another iPhone's data (the Connect tab) and tap Pair with iPhone. Tap Allow here, then Add VPN on that device.")
        }
        .navigationTitle("iPhone or iPad")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A Mac: Open Bars for Mac from the same link, then the same steps.
private struct MacGuide: View {
    var body: some View {
        List {
            Section {
                Text("Send the link to your Mac, for example with AirDrop, Messages or Mail, and open it there.")
                    .font(.subheadline)
                ShareLink(item: Links.getOpenBars) { Label("Send Link", systemImage: "square.and.arrow.up") }
            } header: {
                StepHeader(1, "Get Open Bars for Mac")
            } footer: {
                Text(Links.getOpenBarsNote)
            }
            JoinHotspotSection(number: 2, on: "On the Mac, open the Wi‑Fi menu and choose",
                               footer: "You can also connect this iPhone to the Mac with a USB cable instead.")
            PairSection(number: 3,
                        how: "Open the Open Bars app on the Mac and click Pair with iPhone. Tap Allow here, then click Add VPN on the Mac.")
        }
        .navigationTitle("Mac")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A Windows PC: Open Bars for Windows from GitHub, then pair.
private struct WindowsGuide: View {
    var body: some View {
        List {
            Section {
                Text("On the PC, open this page and download the newest **OpenBars-windows-x64** file (for Windows on ARM: **arm64**):")
                    .font(.subheadline)
                Text(verbatim: Links.releasesText)
                    .font(.subheadline.monospaced())
                    .textSelection(.enabled)
                ShareLink(item: Links.releases) { Label("Send Link", systemImage: "square.and.arrow.up") }
            } header: {
                StepHeader(1, "Get Open Bars for Windows")
            } footer: {
                Text("Send the link to yourself, for example by email, to open it on the PC.")
            }
            Section {
                Text("Open the file you downloaded. If Windows says “Windows protected your PC”, click **More info**, then **Run anyway**. When Open Bars asks to install its background service, choose **Yes**.")
                    .font(.subheadline)
            } header: {
                StepHeader(2, "Run it")
            } footer: {
                Text("Windows shows that warning because Open Bars for Windows isn't code-signed yet.")
            }
            JoinHotspotSection(number: 3, on: "On the PC, open the Wi‑Fi list and choose", footer: nil)
            PairSection(number: 4, how: "In Open Bars on the PC, click Pair with iPhone, then tap Allow here.")
            Section {
                NavigationLink("Use the WireGuard App Instead") {
                    WireGuardGuide(intro: "Install WireGuard for Windows on the PC from wireguard.com/install, then add the PC here. You'll turn its tunnel on and off yourself in WireGuard.",
                                   name: "PC", kind: .laptop)
                }
            } footer: {
                Text("If Windows won't open Open Bars at all, Smart App Control may be blocking it. It blocks apps that aren't code-signed, with no Run anyway.")
            }
        }
        .navigationTitle("Windows PC")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Anything else: create a config for the WireGuard app.
private struct WireGuardGuide: View {
    @EnvironmentObject var store: Store
    var intro: String
    @State var name = ""
    @State var kind: DeviceKind = .laptop
    @State private var created: Device?

    var body: some View {
        List {
            Section {
                Text(intro).font(.subheadline)
                Link(destination: Links.wireGuardInstall) {
                    Label("WireGuard for Every Platform", systemImage: "arrow.up.right.square")
                }
            }
            Section {
                TextField("Name, e.g. Gaming PC", text: $name)
                Picker("Type", selection: $kind) {
                    ForEach(DeviceKind.wireGuardChoices) { k in
                        Label(k.title, systemImage: k.symbol).tag(k)
                    }
                }
                Button("Create") {
                    if let d = store.addDevice(name: name, kind: kind) {
                        name = ""
                        created = d
                    }
                }
            } header: {
                Text("Add it here")
            } footer: {
                Text("You'll get a QR code and a config file to import into the WireGuard app. Turn the tunnel on in WireGuard while the device is on this iPhone's hotspot, and off when it leaves, or it won't reach the internet.")
            }
        }
        .navigationTitle("WireGuard App")
        .navigationBarTitleDisplayMode(.inline)
        .navigationDestination(item: $created) { d in
            DeviceDetailView(deviceID: d.id, startWithQR: true)
        }
    }
}

// MARK: - Parts

/// “1  Get Open Bars”: a numbered step as a section header.
private struct StepHeader: View {
    let number: Int
    let title: String
    init(_ number: Int, _ title: String) { self.number = number; self.title = title }

    var body: some View {
        HStack(spacing: 8) {
            Text("\(number)")
                .font(.caption.weight(.bold)).foregroundStyle(.white)
                .frame(width: 20, height: 20)
                .background(Circle().fill(Color.accentColor))
            Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(.primary)
        }
        .textCase(nil)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Step \(number): \(title)")
    }
}

/// Join this iPhone's hotspot, naming it when we know the name.
private struct JoinHotspotSection: View {
    @EnvironmentObject var store: Store
    let number: Int
    /// The start of the sentence, ending before the hotspot's name.
    let on: String
    let footer: String?

    var body: some View {
        let name = store.state.settings.hotspotName.trimmingCharacters(in: .whitespaces)
        Section {
            Text(name.isEmpty
                 ? "\(on) this iPhone's name. The password is in Settings › Personal Hotspot on this iPhone."
                 : "\(on) “\(name)”. The password is in Settings › Personal Hotspot on this iPhone.")
                .font(.subheadline)
        } header: {
            StepHeader(number, "Join this iPhone's hotspot")
        } footer: {
            if let footer { Text(footer) }
        }
    }
}

/// Pair: the steps on the other device, then this iPhone's side of it.
private struct PairSection: View {
    @EnvironmentObject var store: Store
    let number: Int
    let how: String

    var body: some View {
        Section {
            Text(how).font(.subheadline)
            if !store.running {
                Label("Sharing is off, so the device can't reach this iPhone yet.", systemImage: "exclamationmark.circle")
                    .font(.subheadline).foregroundStyle(.orange)
                if store.busy {
                    ProgressView()
                } else {
                    Button("Start Sharing") { store.start() }
                        .buttonStyle(.borderedProminent)
                }
            } else if let requests = store.status.pairing, !requests.isEmpty {
                ForEach(requests) { PairingRequestRow(request: $0) }
            } else {
                HStack(spacing: 10) {
                    ProgressView()
                    Text("Waiting for it to ask…").font(.subheadline).foregroundStyle(.secondary)
                }
            }
        } header: {
            StepHeader(number, "Pair it")
        } footer: {
            Text("After this, it connects by itself whenever it's on this hotspot. Its keys never leave it.")
        }
    }
}
