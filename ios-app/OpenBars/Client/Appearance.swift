import SwiftUI
#if os(iOS)
import UIKit
#else
import AppKit
#endif

/// App-wide Light/Dark choice. Defaults to following the system setting.
enum AppAppearance: String, CaseIterable, Identifiable {
    case system, light, dark

    var id: String { rawValue }

    var title: String {
        switch self {
        case .system: return "System"
        case .light: return "Light"
        case .dark: return "Dark"
        }
    }

    var symbol: String {
        switch self {
        case .system: return "circle.lefthalf.filled"
        case .light: return "sun.max"
        case .dark: return "moon"
        }
    }

    /// Applied to the windows directly: SwiftUI's preferredColorScheme(nil)
    /// doesn't reliably switch back to following the system.
    @MainActor func apply() {
        #if os(iOS)
        let style: UIUserInterfaceStyle = self == .light ? .light : self == .dark ? .dark : .unspecified
        for scene in UIApplication.shared.connectedScenes {
            (scene as? UIWindowScene)?.windows.forEach { $0.overrideUserInterfaceStyle = style }
        }
        #else
        NSApp.appearance = self == .light ? NSAppearance(named: .aqua)
            : self == .dark ? NSAppearance(named: .darkAqua) : nil
        #endif
    }
}

private struct AppearanceModifier: ViewModifier {
    @AppStorage("appearance") private var appearance = AppAppearance.system

    func body(content: Content) -> some View {
        content
            .onAppear { appearance.apply() }
            .onChange(of: appearance) { _, new in new.apply() }
    }
}

extension View {
    /// Put on the root view of each window.
    func appAppearance() -> some View { modifier(AppearanceModifier()) }
}

/// Segmented control for Settings screens.
struct AppearancePicker: View {
    @AppStorage("appearance") private var appearance = AppAppearance.system

    var body: some View {
        Picker("Appearance", selection: $appearance) {
            ForEach(AppAppearance.allCases) { a in
                Text(a.title).tag(a)
            }
        }
        .pickerStyle(.segmented)
        .labelsHidden()
    }
}

/// Compact toolbar menu (Connect tab and the Mac window).
struct AppearanceMenu: View {
    @AppStorage("appearance") private var appearance = AppAppearance.system

    var body: some View {
        Menu {
            Picker("Appearance", selection: $appearance) {
                ForEach(AppAppearance.allCases) { a in
                    Label(a.title, systemImage: a.symbol).tag(a)
                }
            }
            .pickerStyle(.inline)
        } label: {
            Image(systemName: appearance.symbol)
        }
        .help("Appearance")
        .accessibilityLabel("Appearance: \(appearance.title)")
    }
}
