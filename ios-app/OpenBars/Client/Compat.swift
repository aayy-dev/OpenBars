import SwiftUI
#if os(iOS)
import UIKit
#else
import AppKit
#endif

// Open Bars runs on iOS 15 and macOS 13. Each helper here uses the newer API
// where the system has it, and an older equivalent where it doesn't, so the
// screens are unchanged on current systems and still work on older ones.

extension View {
    /// onChange with the new value (the two-value form is iOS 17 / macOS 14).
    @ViewBuilder
    func onChangeCompat<V: Equatable>(of value: V, perform action: @escaping (V) -> Void) -> some View {
        if #available(iOS 17.0, macOS 14.0, *) {
            onChange(of: value) { _, new in action(new) }
        } else {
            onChange(of: value, perform: action)
        }
    }

    /// Grouped form style (iOS 16 / macOS 13). Older systems group forms anyway.
    @ViewBuilder
    func groupedFormStyle() -> some View {
        if #available(iOS 16.0, macOS 13.0, *) { formStyle(.grouped) } else { self }
    }

    /// Rolling digits when a number changes (iOS 16 / macOS 13).
    @ViewBuilder
    func numericTextTransition() -> some View {
        if #available(iOS 16.0, macOS 13.0, *) { contentTransition(.numericText()) } else { self }
    }

    /// Capsule-shaped bordered buttons (iOS 15, macOS 14).
    @ViewBuilder
    func capsuleButtonShape() -> some View {
        if #available(iOS 15.0, macOS 14.0, *) { buttonBorderShape(.capsule) } else { self }
    }

    /// Pushes `destination` while `isPresented` is true: navigationDestination
    /// on iOS 16 / macOS 13, a hidden NavigationLink before.
    @ViewBuilder
    func navigationDestinationCompat<D: View>(isPresented: Binding<Bool>,
                                              @ViewBuilder destination: @escaping () -> D) -> some View {
        if #available(iOS 16.0, macOS 13.0, *) {
            navigationDestination(isPresented: isPresented, destination: destination)
        } else {
            background(NavigationLink(isActive: isPresented, destination: destination) { EmptyView() }.hidden())
        }
    }
}

/// NavigationStack on iOS 16 / macOS 13, a one-column NavigationView before.
struct NavStack<Content: View>: View {
    @ViewBuilder var content: () -> Content

    var body: some View {
        if #available(iOS 16.0, macOS 13.0, *) {
            NavigationStack { content() }
        } else {
            #if os(iOS)
            NavigationView { content() }.navigationViewStyle(.stack)
            #else
            NavigationView { content() }
            #endif
        }
    }
}

/// A label and its value in a list row (LabeledContent on iOS 16 / macOS 13).
struct LabeledRow<Content: View>: View {
    private let title: Text
    private let content: Content

    init(_ titleKey: LocalizedStringKey, @ViewBuilder content: () -> Content) {
        title = Text(titleKey)
        self.content = content()
    }

    @_disfavoredOverload
    init<S: StringProtocol>(_ title: S, @ViewBuilder content: () -> Content) {
        self.title = Text(title)
        self.content = content()
    }

    var body: some View {
        // Each branch is a single view: a multi-view layout directly inside an
        // availability check can crash on older systems.
        if #available(iOS 16.0, macOS 13.0, *) {
            LabeledContent { content } label: { title }
        } else {
            LabeledRowFallback(title: title, content: content)
        }
    }
}

private struct LabeledRowFallback<Content: View>: View {
    let title: Text
    let content: Content

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            title
            Spacer(minLength: 16)
            content
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.trailing)
        }
    }
}

extension LabeledRow where Content == Text {
    init<V: StringProtocol>(_ titleKey: LocalizedStringKey, value: V) {
        self.init(titleKey) { Text(value) }
    }

    @_disfavoredOverload
    init<S: StringProtocol, V: StringProtocol>(_ title: S, value: V) {
        self.init(title) { Text(value) }
    }
}

/// An empty list or screen (ContentUnavailableView on iOS 17 / macOS 14).
struct EmptyState: View {
    let title: String
    let systemImage: String

    var body: some View {
        if #available(iOS 17.0, macOS 14.0, *) {
            ContentUnavailableView(title, systemImage: systemImage)
        } else {
            EmptyStateFallback(title: title, systemImage: systemImage)
        }
    }
}

private struct EmptyStateFallback: View {
    let title: String
    let systemImage: String

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.system(size: 44))
                .foregroundStyle(.secondary)
            Text(title).font(.title3.weight(.semibold))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }
}

/// No search results for `text`.
struct SearchEmptyState: View {
    let text: String

    var body: some View {
        if #available(iOS 17.0, macOS 14.0, *) {
            ContentUnavailableView.search(text: text)
        } else {
            EmptyState(title: "No Results for “\(text)”", systemImage: "magnifyingglass")
        }
    }
}

extension FileManager {
    /// Application Support, created if needed. (URL.applicationSupportDirectory
    /// is iOS 16 / macOS 13; this is the same folder.)
    static var appSupportFolder: URL {
        let folder = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        return folder
    }
}

/// Answers once, from whichever thread gets there first (OSAllocatedUnfairLock
/// is iOS 16 / macOS 13).
final class OnceFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false

    /// True the first time only.
    func claim() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if done { return false }
        done = true
        return true
    }
}

#if os(iOS)
/// Shares a URL: ShareLink on iOS 16, the share sheet on iOS 15.
struct ShareButton<Label: View>: View {
    let url: URL
    @ViewBuilder var label: () -> Label
    @State private var sharing = false

    var body: some View {
        if #available(iOS 16.0, *) {
            ShareLink(item: url, label: label)
        } else {
            Button(action: { sharing = true }, label: label)
                .sheet(isPresented: $sharing) { ActivitySheet(items: [url]) }
        }
    }
}

/// The system share sheet (for iOS 15, which has no ShareLink).
struct ActivitySheet: UIViewControllerRepresentable {
    let items: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
#else
extension NSApplication {
    /// Brings the app forward (activate() is macOS 14).
    func activateCompat() {
        if #available(macOS 14.0, *) {
            activate()
        } else {
            activate(ignoringOtherApps: true)
        }
    }
}
#endif
