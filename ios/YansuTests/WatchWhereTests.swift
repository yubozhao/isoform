import XCTest
@testable import Yansu

/// "Where your hours are" (phone-watch v1.1.0 rules 11 to 13, v1.2.0 rules 1 to 5). Bo, 2026-11-18:
/// "25 on this phone" said neither 25 of what, nor what happens next, nor whether anything was his to do.
final class WatchWhereTests: XCTestCase {
    /// 3:00 PM in Los Angeles on 2026-11-18, so an hour ended minutes ago is today's.
    private let now = Date(timeIntervalSince1970: 1795042800)
    private var calendar: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        return calendar
    }

    private func row(_ stage: WatchRecordingStage, endedMinutesAgo: Double = 5) -> WatchRecordingRow {
        WatchRecordingRow(id: UUID().uuidString, startedAt: now.addingTimeInterval(-endedMinutesAgo * 60 - 3000),
                          durationSeconds: 3000, stage: stage)
    }

    private func place(_ rows: [WatchRecordingRow], _ outcome: WatchRecordingForwarder.Outcome = .drained,
                       _ computer: WatchComputerState = .reachable, cause: WatchWaitCause? = nil,
                       sendingSince: Date? = nil) -> WatchWhere {
        WatchWhere.of(rows: rows, outcome: outcome, computer: computer, cause: cause, sendingSince: sendingSince,
                      now: now, calendar: calendar, locale: Locale(identifier: "en_US"))
    }

    func testThreePlacesAlwaysWithTheirCounts() {
        let rows = [row(.onWatch), row(.onWatch), row(.onPhone), row(.transcribed), row(.onComputer),
                    row(.transcribed, endedMinutesAgo: 60 * 30)]
        let here = place(rows, .waiting, .asleep)
        XCTAssertEqual(here.onWatch, 2)
        XCTAssertEqual(here.onPhone, 1)
        XCTAssertEqual(here.onComputer, 2, "on the computer: today's hours only")
        XCTAssertEqual(WatchWhere.hours(1), "1 hour")
        XCTAssertEqual(WatchWhere.hours(25), "25 hours")
    }

    func testEveryHourOnTheComputer() {
        let here = place([row(.transcribed, endedMinutesAgo: 20)])
        XCTAssertTrue(here.sentence.hasPrefix("Every hour is on your computer, the last at "), here.sentence)
        XCTAssertNil(here.ringed)
        XCTAssertFalse(here.showsTryAgain)
        XCTAssertEqual(place([]).sentence, "", "nothing recorded yet: the places say it")
    }

    func testTheComputerAsleepOrOutOfReach() {
        let waiting = (0..<25).map { _ in row(.onPhone, endedMinutesAgo: 90) }
        var here = place(waiting, .waiting, .asleep)
        XCTAssertEqual(here.sentence, "25 hours are on this phone, waiting for your computer. It's asleep; they go when it wakes. Nothing for you to do.")
        XCTAssertEqual(here.ringed, .phone)
        XCTAssertFalse(here.showsTryAgain, "an asleep computer needs no tap")

        here = place(waiting, .waiting, .unreachable)
        XCTAssertEqual(here.sentence, "25 hours are on this phone. Your computer can't be reached; they go as soon as it can. Nothing for you to do.")
        XCTAssertTrue(here.showsTryAgain, "after 30 minutes, Try again")
        // Under 30 minutes: no ring and no Try again.
        here = place([row(.onPhone, endedMinutesAgo: 5)], .waiting, .unreachable)
        XCTAssertEqual(here.sentence, "1 hour is on this phone. Your computer can't be reached; they go as soon as it can. Nothing for you to do.")
        XCTAssertNil(here.ringed)
        XCTAssertFalse(here.showsTryAgain)

        here = place(waiting, .waiting, .closedEyes)
        XCTAssertEqual(here.sentence, "Your computer has Closed Eyes on. Hours wait here until it ends.")
        XCTAssertNil(here.ringed, "Closed Eyes is on because the person turned it on")
    }

    func testAnHourThatCouldNotBeSentComesFirst() {
        let here = place([row(.failedToSend), row(.onPhone, endedMinutesAgo: 90), row(.onWatch)], .waiting, .asleep)
        XCTAssertEqual(here.sentence, "1 hour couldn't reach your computer. Update Isoform on your computer, then try again.")
        XCTAssertTrue(here.showsTryAgain)
        XCTAssertEqual(here.ringed, .phone)
        XCTAssertEqual(here.onPhone, 2, "the failed hour is still on this phone")
    }

    /// v1.2.0: "Isoform was closed" only when iOS said so; the current reason wins; "going now"
    /// only after a few seconds, so a quick send does not flash.
    func testHoursThatAreGoing() {
        let six = (0..<6).map { _ in row(.sending, endedMinutesAgo: 120) }
        var here = place(six, .sending, .reachable, cause: .closed)
        XCTAssertEqual(here.sentence, "Isoform was closed on this phone, so 6 hours waited. They're going to your computer now.")
        XCTAssertEqual(here.fact, "Swiping Isoform away stops sending until you open it again.")
        XCTAssertNil(here.ringed, "hours that are going are plain")
        XCTAssertFalse(here.showsTryAgain)

        here = place(six, .sending, .reachable, cause: .refreshOff)
        XCTAssertEqual(here.sentence, "Background App Refresh is off for Isoform, so 6 hours waited. They're going to your computer now. Turn it on in Settings › General › Background App Refresh.")
        XCTAssertNil(here.fact)

        // The computer is asleep when the phone opens: that sentence, not the closed one.
        here = place(six, .waiting, .asleep, cause: .closed)
        XCTAssertTrue(here.sentence.hasSuffix("It's asleep; they go when it wakes. Nothing for you to do."), here.sentence)
        XCTAssertNil(here.fact)

        // No cause known: plain "going", and only once the send has run a few seconds.
        here = place(six, .sending, .reachable, sendingSince: now.addingTimeInterval(-1))
        XCTAssertEqual(here.sentence, "")
        here = place(six, .sending, .reachable, sendingSince: now.addingTimeInterval(-4))
        XCTAssertEqual(here.sentence, "6 hours are going to your computer now.")
        here = place([row(.sending)], .sending, .reachable, sendingSince: now.addingTimeInterval(-4))
        XCTAssertEqual(here.sentence, "1 hour is going to your computer now.")
    }

    /// Every stop says since when (phone-watch v1.1.0 rule 8). Why is not known to the phone yet.
    func testNowSaysSinceWhen() {
        let english = Locale(identifier: "en_US")
        func line(_ status: WatchLiveStatus, since: Date?) -> String {
            WatchTabCopy.now(connection: .ready(reachable: true), status: status, pauseUntil: nil, lastHeard: nil,
                             since: since, now: now, calendar: calendar, locale: english).line
        }
        // 3:00 PM now; 9:40 AM today is 5 h 20 min earlier, 7:49 PM yesterday 19 h 11 min earlier.
        let morning = now.addingTimeInterval(-(5 * 3600 + 20 * 60))
        let lastNight = now.addingTimeInterval(-(19 * 3600 + 11 * 60))
        XCTAssertEqual(line(.notRecording, since: morning), "Since 9:40\u{202F}AM. Tap the mark on your watch to listen.")
        XCTAssertEqual(line(.notRecording, since: lastNight), "Since 7:49\u{202F}PM yesterday. Tap the mark on your watch to listen.")
        XCTAssertEqual(line(.notRecording, since: nil), "Tap the mark on your watch to listen.", "unknown since when: it does not guess")
        XCTAssertEqual(line(.stoppedToday, since: morning), "You held the mark to stop, at 9:40\u{202F}AM. It listens again tomorrow.")
        XCTAssertEqual(line(.waitingForMicrophone, since: morning), "A call has the microphone, since 9:40\u{202F}AM. It listens again by itself after.")
        let older = WatchTabCopy.sinceText(now.addingTimeInterval(-4 * 86400), now: now, calendar: calendar, locale: english)
        XCTAssertTrue(older.contains("Nov 14"), older)
        // An hour the computer has not named yet.
        let held = WatchRecordingRow(id: "x", startedAt: now.addingTimeInterval(-7200), durationSeconds: 49 * 60, stage: .onPhone)
        XCTAssertEqual(WatchTabCopy.waitingRow(held), "49 min of talk · waiting for your computer")
    }

    /// Today's watch line (phone-watch v1.1.0 rule 18, phone-duo v1.0.0 rule 12, phone-watch v1.2.0
    /// rule 7): always there with a watch; Closed Eyes first, then what stops listening; plain
    /// while hours are going; amber only when they cannot go.
    func testTodaysWatchLine() {
        let english = Locale(identifier: "en_US")
        func line(_ state: WatchNow.Kind, rows: [WatchRecordingRow] = [], _ outcome: WatchRecordingForwarder.Outcome = .drained,
                  hasWatch: Bool = true, until: Date? = nil, since: Date? = nil) -> WatchTodayLineText? {
            WatchTodayLineText.of(hasWatch: hasWatch, now: state, closedEyesUntil: until, since: since, rows: rows,
                                  outcome: outcome, now: now, calendar: calendar, locale: english)
        }
        XCTAssertEqual(line(.listening), .init(text: "Your watch is listening · every hour is on your computer", amber: false))
        XCTAssertEqual(line(.notListening, since: now.addingTimeInterval(-(5 * 3600 + 20 * 60))),
                       .init(text: "Your watch isn't listening · since 9:40\u{202F}AM", amber: true))
        XCTAssertEqual(line(.notListening)?.text, "Your watch isn't listening")
        // Hours that cannot go are amber; hours that are going are plain.
        let waited = (0..<25).map { _ in row(.onPhone, endedMinutesAgo: 90) }
        XCTAssertEqual(line(.listening, rows: waited, .waiting),
                       .init(text: "25 hours from your watch are waiting on this phone", amber: true))
        XCTAssertEqual(line(.listening, rows: Array(waited.prefix(6)), .sending),
                       .init(text: "6 hours from your watch are going to your computer", amber: false))
        XCTAssertEqual(line(.listening, rows: [row(.onPhone, endedMinutesAgo: 5)], .waiting),
                       .init(text: "1 hour from your watch is going to your computer", amber: false),
                       "under half an hour nothing is stuck yet")
        XCTAssertEqual(line(.listening, rows: [row(.failedToSend)] + waited, .waiting),
                       .init(text: "1 hour from your watch couldn't be sent", amber: true))
        // When two are true, the one that stops listening comes first; Closed Eyes before both.
        XCTAssertEqual(line(.notListening, rows: waited, .waiting)?.text, "Your watch isn't listening")
        let noon = now.addingTimeInterval(-(2 * 3600 + 56 * 60))
        XCTAssertEqual(line(.closedEyes, rows: waited, .waiting, until: noon),
                       .init(text: "Closed Eyes until 12:04\u{202F}PM · nothing is seen or heard", amber: true))
        XCTAssertEqual(line(.closedEyes)?.text, "Closed Eyes until you end it · nothing is seen or heard")
        // With no watch paired the line shows only during Closed Eyes.
        XCTAssertNil(line(.listening, hasWatch: false))
        XCTAssertNil(line(.notListening, rows: waited, .waiting, hasWatch: false))
        XCTAssertEqual(line(.closedEyes, hasWatch: false)?.amber, true)
        // The wearer's own stop for the day is said, and plainly; a watch that has not said gets no line.
        XCTAssertEqual(line(.offToday), .init(text: "Your watch isn't listening", amber: false))
        XCTAssertNil(line(.unknown))
    }

    func testHoursOnTheWatchAndTheOlderLines() {
        var here = place([row(.onWatch), row(.onWatch), row(.onWatch), row(.transcribed)])
        XCTAssertEqual(here.sentence, "3 hours are on your watch. They come to this phone when it's near.")
        XCTAssertNil(here.ringed)
        here = place([row(.onWatch)])
        XCTAssertEqual(here.sentence, "1 hour is on your watch. It comes to this phone when it's near.")
        // What the phone already said for these stays said.
        here = place([row(.onPhone)], .pairAgain)
        XCTAssertEqual(here.sentence, "Open Settings and choose Pair again. Your recordings stay on this phone until then.")
        here = place([row(.onPhone)], .unsupported)
        XCTAssertEqual(here.sentence, "Update Isoform on your computer to receive recordings.")
        here = place([row(.onPhone)], .relayFull)
        XCTAssertTrue(here.sentence.hasPrefix("Too many recordings are waiting for your computer."))
    }
}
