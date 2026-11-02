import SwiftUI

/// Checks each link the tunnel needs, in order, with a fix button where the
/// app can fix it.
struct DiagnoseView: View {
    @EnvironmentObject var connect: ConnectController
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section {
                    ForEach(connect.diagnostics) { DiagnosticRow(check: $0) }
                    if connect.diagnosing {
                        HStack(spacing: 10) {
                            ProgressView().controlSize(.small)
                            Text("Checking…").foregroundStyle(.secondary)
                        }
                    }
                } footer: {
                    if !connect.diagnosing, !connect.diagnostics.isEmpty { Text(summary) }
                }
            }
            .navigationTitle("Diagnose")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .primaryAction) {
                    Button("Check Again") { Task { await connect.runDiagnostics() } }
                        .disabled(connect.diagnosing)
                }
            }
            .task { await connect.runDiagnostics() }
        }
        #if os(macOS)
        .frame(minWidth: 460, minHeight: 440)
        #endif
    }

    private var summary: String {
        if let bad = connect.diagnostics.first(where: { $0.state == .failed }) {
            return "Stopped at “\(bad.title)”: the checks after it depend on it."
        }
        if connect.diagnostics.contains(where: { $0.state == .warning }) {
            return "Working, with something worth a look above."
        }
        return "Everything checks out."
    }
}

private struct DiagnosticRow: View {
    @EnvironmentObject var connect: ConnectController
    let check: DiagnosticCheck

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(tint)
            VStack(alignment: .leading, spacing: 4) {
                Text(check.title).font(.body.weight(.medium))
                Text(check.detail).font(.subheadline).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if let fix = check.fix, let title = fix.title {
                    Button(title) { Task { await connect.apply(fix) } }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .disabled(connect.busy || connect.diagnosing)
                }
            }
        }
        .padding(.vertical, 2)
    }

    private var symbol: String {
        switch check.state {
        case .ok: return "checkmark.circle.fill"
        case .warning: return "exclamationmark.triangle.fill"
        case .failed: return "xmark.octagon.fill"
        }
    }

    private var tint: Color {
        switch check.state {
        case .ok: return .green
        case .warning: return .orange
        case .failed: return .red
        }
    }
}
