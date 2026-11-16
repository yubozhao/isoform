import XCTest
@testable import Yansu

// The Today card's states (handoff 2026-10-28, section 2), decided by TodayBriefCard without a view.
final class TodayBriefCardTests: XCTestCase {
    private var utc: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        return calendar
    }

    private func brief(id: String, asOf: String, paragraphs: [String] = ["Three meetings today, and Priya is waiting on you."],
                       cards: [MobileBriefCard] = []) -> MobileBriefV1 {
        var value = MobileBriefV1(briefId: id, slot: "morning", localDate: "2026-10-28", asOf: asOf, kind: "morning",
                                  narrativeStatus: "ok", narrative: paragraphs.map { BriefParagraph(text: $0) })
        value.conversationKey = "conversation-\(id)"
        value.cards = cards
        return value
    }

    private func date(_ value: String) throws -> Date {
        try XCTUnwrap(RelayTime.parse(value))
    }

    /// The time as the card shows it in English on this device, so expectations match its space character.
    private func clock(_ value: Date) -> String {
        BriefClock.text(value, locale: Locale(identifier: "en_US"))
    }

    // MARK: Replaced in place (phone-today rule 6)

    func testCardHasNoNewerBriefRowANewerBriefReplacesItInPlace() throws {
        // "A newer brief is ready ›" goes: the card has no choice to hold an older brief behind a row.
        let source = try String(contentsOfFile: #filePath.replacingOccurrences(of: "YansuTests/TodayBriefCardTests.swift",
                                                                              with: "Yansu/TodayBriefCard.swift"), encoding: .utf8)
        XCTAssertFalse(source.contains("A newer brief is ready"))
        XCTAssertFalse(source.contains("feed.brief.newer"))
    }

    // MARK: First sentences and folding (phone-today rules 4 and 5)

    func testFirstSentencesTakeThreeSentencesAcrossParagraphsAndSkipAGreeting() throws {
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z",
                          paragraphs: ["Good evening.", "Lena moved the review to 10:00. Two tests still fail.",
                                       "Sam is waiting on the pricing page. Nothing else changed."])
        XCTAssertEqual(TodayBriefCard.firstSentences(value, locale: Locale(identifier: "en_US")),
                       "Lena moved the review to 10:00. Two tests still fail. Sam is waiting on the pricing page.")
    }

    func testFoldIsKeptPerBriefAndNeverForAnother() throws {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "fold-\(UUID().uuidString)"))
        XCTAssertFalse(TodayBriefFold.isFolded("a", defaults: defaults))
        TodayBriefFold.fold("a", defaults: defaults)
        XCTAssertTrue(TodayBriefFold.isFolded("a", defaults: defaults))
        XCTAssertFalse(TodayBriefFold.isFolded("b", defaults: defaults), "a newer brief opens unfolded")
    }

    // MARK: The opening paragraph

    func testOpeningParagraphLeadsWithSubstanceNotAGreeting() throws {
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z",
                          paragraphs: ["Good morning.", "Three meetings today, and Priya is waiting on you."])
        let opening = try XCTUnwrap(TodayBriefCard.opening(value))
        XCTAssertEqual(opening.index, 1)
        XCTAssertEqual(opening.text, "Three meetings today, and Priya is waiting on you.")
    }

    func testOpeningParagraphSkipsAChineseGreetingToo() throws {
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z", paragraphs: ["早上好。", "今天有三场会议。"])
        XCTAssertEqual(TodayBriefCard.opening(value)?.text, "今天有三场会议。")
    }

    func testOpeningParagraphIsTheFirstParagraphWhenItIsNotAGreeting() throws {
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z", paragraphs: ["Three meetings.", "Second."])
        XCTAssertEqual(TodayBriefCard.opening(value)?.index, 0)
    }

    func testOpeningParagraphIsNilWithoutWords() throws {
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z", paragraphs: ["   "])
        XCTAssertNil(TodayBriefCard.opening(value))
    }

    func testGreetingDetectionMatchesOnlyABareGreeting() {
        XCTAssertTrue(TodayBriefCard.isGreeting("Good morning."))
        XCTAssertTrue(TodayBriefCard.isGreeting("  good evening!  "))
        XCTAssertTrue(TodayBriefCard.isGreeting("晚上好"))
        XCTAssertFalse(TodayBriefCard.isGreeting("Good morning. Three meetings today."))
        XCTAssertFalse(TodayBriefCard.isGreeting("Hello there, Priya is waiting."))
    }

    // MARK: Chips

    func testChipsAreAtMostThreeParagraphsInOrder() throws {
        let paragraphs = ["One.", "Two.", "Three.", "Four.", "Five."]
        let cards = (0..<5).map { index in
            MobileBriefCard(id: "q\(index)", kind: "question", mode: "send", label: "Question \(index)",
                            message: "Tell me about \(index)", paragraph: index)
        }
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z", paragraphs: paragraphs, cards: cards)
        XCTAssertEqual(TodayBriefCard.chipParagraphs(value), [0, 1, 2])
    }

    func testChipsSkipParagraphsWithoutAQuestion() throws {
        let cards = [MobileBriefCard(id: "q2", kind: "question", mode: "send", label: "Ask", message: "Why", paragraph: 2)]
        let value = brief(id: "a", asOf: "2026-10-28T15:41:00Z", paragraphs: ["One.", "Two.", "Three."], cards: cards)
        XCTAssertEqual(TodayBriefCard.chipParagraphs(value), [2])
    }

    // MARK: Play

    /// P138: "Recording on its way" fills the player's place only while the computer is still making the recording.
    func testRecordingLineShowsOnlyWhileTheComputerIsMakingTheRecording() throws {
        // The computer's own payload, as the phone decodes it from a brief.
        let decoded = try JSONDecoder().decode(BriefAudio.self, from: Data(
            #"{"status":"rendering","mime":"","bytes":0,"durationSeconds":0}"#.utf8))
        XCTAssertEqual(decoded.status, "rendering")
        XCTAssertTrue(decoded.isRecording)
        XCTAssertTrue(TodayBriefCard.showsRecordingLine(hasWords: true, playShown: false, audio: decoded))
        let recording = BriefAudio(status: "rendering")
        XCTAssertTrue(TodayBriefCard.showsRecordingLine(hasWords: true, playShown: false, audio: recording))
        XCTAssertFalse(TodayBriefCard.showsRecordingLine(hasWords: true, playShown: true, audio: recording),
                       "Play takes the place when it is shown")
        XCTAssertFalse(TodayBriefCard.showsRecordingLine(hasWords: false, playShown: false, audio: recording),
                       "no words, no line")
        XCTAssertFalse(TodayBriefCard.showsRecordingLine(hasWords: true, playShown: false, audio: nil),
                       "a brief with no audio, such as the template sentence, shows nothing in that place")
        XCTAssertFalse(TodayBriefCard.showsRecordingLine(hasWords: true, playShown: false, audio: BriefAudio(status: "ok")),
                       "a finished status is not a recording on its way")
        XCTAssertFalse(TodayBriefCard.showsRecordingLine(hasWords: true, playShown: false, audio: BriefAudio(status: "downloading")),
                       "a voice still installing is not a recording being made")
    }

    func testPlayAppearsOnlyWithWordsAndAudioOrPlayback() {
        func play(words: Bool = true, phone: Bool = false, holds: Bool = false, computer: Bool = false,
                  online: Bool? = true) -> Bool {
            TodayBriefCard.showsPlay(hasWords: words, hasAudio: phone, holdsBrief: holds, audioOnComputer: computer,
                                     computerOnline: online)
        }
        XCTAssertFalse(play(words: false, phone: true), "no Play without text")
        XCTAssertFalse(play(), "text first, audio later: the package has no audio yet")
        XCTAssertTrue(play(phone: true))
        XCTAssertTrue(play(phone: true, online: false), "audio on the phone plays with the computer asleep")
        XCTAssertTrue(play(holds: true, online: false), "the card is the player while it holds this brief")
        XCTAssertTrue(play(computer: true), "audio still on the computer: the tap fetches it")
        XCTAssertTrue(play(computer: true, online: nil))
        XCTAssertFalse(play(computer: true, online: false), "audio only on an unreachable computer")
    }

    // MARK: Placeholder

    func testPlaceholderRowsShowOnceBeforeAnyBriefAfterPairing() {
        XCTAssertTrue(TodayBriefCard.showsPlaceholder(hasBrief: false, placeholderShown: false))
        XCTAssertFalse(TodayBriefCard.showsPlaceholder(hasBrief: false, placeholderShown: true), "never again")
        XCTAssertFalse(TodayBriefCard.showsPlaceholder(hasBrief: true, placeholderShown: false), "a brief replaces it")
    }

    func testPlaceholderMemoryIsKeptPerPairing() throws {
        let suite = "TodayBriefPlaceholderTests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        XCTAssertFalse(TodayBriefPlaceholder.wasShown(hubID: "mac-a", defaults: defaults))
        TodayBriefPlaceholder.markShown(hubID: "mac-a", defaults: defaults)
        XCTAssertTrue(TodayBriefPlaceholder.wasShown(hubID: "mac-a", defaults: defaults))
        XCTAssertFalse(TodayBriefPlaceholder.wasShown(hubID: "mac-b", defaults: defaults), "a new Mac gets its own placeholder")
        TodayBriefPlaceholder.reset(hubID: "mac-a", defaults: defaults)
        XCTAssertFalse(TodayBriefPlaceholder.wasShown(hubID: "mac-a", defaults: defaults), "a new pairing can show it again")
    }

    // MARK: Lines

    func testMakingLineShowsOnlyWhileTheMacIsMakingABrief() throws {
        let ready = "2026-10-28T18:03:00Z"
        let queued = TodayBriefRequest(state: "queued", readyAt: ready)
        XCTAssertEqual(TodayBriefCard.makingLine(queued, calendar: utc, time: { self.clock($0) }),
                       "Your evening brief is being made · ready around \(clock(try! date(ready)))")
        XCTAssertNil(TodayBriefCard.makingLine(TodayBriefRequest(state: "failed", readyAt: ready), time: { _ in "" }))
        XCTAssertNil(TodayBriefCard.makingLine(nil, time: { _ in "" }))
    }

    func testMakingLineReadsTheComputersStatusForAnyBuild() throws {
        let now = try date("2026-10-28T18:00:00Z")
        let ahead = MobileBriefStatus.Making(kind: "evening", slot: "evening", readyAt: "2026-10-28T18:03:00Z")
        XCTAssertEqual(TodayBriefCard.makingLine(ahead, now: now, time: { self.clock($0) }),
                       "Your evening brief is being made · ready around \(clock(try date("2026-10-28T18:03:00Z")))")
        let passed = MobileBriefStatus.Making(kind: "evening", readyAt: "2026-10-28T17:59:00Z")
        XCTAssertEqual(TodayBriefCard.makingLine(passed, now: now, time: { self.clock($0) }),
                       "Your evening brief is being made", "no time once readyAt has passed")
        XCTAssertNil(TodayBriefCard.makingLine(nil as MobileBriefStatus.Making?, now: now, time: { _ in "" }))
    }

    func testWaitingLineShowsWhenAScheduledBriefIsDueWhileTheMacSleeps() throws {
        let schedule = MobileBriefSchedule(morning: "06:50", evening: "18:00")
        let now = try date("2026-10-28T07:30:00Z")
        let stale = brief(id: "evening", asOf: "2026-10-27T18:02:00Z")
        let due = TodayBriefCard.dueWhileAsleep(schedule, current: stale, asleep: true, now: now, calendar: utc)
        XCTAssertEqual(due, try date("2026-10-28T06:50:00Z"))
        XCTAssertEqual(TodayBriefCard.waitingLine(try XCTUnwrap(due), time: { self.clock($0) }),
                       "Your \(clock(try date("2026-10-28T06:50:00Z"))) brief comes when your computer wakes. It's asleep.")
    }

    func testWaitingLineStopsOnceTheDueBriefIsMadeOrTheMacIsBack() throws {
        let schedule = MobileBriefSchedule(morning: "06:50", evening: "18:00")
        let now = try date("2026-10-28T07:30:00Z")
        let made = brief(id: "morning", asOf: "2026-10-28T06:52:00Z")
        XCTAssertNil(TodayBriefCard.dueWhileAsleep(schedule, current: made, asleep: true, now: now, calendar: utc))
        let stale = brief(id: "evening", asOf: "2026-10-27T18:02:00Z")
        XCTAssertNil(TodayBriefCard.dueWhileAsleep(schedule, current: stale, asleep: false, now: now, calendar: utc),
                     "a reachable Mac makes the brief itself")
        let beforeAnySlot = try date("2026-10-28T05:00:00Z")
        XCTAssertNil(TodayBriefCard.dueWhileAsleep(schedule, current: stale, asleep: true, now: beforeAnySlot, calendar: utc),
                     "yesterday's evening brief covers yesterday's evening slot")
    }

    func testClockSlotsAcceptOnlyHourAndMinute() throws {
        let day = try date("2026-10-28T12:00:00Z")
        XCTAssertEqual(TodayBriefCard.clockDate("06:50", on: day, calendar: utc), try date("2026-10-28T06:50:00Z"))
        XCTAssertNil(TodayBriefCard.clockDate("25:00", on: day, calendar: utc))
        XCTAssertNil(TodayBriefCard.clockDate("soon", on: day, calendar: utc))
    }

    // MARK: Reading

    func testBriefOnScreenCountsReadingSeparatelyFromShowing() async throws {
        await MainActor.run {
            let onScreen = BriefOnScreen()
            onScreen.show("card-brief")
            XCTAssertTrue(onScreen.isShowing("card-brief"))
            XCTAssertFalse(onScreen.isReading("card-brief"), "a card that shows a brief is not reading it")
            onScreen.show("card-brief", reading: true)
            XCTAssertTrue(onScreen.isReading("card-brief"))
            onScreen.hide("card-brief", reading: true)
            XCTAssertFalse(onScreen.isReading("card-brief"))
            XCTAssertTrue(onScreen.isShowing("card-brief"))
        }
    }

    func testOnlyAFinishedRecordingOnTheComputerCountsAsAudio() {
        XCTAssertTrue(BriefAudio(mime: "audio/mp4", bytes: 10, durationSeconds: 3).isFinished, "an older brief")
        XCTAssertTrue(BriefAudio(status: "ok", mime: "audio/mp4", bytes: 10, durationSeconds: 3).isFinished)
        XCTAssertFalse(BriefAudio(status: "downloading", mime: "", bytes: 0, durationSeconds: 0).isFinished)
        XCTAssertFalse(BriefAudio(status: "later", mime: "audio/mp4", bytes: 10, durationSeconds: 3).isFinished,
                       "an unknown status never means there is something to play")
        let decoded = try? JSONDecoder().decode(BriefAudio.self, from: Data(
            #"{"status":"downloading","statusMessage":"Voice downloading","mime":"","bytes":0,"durationSeconds":0}"#.utf8))
        XCTAssertEqual(decoded?.status, "downloading")
        XCTAssertEqual(decoded?.isFinished, false)
    }
}
