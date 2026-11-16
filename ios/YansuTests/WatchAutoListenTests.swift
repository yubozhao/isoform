import XCTest
@testable import Yansu

// Guards watch v1.1.0: recording starts by itself when the watch app comes to the front, and a
// stop by the wearer holds until they start it again.
final class WatchAutoListenTests: XCTestCase {
    private func step(consent: Bool = true, microphone: WatchAutoListen.Microphone = .allowed, stopped: Bool = false,
                      recording: Bool = false, held: Bool = false) -> WatchAutoListen.Step {
        WatchAutoListen.step(consentSeen: consent, microphone: microphone, stoppedByWearer: stopped,
                             isRecording: recording, heldByInterruption: held)
    }

    func testItStartsByItselfWhenTheAppComesToTheFront() {
        XCTAssertEqual(step(), .start)
    }

    func testTheFirstTimeItAsksForTheMicrophoneByStarting() {
        XCTAssertEqual(step(microphone: .notAsked), .start)
    }

    func testNothingIsRecordedBeforeTheFirstRunSheetIsRead() {
        XCTAssertEqual(step(consent: false), .nothing)
        XCTAssertEqual(step(consent: false, microphone: .notAsked), .nothing)
    }

    func testAStopByTheWearerHolds() {
        XCTAssertEqual(step(stopped: true), .nothing)
        XCTAssertEqual(step(microphone: .notAsked, stopped: true), .nothing)
        XCTAssertEqual(step(microphone: .denied, stopped: true), .nothing)
    }

    func testAStopIsTheOffForThatDayOnly() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try XCTUnwrap(TimeZone(identifier: "America/Los_Angeles"))
        func at(_ day: Int, _ hour: Int) -> Date {
            calendar.date(from: DateComponents(year: 2026, month: 11, day: day, hour: hour))!
        }
        let stopped = at(14, 15)
        XCTAssertTrue(WatchAutoListen.stopHolds(stoppedAt: stopped, now: at(14, 16), calendar: calendar))
        XCTAssertTrue(WatchAutoListen.stopHolds(stoppedAt: stopped, now: at(14, 23), calendar: calendar))
        XCTAssertFalse(WatchAutoListen.stopHolds(stoppedAt: stopped, now: at(15, 7), calendar: calendar))
        XCTAssertFalse(WatchAutoListen.stopHolds(stoppedAt: nil, now: at(14, 16), calendar: calendar))
    }

    func testTheStopIsKeptAndCleared() throws {
        let suite = "watch-auto-listen-\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        XCTAssertNil(WatchAutoListen.stoppedAt(in: defaults))
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        WatchAutoListen.setStopped(now, in: defaults)
        XCTAssertEqual(WatchAutoListen.stoppedAt(in: defaults), now)
        WatchAutoListen.setStopped(nil, in: defaults)
        XCTAssertNil(WatchAutoListen.stoppedAt(in: defaults))
    }

    func testADeniedMicrophoneSaysSoInsteadOfAsking() {
        XCTAssertEqual(step(microphone: .denied), .showDenied)
    }

    func testARunningRecordingIsLeftAlone() {
        XCTAssertEqual(step(recording: true), .nothing)
        XCTAssertEqual(step(stopped: true, recording: true), .nothing)
    }

    func testAfterACallTheMicrophoneIsTakenBack() {
        XCTAssertEqual(step(recording: true, held: true), .resume)
    }
}
