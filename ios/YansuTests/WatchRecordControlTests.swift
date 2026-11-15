import XCTest
@testable import Yansu

// Guards Bo's rule of 2026-11-15: a recording on the watch stops on a hold, never on a tap.
final class WatchRecordControlTests: XCTestCase {
    func testATapNeverStopsARecording() {
        XCTAssertEqual(WatchRecordControl.action(for: .tap, isRecording: true), .none)
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
