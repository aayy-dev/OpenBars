import Foundation

/// A byte count the way iOS shows file sizes ("1.2 MB", 1 kB = 1,000 bytes).
func formatBytes(_ bytes: Int64) -> String {
    byteFormatter.string(fromByteCount: bytes)
}

private let byteFormatter: ByteCountFormatter = {
    let f = ByteCountFormatter()
    f.countStyle = .file
    f.allowsNonnumericFormatting = false // "0 bytes", not "Zero KB"
    return f
}()

/// How long ago a Unix time was: "just now", "5 min. ago"; "never" for 0.
func formatAgo(_ unixSeconds: Int64) -> String {
    if unixSeconds <= 0 { return "never" }
    let elapsed = Date().timeIntervalSince1970 - TimeInterval(unixSeconds)
    if elapsed < 10 { return "just now" }
    return agoFormatter.localizedString(fromTimeInterval: -elapsed)
}

private let agoFormatter: RelativeDateTimeFormatter = {
    let f = RelativeDateTimeFormatter()
    f.unitsStyle = .abbreviated
    return f
}()
