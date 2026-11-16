import XCTest
@testable import Yansu

// Guards Bo's rule of 2026-11-15: a recording on the watch stops on a hold, never on a tap.
final class WatchRecordControlTests: XCTestCase {
    func testATapNeverStopsARecording() {
        XCTAssertNotEqual(WatchRecordControl.action(for: .tap, isRecording: true), .stop)
    }

    /// While recording, a tap marks the moment (watch v1.1.0): "this matters".
    func testATapWhileRecordingMarksTheMoment() {
        XCTAssertEqual(WatchRecordControl.action(for: .tap, isRecording: true), .mark)
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: 0.3), isRecording: true), .none, "a short hold is not a mark")
    }

    func testATapStartsARecording() {
        XCTAssertEqual(WatchRecordControl.action(for: .tap, isRecording: false), .start)
    }

    func testOnlyAFullHoldStops() {
        let hold = WatchRecordControl.holdSeconds
        XCTAssertGreaterThanOrEqual(hold, 1)
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: 0), isRecording: true), .none)
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: hold * 0.9), isRecording: true), .none)
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: hold), isRecording: true), .stop)
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: hold * 3), isRecording: true), .stop)
    }

    func testAHoldWhileIdleStillStarts() {
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: WatchRecordControl.holdSeconds), isRecording: false), .start)
        XCTAssertEqual(WatchRecordControl.action(for: .hold(seconds: 0.2), isRecording: false), .none)
    }

    /// The double tap and the Action button do what a tap does, and never stop a recording.
    func testDoubleTapAndTheActionButtonMarkOrStartAndNeverStop() {
        for touch in [WatchRecordControl.Touch.doubleTap, .actionButton] {
            XCTAssertEqual(WatchRecordControl.action(for: touch, isRecording: true), .mark)
            XCTAssertEqual(WatchRecordControl.action(for: touch, isRecording: false), .start)
        }
    }

    func testVoiceOverActivateStartsAndStops() {
        XCTAssertEqual(WatchRecordControl.action(for: .accessibilityActivate, isRecording: true), .stop)
        XCTAssertEqual(WatchRecordControl.action(for: .accessibilityActivate, isRecording: false), .start)
    }

    func testProgressRunsFromZeroToOne() {
        let hold = WatchRecordControl.holdSeconds
        XCTAssertEqual(WatchRecordControl.progress(heldSeconds: -1), 0)
        XCTAssertEqual(WatchRecordControl.progress(heldSeconds: .nan), 0)
        XCTAssertEqual(WatchRecordControl.progress(heldSeconds: hold / 2), 0.5, accuracy: 0.0001)
        XCTAssertEqual(WatchRecordControl.progress(heldSeconds: hold * 4), 1)
    }
}
