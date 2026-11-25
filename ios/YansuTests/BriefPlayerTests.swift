import Foundation
import MediaPlayer
import XCTest
@testable import Yansu

// The player holds the brief that was started, names it, follows the spoken paragraph, remembers where it stopped and
// whether it was played, and comes back after a restart only when it was playing in the last 30 minutes.
@MainActor
final class BriefPlayerTests: XCTestCase {
    private let hub = "11111111-2222-4333-8444-555555555555"

    @MainActor
    private final class TestEngine: BriefAudioPlayback {
        var duration: TimeInterval = 60
        var currentTime: TimeInterval = 0
        var rate: Float = 1
        private(set) var playing = false
        func play() -> Bool { playing = true; return true }
        func pause() { playing = false }
        func stop() { playing = false }
    }

    private func makeCache() throws -> BriefAudioCache {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        return BriefAudioCache(directory: directory)
    }

    private func paragraph(_ index: Int, _ start: Double) -> BriefAudioParagraph {
        BriefAudioParagraph(source: "narrative", index: index, startSeconds: start)
    }

    private func brief(id: String = "2026-10-28-morning", asOf: String = "2026-10-28T13:00:00Z",
                       texts: [String] = ["Good morning. Three meetings today, and Priya is waiting on you.",
                                          "Second paragraph here.", "Third one."],
                       paragraphs: [BriefAudioParagraph]? = nil) -> MobileBriefV1 {
        var brief = MobileBriefV1(briefId: id, slot: "morning", localDate: "2026-10-28", asOf: asOf, kind: "morning",
                                  narrativeStatus: "ok", narrative: texts.map { BriefParagraph(text: $0) })
        brief.audio = BriefAudio(mime: "audio/wav", bytes: 3, durationSeconds: 60,
                                 paragraphs: paragraphs ?? [paragraph(0, 0), paragraph(1, 10), paragraph(2, 25)])
        return brief
    }

    /// Writes the brief's audio to the cache, so Play starts without asking the Mac.
    private func withAudio(_ brief: MobileBriefV1, in cache: BriefAudioCache) throws {
        try cache.store(Data([1, 2, 3]), brief: brief, hubID: hub, audio: try XCTUnwrap(brief.audio))
    }

    private func makePlayer(_ cache: BriefAudioCache, engine: TestEngine? = nil,
                            offersExtras: Bool = true) -> BriefAudioPlayer {
        let engine = engine ?? TestEngine()
        return BriefAudioPlayer(cache: cache, requestAudio: { _ in throw BriefAudioError.notAvailable },
                                downloadAudio: { _ in throw BriefAudioError.notAvailable },
                                makeEngine: { _ in engine }, managesSystemPlayback: false, offersExtras: offersExtras)
    }

    // Scope cut B2 (phone half): Hear it is play, pause and a position bar. The phone's player offers no speed or
    // seek, a speed saved before still plays at normal speed, and the bar's position and length are the audio's.
    func testThePhonesPlayerIsPlayPauseAndABar() async throws {
        XCTAssertFalse(BriefPlayerExtras.onPhone, "the phone shows no speed, 15 s seek or read-along")
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        try cache.writeRecall(BriefPlaybackRecall(speed: 1.5))
        let engine = TestEngine()
        let player = makePlayer(cache, engine: engine, offersExtras: BriefPlayerExtras.onPhone)
        XCTAssertFalse(player.offersExtras)
        XCTAssertEqual(player.speed, .normal, "a speed chosen before the cut is not applied")

        await player.toggle(brief: spoken, hubID: hub)
        XCTAssertEqual(player.state, .playing)
        XCTAssertEqual(engine.rate, 1)
        XCTAssertEqual(player.duration, 60, "the bar's length")
        await player.toggle(brief: spoken, hubID: hub)
        XCTAssertEqual(player.state, .paused)
        XCTAssertFalse(engine.playing)
        await player.toggle(brief: spoken, hubID: hub)
        XCTAssertEqual(player.state, .playing)
        XCTAssertEqual(engine.rate, 1)
    }

    func testPlayerKeepsTheBriefItStartedWhenANewerOneArrives() async throws {
        let cache = try makeCache()
        let started = brief(id: "a", asOf: "2026-10-28T13:00:00Z")
        let newer = brief(id: "b", asOf: "2026-10-28T14:00:00Z")
        try withAudio(started, in: cache)
        let player = makePlayer(cache)
        await player.toggle(brief: started, hubID: hub)
        XCTAssertEqual(player.state, .playing)

        player.receive(brief: newer, hubID: hub)
        await player.prefetch(brief: newer, hubID: hub, allowHubRequest: false)

        XCTAssertEqual(player.brief?.identifier, "a")
        XCTAssertTrue(player.matches(brief: started, hubID: hub))
        XCTAssertFalse(player.matches(brief: newer, hubID: hub))
        XCTAssertEqual(player.state, .playing)
    }

