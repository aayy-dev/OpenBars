import SwiftUI

/// Connect's Settings: the hotspot name and pairing (once paired),
/// appearance, and on the Mac where Open Bars shows up. On the Mac this is
/// the only settings screen (the menu bar gear and ⌘, open it).
struct ConnectSettingsView: View {
    @EnvironmentObject var connect: ConnectController
    @State private var confirmForget = false

    var body: some View {
        Form {
            if connect.pairing != nil { hotspotSection }
            if let p = connect.pairing {
                Section("This Device") {
                    LabeledContent("Paired as", value: p.deviceName)
                    LabeledContent("Tunnel address", value: p.address)
                    if !p.address6.isEmpty {
                        LabeledContent("Tunnel IPv6", value: p.address6)
                    }
                    LabeledContent("iPhone", value: p.endpoint)
                }
            }

            Section {
                AppearancePicker()
            } header: {
                Text("Appearance")
            } footer: {
                #if os(macOS)
                Text("System follows your Mac's Light/Dark setting.")
                #else
                Text("System follows your device's Light/Dark setting.")
                #endif
            }

            #if os(macOS)
            MacPresenceSection()
            #endif

            if connect.pairing != nil {
                Section {
                    Button("Pair Again") { Task { await connect.pair() } }
                        .disabled(connect.busy)
                    Button("Forget This iPhone", role: .destructive) { confirmForget = true }
                        .disabled(connect.busy)
                } footer: {
                    Text("Forgetting removes the VPN from this device. To remove it on the iPhone too: Share › the device › Remove Device.")
                }
            }

            AboutSection()
        }
        .formStyle(.grouped)
        .navigationTitle("Settings")
        .confirmationDialog("Forget this iPhone?", isPresented: $confirmForget, titleVisibility: .visible) {
            Button("Forget", role: .destructive) { Task { await connect.forget() } }
        } message: {
            Text("The Open Bars VPN is removed from this device. You can pair again any time.")
        }
    }

    private var hotspotSection: some View {
        Section {
            TextField("Hotspot name", text: $connect.hotspotName)
                .autocorrectionDisabled()
                #if os(iOS)
                .textInputAutocapitalization(.never)
                #endif
            #if os(macOS)
            Toggle("Also when connected by USB cable", isOn: $connect.useOnUSB)
            #endif
            Button("Save") { Task { await connect.applyVPN() } }
                .disabled(connect.busy)
        } header: {
            Text("Hotspot")
        } footer: {
            Text("The VPN turns on by itself when this device joins this Wi‑Fi network. It's the iPhone's name, filled in when you pair.")
        }
    }
}
