import XCTest
@testable import Yansu

// Guards Bo's rule of 2026-11-16 for the watch: record everything. A started recording goes on
// through the hour change and for as long as listening is on; the microphone is never closed or
// reopened for a reason of time; no audio is lost between two parts; and when the microphone is
// lost, the loss is counted and visible.
//
// The capture here only counts: it stands for the watch's audio engine. What a real watch does
// with its screen off is not something a test on a simulator can show.
final class WatchRecordingSessionTests: XCTestCase {
    /// A microphone that delivers audio as the test's clock moves.
    private final class CountingCapture: WatchCaptureDriving {
        var starts = 0
        var stops = 0
        var rotations = 0
        var files: [URL] = []
        var startError: Error?
        var rotateError: Error?
        private(set) var secondsInFile: TimeInterval = 0
        private(set) var lastAudioAt: Date?
        private(set) var asked: TimeInterval?
        private var open = false

        func start(file: URL) throws {
            if let startError { throw startError }
            starts += 1
            open = true
            secondsInFile = 0
            files.append(file)
            try Data([1]).write(to: file)
        }
        func rotate(to next: URL) throws -> TimeInterval {
            if let rotateError { throw rotateError }
            rotations += 1
            files.append(next)
            try Data([1]).write(to: next)
            defer { secondsInFile = 0 }
            return secondsInFile
        }
        func stop() -> TimeInterval {
            stops += 1
            open = false
            lastAudioAt = nil
            defer { secondsInFile = 0 }
            return secondsInFile
        }
        func askWhenFileHolds(_ seconds: TimeInterval) { asked = seconds }
        /// Audio arrives for `seconds`, ending at `now`.
        func hear(_ seconds: TimeInterval, until now: Date) {
            guard open else { return }
            secondsInFile += seconds
            lastAudioAt = now
        }
    }

    private var directory: URL!
    private var calendar: Calendar = {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        return calendar
    }()

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDown() {
        try? FileManager.default.removeItem(at: directory)
    }

    private func date(_ hour: Int, _ minute: Int, _ second: Int = 0) -> Date {
        calendar.date(from: DateComponents(year: 2026, month: 11, day: 16, hour: hour, minute: minute, second: second))!
    }

    private func make() -> (WatchRecordingSession, CountingCapture, WatchRecordingLibrary, WatchGapLog) {
        let library = WatchRecordingLibrary(directory: directory.appendingPathComponent("recordings"))
        let capture = CountingCapture()
        let gaps = WatchGapLog(url: directory.appendingPathComponent("gaps.json"), calendar: calendar)
        return (WatchRecordingSession(library: library, capture: capture, gaps: gaps, calendar: calendar), capture, library, gaps)
    }

    /// Runs the session second by second from `from` to `to`, the way the watch's capture and
    /// its two-second look would.
    private func run(_ session: WatchRecordingSession, _ capture: CountingCapture, from: Date, to: Date) {
        var now = from
        while now < to {
            now = now.addingTimeInterval(1)
            capture.hear(1, until: now)
            session.partIsDue(at: now)
        }
    }

    /// Every part of every recording, oldest first.
    private func parts(_ library: WatchRecordingLibrary) -> [WatchRecordingPart] {
        library.all().sorted { $0.startedAt < $1.startedAt }.flatMap(\.parts)
    }

    /// Bo's meeting of 2026-11-16, 11:45 to 12:15: recording died a moment after 12:00. It must
    /// come out as 11 AM and 12 PM with no second missing, and the microphone opened once.
    func testAMeetingAcrossTheHourIsRecordedWholeWithTheMicrophoneOpenedOnce() throws {
        let (session, capture, library, gaps) = make()
        try session.begin(at: date(11, 45))
        run(session, capture, from: date(11, 45), to: date(12, 15))
        session.end(at: date(12, 15))

        XCTAssertEqual(capture.starts, 1, "the microphone is opened once")
        XCTAssertEqual(capture.stops, 1, "and closed only when listening ends")
        let hours = library.all().sorted { $0.startedAt < $1.startedAt }
        XCTAssertEqual(hours.count, 2)
        XCTAssertEqual(hours.map { calendar.component(.hour, from: $0.startedAt) }, [11, 12])
        XCTAssertEqual(hours.map(\.finished), [true, true])
        XCTAssertEqual(hours[0].parts.reduce(0) { $0 + $1.durationSeconds }, 15 * 60, accuracy: 0.001)
        XCTAssertEqual(hours[1].parts.reduce(0) { $0 + $1.durationSeconds }, 15 * 60, accuracy: 0.001)
        XCTAssertEqual(hours[1].startedAt, date(12, 0))

        // No gap: each part begins exactly where the one before it ends.
        let all = parts(library)
        XCTAssertEqual(all.first?.startedAt, date(11, 45))
        for (previous, next) in zip(all, all.dropFirst()) {
            XCTAssertEqual(next.startedAt.timeIntervalSince(previous.startedAt), previous.durationSeconds, accuracy: 0.001)
        }
        XCTAssertEqual(all.reduce(0) { $0 + $1.durationSeconds }, 30 * 60, accuracy: 0.001)
        XCTAssertEqual(hours[0].parts.last?.last, true)
        XCTAssertTrue(all.allSatisfy { $0.durationSeconds <= WatchRecordingWire.partSeconds })

        let summary = gaps.summary(for: date(12, 15))
        XCTAssertEqual(summary.listeningSeconds, 30 * 60)
        XCTAssertEqual(summary.recordedSeconds, 30 * 60)
        XCTAssertEqual(summary.stops, 0)
    }

