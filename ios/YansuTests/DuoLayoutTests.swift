import XCTest
@testable import Yansu

final class DuoLayoutTests: XCTestCase {
    func testWideRequiresWideWindowAndRegularClassOrReportedFold() {
        XCTAssertFalse(DuoLayout.metrics(width: 799, height: 678,
                                         horizontalSizeClassIsRegular: true).isWide)
        XCTAssertFalse(DuoLayout.metrics(width: 890, height: 626,
                                         horizontalSizeClassIsRegular: false).isWide)
        XCTAssertTrue(DuoLayout.metrics(width: 890, height: 626,
                                        horizontalSizeClassIsRegular: true).isWide)
        XCTAssertTrue(DuoLayout.metrics(width: 890, height: 626,
                                        horizontalSizeClassIsRegular: false,
                                        reportedFoldX: 445).isWide)
    }

    /// Bo's open Duo, 2026-11-18: with fixed pane widths the right pane straddled the fold and
    /// the column broke. The fold splits the page on every tab and in every state, and nothing
    /// crosses it (phone-duo v1.0.0 rule 1).
    func testTheFoldSplitsThePageOnEveryTab() {
        for foldX: CGFloat in [445, 420, 470] {
            let metrics = DuoMetrics(width: 890, height: 626, foldX: foldX)
            for tab in [DuoTab.today, .watch, .chat] {
                for hasSelection in [false, true] {
                    let panes = DuoLayout.frames(for: tab, metrics: metrics, hasSelection: hasSelection)
                    XCTAssertEqual(panes.left.minX, 0)
                    XCTAssertEqual(panes.left.maxX, foldX, "the left pane runs to the fold")
                    XCTAssertEqual(panes.right.minX, foldX, "the right pane starts at the fold")
                    XCTAssertEqual(panes.right.maxX, 890 - DuoLayout.rightPaneToolbarInset, "and ends at the rail")
                }
            }
        }
        // No fold reported: the split is the middle.
        let middle = DuoLayout.frames(for: .today, metrics: DuoMetrics(width: 890, height: 626, foldX: nil))
        XCTAssertEqual(middle.left.maxX, 445)
        XCTAssertEqual(middle.right.minX, 445)
        // A fold reported past the rail never gives the right pane a negative width.
        let odd = DuoLayout.frames(for: .chat, metrics: DuoMetrics(width: 890, height: 626, foldX: 2000))
        XCTAssertGreaterThanOrEqual(odd.right.width, 0)
        XCTAssertEqual(DuoLayout.paneInset, 24)
    }

    func testPaneWidthsAndFullScreenUseWholeWindow() {
        let metrics = DuoLayout.metrics(width: 890, height: 626,
                                        horizontalSizeClassIsRegular: true)
        XCTAssertEqual(DuoLayout.frames(for: .today, metrics: metrics).left.width, 445)
        XCTAssertEqual(DuoLayout.frames(for: .today, metrics: metrics, hasSelection: true).left.width, 445)
        XCTAssertEqual(DuoLayout.frames(for: .chat, metrics: metrics).left.width, 445)
        let frames = DuoLayout.frames(for: .today, metrics: metrics, fullScreen: true)
        XCTAssertEqual(frames.fullScreen, CGRect(x: 0, y: 0, width: 890, height: 626))
        XCTAssertEqual(DuoLayout.frames(for: .today, metrics: metrics).right.maxX, 810)
        XCTAssertEqual(frames.text.minX, 80)
        XCTAssertEqual(frames.text.maxX, 810)
    }

    func testFoldClearanceNudgesControlsToNearestPane() {
        let foldX: CGFloat = 445
        let left = DuoLayout.clear(CGRect(x: 420, y: 10, width: 40, height: 32),
                                   ofFoldAt: foldX, containerWidth: 890)
        let right = DuoLayout.clear(CGRect(x: 430, y: 10, width: 40, height: 32),
                                    ofFoldAt: foldX, containerWidth: 890)

        XCTAssertEqual(foldX - left.maxX, 16)
        XCTAssertEqual(right.minX - foldX, 16)
    }

    /// Bo, 2026-11-18: on a wide phone Watch opened only as a sheet from Settings. The rail has
    /// three tabs, Today, Watch, Chat, and Watch shows by the same rule as on the narrow bar.
    func testTheWideRailHasWatchBetweenTodayAndChatWhenThePhoneHasAWatch() {
        XCTAssertEqual(DuoTab.rail(showsWatch: true), [.today, .watch, .chat])
        XCTAssertEqual(DuoTab.rail(showsWatch: false), [.today, .chat])
        // Selecting Watch shows the Watch page in the main area; without a watch it stays on Today.
        XCTAssertEqual(DuoTab.shown(for: .watch, showsWatch: true), .watch)
        XCTAssertEqual(DuoTab.shown(for: .watch, showsWatch: false), .today)
        XCTAssertEqual(DuoTab.shown(for: .today, showsWatch: true), .today)
        XCTAssertEqual(DuoTab.shown(for: .chat, showsWatch: true), .chat)
        XCTAssertEqual(DuoTab.shown(for: .tasks, showsWatch: true), .today)
        XCTAssertEqual(DuoTab.shown(for: .settings, showsWatch: true), .today)
        // Each rail button selects its own tab of the app.
        XCTAssertEqual(DuoTab.rail(showsWatch: true).map(\.routerTab), [.today, .watch, .chat])
        // Watch has its two panes like the other tabs: left of the fold and right of it.
        let metrics = DuoMetrics(width: 890, height: 626, foldX: 445)
        let panes = DuoLayout.frames(for: .watch, metrics: metrics)
        XCTAssertEqual(panes.left.maxX, 445)
        XCTAssertEqual(panes.right.minX, 445)
    }

