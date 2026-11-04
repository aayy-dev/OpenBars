import SwiftUI

/// Share settings. Edits a copy of the settings; Save hands it to the store,
/// which restarts sharing if anything changed. Cancel discards the edits.
struct SettingsView: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    @State var settings: AppSettings

    var body: some View {
        NavStack {
            Form {
                Section {
                    AppearancePicker()
                } header: {
                    Text("Appearance")
                } footer: {
                    Text("System follows your device's Light/Dark setting. Applies right away.")
                }
                Section {
                    NavigationLink { LimitSettingsForm(settings: $settings) } label: {
                        LabeledRow("Data Limits", value: limitSummary)
                    }
                } footer: {
                    Text("Limits for new devices and for all devices together. Each device's own limits are on the device.")
                }
                NotificationSettingsSection(prefs: $settings.notifications)
                Section {
                    Toggle("Use cellular data only", isOn: $settings.bindCellular)
                } footer: {
                    Text("Your devices' new connections go out over this iPhone's cellular data, even while it's also on Wi‑Fi. If this iPhone has a VPN turned on, connections go through the VPN instead.")
                }
                Section {
                    TextField("Hotspot name", text: $settings.hotspotName)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    if settings.hotspotName.isEmpty, let name = Store.deviceName {
                        Button("Use “\(name)”") { settings.hotspotName = name }
                    }
                } header: {
                    Text("Hotspot")
                } footer: {
                    Text("Your hotspot's Wi‑Fi name is this iPhone's name (Settings → General → About → Name). Paired devices use it to turn their VPN on only while they're on your hotspot.")
                }
                Section("Tunnel") {
                    Stepper("UDP port \(String(settings.listenPort))", value: $settings.listenPort, in: 1024...65535)
                    TextField("Upstream DNS servers", text: $settings.dns)
                        .keyboardType(.numbersAndPunctuation)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                }
                Section {
                    Toggle("Config download page", isOn: $settings.webPageEnabled)
                    if settings.webPageEnabled {
                        Stepper("Port \(String(settings.webPort))", value: $settings.webPort, in: 1024...65535)
                    }
                } footer: {
                    Text("A computer on your hotspot can open this page in its web browser to download its config file. Each config is offered once, for 15 minutes after you add the device (or tap Allow Download on it), so nobody else on your hotspot can take its key.")
                }
                Section {
                    Toggle("Keep sharing in the background", isOn: $settings.backgroundSharing)
                    Toggle("Start when app opens", isOn: $settings.autoStart)
                } header: {
                    Text("Background")
                } footer: {
                    Text("On: sharing runs as a system VPN component, so it keeps going when you lock this iPhone or switch apps. iOS allows one VPN at a time, so this iPhone can't use another VPN app while sharing.\n\nOff: sharing runs inside Open Bars and pauses shortly after you leave the app, so keep it on screen (the display stays awake while sharing). In this mode, hotspot devices can go through a VPN app running on this iPhone.")
                }
                Section {
                    Toggle("Turn on Personal Hotspot when starting", isOn: $settings.hotspotShortcut)
                    if settings.hotspotShortcut {
                        TextField("Shortcut name", text: $settings.hotspotShortcutName)
                            .autocorrectionDisabled()
                        if let url = HotspotShortcutFiles.hotspotOnly {
                            ShareButton(url: url) {
                                Label("Get the “Open Bars Hotspot” shortcut", systemImage: "square.and.arrow.down")
                            }
                        }
                    }
                    NavigationLink("More ways to turn it on") { HotspotHelpView() }
                } header: {
                    Text("Personal Hotspot")
                } footer: {
                    Text("Apps can't switch on Personal Hotspot, but Shortcuts can. With this on, Start (and “Start when app opens”) runs the shortcut named above: Open Bars switches to Shortcuts for a moment and comes back.")
                }
                Section {
                    Text(store.serverPublicKey)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                } header: {
                    Text("Server public key")
                }
                AboutSection()
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", systemImage: "xmark") { dismiss() }
                }
                ToolbarItem(placement: .pinnedTrailing) {
                    Button("Save", systemImage: "checkmark") {
                        store.updateSettings(settings)
                        dismiss()
                    }
                }
            }
        }
    }

    private var limitSummary: String {
        if settings.capRules.isEmpty && settings.newDeviceRules.isEmpty { return "Per device" }
        if let c = settings.capRules.first { return "All: " + c.summary }
        return "New: " + (settings.newDeviceRules.first?.summary ?? "")
    }
}
