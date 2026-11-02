import SwiftUI

/// Add a device: pair one running Open Bars (recommended for Apple devices),
/// or create a config for the WireGuard app (anything else).
struct AddDeviceView: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var kind: DeviceKind = .laptop
    @State private var created: Device?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Label("Mac, iPad or iPhone", systemImage: "laptopcomputer.and.iphone")
                        .font(.headline)
                    Text("Install Open Bars on it and join this hotspot. Then open **Connect › Pair with iPhone**: the request shows up here for you to allow.")
                        .font(.subheadline)
                    if !store.running {
                        Label("Tap Start first, so the device can reach this iPhone.", systemImage: "exclamationmark.circle")
                            .font(.subheadline).foregroundStyle(.orange)
                    } else if let requests = store.status.pairing, !requests.isEmpty {
                        ForEach(requests) { PairingRequestRow(request: $0) }
                    } else {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text("Waiting for a pairing request…").font(.subheadline).foregroundStyle(.secondary)
                        }
                    }
                } header: {
                    Text("With Open Bars · recommended")
                } footer: {
                    Text("Paired devices connect by themselves whenever they're on this hotspot. Their keys never leave them.")
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
                    Text("With the WireGuard app")
                } footer: {
                    Text("For Windows, Android, Linux and others. You'll get a QR code and a config file to import into the WireGuard app.")
                }
            }
            .navigationTitle("Add Device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { Button("Done") { dismiss() } }
            }
            .navigationDestination(item: $created) { d in
                DeviceDetailView(deviceID: d.id, startWithQR: true)
            }
        }
    }
}
