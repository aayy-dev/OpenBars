import SwiftUI

/// Required next to any use of the name (wireguard.com/trademark-policy).
let wireGuardTrademark = "“WireGuard” and the “WireGuard” logo are registered trademarks of Jason A. Donenfeld."

/// PRIVACY.md in the repository.
let privacyPolicyURL = URL(string: "https://github.com/aayy-dev/OpenBars/blob/main/PRIVACY.md")!

/// What Open Bars collects (nothing), shown before sharing or pairing starts:
/// App Review asks VPN apps to say so on screen before the service is used
/// (guideline 5.4).
struct PrivacyNote: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(text)
            Link("Privacy Policy", destination: privacyPolicyURL)
        }
    }
}

/// Settings › About, in Share's and Connect's settings (iOS and Mac).
struct AboutSection: View {
    var body: some View {
        Section {
            LabeledRow("Version", value: Self.version)
            NavigationLink("Open-Source Licenses") { LicensesView() }
            Link("Privacy Policy", destination: privacyPolicyURL)
            Link("Source Code", destination: URL(string: "https://github.com/aayy-dev/OpenBars")!)
        } header: {
            Text("About")
        }
    }

    private static var version: String {
        let info = Bundle.main.infoDictionary ?? [:]
        let short = info["CFBundleShortVersionString"] as? String ?? "?"
        let build = info["CFBundleVersion"] as? String ?? "?"
        return "\(short) (\(build))"
    }
}

/// The open-source code built into Open Bars. Resources/Licenses.json is
/// written by tools/make_licenses.py.
struct OpenSourceComponent: Decodable, Identifiable {
    let name: String
    let module: String
    let version: String?
    let license: String
    let copyright: String?
    let url: String?
    let text: String
    var id: String { module }

    static let bundled: [OpenSourceComponent] = {
        struct File: Decodable { let components: [OpenSourceComponent] }
        guard let url = Bundle.main.url(forResource: "Licenses", withExtension: "json"),
              let data = try? Data(contentsOf: url),
              let file = try? JSONDecoder().decode(File.self, from: data) else { return [] }
        return file.components
    }()
}

struct LicensesView: View {
    var body: some View {
        Form {
            Section {
                ForEach(OpenSourceComponent.bundled) { c in
                    NavigationLink { LicenseTextView(component: c) } label: {
                        LabeledRow(c.name, value: c.license)
                    }
                }
            } footer: {
                Text("Open Bars includes this open-source software. Each one's license is inside.")
            }
            Section {
                Text(wireGuardTrademark)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .groupedFormStyle()
        .navigationTitle("Open-Source Licenses")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }
}

private struct LicenseTextView: View {
    let component: OpenSourceComponent

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(component.module).font(.subheadline.monospaced())
                    if let version = component.version {
                        Text(version).font(.caption.monospaced()).foregroundStyle(.secondary)
                    }
                }
                if let link = component.url.flatMap(URL.init(string:)) {
                    Link(link.absoluteString, destination: link).font(.subheadline)
                }
                if let copyright = component.copyright {
                    Text(copyright).font(.caption.monospaced())
                }
                Text(component.text).font(.caption.monospaced())
            }
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding()
        }
        .navigationTitle(component.name)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }
}
