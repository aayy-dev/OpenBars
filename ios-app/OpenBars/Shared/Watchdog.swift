import Foundation

/// Tells the user when background sharing ends without warning.
///
/// iOS calls stopTunnel when it stops the extension the normal way, but not
/// when it ends the process outright (over its memory limit, or a crash),
/// so nothing could say so then. Instead, while sharing runs, a "Sharing
/// stopped" notification stays scheduled `grace` seconds ahead and is pushed
/// back every `every` seconds. If the process dies, nothing pushes it back
/// and iOS shows it.
///
/// While the device sleeps the extension's timers don't run, so the notice
/// is taken off the schedule then (sleep/wake) instead of going off falsely.
final class Watchdog {
    static let noticeID = "watchdog"

    private let every: TimeInterval
    private let grace: TimeInterval
    private let queue = DispatchQueue(label: "dev.aayy.openbars.watchdog")
    private var timer: DispatchSourceTimer?
    private var asleep = false
    private var running = false

    init(every: TimeInterval = 30, grace: TimeInterval = 90) {
        self.every = every
        self.grace = grace
    }

    /// How the last run ended, read before start().
    struct LastRun {
        /// It ended without stop(): iOS ended the process, or it crashed.
        var endedWithoutWarning: Bool
        /// Its last sign of life.
        var lastAlive: Date?
        /// When the "Sharing stopped" notice showed (nil: it didn't).
        var noticeShown: Date?
    }

    static func lastRun(now: Date = Date()) -> LastRun {
        let alive = SharedState.watchdogAlive
        let due = SharedState.watchdogDue
        return LastRun(endedWithoutWarning: alive > 0,
                       lastAlive: alive > 0 ? Date(timeIntervalSince1970: alive) : nil,
                       noticeShown: due > 0 && now.timeIntervalSince1970 >= due ? Date(timeIntervalSince1970: due) : nil)
    }

    func start() {
        queue.async { [self] in
            running = true
            asleep = false
            push()
            let t = DispatchSource.makeTimerSource(queue: queue)
            t.schedule(deadline: .now() + every, repeating: every, leeway: .seconds(2))
            t.setEventHandler { [weak self] in self?.push() }
            t.resume()
            timer = t
        }
    }

    /// Sharing stopped the normal way: no notice.
    func stop() {
        queue.sync {
            running = false
            timer?.cancel()
            timer = nil
            Notifier.removePending(id: Self.noticeID)
            SharedState.watchdogDue = 0
            SharedState.watchdogAlive = 0
        }
    }

    /// The device is about to sleep.
    func sleep() {
        queue.sync {
            asleep = true
            Notifier.removePending(id: Self.noticeID)
            SharedState.watchdogDue = 0
            SharedState.watchdogAlive = Date().timeIntervalSince1970
        }
    }

    /// The device woke up.
    func wake() {
        queue.async { [self] in
            asleep = false
            push()
        }
    }

    /// On `queue`: still alive, so the notice moves `grace` seconds ahead.
    private func push() {
        guard running, !asleep else { return }
        let now = Date().timeIntervalSince1970
        SharedState.watchdogAlive = now
        guard SharedState.notificationPrefs.stopped else {
            Notifier.removePending(id: Self.noticeID)
            SharedState.watchdogDue = 0
            return
        }
        SharedState.watchdogDue = now + grace
        Notifier.postSimple(id: Self.noticeID, title: "Sharing stopped",
                            body: "Background sharing ended without warning, so your devices lost the connection. Open Open Bars to start it again.",
                            after: grace)
    }
}
