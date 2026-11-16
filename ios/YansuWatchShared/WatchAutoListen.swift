import Foundation

// Recording is the standing state: the watch records whenever its app comes to the front, with
// nothing to tap, until the wearer holds to stop. A stop is the off for that day: it holds until
// the wearer starts again or the day ends, so raising the wrist never restarts what they just
// ended, and the next morning the watch records again by itself. watchOS does not let an app open the
// microphone from the background, so "comes to the front" is the most automatic start there is:
// opening the app, tapping the complication, or the Action button.
enum WatchAutoListen {
    /// When the wearer stopped it, kept across launches (UserDefaults on the watch, seconds since
    /// 1970; absent when they did not).
    static let stoppedKey = "watch.stoppedByWearerAt"

    /// A stop holds for the rest of the day it was made on.
    static func stopHolds(stoppedAt: Date?, now: Date, calendar: Calendar = .current) -> Bool {
        guard let stoppedAt else { return false }
        return calendar.isDate(stoppedAt, inSameDayAs: now)
    }

    static func stoppedAt(in defaults: UserDefaults = .standard) -> Date? {
        guard let seconds = defaults.object(forKey: stoppedKey) as? Double, seconds > 0 else { return nil }
        return Date(timeIntervalSince1970: seconds)
    }

    static func setStopped(_ date: Date?, in defaults: UserDefaults = .standard) {
        if let date {
            defaults.set(date.timeIntervalSince1970, forKey: stoppedKey)
        } else {
            defaults.removeObject(forKey: stoppedKey)
        }
    }

    enum Microphone: Equatable {
        case notAsked, allowed, denied
    }

    enum Step: Equatable {
        /// Leave things as they are.
        case nothing
        /// Start recording (asks for the microphone the first time).
        case start
        /// A call or Siri ended while the app was not in front: open the microphone again.
        case resume
        /// Say where to allow the microphone.
        case showDenied
    }

    /// What the watch does when its app comes to the front.
    static func step(consentSeen: Bool, microphone: Microphone, stoppedByWearer: Bool,
                     isRecording: Bool, heldByInterruption: Bool) -> Step {
        // Nothing is recorded before the wearer has read where recordings go.
        guard consentSeen else { return .nothing }
        if isRecording { return heldByInterruption ? .resume : .nothing }
        if stoppedByWearer { return .nothing }
        switch microphone {
        case .denied: return .showDenied
        case .notAsked, .allowed: return .start
        }
    }
}
