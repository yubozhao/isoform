import Foundation

// What the watch recorded against how long it was meant to listen, so a silent loss is visible.
//
// While a listening session is on, every second should end up in a part. The log keeps, per
// day, how long sessions were on, how many seconds of audio were kept, and each stretch in which
// the microphone was not held, with its reason. The phone shows the day's line; nothing here
// holds audio or words.
final class WatchGapLog: @unchecked Sendable {
    enum Reason: String, Codable, Sendable {
        /// A call or Siri took the microphone.
        case interrupted
        /// The microphone stopped delivering audio by itself.
        case quiet
        /// The audio route or format changed and the capture could not go on.
        case routeChanged
        /// The app was not running although listening was on (it was ended by the system).
        case appEnded
    }

    struct Gap: Codable, Equatable, Sendable {
        var from: Date
        /// Nil while the microphone is still not held.
        var to: Date?
        var reason: Reason
    }

    struct Day: Codable, Equatable, Sendable {
        /// The day's start, local time.
        var day: Date
        /// Seconds a listening session was on.
        var listeningSeconds: Double = 0
        /// Seconds of audio kept in parts.
        var recordedSeconds: Double = 0
        var gaps: [Gap] = []

        /// Seconds the microphone was not held although listening was on.
        func missedSeconds(now: Date) -> Double {
            gaps.reduce(0) { $0 + max(0, ($1.to ?? now).timeIntervalSince($1.from)) }
        }
    }

    /// What the phone is told: small, and never more than numbers.
    struct Summary: Equatable, Sendable {
        var listeningSeconds: Int
        var recordedSeconds: Int
        var missedSeconds: Int
        var stops: Int
        /// The microphone is not held right now although listening is on.
        var stoppedNow: Bool
    }

    static let keptDays = 14

    private let url: URL?
    private let calendar: Calendar
    private let lock = NSLock()
    private var days: [Day] = []
    /// When the running session began, or last was accounted for.
    private var onSince: Date?

    private struct Stored: Codable {
        var days: [Day]
        var onSince: Date?
    }

    init(url: URL?, calendar: Calendar = .current) {
        self.url = url
        self.calendar = calendar
        if let url, let data = try? Data(contentsOf: url), let stored = try? JSONDecoder().decode(Stored.self, from: data) {
            days = stored.days
            onSince = stored.onSince
        }
    }

    /// A listening session began.
    func began(at now: Date) {
        lock.withLock {
            onSince = now
            saveLocked()
        }
    }

    /// The session ended on purpose. An open gap ends with it.
    func ended(at now: Date) {
        lock.withLock {
            accountLocked(until: now)
            closeLocked(at: now)
            onSince = nil
            saveLocked()
        }
    }

    /// Audio was kept.
    func recorded(_ seconds: Double, at date: Date) {
        guard seconds > 0 else { return }
        lock.withLock {
            let index = dayIndexLocked(for: date)
            days[index].recordedSeconds += seconds
            saveLocked()
        }
    }

    /// The microphone is no longer held.
    func open(at now: Date, reason: Reason) {
        lock.withLock {
            accountLocked(until: now)
            let index = dayIndexLocked(for: now)
            if days[index].gaps.last?.to != nil || days[index].gaps.isEmpty {
                days[index].gaps.append(Gap(from: now, to: nil, reason: reason))
            }
            saveLocked()
        }
    }

    /// The microphone is held again.
    func close(at now: Date) {
        lock.withLock {
            accountLocked(until: now)
            closeLocked(at: now)
            saveLocked()
        }
    }

    /// At launch: listening was on when the app last ran, so the app was ended while it listened.
    /// Returns when that session had last been accounted for, nil when none was on.
    @discardableResult
    func foundAfterLaunch(at now: Date, lastAudioAt: Date?) -> Date? {
        lock.withLock {
            guard let since = onSince else { return nil }
            let from = lastAudioAt ?? since
            let index = dayIndexLocked(for: from)
            if days[index].gaps.last?.to != nil || days[index].gaps.isEmpty {
                days[index].gaps.append(Gap(from: from, to: now, reason: .appEnded))
            } else {
                days[index].gaps[days[index].gaps.count - 1].to = now
            }
            days[index].listeningSeconds += max(0, now.timeIntervalSince(since))
            onSince = nil
            saveLocked()
            return since
        }
    }

    func summary(for now: Date) -> Summary {
        lock.withLock {
            let start = calendar.startOfDay(for: now)
            let day = days.first { $0.day == start } ?? Day(day: start)
            let running = onSince.map { max(0, now.timeIntervalSince(max($0, start))) } ?? 0
            return Summary(listeningSeconds: Int((day.listeningSeconds + running).rounded()),
                           recordedSeconds: Int(day.recordedSeconds.rounded()),
                           missedSeconds: Int(day.missedSeconds(now: now).rounded()),
                           stops: day.gaps.count,
                           stoppedNow: day.gaps.last.map { $0.to == nil } ?? false)
        }
    }

    func day(for now: Date) -> Day? {
        lock.withLock { days.first { $0.day == calendar.startOfDay(for: now) } }
    }

    private func accountLocked(until now: Date) {
        guard let since = onSince else { return }
        let index = dayIndexLocked(for: now)
        days[index].listeningSeconds += max(0, now.timeIntervalSince(since))
        onSince = now
    }

    private func closeLocked(at now: Date) {
        for index in days.indices {
            for gap in days[index].gaps.indices where days[index].gaps[gap].to == nil {
                days[index].gaps[gap].to = now
            }
        }
    }

    private func dayIndexLocked(for date: Date) -> Int {
        let start = calendar.startOfDay(for: date)
        if let index = days.firstIndex(where: { $0.day == start }) { return index }
        days.append(Day(day: start))
        days.sort { $0.day < $1.day }
        if days.count > Self.keptDays { days.removeFirst(days.count - Self.keptDays) }
        return days.firstIndex(where: { $0.day == start }) ?? days.count - 1
    }

    private func saveLocked() {
        guard let url, let data = try? JSONEncoder().encode(Stored(days: days, onSince: onSince)) else { return }
        try? data.write(to: url, options: .atomic)
    }
}