    /// Screen off, wrist down, the app away from the screen: none of them is a reason of time or
    /// anything the session acts on. Three hours go by with one open microphone and every second kept.
    func testThreeHoursGoByWithOneOpenMicrophoneAndNothingMissing() throws {
        let (session, capture, library, _) = make()
        try session.begin(at: date(9, 20))
        run(session, capture, from: date(9, 20), to: date(12, 20))
        XCTAssertEqual(capture.starts, 1)
        XCTAssertEqual(capture.stops, 0)
        session.end(at: date(12, 20))
        XCTAssertEqual(library.all().count, 4)
        XCTAssertEqual(parts(library).reduce(0) { $0 + $1.durationSeconds }, 3 * 3600, accuracy: 0.001)
        XCTAssertEqual(Set(library.all().map { calendar.component(.hour, from: $0.startedAt) }), [9, 10, 11, 12])
    }

    /// A call takes the microphone for four minutes in a 35-minute session. What came before is
    /// kept, listening stays on, recording comes back, and the four minutes are counted as missed.
    func testAnInterruptionKeepsWhatWasRecordedAndIsCountedAsAGap() throws {
        let (session, capture, library, gaps) = make()
        try session.begin(at: date(14, 0))
        run(session, capture, from: date(14, 0), to: date(14, 12))
        session.lostMicrophone(at: date(14, 12), reason: .interrupted)
        XCTAssertTrue(session.isOn)
        XCTAssertFalse(session.capturing)
        XCTAssertTrue(gaps.summary(for: date(14, 13)).stoppedNow)

        // In the background watchOS refuses the microphone: the session keeps waiting.
        capture.startError = CocoaError(.fileWriteUnknown)
        XCTAssertThrowsError(try session.resume(at: date(14, 14)))
        XCTAssertTrue(session.isOn)
        XCTAssertFalse(session.capturing)

        capture.startError = nil
        try session.resume(at: date(14, 16))
        run(session, capture, from: date(14, 16), to: date(14, 35))
        session.end(at: date(14, 35))

        XCTAssertEqual(parts(library).reduce(0) { $0 + $1.durationSeconds }, 31 * 60, accuracy: 0.001)
        XCTAssertEqual(library.all().count, 1, "the same hour stays one recording")
        let summary = gaps.summary(for: date(14, 35))
        XCTAssertEqual(summary.listeningSeconds, 35 * 60)
        XCTAssertEqual(summary.recordedSeconds, 31 * 60)
        XCTAssertEqual(summary.missedSeconds, 4 * 60)
        XCTAssertEqual(summary.stops, 1)
        XCTAssertFalse(summary.stoppedNow)
        XCTAssertEqual(gaps.day(for: date(14, 35))?.gaps.first?.reason, .interrupted)
    }

    /// A wait that runs across the hour: what comes after belongs to the next hour.
    func testAfterAnInterruptionAcrossTheHourTheNextHourBegins() throws {
        let (session, capture, library, _) = make()
        try session.begin(at: date(14, 50))
        run(session, capture, from: date(14, 50), to: date(14, 58))
        session.lostMicrophone(at: date(14, 58), reason: .interrupted)
        try session.resume(at: date(15, 3))
        run(session, capture, from: date(15, 3), to: date(15, 33))
        session.end(at: date(15, 33))
        let hours = library.all().sorted { $0.startedAt < $1.startedAt }
        XCTAssertEqual(hours.map { calendar.component(.hour, from: $0.startedAt) }, [14, 15])
        XCTAssertEqual(hours.map(\.finished), [true, true])
        XCTAssertEqual(hours[1].parts.reduce(0) { $0 + $1.durationSeconds }, 30 * 60, accuracy: 0.001)
    }

