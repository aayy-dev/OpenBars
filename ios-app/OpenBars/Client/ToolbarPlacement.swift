#if os(iOS)
import SwiftUI

extension ToolbarItemPlacement {
    /// Where Done and Save go. On iOS 27 it's pinned to the top trailing
    /// corner, so it stays visible when iPhone Duo shows the bars down the
    /// side of the display ("Preparing your app for iPhone Duo").
    static var pinnedTrailing: ToolbarItemPlacement {
        if #available(iOS 27.0, *) { return .topBarPinnedTrailing }
        return .topBarTrailing
    }
}
#endif
