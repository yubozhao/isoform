import XCTest
@testable import Yansu

@MainActor
final class DuoNavigationTests: XCTestCase {
    func testVisiblePanesKeepGeneralThenSpecificInWideAndSpecificInNarrow() {
        let navigation = DuoNavigation()
        navigation.push(.item("brief-card"), on: .today)
        navigation.push(.thread("topic"), on: .chat)
        navigation.push(.draft("topic"), on: .chat)

        XCTAssertEqual(navigation.visiblePanes(on: .today, wide: true), [.today, .item("brief-card")])
        XCTAssertEqual(navigation.visiblePanes(on: .chat, wide: true), [.thread("topic"), .draft("topic")])
        XCTAssertEqual(navigation.visiblePanes(on: .today, wide: false), [.item("brief-card")])
    }

    func testChangingLayoutDoesNotMutatePerTabPaths() {
        let navigation = DuoNavigation()
        navigation.push(.item("brief-card"), on: .today)
        navigation.push(.thread("topic"), on: .chat)
        navigation.push(.draft("main"), on: .chat)
        let wide = DuoLayout.metrics(width: 890, height: 626, horizontalSizeClassIsRegular: true)
        let narrow = DuoLayout.metrics(width: 600, height: 900, horizontalSizeClassIsRegular: false)
        for metrics in [wide, narrow] {
            _ = navigation.visiblePanes(on: .today, wide: metrics.isWide)
            _ = navigation.visiblePanes(on: .chat, wide: metrics.isWide)
        }

        XCTAssertEqual(navigation.path(for: .today), [.today, .item("brief-card")])
        XCTAssertEqual(navigation.path(for: .chat), [.topics, .thread("topic"), .draft("main")])
    }

    func testChatDraftAndAudioPlayerSurviveLayoutMetricsChanges() async throws {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }

        let chat = ChatStore(
            hub: ChatUITestHub(now: Date()),
            journal: PendingWriteJournal(fileURL: directory.appendingPathComponent("writes.json")),
            guardian: LocalDecisionGuard(),
            presence: { true },
            isPaired: { true },
            cache: ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true))
        )
        chat.setDraft("typed while the panes were open", in: ChatUITestSeed.mainChatID)

        let audioCache = BriefAudioCache(directory: directory.appendingPathComponent("audio", isDirectory: true))
        var brief = MobileBriefV1(briefId: "duo-layout", asOf: Date().ISO8601Format())
        let audio = BriefAudio(mime: "audio/wav", bytes: 3, durationSeconds: 60)
        brief.audio = audio
        try audioCache.store(Data([1, 2, 3]), brief: brief, hubID: "duo-layout", audio: audio)
        let engine = LayoutTestAudioEngine()
        let player = BriefAudioPlayer(
            cache: audioCache,
            requestAudio: { _ in throw BriefAudioError.notAvailable },
            downloadAudio: { _ in throw BriefAudioError.notAvailable },
            makeEngine: { _ in engine },
            managesSystemPlayback: false
        )
        await player.prepare(brief: brief, hubID: "duo-layout")
        await player.toggle()
        XCTAssertEqual(player.state, .playing)

        let navigation = DuoNavigation()
        navigation.push(.thread("topic"), on: .chat)
        let metrics = [
            DuoLayout.metrics(width: 890, height: 626, horizontalSizeClassIsRegular: true),
            DuoLayout.metrics(width: 600, height: 900, horizontalSizeClassIsRegular: false)
        ]
        for metric in metrics {
            _ = navigation.visiblePanes(on: .chat, wide: metric.isWide)
            XCTAssertEqual(chat.draft(in: ChatUITestSeed.mainChatID), "typed while the panes were open")
            XCTAssertEqual(player.state, .playing)
        }
    }

    func testCloseReturnsToThePreviousGeneralStep() {
        let navigation = DuoNavigation()
        navigation.push(.item("card"), on: .today)
        navigation.close(on: .today)
        XCTAssertEqual(navigation.path(for: .today), [.today])
        navigation.close(on: .today)
        XCTAssertEqual(navigation.path(for: .today), [.today])
    }

    func testTodaySelectionClearsWhenCardDisappearsOrFeedBecomesUnavailable() {
        for (isPaired, repairNeeded, cardIDs) in [
            (true, false, Set(["other"])),
            (false, false, Set(["brief-card"])),
            (true, true, Set(["brief-card"]))
        ] {
            let navigation = DuoNavigation()
            navigation.push(.item("brief-card"), on: .today)
            navigation.reconcileTodaySelection(isPaired: isPaired, repairNeeded: repairNeeded, cardIDs: cardIDs)
            XCTAssertEqual(navigation.path(for: .today), [.today])
        }

        let navigation = DuoNavigation()
        navigation.push(.item("brief-card"), on: .today)
        navigation.reconcileTodaySelection(isPaired: true, repairNeeded: false, cardIDs: ["brief-card"])
        XCTAssertEqual(navigation.path(for: .today), [.today, .item("brief-card")])
    }
}

@MainActor
private final class LayoutTestAudioEngine: BriefAudioPlayback {
    let duration: TimeInterval = 60
    var currentTime: TimeInterval = 0
    var rate: Float = 1
    private(set) var playing = false

    func play() -> Bool {
        playing = true
        return true
    }

    func pause() { playing = false }

    func stop() { playing = false }
}
