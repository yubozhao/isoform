import Foundation

// Recordings are kept by the clock hour. While the watch records, the recording that is open is
// closed at the next hour and a new one begins at once, with no stop in between; so the phone and
// the computer get an hour at a time, each sent and turned into text on its own, and nothing ever
// reads "recording for two hours". Inside an hour the audio is still written in parts of
// WatchRecordingWire.partSeconds, so it starts travelling before the hour ends.
enum WatchHourClock {
    /// An hour begun this close to its end runs through the next one instead of being cut
    /// seconds after it began.
    static let shortestHourSeconds: TimeInterval = 60
    /// A part is never asked to run shorter than this.
    static let shortestPartSeconds: TimeInterval = 1

    /// When the recording begun at `start` is closed: the next clock hour, or the one after when
    /// the next is less than `shortestHourSeconds` away.
    static func cut(after start: Date, calendar: Calendar = .current) -> Date {
        let onTheHour = DateComponents(minute: 0, second: 0, nanosecond: 0)
        let fallback = start.addingTimeInterval(3600)
        guard let next = calendar.nextDate(after: start, matching: onTheHour, matchingPolicy: .nextTime) else {
            return fallback
        }
        if next.timeIntervalSince(start) >= shortestHourSeconds { return next }
        return calendar.nextDate(after: next, matching: onTheHour, matchingPolicy: .nextTime) ?? fallback
    }

    /// How long the part begun at `date` runs, and whether its end is the hour's cut.
    static func part(from date: Date, cut: Date,
                     partSeconds: TimeInterval = WatchRecordingWire.partSeconds) -> (seconds: TimeInterval, closesHour: Bool) {
        let left = cut.timeIntervalSince(date)
        if left <= partSeconds { return (max(shortestPartSeconds, left), true) }
        return (partSeconds, false)
    }

    /// Whether a recording whose cut is `cut` is over at `date` (after a call held the microphone
    /// across the hour, the next part belongs to a new recording).
    static func isPast(_ cut: Date, at date: Date) -> Bool { date >= cut }

    /// The most parts one hour can hold before it must be closed early: the last number the wire
    /// allows closes the recording by itself.
    static func mustClose(afterPart part: Int) -> Bool { part + 1 >= WatchRecordingWire.maxParts - 1 }

    /// A recording's length in whole minutes for the list, never less than one.
    static func minutes(_ seconds: Double) -> Int {
        guard seconds.isFinite, seconds > 0 else { return 1 }
        return max(1, Int((seconds / 60).rounded()))
    }
}