    /// The Watch item is the mark in the watch's pose, with an amber dot when something needs
    /// looking at: Not listening, or hours that waited 30 minutes or could not be sent
    /// (phone-watch v1.1.0 rule 3).
    func testTheWatchItemShowsThePoseAndTheAmberDot() {
        let now = Date()
        func row(_ stage: WatchRecordingStage, endedMinutesAgo: Double) -> WatchRecordingRow {
            WatchRecordingRow(id: UUID().uuidString, startedAt: now.addingTimeInterval(-endedMinutesAgo * 60 - 600),
                              durationSeconds: 600, stage: stage)
        }
        func item(_ status: WatchLiveStatus?, reachable: Bool = true, rows: [WatchRecordingRow] = [],
                  outcome: WatchRecordingForwarder.Outcome = .waiting) -> WatchItemState {
            WatchItemState.of(connection: .ready(reachable: reachable), status: status, rows: rows, outcome: outcome, now: now)
        }
        XCTAssertEqual(item(.recording), WatchItemState(pose: .listening, needsLook: false))
        XCTAssertEqual(item(.recording, reachable: false).pose, .listening, "out of reach it keeps listening")
        XCTAssertEqual(item(.pausedOnWatch), WatchItemState(pose: .closedEyes, needsLook: false))
        XCTAssertEqual(item(.pausedOnComputer).pose, .closedEyes)
        XCTAssertEqual(item(.notRecording), WatchItemState(pose: .shut, needsLook: true))
        XCTAssertEqual(item(.waitingForMicrophone), WatchItemState(pose: .shut, needsLook: true))
        XCTAssertEqual(item(.stoppedToday), WatchItemState(pose: .shut, needsLook: false), "the wearer's own stop is plain")
        // Hours on the phone: the dot only for hours that cannot go (phone-watch v1.2.0 rule 7):
        // the computer not reached and half an hour gone, or an hour that failed.
        XCTAssertFalse(item(.recording, rows: [row(.onPhone, endedMinutesAgo: 5)]).needsLook)
        XCTAssertTrue(item(.recording, rows: [row(.onPhone, endedMinutesAgo: 31)]).needsLook)
        XCTAssertFalse(item(.recording, rows: [row(.onPhone, endedMinutesAgo: 31)], outcome: .drained).needsLook,
                       "hours that are going stay plain, however long they waited")
        XCTAssertFalse(item(.recording, rows: [row(.onWatch, endedMinutesAgo: 90)]).needsLook, "hours on the watch come by themselves")
        XCTAssertTrue(item(.recording, rows: [row(.failedToSend, endedMinutesAgo: 1)], outcome: .drained).needsLook)
        XCTAssertFalse(item(.recording, rows: [row(.transcribed, endedMinutesAgo: 90)]).needsLook)
        XCTAssertEqual(WatchItemState.of(connection: .appMissing, status: nil, rows: [], now: now).pose, .shut)
    }

    @MainActor
    func testTheWideLayoutKeepsNoPathForWatch() {
        let navigation = DuoNavigation()
        XCTAssertEqual(navigation.path(for: .watch), [])
        navigation.setPushedPath([.item("x")], on: .watch)
        XCTAssertEqual(navigation.path(for: .watch), [])
        XCTAssertEqual(navigation.path(for: .today), [.today])
        XCTAssertEqual(navigation.path(for: .chat), [.topics])
    }

    func testControlFramesStayClearOfTheFoldInEveryWideState() {
        let states: [(DuoTab, Bool)] = [
            (.today, false), (.today, true), (.chat, false),
            (.chat, true)
        ]
        for foldX: CGFloat in [445, 420, 470] {
            let metrics = DuoMetrics(width: 890, height: 626, foldX: foldX)
            for (tab, hasSelection) in states {
                let panes = DuoLayout.frames(for: tab, metrics: metrics,
                                             hasSelection: hasSelection)
                XCTAssertFalse((panes.left.maxX..<panes.right.minX).contains(foldX))
                for control in DuoLayout.controlFrames(for: tab, metrics: metrics,
                                                       hasSelection: hasSelection) {
                    XCTAssertTrue(foldX - control.maxX >= 16 || control.minX - foldX >= 16)
                    XCTAssertGreaterThanOrEqual(control.minX, 0)
                    XCTAssertLessThanOrEqual(control.maxX, metrics.width)
                }
            }
        }
    }

    func testMotionDocksClearOfTheFold() {
        let player = DuoMotion.dockedPlayer(size: CGSize(width: 52, height: 52),
                                            in: CGRect(x: 0, y: 0, width: 890, height: 626),
                                            foldX: 445)
        XCTAssertEqual(player.maxX, 874)
        XCTAssertEqual(player.maxY, 610)
        XCTAssertGreaterThanOrEqual(player.minX - 445, DuoLayout.foldClearance)
    }
}
