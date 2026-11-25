import Foundation

// "Where your hours are" (phone-watch v1.1.0 rules 11 to 13, v1.2.0 rules 1 to 5): three places
// with their counts, and under them one sentence, the one thing the wearer needs. Always shown,
// not only when something is wrong.

/// What holds the hours back, as far as this phone knows.
enum WatchComputerState: Equatable, Sendable {
    /// Reached, or nothing known against it.
    case reachable
    case asleep
    case unreachable
    /// Closed Eyes is on there: hours wait until it ends.
    case closedEyes
}

/// Why hours waited while the app was not running, when iOS says so (v1.2.0 rule 1).
enum WatchWaitCause: Int, Equatable, Sendable, Codable {
    /// The person swiped Isoform away (NSURLErrorCancelledReasonUserForceQuitApplication).
    case closed = 0
    /// Background App Refresh is off for Isoform (NSURLErrorCancelledReasonBackgroundUpdatesDisabled).
    case refreshOff = 1
}

struct WatchWhere: Equatable, Sendable {
    enum Place: Equatable, Sendable { case watch, phone, computer }

    /// Hours not yet on the computer, from any day.
    let onWatch: Int
    let onPhone: Int
    /// Today's hours on the computer.
    let onComputer: Int
    /// The place holding hours that cannot go, ringed in amber. Nil while every hour is across or going.
    let ringed: Place?
    let sentence: String
    /// The one fact under the sentence ("Swiping Isoform away stops sending until you open it again.").
    let fact: String?
    /// Something is stuck and a tap may help.
    let showsTryAgain: Bool

    static func hours(_ count: Int) -> String {
        count == 1 ? String(localized: "1 hour") : String(localized: "\(count) hours")
    }

