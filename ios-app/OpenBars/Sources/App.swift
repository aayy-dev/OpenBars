import SwiftUI

@main
struct OpenBarsApp: App {
    @StateObject private var store = Store()
    @StateObject private var connect = ConnectController()
    @AppStorage("tab") private var tab = "share"
    /// The first-launch question (Share or Connect) has been answered.
    @AppStorage("welcomeDone") private var welcomeDone = false
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            // Share: this iPhone runs the hotspot server.
            // Connect: this device uses another iPhone's hotspot.
            TabView(selection: $tab) {
                ShareView()
                    .environmentObject(store)
                    .tabItem { Label("Share", systemImage: "personalhotspot") }
                    .tag("share")
                ClientView()
                    .environmentObject(connect)
                    .tabItem { Label("Connect", systemImage: "lock.shield") }
                    .tag("connect")
            }
            .appAppearance()
            // First launch only: which tab this device needs. Never shown to
            // anyone who already shared to a device or paired with an iPhone.
            .fullScreenCover(isPresented: Binding(
                get: { !welcomeDone && store.state.devices.isEmpty && connect.pairing == nil },
                set: { if !$0 { welcomeDone = true } }
            )) {
                WelcomeView { choice in
                    tab = choice
                    welcomeDone = true
                }
                .appAppearance()
            }
            .onChange(of: scenePhase) { _, phase in
                if phase == .active { store.appBecameActive() }
                if phase == .background { store.appWentToBackground() }
            }
            // openbars://hotspot — Shortcuts reporting back after the hotspot shortcut.
            .onOpenURL { store.handleURL($0) }
            // The Allow/Don't Allow prompt for a pairing device lives on Share.
            .onChange(of: store.nextPairingRequest?.publicKey) { _, key in
                if key != nil { tab = "share" }
            }
        }
    }
}
