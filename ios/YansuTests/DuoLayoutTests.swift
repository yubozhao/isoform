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

    func testPaneWidthsAndFullScreenUseWholeWindow() {
        let metrics = DuoLayout.metrics(width: 890, height: 626,
                                        horizontalSizeClassIsRegular: true)
        XCTAssertEqual(DuoLayout.frames(for: .today, metrics: metrics).left.width, 250)
        XCTAssertEqual(DuoLayout.frames(for: .today, metrics: metrics, hasSelection: true).left.width, 360)
        XCTAssertEqual(DuoLayout.frames(for: .chat, metrics: metrics).left.width, 300)
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
