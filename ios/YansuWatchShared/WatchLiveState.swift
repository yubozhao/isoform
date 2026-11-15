import Foundation

// Whether the watch is recording right now, shared between the watch app and its complication
// through the app group's defaults. It holds one time and nothing else: no audio, no words, no
// heart rate and no place ever go into the shared container.
enum WatchLiveState {
    static let group = "group.com.isoform.companion"
    static let key = "watch.recordingSince"
    static let complicationKind = "com.isoform.companion.watch.record"
    /// The longest a recording can run (64 parts). A start older than this is a leftover from an
    /// app that was killed, and reads as not recording.
    static let longest = Double(WatchRecordingWire.maxParts) * WatchRecordingWire.partSeconds

    static var shared: UserDefaults? { UserDefaults(suiteName: group) }

    /// Records that a recording began at `since`, or that none is running.
    static func write(recordingSince since: Date?, to defaults: UserDefaults? = shared) {
        guard let defaults else { return }
        if let since {
            defaults.set(since.timeIntervalSince1970, forKey: key)
        } else {
            defaults.removeObject(forKey: key)
        }
    }

    /// When the running recording began; nil when none is running.
    static func recordingSince(in defaults: UserDefaults? = shared, now: Date = Date()) -> Date? {
        guard let defaults, let stored = defaults.object(forKey: key) as? Double, stored > 0 else { return nil }
        let since = Date(timeIntervalSince1970: stored)
        let age = now.timeIntervalSince(since)
        guard age >= -60, age <= longest else { return nil }
        return since
    }
}