    func testTheSpokenParagraphFollowsTheAudioTimings() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let engine = TestEngine()
        let player = makePlayer(cache, engine: engine)
        await player.toggle(brief: spoken, hubID: hub)

        for (seconds, expected) in [(0.0, 0), (9.9, 0), (10, 1), (12, 1), (25, 2), (59, 2)] {
            engine.currentTime = seconds
            player.tick()
            XCTAssertEqual(player.spokenParagraph, expected, "at \(seconds) seconds")
        }
    }

    func testSeekingToAParagraphAndSkippingStayInsideTheAudio() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let engine = TestEngine()
        let player = makePlayer(cache, engine: engine)
        await player.toggle(brief: spoken, hubID: hub)

        player.seek(toParagraph: 2)
        XCTAssertEqual(player.position, 25, accuracy: 0.001)
        XCTAssertEqual(engine.currentTime, 25, accuracy: 0.001)
        player.skip(by: 15)
        XCTAssertEqual(player.position, 40, accuracy: 0.001)
        player.skip(by: 30)
        XCTAssertEqual(player.position, 60, accuracy: 0.001, "never past the end")
        player.skip(by: -15)
        XCTAssertEqual(player.position, 45, accuracy: 0.001)
        player.skip(by: -100)
        XCTAssertEqual(player.position, 0, accuracy: 0.001, "never before the start")
    }

    func testSpeedAppliesToTheEngineAndIsRemembered() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let engine = TestEngine()
        let player = makePlayer(cache, engine: engine)
        await player.toggle(brief: spoken, hubID: hub)

        player.setSpeed(.fastest)
        XCTAssertEqual(engine.rate, 1.5)
        let reopened = self.makePlayer(cache)
        XCTAssertEqual(reopened.speed, .fastest)
        XCTAssertEqual(BriefPlaybackSpeed.normal.next, .faster)
        XCTAssertEqual(BriefPlaybackSpeed.faster.next, .fastest)
        XCTAssertEqual(BriefPlaybackSpeed.fastest.next, .normal)
        XCTAssertEqual(BriefPlaybackSpeed.faster.label, "1.25×")
    }

    func testFinishingMarksThePlayedAndPlayingAgainStartsAtTheTop() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let engine = TestEngine()
        let player = makePlayer(cache, engine: engine)
        await player.toggle(brief: spoken, hubID: hub)
        engine.currentTime = 33
        player.tick()
        player.pause()
        XCTAssertEqual(player.playbackMemory(brief: spoken, hubID: hub),
                       BriefPlaybackMemory(seconds: 33, duration: 60, played: false))

        await player.toggle()
        XCTAssertEqual(player.state, .playing)
        player.playbackFinished()

        XCTAssertTrue(player.finished)
        XCTAssertEqual(player.state, .ready)
        XCTAssertEqual(player.playbackMemory(brief: spoken, hubID: hub),
                       BriefPlaybackMemory(seconds: 0, duration: 60, played: true))

        await player.toggle(brief: spoken, hubID: hub)
        XCTAssertEqual(player.state, .playing)
        XCTAssertFalse(player.finished)
        XCTAssertEqual(engine.currentTime, 0, accuracy: 0.001)
        XCTAssertEqual(player.playbackMemory(brief: spoken, hubID: hub)?.played, false)
    }

    func testWhereAPlayerStoppedIsRememberedAndAfterARestartTheBriefComesBackOnlyWithinThirtyMinutes() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let engine = TestEngine()
        let first = makePlayer(cache, engine: engine)
        await first.toggle(brief: spoken, hubID: hub)
        engine.currentTime = 40
        first.tick()
        first.pause()

        let lateRestart = makePlayer(cache)
        lateRestart.restoreRecentSession(hubID: hub, now: Date().addingTimeInterval(31 * 60)) { _ in spoken }
        XCTAssertNil(lateRestart.brief, "nothing comes back after thirty minutes")
        XCTAssertFalse(lateRestart.started)
        XCTAssertEqual(lateRestart.playbackMemory(brief: spoken, hubID: hub)?.seconds ?? -1, 40, accuracy: 0.001,
                       "the place is remembered either way")

        let soonRestart = makePlayer(cache)
        soonRestart.restoreRecentSession(hubID: hub, now: Date().addingTimeInterval(29 * 60)) {
            $0 == spoken.identifier ? spoken : nil
        }
        XCTAssertEqual(soonRestart.brief?.identifier, spoken.identifier)
        XCTAssertTrue(soonRestart.started)
        XCTAssertEqual(soonRestart.state, .idle, "a restored brief waits for Play")
        XCTAssertEqual(soonRestart.position, 40, accuracy: 0.001)
    }

    func testAPlayerThatWasDismissedIsNotRestored() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let player = makePlayer(cache)
        await player.toggle(brief: spoken, hubID: hub)
        player.dismiss()
        XCTAssertFalse(player.started)

        let restart = self.makePlayer(cache)
        restart.restoreRecentSession(hubID: hub) { $0 == spoken.identifier ? spoken : nil }
        XCTAssertNil(restart.brief)
    }

    func testPlayIsDimmedWhenTheAudioIsNotOnThePhoneAndTheMacIsOut() throws {
        let cache = try makeCache()
        let spoken = brief()
        let player = makePlayer(cache)
        XCTAssertFalse(player.hasAudioOnPhone(brief: spoken, hubID: hub))
        XCTAssertFalse(player.canStart(brief: spoken, hubID: hub, computerOnline: false))
        XCTAssertTrue(player.canStart(brief: spoken, hubID: hub, computerOnline: true))
        XCTAssertTrue(player.canStart(brief: spoken, hubID: hub, computerOnline: nil))

        try withAudio(spoken, in: cache)
        XCTAssertTrue(player.hasAudioOnPhone(brief: spoken, hubID: hub))
        XCTAssertTrue(player.canStart(brief: spoken, hubID: hub, computerOnline: false), "audio on the phone plays offline")
    }

    func testLockScreenNamesTheBriefAndTheOpeningWordsOfTheSpokenParagraph() async throws {
        let cache = try makeCache()
        let spoken = brief()
        try withAudio(spoken, in: cache)
        let engine = TestEngine()
        let player = makePlayer(cache, engine: engine)
        await player.toggle(brief: spoken, hubID: hub)
        engine.currentTime = 12
        player.tick()

        let info = player.nowPlayingInfo
        XCTAssertEqual(info[MPMediaItemPropertyTitle] as? String, spoken.titleText)
        XCTAssertEqual(info[MPMediaItemPropertyArtist] as? String, "Second paragraph here.")
        XCTAssertEqual(info[MPNowPlayingInfoPropertyPlaybackRate] as? Double, 1.0)
    }

    func testOpeningWordsAreTheFirstEight() {
        XCTAssertEqual(BriefAudioTimeline.openingWords("One two three four five six seven eight nine ten"),
                       "One two three four five six seven eight")
        XCTAssertEqual(BriefAudioTimeline.openingWords("  Short   words "), "Short words")
    }

    func testSpokenTimingsIgnoreWeeklyRecapAndMalformedEntries() {
        let timings = [
            BriefAudioParagraph(source: "weeklyRecap", index: 0, startSeconds: 0),
            paragraph(1, 20),
            paragraph(0, 0),
            BriefAudioParagraph(source: "narrative", index: 2, startSeconds: .nan),
            BriefAudioParagraph(source: "narrative", index: -1, startSeconds: 5),
        ]
        XCTAssertEqual(BriefAudioTimeline.narrativeTimings(timings).map(\.index), [0, 1])
        XCTAssertEqual(BriefAudioTimeline.narrativeParagraph(at: 21, in: timings), 1)
        XCTAssertNil(BriefAudioTimeline.narrativeParagraph(at: 21, in: nil))
        XCTAssertEqual(BriefAudioTimeline.start(ofNarrative: 1, in: timings), 20)
        XCTAssertNil(BriefAudioTimeline.start(ofNarrative: 2, in: timings))
    }

    func testPackageFieldsDecodeAndOlderBriefsStillRead() throws {
        let newer = try JSONDecoder().decode(MobileBriefV1.self, from: Data("""
        {"briefId":"2026-10-28-evening","slot":"evening","asOf":"2026-10-28T18:09:00Z","kind":"evening",
         "dueAt":"2026-10-28T18:00:00Z","narrativeStatus":"ok","narrative":[{"text":"Hello there."}],
         "audio":{"mime":"audio/wav","bytes":3,"durationSeconds":60,
                  "paragraphs":[{"source":"narrative","index":0,"startSeconds":0}]}}
        """.utf8))
        XCTAssertEqual(newer.kind, "evening")
        XCTAssertEqual(newer.dueDate, RelayTime.parse("2026-10-28T18:00:00Z"))
        XCTAssertEqual(newer.audio?.paragraphs, [paragraph(0, 0)])

        let older = try JSONDecoder().decode(MobileBriefV1.self, from: Data("""
        {"briefId":"2026-10-27-morning","slot":"morning","asOf":"2026-10-27T07:00:00Z","narrative":[{"text":"Old."}],
         "audio":{"mime":"audio/wav","bytes":3,"durationSeconds":60}}
        """.utf8))
        XCTAssertNil(older.kind)
        XCTAssertNil(older.dueAt)
        XCTAssertNil(older.audio?.paragraphs)
        XCTAssertEqual(older.narrative.first?.text, "Old.")
    }

    func testSpokenTimingsFollowTheParagraphsThatRemain() throws {
        // An empty paragraph is dropped when the brief is read, so the timings after it move up one place.
        let brief = try JSONDecoder().decode(MobileBriefV1.self, from: Data("""
        {"briefId":"sanitized","asOf":"2026-10-28T13:00:00Z","narrativeStatus":"ok",
         "narrative":[{"text":""},{"text":"Second."},{"text":"Third."}],
         "audio":{"mime":"audio/wav","bytes":3,"durationSeconds":60,
                  "paragraphs":[{"source":"narrative","index":1,"startSeconds":0},
                                {"source":"narrative","index":2,"startSeconds":20}]}}
        """.utf8))
        XCTAssertEqual(brief.narrative.map(\.text), ["Second.", "Third."])
        XCTAssertEqual(brief.audio?.paragraphs?.map(\.index), [0, 1])
        XCTAssertEqual(brief.audio?.paragraphs?.map(\.startSeconds), [0, 20])
    }

    func testBriefOnScreenCountsEachViewThatShowsIt() {
        let screens = BriefOnScreen()
        screens.show("a")
        screens.show("a")
        screens.hide("a")
        XCTAssertTrue(screens.isShowing("a"))
        screens.hide("a")
        XCTAssertFalse(screens.isShowing("a"))
        screens.hide("a")
        XCTAssertFalse(screens.isShowing("a"))
    }

    func testOpeningABriefRoutesToItsViewAtTheSpokenParagraph() {
        let router = NotificationRouter()
        router.openBrief("evening", landsOnSpoken: true)
        XCTAssertEqual(router.briefRoute?.briefID, "evening")
        XCTAssertEqual(router.briefRoute?.landsOnSpoken, true)
    }

    func testDownloadingANewClipKeepsTheSpeedAndTheRestoreRecord() throws {
        let cache = try makeCache()
        let spoken = brief()
        try cache.store(Data([1, 2, 3]), brief: spoken, hubID: hub, audio: try XCTUnwrap(spoken.audio))
        try cache.writeRecall(BriefPlaybackRecall(speed: 1.25, briefID: spoken.identifier, hubID: hub, playingAt: Date()))
        let next = brief(id: "2026-10-28-evening")
        try cache.store(Data([4, 5, 6]), brief: next, hubID: hub, audio: try XCTUnwrap(next.audio))
        let recall = try XCTUnwrap(cache.readRecall())
        XCTAssertEqual(recall.speed, 1.25)
        XCTAssertEqual(recall.briefID, spoken.identifier)
    }

    func testResumeLedgerKeepsTheLengthAndPlayedMarkForThirtyDays() throws {
        let cache = try makeCache()
        let spoken = brief()
        try cache.store(Data([1, 2, 3]), brief: spoken, hubID: hub, audio: try XCTUnwrap(spoken.audio))
        try cache.savePosition(20, duration: 60, brief: spoken, hubID: hub)
        XCTAssertEqual(cache.playbackMemory(brief: spoken, hubID: hub), BriefPlaybackMemory(seconds: 20, duration: 60, played: false))
        try cache.savePosition(0, played: true, brief: spoken, hubID: hub)
        XCTAssertEqual(cache.playbackMemory(brief: spoken, hubID: hub)?.played, true)
        XCTAssertEqual(cache.playbackMemory(brief: spoken, hubID: hub)?.duration, 60, "the length is kept")
        XCTAssertEqual(BriefAudioCache.positionRetentionInterval, 30 * 24 * 60 * 60)
    }
}