    /// The microphone stops delivering audio by itself. After two seconds the session says so.
    func testAMicrophoneThatGoesQuietIsNoticedAfterTwoSeconds() throws {
        let (session, capture, _, _) = make()
        try session.begin(at: date(16, 0))
        run(session, capture, from: date(16, 0), to: date(16, 10))
        XCTAssertFalse(session.microphoneWentQuiet(at: date(16, 10, 1)))
        XCTAssertFalse(session.microphoneWentQuiet(at: date(16, 10, 2)))
        XCTAssertTrue(session.microphoneWentQuiet(at: date(16, 10, 3)))
        session.lostMicrophone(at: date(16, 10, 3), reason: .quiet)
        XCTAssertFalse(session.microphoneWentQuiet(at: date(16, 11)), "a lost microphone is not reported again")
        try session.resume(at: date(16, 10, 4))
        XCTAssertEqual(capture.starts, 2)
        capture.hear(1, until: date(16, 10, 5))
        XCTAssertFalse(session.microphoneWentQuiet(at: date(16, 10, 6)))
    }

    /// The next file cannot be opened: the capture keeps writing where it was, and nothing is lost.
    func testWhenTheNextFileCannotBeOpenedRecordingGoesOnInTheOldOne() throws {
        let (session, capture, library, _) = make()
        try session.begin(at: date(17, 0))
        capture.rotateError = CocoaError(.fileWriteOutOfSpace)
        run(session, capture, from: date(17, 0), to: date(17, 7))
        XCTAssertEqual(capture.secondsInFile, 7 * 60, accuracy: 0.001)
        capture.rotateError = nil
        run(session, capture, from: date(17, 7), to: date(17, 8))
        session.end(at: date(17, 8))
        XCTAssertEqual(parts(library).reduce(0) { $0 + $1.durationSeconds }, 8 * 60, accuracy: 0.001)
    }

    /// The system ended the app while listening was on. At the next launch that is a counted gap,
    /// the unfinished recording is closed, and listening can begin again.
    func testAfterTheAppWasEndedTheGapIsCountedAndListeningBeginsAgain() throws {
        var (session, capture, library, gaps) = make()
        try session.begin(at: date(18, 0))
        run(session, capture, from: date(18, 0), to: date(18, 31))
        XCTAssertEqual(parts(library).reduce(0) { $0 + $1.durationSeconds }, 30 * 60, accuracy: 0.001,
                       "every finished part is already on the watch's storage")

        // The app is gone. A new launch reads what is on disk.
        (session, capture, library, gaps) = make()
        _ = library.recoverInterrupted { _ in 0 }
        XCTAssertNotNil(gaps.foundAfterLaunch(at: date(18, 40), lastAudioAt: nil))
        XCTAssertNil(gaps.foundAfterLaunch(at: date(18, 41), lastAudioAt: nil), "counted once")
        XCTAssertEqual(gaps.day(for: date(18, 40))?.gaps.last?.reason, .appEnded)
        XCTAssertTrue(library.all().allSatisfy(\.finished))

        try session.begin(at: date(18, 40))
        run(session, capture, from: date(18, 40), to: date(19, 15))
        session.end(at: date(19, 15))
        XCTAssertEqual(capture.starts, 1)
        XCTAssertEqual(parts(library).reduce(0) { $0 + $1.durationSeconds }, 65 * 60, accuracy: 0.001)
        XCTAssertEqual(gaps.summary(for: date(19, 15)).stops, 1)
    }

    /// A mark is a time inside the running part, and none is taken while nothing is recorded.
    func testMarksAreKeptWithTheirPartAndNotTakenWithoutAMicrophone() throws {
        let (session, capture, library, _) = make()
        XCTAssertFalse(session.mark())
        try session.begin(at: date(20, 0))
        run(session, capture, from: date(20, 0), to: date(20, 2))
        XCTAssertTrue(session.mark())
        run(session, capture, from: date(20, 2), to: date(20, 6))
        session.lostMicrophone(at: date(20, 6), reason: .interrupted)
        XCTAssertFalse(session.mark())
        session.end(at: date(20, 7))
        XCTAssertEqual(parts(library).first?.marks, [120])
        XCTAssertNil(parts(library).last?.marks)
    }
}
