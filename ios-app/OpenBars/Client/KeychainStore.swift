import Foundation
import Security

/// Small Keychain wrapper. Connect keeps this device's WireGuard private key
/// here; Keychain items survive reinstalling the app, so pairing does too.
enum KeychainStore {
    private static let service = "dev.aayy.openbars"

    private static func query(_ key: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: key]
    }

    static func get(_ key: String) -> String? {
        var q = query(key)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess,
              let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func set(_ key: String, _ value: String) {
        let data = Data(value.utf8)
        let attrs: [String: Any] = [kSecValueData as String: data,
                                    kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlock]
        if SecItemUpdate(query(key) as CFDictionary, attrs as CFDictionary) == errSecItemNotFound {
            var add = query(key)
            add.merge(attrs) { $1 }
            SecItemAdd(add as CFDictionary, nil)
        }
    }
}