    /// `watchFound` is false while the system says Isoform is not on the watch.
    static func of(rows: [WatchRecordingRow], outcome: WatchRecordingForwarder.Outcome, computer: WatchComputerState,
                   cause: WatchWaitCause? = nil, sendingSince: Date? = nil, watchFound: Bool = true, now: Date = Date(),
                   calendar: Calendar = .current, locale: Locale = .current) -> WatchWhere {
        let onWatch = rows.filter { $0.stage == .onWatch }.count
        let failed = rows.filter { $0.stage == .failedToSend }.count
        let onPhone = rows.filter { $0.stage == .onPhone || $0.stage == .sending }.count + failed
        let across = rows.filter { $0.stage == .onComputer || $0.stage == .transcribed || $0.stage == .failedOnComputer }
        let onComputer = across.filter { calendar.isDate($0.startedAt, inSameDayAs: now) }.count
        func ended(_ row: WatchRecordingRow) -> Date { row.startedAt.addingTimeInterval(row.durationSeconds) }
        let waitedLong = rows.contains { ($0.stage == .onPhone || $0.stage == .sending) && now.timeIntervalSince(ended($0)) >= 30 * 60 }

        func make(_ sentence: String, ringed: Place? = nil, fact: String? = nil, tryAgain: Bool = false) -> WatchWhere {
            WatchWhere(onWatch: onWatch, onPhone: onPhone, onComputer: onComputer, ringed: ringed, sentence: sentence,
                       fact: fact, showsTryAgain: tryAgain)
        }
        // An hour that could not be sent comes first: it is the one thing that is the wearer's to do.
        if failed > 0 {
            let lead = failed == 1 ? String(localized: "1 hour couldn't reach your computer.")
                : String(localized: "\(failed) hours couldn't reach your computer.")
            return make(lead + " " + String(localized: "Update Isoform on your computer, then try again."),
                        ringed: .phone, tryAgain: true)
        }
        if onPhone > 0 {
            // The current reason wins over why they waited (v1.2.0 rule 4).
            switch outcome {
            case .pairAgain:
                return make(String(localized: "Open Settings and choose Pair again. Your recordings stay on this phone until then."),
                            ringed: .phone)
            case .unsupported:
                return make(String(localized: "Update Isoform on your computer to receive recordings."), ringed: .phone)
            case .relayFull:
                return make(String(localized: "Too many recordings are waiting for your computer. They stay on this phone and go as it takes them."),
                            ringed: waitedLong ? .phone : nil)
            case .waiting, .drained, .sending:
                break
            }
            switch computer {
            case .closedEyes:
                return make(String(localized: "Your computer has Closed Eyes on. Hours wait here until it ends."))
            case .asleep where outcome != .sending:
                let lead = onPhone == 1 ? String(localized: "1 hour is on this phone, waiting for your computer.")
                    : String(localized: "\(onPhone) hours are on this phone, waiting for your computer.")
                return make(lead + " " + String(localized: "It's asleep; they go when it wakes. Nothing for you to do."),
                            ringed: waitedLong ? .phone : nil)
            case .unreachable where outcome != .sending:
                let lead = onPhone == 1 ? String(localized: "1 hour is on this phone.") : String(localized: "\(onPhone) hours are on this phone.")
                return make(lead + " " + String(localized: "Your computer can't be reached; they go as soon as it can. Nothing for you to do."),
                            ringed: waitedLong ? .phone : nil, tryAgain: waitedLong)
            default:
                break
            }
            if outcome == .waiting {
                // The last try did not get through and nothing says why.
                let lead = onPhone == 1 ? String(localized: "1 hour is on this phone.") : String(localized: "\(onPhone) hours are on this phone.")
                return make(lead + " " + String(localized: "Your computer can't be reached; they go as soon as it can. Nothing for you to do."),
                            ringed: waitedLong ? .phone : nil, tryAgain: waitedLong)
            }
            // They are going. Say why they waited only when iOS said so.
            let going = String(localized: "They're going to your computer now.")
            switch cause {
            case .closed:
                let lead = onPhone == 1 ? String(localized: "Isoform was closed on this phone, so 1 hour waited.")
                    : String(localized: "Isoform was closed on this phone, so \(onPhone) hours waited.")
                return make(lead + " " + going, fact: String(localized: "Swiping Isoform away stops sending until you open it again."))
            case .refreshOff:
                let lead = onPhone == 1 ? String(localized: "Background App Refresh is off for Isoform, so 1 hour waited.")
                    : String(localized: "Background App Refresh is off for Isoform, so \(onPhone) hours waited.")
                return make(lead + " " + going + " " + String(localized: "Turn it on in Settings › General › Background App Refresh."))
            case nil:
                break
            }
            // A quick send does not flash a sentence: only once it has run a few seconds.
            if let sendingSince, now.timeIntervalSince(sendingSince) >= 3 {
                return make(onPhone == 1 ? String(localized: "1 hour is going to your computer now.")
                            : String(localized: "\(onPhone) hours are going to your computer now."))
            }
        }
        if onWatch > 0, !watchFound {
            // The system says Isoform is not on the watch and not why (v1.5.0 W2, W6): where the
            // hours are, what happens next, and the one thing that may be the wearer's to do.
            let lead = onWatch == 1 ? String(localized: "1 hour is on your watch, waiting for this phone.")
                : String(localized: "\(onWatch) hours are on your watch, waiting for this phone.")
            let next = onWatch == 1 ? String(localized: "This phone can't reach Isoform there right now; it comes when it can.")
                : String(localized: "This phone can't reach Isoform there right now; they come when it can.")
            return make(lead + " " + next + " "
                        + String(localized: "If Isoform doesn't come back on your watch, install it from the Watch app on this phone."))
        }
        if onWatch > 0 {
            return make(onWatch == 1 ? String(localized: "1 hour is on your watch. It comes to this phone when it's near.")
                        : String(localized: "\(onWatch) hours are on your watch. They come to this phone when it's near."))
        }
        if onPhone > 0 {
            // Going, for less than a few seconds: the places say where they are.
            return make("")
        }
        if let last = across.map(ended).max() {
            return make(String(localized: "Every hour is on your computer, the last at \(WatchTabCopy.clock(last, locale: locale))."))
        }
        return make("")
    }
}
