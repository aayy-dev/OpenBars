import Foundation
import NetworkExtension

/// Manages the OpenBarsTunnel extension: installs its VPN configuration with
/// on-demand rules (like Tailscale's toggle), starts/stops it, and exchanges
/// messages with the running extension.
@MainActor
final class TunnelController: ObservableObject {
    static let providerBundleID = "dev.aayy.openbars.tunnel"

    @Published private(set) var manager: NETunnelProviderManager?
    @Published private(set) var installed = false

    var status: NEVPNStatus { manager?.connection.status ?? .invalid }
    var isActive: Bool { [.connected, .connecting, .reasserting].contains(status) }
    var isEnabled: Bool { manager?.isEnabled ?? false }
    var isOnDemandEnabled: Bool { manager?.isOnDemandEnabled ?? false }

    private var observer: Any?

    init() {
        // Watch for VPN status changes so the UI stays in sync.
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange, object: nil, queue: .main
        ) { [weak self] _ in self?.objectWillChange.send() }
    }

    deinit { observer.map { NotificationCenter.default.removeObserver($0) } }

    func load() async {
        let all = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        manager = all.first { ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.providerBundleID }
        installed = manager != nil
    }

    /// Ensures the tunnel configuration exists with the current engine config.
    /// iOS only shows the "add VPN configurations" prompt the first time.
    func install(config: String, onDemand: Bool) async throws {
        await load()
        let needsSave: Bool
        let m: NETunnelProviderManager

        if let existing = manager {
            m = existing
            try await m.loadFromPreferences()
            // Update the engine config if it changed; skip saving otherwise
            // so iOS doesn't re-prompt.
            let oldConfig = (m.protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration?["config"] as? String
            needsSave = (oldConfig != config) || m.isOnDemandEnabled != onDemand || !m.isEnabled
        } else {
            m = NETunnelProviderManager()
            needsSave = true
        }

        if needsSave {
            let proto = (m.protocolConfiguration as? NETunnelProviderProtocol) ?? NETunnelProviderProtocol()
            proto.providerBundleIdentifier = Self.providerBundleID
            proto.serverAddress = "This iPhone"
            proto.providerConfiguration = ["config": config]
            proto.disconnectOnSleep = false
            // Keep the hotspot bridge (172.20.10.0/28) alive: local traffic must
            // not go through the tunnel, otherwise iOS tears down bridge100.
            proto.excludeLocalNetworks = true
            proto.includeAllNetworks = false
            m.protocolConfiguration = proto
            m.localizedDescription = "Open Bars"
            m.isEnabled = true

            if onDemand {
                let always = NEOnDemandRuleConnect()
                always.interfaceTypeMatch = .any
                m.onDemandRules = [always]
                m.isOnDemandEnabled = true
            } else {
                m.isOnDemandEnabled = false
            }

            try await m.saveToPreferences()
            try await m.loadFromPreferences()
        }

        manager = m
        installed = true
    }

    func start() throws {
        guard let m = manager else { return }
        if m.connection.status == .disconnected || m.connection.status == .invalid {
            try m.connection.startVPNTunnel()
        }
    }

    func stop() {
        manager?.connection.stopVPNTunnel()
    }

    /// Toggle on-demand without reinstalling the whole config.
    func setOnDemand(_ enabled: Bool) async throws {
        guard let m = manager else { return }
        try await m.loadFromPreferences()
        if enabled {
            let always = NEOnDemandRuleConnect()
            always.interfaceTypeMatch = .any
            m.onDemandRules = [always]
        }
        m.isOnDemandEnabled = enabled
        try await m.saveToPreferences()
    }

    func remove() async throws {
        guard let m = manager else { return }
        try await m.removeFromPreferences()
        manager = nil
        installed = false
    }

    /// Sends a message to the running extension and returns its reply.
    func send(_ message: String) async -> String? {
        guard let session = manager?.connection as? NETunnelProviderSession,
              session.status == .connected else { return nil }
        return await withCheckedContinuation { cont in
            do {
                try session.sendProviderMessage(Data(message.utf8)) { data in
                    cont.resume(returning: data.map { String(decoding: $0, as: UTF8.self) })
                }
            } catch {
                cont.resume(returning: nil)
            }
        }
    }
}
