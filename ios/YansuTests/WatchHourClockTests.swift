import XCTest
@testable import Yansu

// Guards Bo's rule of 2026-11-15: the watch keeps recordings by the clock hour, each one closed
// and sent on its own, instead of one recording that grows for as long as the watch records.
final class WatchHourClockTests: XCTestCase {
    private var calendar: Calendar = {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        return calendar
    }()

    private func date(_ hour: Int, _ minute: Int, _ second: Int = 0, day: Int = 14) -> Date {
        calendar.date(from: DateComponents(year: 2026, month: 11, day: day, hour: hour, minute: minute, second: second))!
    }

    func testARecordingIsCutAtTheNextClockHour() {
        XCTAssertEqual(WatchHourClock.cut(after: date(10, 23), calendar: calendar), date(11, 0))
        XCTAssertEqual(WatchHourClock.cut(after: date(10, 0), calendar: calendar), date(11, 0))
        XCTAssertEqual(WatchHourClock.cut(after: date(23, 30), calendar: calendar), date(0, 0, day: 15))
    }

    func testAnHourBegunInItsLastMinuteRunsThroughTheNext() {
        XCTAssertEqual(WatchHourClock.cut(after: date(10, 59, 30), calendar: calendar), date(12, 0))
        XCTAssertEqual(WatchHourClock.cut(after: date(10, 59, 0), calendar: calendar), date(11, 0))
    }

    func testTheCutFollowsTheClockInAHalfHourZone() {
        var india = Calendar(identifier: .gregorian)
        india.timeZone = TimeZone(identifier: "Asia/Kolkata")!
        let start = india.date(from: DateComponents(year: 2026, month: 11, day: 14, hour: 10, minute: 10))!
        let cut = WatchHourClock.cut(after: start, calendar: india)
        XCTAssertEqual(india.dateComponents([.hour, .minute], from: cut), DateComponents(hour: 11, minute: 0))
    }

    func testPartsFillTheHourAndTheLastOneClosesIt() {
        let cut = date(11, 0)
        var at = date(10, 23)
        var lengths: [TimeInterval] = []
        var closed = false
        while !closed {
            let plan = WatchHourClock.part(from: at, cut: cut)
            lengths.append(plan.seconds)
            closed = plan.closesHour
            at = at.addingTimeInterval(plan.seconds)
            XCTAssertLessThan(lengths.count, 10)
        }
        XCTAssertEqual(lengths, Array(repeating: 5 * 60, count: 7) + [2 * 60])
        XCTAssertEqual(at, cut)
    }

    func testAFullHourIsTwelvePartsAndNeverOneLongRecording() {
        let start = date(10, 0)
        let cut = WatchHourClock.cut(after: start, calendar: calendar)
        var at = start
        var parts = 0
        while true {
            let plan = WatchHourClock.part(from: at, cut: cut)
            parts += 1
            at = at.addingTimeInterval(plan.seconds)
            XCTAssertLessThanOrEqual(plan.seconds, WatchRecordingWire.partSeconds)
            if plan.closesHour { break }
        }
        XCTAssertEqual(parts, 12)
        XCTAssertEqual(at.timeIntervalSince(start), 3600)
    }

    func testAPartStartedLateStillRunsAMoment() {
        let plan = WatchHourClock.part(from: date(11, 0, 5), cut: date(11, 0))
        XCTAssertEqual(plan.seconds, WatchHourClock.shortestPartSeconds)
        XCTAssertTrue(plan.closesHour)
    }

    func testAfterACallAcrossTheHourTheNextPartIsANewRecording() {
        XCTAssertFalse(WatchHourClock.isPast(date(11, 0), at: date(10, 59, 59)))
        XCTAssertTrue(WatchHourClock.isPast(date(11, 0), at: date(11, 0)))
        XCTAssertTrue(WatchHourClock.isPast(date(11, 0), at: date(11, 20)))
    }

    func testAnHourOfManyInterruptionsClosesBeforeTheWireRunsOutOfNumbers() {
        XCTAssertFalse(WatchHourClock.mustClose(afterPart: 3))
        XCTAssertTrue(WatchHourClock.mustClose(afterPart: WatchRecordingWire.maxParts - 2))
    }

    func testTheListShowsWholeMinutes() {
        XCTAssertEqual(WatchHourClock.minutes(3600), 60)
        XCTAssertEqual(WatchHourClock.minutes(37 * 60 + 20), 37)
        XCTAssertEqual(WatchHourClock.minutes(10), 1)
        XCTAssertEqual(WatchHourClock.minutes(.nan), 1)
    }

    /// The watch's own record of a morning: three hours are three recordings, each finished.
    func testHoursAreSeparateFinishedRecordingsInTheLibrary() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let library = WatchRecordingLibrary(directory: directory)
        var at = date(9, 40)
        let stop = date(11, 20)
        var id = WatchRecordingWire.newRecordingID()
        library.begin(id: id, at: at)
        var cut = WatchHourClock.cut(after: at, calendar: calendar)
        while at < stop {
            let plan = WatchHourClock.part(from: at, cut: cut)
            let seconds = min(plan.seconds, stop.timeIntervalSince(at))
            let last = plan.closesHour || at.addingTimeInterval(seconds) >= stop
            library.addPart(recordingID: id, startedAt: at, durationSeconds: seconds, last: last)
            at = at.addingTimeInterval(seconds)
            if plan.closesHour, at < stop {
                id = WatchRecordingWire.newRecordingID()
                library.begin(id: id, at: at)
                cut = WatchHourClock.cut(after: at, calendar: calendar)
            }
        }
        let hours = library.all().reversed()
        XCTAssertEqual(hours.map(\.startedAt), [date(9, 40), date(10, 0), date(11, 0)])
        XCTAssertEqual(hours.map(\.durationSeconds), [20 * 60, 60 * 60, 20 * 60])
        XCTAssertTrue(hours.allSatisfy(\.finished))
        XCTAssertTrue(hours.allSatisfy { $0.parts.last?.last == true })
        XCTAssertLessThanOrEqual(hours.map(\.durationSeconds).max() ?? 0, 3600)
    }
}
