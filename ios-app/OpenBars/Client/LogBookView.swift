import CoreTransferable
import SwiftUI
import UniformTypeIdentifiers

/// A log (Share or Connect) as a monospaced list, newest at the bottom, with
/// a level filter, search, export and the Detailed logging switch. Used by
/// Share's log sheet, Connect's log sheet on iOS and the Mac's Connect Log window.
struct LogBookView: View {
    @ObservedObject var book: LogBook
    @AppStorage(LogFormat.utcKey) private var utc = false
    @State private var filter: Filter = .all
    @State private var search = ""
    @State private var confirmClear = false
    @State private var atBottom = true
    @State private var exportURL: URL? // iOS 15's share sheet
    #if os(iOS)
    @Environment(\.dismiss) private var dismiss
    #endif

    enum Filter: Hashable { case all, problems }

    private static let bottom = "bottom"

    private var shown: [LogEntry] {
        let q = search.trimmingCharacters(in: .whitespaces)
        return book.entries.filter { e in
            if filter == .problems && e.level < .warn { return false }
            guard !q.isEmpty else { return true }
            return e.message.localizedCaseInsensitiveContains(q)
                || e.source.rawValue.localizedCaseInsensitiveContains(q)
                || (e.hint?.localizedCaseInsensitiveContains(q) ?? false)
        }
    }

    var body: some View {
        let rows = shown
        VStack(spacing: 0) {
            Picker("Show", selection: $filter) {
                Text("All").tag(Filter.all)
                Text("Warnings & errors").tag(Filter.problems)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(.horizontal)
            .padding(.vertical, 8)
            if let until = book.detailedUntil, book.isDetailed {
                detailedBanner(until)
            }
            Divider()
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 3) {
                        ForEach(rows) { LogRow(entry: $0, utc: utc) }
                        Text("Detailed logging (in the \(Image(systemName: "ellipsis.circle")) menu) adds per-connection detail for 30 minutes. Detailed logs include the addresses your devices connect to and stay on this device unless you share them.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 12)
                        Color.clear.frame(height: 1).id(Self.bottom)
                            .onAppear { atBottom = true }
                            .onDisappear { atBottom = false }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                }
                .overlay {
                    if rows.isEmpty {
                        if search.isEmpty {
                            EmptyState(title: filter == .all ? "Nothing logged yet" : "No warnings or errors",
                                       systemImage: "doc.text.magnifyingglass")
                        } else {
                            SearchEmptyState(text: search)
                        }
                    }
                }
                .onAppear { proxy.scrollTo(Self.bottom, anchor: .bottom) }
                .onChangeCompat(of: book.entries.last?.id) { _ in
                    if atBottom { proxy.scrollTo(Self.bottom, anchor: .bottom) }
                }
                .onChangeCompat(of: filter) { _ in proxy.scrollTo(Self.bottom, anchor: .bottom) }
            }
        }
        .searchable(text: $search, prompt: "Search")
        .navigationTitle("\(book.kind.title) Log")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar { toolbar }
        .confirmationDialog("Clear the \(book.kind.title) log?", isPresented: $confirmClear) {
            Button("Clear Log", role: .destructive) { book.clear() }
        } message: {
            Text("This deletes every entry on this device.")
        }
    }

    private func detailedBanner(_ until: Date) -> some View {
        Label {
            Text("Detailed logging is on until \(until, style: .time). It turns itself off after 30 minutes.")
        } icon: {
            Image(systemName: "ant")
        }
        .font(.footnote)
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal)
        .padding(.bottom, 8)
    }

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        #if os(iOS)
        ToolbarItemGroup(placement: .topBarLeading) {
            shareButton
            optionsMenu
        }
        ToolbarItem(placement: .pinnedTrailing) {
            Button("Done", systemImage: "checkmark") { dismiss() }
        }
        #else
        ToolbarItemGroup {
            shareButton
            Button {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(book.exportText(), forType: .string)
            } label: { Label("Copy", systemImage: "doc.on.doc") }
                .help("Copy the whole log")
                .disabled(book.entries.isEmpty)
            optionsMenu
        }
        #endif
    }

    @ViewBuilder private var shareButton: some View {
        if #available(iOS 16.0, macOS 13.0, *) {
            ShareLink(item: LogExport(book: book), preview: SharePreview(book.exportFileName)) {
                Label("Share", systemImage: "square.and.arrow.up")
            }
            .disabled(book.entries.isEmpty)
        } else {
            #if os(iOS)
            // iOS 15 has no ShareLink: write the file, then show the share sheet.
            Button {
                exportURL = try? LogExport.writeFile(name: book.exportFileName, text: book.exportText())
            } label: {
                Label("Share", systemImage: "square.and.arrow.up")
            }
            .disabled(book.entries.isEmpty)
            .sheet(isPresented: Binding(get: { exportURL != nil }, set: { if !$0 { exportURL = nil } })) {
                if let url = exportURL { ActivitySheet(items: [url]) }
            }
            #endif
        }
    }

    private var optionsMenu: some View {
        Menu {
            Toggle(isOn: Binding(get: { book.isDetailed }, set: { book.setDetailed($0) })) {
                Label("Detailed Logging (30 min)", systemImage: "ant")
            }
            Toggle(isOn: $utc) {
                Label("Show Times in UTC", systemImage: "globe")
            }
            Divider()
            Button(role: .destructive) { confirmClear = true } label: {
                Label("Clear Log…", systemImage: "trash")
            }
            .disabled(book.entries.isEmpty)
        } label: {
            Label("Options", systemImage: "ellipsis.circle")
        }
    }
}

/// One entry: its text line(s), colored by level.
private struct LogRow: View {
    let entry: LogEntry
    let utc: Bool

    var body: some View {
        Text(LogFormat.text(entry, utc: utc))
            #if os(iOS)
            .font(.system(size: 11, design: .monospaced))
            #else
            .font(.system(.caption, design: .monospaced))
            #endif
            .foregroundStyle(color)
            .frame(maxWidth: .infinity, alignment: .leading)
            .textSelection(.enabled)
    }

    private var color: Color {
        switch entry.level {
        case .error: return .red
        case .warn: return .orange
        case .debug: return .secondary
        case .info: return .primary
        }
    }
}

/// The log as a .txt file for the share sheet ("Open Bars Share log 2026-11-02.txt").
struct LogExport {
    let book: LogBook

    /// Writes the exported log to a temporary file to share.
    static func writeFile(name: String, text: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("log-export", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent(name)
        try text.write(to: url, atomically: true, encoding: .utf8)
        return url
    }
}

@available(iOS 16.0, macOS 13.0, *) // Transferable and ShareLink
extension LogExport: Transferable {
    static var transferRepresentation: some TransferRepresentation {
        FileRepresentation(exportedContentType: .plainText) { export in
            let (name, text) = await MainActor.run { (export.book.exportFileName, export.book.exportText()) }
            return SentTransferredFile(try LogExport.writeFile(name: name, text: text))
        }
    }
}
