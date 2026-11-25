import XCTest
@testable import Yansu

final class WatchRecordingWireTests: XCTestCase {
    func testPartInfoSurvivesTheTransferMetadata() throws {
        let info = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 3, last: true,
                                          startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 61.5,
                                          model: "Apple Watch")
        XCTAssertEqual(WatchRecordingPartInfo(metadata: info.metadata), info)
    }

    func testMalformedMetadataIsRefused() {
        let good = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 0, last: false,
                                          startedAt: Date(), durationSeconds: 5).metadata
        XCTAssertNil(WatchRecordingPartInfo(metadata: nil))
        for (key, value) in [("recordingId", "NOT-A-UUID" as Any), ("recordingId", UUID().uuidString as Any),
                             ("part", 64 as Any), ("part", -1 as Any), ("durationSeconds", 0.0 as Any),
                             ("v", 2 as Any), ("startedAt", "today" as Any)] {
            var bad = good
            bad[key] = value
            XCTAssertNil(WatchRecordingPartInfo(metadata: bad), "\(key)=\(value)")
        }
    }

    func testStatesSurviveTheApplicationContextAndUnknownOnesAreDropped() {
        let id = WatchRecordingWire.newRecordingID()
        var context = WatchRecordingWire.context([id: .transcribed])
        XCTAssertEqual(WatchRecordingWire.states(fromContext: context), [id: .transcribed])
        context[WatchRecordingWire.contextKey] = [id: "somewhere new", "bad id": "onPhone"]
        XCTAssertEqual(WatchRecordingWire.states(fromContext: context), [:])
        XCTAssertEqual(WatchRecordingRemoteState(computerState: "a state from a newer computer"), .onComputer)
    }

    func testHeartRateAndCoarseLocationSurviveTheTransferMetadata() throws {
        let context = try XCTUnwrap(WatchRecordingContext(
            heartRate: .init(readings: [71.4, 104.2, 131.3, 300, 0]),
            location: .init(latitude: 37.759812, longitude: -122.414923, accuracyMeters: 35, place: " Mission District\n")))
        XCTAssertEqual(context.heartRate, .init(avg: 102, min: 71, max: 131, samples: 3))
        // Two decimals, about a kilometer: the precise position never leaves the watch.
        XCTAssertEqual(context.location?.latitude, 37.76)
        XCTAssertEqual(context.location?.longitude, -122.41)
        XCTAssertEqual(context.location?.accuracyMeters, 1_000)
        XCTAssertEqual(context.location?.place, "Mission District")

        let info = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 1, last: false,
                                          startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 900,
                                          context: context)
        // The metadata crosses the watch link as a property list.
        let data = try PropertyListSerialization.data(fromPropertyList: info.metadata, format: .binary, options: 0)
        let back = try XCTUnwrap(PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
        XCTAssertEqual(WatchRecordingPartInfo(metadata: back), info)
    }

    func testAnUnusableContextIsLeftOutAndThePartStillArrives() throws {
        XCTAssertNil(WatchRecordingContext.HeartRate(readings: []))
        XCTAssertNil(WatchRecordingContext.HeartRate(readings: [0, 400]))
        XCTAssertNil(WatchRecordingContext.Location(latitude: 0, longitude: 0))
        XCTAssertNil(WatchRecordingContext.Location(latitude: 91, longitude: 10))
        XCTAssertNil(WatchRecordingContext(heartRate: nil, location: nil))

        var metadata = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 0, last: true,
                                              startedAt: Date(timeIntervalSince1970: 1_800_000_000),
                                              durationSeconds: 60).metadata
        metadata["hrAvg"] = 900.0
        metadata["hrMin"] = 1.0
        metadata["hrMax"] = 950.0
        metadata["hrSamples"] = 4
        metadata["lat"] = 200.0
        metadata["lon"] = 10.0
        let info = try XCTUnwrap(WatchRecordingPartInfo(metadata: metadata))
        XCTAssertNil(info.context)
    }

    func testTheComplicationReadsOnlyARunningRecordingsStartTime() throws {
        let suite = "watch-live-\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        XCTAssertNil(WatchLiveState.recordingSince(in: defaults, now: now))

        let since = now.addingTimeInterval(-125)
        WatchLiveState.write(recordingSince: since, to: defaults)
        XCTAssertEqual(WatchLiveState.recordingSince(in: defaults, now: now), since)
        // The shared container holds that one number and nothing else.
        XCTAssertEqual(defaults.persistentDomain(forName: suite)?.keys.sorted(), [WatchLiveState.key])

        // A start left behind by a killed app stops counting after the longest recording.
        XCTAssertNil(WatchLiveState.recordingSince(in: defaults, now: since.addingTimeInterval(WatchLiveState.longest + 1)))
        // The watch says it records again at every hour, so the leftover is believed for two
        // hours at most, not for a whole day.
        XCTAssertLessThanOrEqual(WatchLiveState.longest, 2 * 60 * 60)
        XCTAssertGreaterThan(WatchLiveState.longest, 61 * 60)

        WatchLiveState.write(recordingSince: nil, to: defaults)
        XCTAssertNil(WatchLiveState.recordingSince(in: defaults, now: now))
        XCTAssertNil(defaults.persistentDomain(forName: suite)?[WatchLiveState.key])
    }

    func testAnAreaNamedBeforeIsNotLookedUpAgain() throws {
        var names = WatchPlaceNames()
        XCTAssertNil(names.name(latitude: 37.76, longitude: -122.41))
        names.remember(" Mission District ", latitude: 37.76, longitude: -122.41)
        names.remember("", latitude: 1, longitude: 1)
        XCTAssertEqual(names.name(latitude: 37.76, longitude: -122.41), "Mission District")
        // The key is the rounded position, so a nearby fix in the same cell finds the same name.
        XCTAssertEqual(names.name(latitude: 37.7601, longitude: -122.4099), "Mission District")
        XCTAssertNil(names.name(latitude: 37.77, longitude: -122.41))
        XCTAssertEqual(names.count, 1)

        // Only the newest few areas are kept.
        for index in 0..<(WatchPlaceNames.kept + 5) {
            names.remember("Area \(index)", latitude: Double(index), longitude: 10)
        }
        XCTAssertEqual(names.count, WatchPlaceNames.kept)
        XCTAssertNil(names.name(latitude: 37.76, longitude: -122.41))

        // Kept across launches, and forgotten when location is turned off.
        let suite = "watch-places-\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        names.save(to: defaults)
        XCTAssertEqual(WatchPlaceNames.load(from: defaults), names)
        WatchPlaceNames.forget(in: defaults)
        XCTAssertEqual(WatchPlaceNames.load(from: defaults).count, 0)
    }

    func testOlderComputerOmitsTheRecordingsCapability() throws {
        XCTAssertNil(try JSONDecoder().decode(HubStatus.self, from: Data("{}".utf8)).recordingContext)
        XCTAssertNil(try JSONDecoder().decode(HubStatus.self, from: Data("{}".utf8)).recordings)
    }

    func testRecordingsRouteIsAGetWithItsIDs() {
        let route = HubRoute.recordings(ids: ["a", "b"])
        XCTAssertEqual(route.method, "GET")
        XCTAssertEqual(route.path, "/recordings")
        // deleted=1 asks for recordings deleted on the computer as well (PROTOCOL.md 24.5).
        XCTAssertEqual(route.query, "deleted=1&ids=a,b")
        XCTAssertEqual(HubRoute.recordings(ids: []).query, "deleted=1")
        let transcript = HubRoute.recordingTranscript(id: "a")
        XCTAssertEqual(transcript.method, "GET")
        XCTAssertEqual(transcript.path, "/recordings/transcript")
        XCTAssertEqual(transcript.query, "id=a")
        XCTAssertNil(try? JSONDecoder().decode(HubStatus.self, from: Data("{}".utf8)).recordingDetail)
    }

    func testTheWatchTellsThePhoneWhatItStillHolds() {
        let id = WatchRecordingWire.newRecordingID()
        let held = [WatchRecordingSummary(id: id, startedAt: Date(timeIntervalSince1970: 1_800_000_000),
                                          durationSeconds: 1810.5, unsentParts: 2, finished: true)]
        var context = WatchRecordingWire.summaryContext(held)
        XCTAssertEqual(WatchRecordingWire.summaries(fromContext: context), held)
        // The two directions share a version key and never read each other's payload.
        XCTAssertTrue(WatchRecordingWire.states(fromContext: context).isEmpty)
        context[WatchRecordingWire.summariesKey] = [["id": "not-a-uuid", "startedAt": 1.0, "durationSeconds": 1.0, "unsent": 1, "finished": true]]
        XCTAssertTrue(WatchRecordingWire.summaries(fromContext: context).isEmpty)
        XCTAssertEqual(WatchRecordingRemoteState(computerState: "deleted"), .deleted)
        XCTAssertEqual(WatchRecordingWire.states(fromContext: WatchRecordingWire.context([id: .deleted])), [id: .deleted])
    }

    func testThePhoneListJoinsWhatItHoldsWithWhatTheWatchStillHolds() {
        let onWatch = WatchRecordingWire.newRecordingID(), running = WatchRecordingWire.newRecordingID()
        let waiting = WatchRecordingWire.newRecordingID(), refused = WatchRecordingWire.newRecordingID()
        let unread = WatchRecordingWire.newRecordingID(), done = WatchRecordingWire.newRecordingID()
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let rows = WatchRecordingRow.rows(phone: [
            PhoneRecording(id: waiting, startedAt: at.addingTimeInterval(10), durationSeconds: 900, state: .onPhone,
                           partsOnPhone: 1, bytesOnPhone: 2048, sending: true),
            PhoneRecording(id: refused, startedAt: at.addingTimeInterval(20), durationSeconds: 60, state: .failed, partsOnPhone: 1),
            PhoneRecording(id: unread, startedAt: at.addingTimeInterval(30), durationSeconds: 60, state: .failed),
            PhoneRecording(id: done, startedAt: at.addingTimeInterval(40), durationSeconds: 60, state: .transcribed),
        ], watch: [
            WatchRecordingSummary(id: onWatch, startedAt: at.addingTimeInterval(50), durationSeconds: 300, unsentParts: 1, finished: true),
            WatchRecordingSummary(id: running, startedAt: at.addingTimeInterval(60), durationSeconds: 0, unsentParts: 0, finished: false),
            WatchRecordingSummary(id: waiting, startedAt: at.addingTimeInterval(10), durationSeconds: 1800, unsentParts: 1, finished: true),
        ])
        XCTAssertEqual(rows.map(\.id), [running, onWatch, done, unread, refused, waiting])
        XCTAssertEqual(rows.map(\.stage), [.recording, .onWatch, .transcribed, .failedOnComputer, .failedToSend, .sending])
        // A recording partly across shows its whole length and the part still on the watch.
        XCTAssertEqual(rows.last?.durationSeconds, 1800)
        XCTAssertEqual(rows.last?.partsOnWatch, 1)
        XCTAssertEqual(rows.last?.bytesOnPhone, 2048)
        XCTAssertNotNil(WatchRecordingsCopy.todayLine(rows))
        XCTAssertNil(WatchRecordingsCopy.todayLine(rows.filter { !$0.stage.waiting }))
        XCTAssertEqual(WatchRecordingsCopy.speaker("you"), "You")
        XCTAssertEqual(WatchRecordingsCopy.speaker("speaker_2"), "Speaker 2")
        XCTAssertNil(WatchRecordingsCopy.speaker(nil))
    }
}

// The names the computer gives the conversations in an hour (PROTOCOL.md 24.7) travel from the
// computer's view to the phone's row and on to the watch's row.
final class WatchRecordingNamesTests: XCTestCase {
    func testTheComputersViewDecodesWithAndWithoutNames() throws {
        let named = try JSONDecoder().decode(RecordingView.self, from: Data(#"{"id":"a","state":"transcribed","conversations":["Pricing with Sam","Standup"]}"#.utf8))
        XCTAssertEqual(named.conversations, ["Pricing with Sam", "Standup"])
        let plain = try JSONDecoder().decode(RecordingView.self, from: Data(#"{"id":"a","state":"transcribed"}"#.utf8))
        XCTAssertNil(plain.conversations)
    }

    func testNamesTravelToTheWatchWithTheStatesAndAreCleaned() {
        let hour = WatchRecordingWire.newRecordingID(), gone = WatchRecordingWire.newRecordingID(), unknown = WatchRecordingWire.newRecordingID()
        let long = String(repeating: "x", count: 80)
        let context = WatchRecordingWire.context([hour: .transcribed, gone: .deleted],
                                                 names: [hour: [" Pricing with Sam ", "", "Standup", long, "Four", "Five"], gone: ["Old"], unknown: ["Stray"]])
        let names = WatchRecordingWire.names(fromContext: context)
        XCTAssertEqual(names.keys.sorted(), [hour])
        XCTAssertEqual(names[hour], ["Pricing with Sam", "Standup", String(long.prefix(WatchRecordingWire.maxNameLength)), "Four"])
        XCTAssertEqual(WatchRecordingWire.states(fromContext: context)[hour], .transcribed)
        // No names: the context is what it always was.
        XCTAssertNil(WatchRecordingWire.context([hour: .transcribed])[WatchRecordingWire.namesKey])
        XCTAssertTrue(WatchRecordingWire.names(fromContext: [WatchRecordingWire.namesKey: ["not-a-uuid": ["x"]], WatchRecordingWire.contextVersionKey: WatchRecordingWire.version]).isEmpty)
    }

    func testTheWatchKeepsAnHoursNamesAndDropsThemWhenThePhoneNoLongerGivesThem() {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let library = WatchRecordingLibrary(directory: directory)
        let hour = WatchRecordingWire.newRecordingID(), other = WatchRecordingWire.newRecordingID()
        for id in [hour, other] {
            library.begin(id: id, at: Date(timeIntervalSince1970: 1_800_000_000))
            library.addPart(recordingID: id, startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 60, last: true)
        }
        library.apply(names: [hour: ["Pricing with Sam"]], known: [hour])
        XCTAssertEqual(library.all().first { $0.id == hour }?.names, ["Pricing with Sam"])
        XCTAssertNil(library.all().first { $0.id == other }?.names)
        // They survive a relaunch of the watch app.
        XCTAssertEqual(WatchRecordingLibrary(directory: directory).all().first { $0.id == hour }?.names, ["Pricing with Sam"])
        // A context that does not mention the hour leaves it alone; one that knows it without names clears them.
        library.apply(names: [:], known: [other])
        XCTAssertEqual(library.all().first { $0.id == hour }?.names, ["Pricing with Sam"])
        library.apply(names: [:], known: [hour])
        XCTAssertNil(library.all().first { $0.id == hour }?.names)
    }

    func testAnHoursRowShowsItsNamesInPlaceOfItsState() {
        let id = WatchRecordingWire.newRecordingID()
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let rows = WatchRecordingRow.rows(phone: [
            PhoneRecording(id: id, startedAt: at, durationSeconds: 3600, state: .transcribed, names: ["Pricing with Sam", "Standup"]),
            PhoneRecording(id: WatchRecordingWire.newRecordingID(), startedAt: at.addingTimeInterval(3600), durationSeconds: 60, state: .onComputer),
        ], watch: [])
        XCTAssertEqual(rows.first { $0.id == id }?.namesLine, "Pricing with Sam, Standup")
        XCTAssertNil(rows.first { $0.id != id }?.namesLine)
    }
}

// One pause covers all of Isoform (PROTOCOL.md 24.8): a pause on the computer reaches the watch,
// which stops and deletes what it recorded since the pause began.
final class WatchPauseTests: XCTestCase {
    private let base = Date(timeIntervalSince1970: 1_800_000_000)

    func testTheComputersStatusDecodesAPause() throws {
        let paused = try JSONDecoder().decode(HubStatus.self, from: Data(#"{"observingPause":{"since":"2026-11-14T11:04:00Z","until":"2026-11-14T12:04:00Z"}}"#.utf8))
        let pause = try XCTUnwrap(paused.observingPause?.pause)
        XCTAssertEqual(pause.until?.timeIntervalSince(try XCTUnwrap(pause.since)), 3600)
        let open = try JSONDecoder().decode(HubStatus.self, from: Data(#"{"observingPause":{}}"#.utf8))
        XCTAssertEqual(open.observingPause?.pause, WatchPause(since: nil, until: nil))
        let observing = try JSONDecoder().decode(HubStatus.self, from: Data("{}".utf8))
        XCTAssertNil(observing.observingPause)
    }

    func testAPauseHoldsUntilItsEndAndAPauseWithNoEndHolds() {
        XCTAssertTrue(WatchPause(since: base, until: base.addingTimeInterval(60)).isActive(at: base.addingTimeInterval(59)))
        XCTAssertFalse(WatchPause(since: base, until: base.addingTimeInterval(60)).isActive(at: base.addingTimeInterval(60)))
        XCTAssertTrue(WatchPause(since: base, until: nil).isActive(at: base.addingTimeInterval(86_400)))
    }

    func testThePauseTravelsToTheWatchWithTheStates() {
        let id = WatchRecordingWire.newRecordingID()
        let pause = WatchPause(since: base, until: base.addingTimeInterval(3600))
        let context = WatchRecordingWire.context([id: .onComputer], pause: pause)
        XCTAssertEqual(WatchRecordingWire.pause(fromContext: context), pause)
        XCTAssertEqual(WatchRecordingWire.pause(fromContext: WatchRecordingWire.context([id: .onComputer], pause: WatchPause())), WatchPause())
        XCTAssertNil(WatchRecordingWire.pause(fromContext: WatchRecordingWire.context([id: .onComputer])), "no pause is said by saying nothing")
        XCTAssertEqual(WatchRecordingWire.states(fromContext: context)[id], .onComputer)
    }

    func testWhileTheComputerIsPausedTheWatchNeitherStartsNorResumes() {
        XCTAssertEqual(WatchAutoListen.step(consentSeen: true, microphone: .allowed, stoppedByWearer: false,
                                            isRecording: false, heldByInterruption: false, pausedOnComputer: true), .nothing)
        XCTAssertEqual(WatchAutoListen.step(consentSeen: true, microphone: .allowed, stoppedByWearer: false,
                                            isRecording: true, heldByInterruption: true, pausedOnComputer: true), .nothing)
        XCTAssertEqual(WatchAutoListen.step(consentSeen: true, microphone: .allowed, stoppedByWearer: false,
                                            isRecording: false, heldByInterruption: false, pausedOnComputer: false), .start)
    }

    func testTheWatchsAskTravelsToThePhoneAndBadAsksAreRefused() {
        XCTAssertEqual(WatchPauseRequest(userInfo: WatchPauseRequest.pause(minutes: 60).userInfo), .pause(minutes: 60))
        XCTAssertEqual(WatchPauseRequest(userInfo: WatchPauseRequest.resume.userInfo), .resume)
        XCTAssertNil(WatchPauseRequest(userInfo: [WatchPauseRequest.key: ["minutes": 2]]))
        XCTAssertNil(WatchPauseRequest(userInfo: [WatchPauseRequest.key: ["minutes": 9000]]))
        XCTAssertNil(WatchPauseRequest(userInfo: [WatchRecordingWire.syncMessageKey: true]))
    }

    /// A pause started on the wrist holds there at once, before the computer knows; once the
    /// computer has a pause, its word is the one that counts; and a pause that ran out is none.
    func testThePauseThatHoldsOnTheWatch() {
        let onWatch = WatchPause(since: base, until: base.addingTimeInterval(3600))
        let onComputer = WatchPause(since: base, until: base.addingTimeInterval(900))
        let soon = base.addingTimeInterval(60)
        XCTAssertEqual(WatchPause.effective(computer: nil, watch: onWatch, now: soon), onWatch)
        XCTAssertEqual(WatchPause.effective(computer: onComputer, watch: onWatch, now: soon), onComputer)
        XCTAssertEqual(WatchPause.effective(computer: onComputer, watch: onWatch, now: base.addingTimeInterval(1800)), onWatch,
                       "the computer's pause ran out; the watch's own still holds")
        XCTAssertNil(WatchPause.effective(computer: onComputer, watch: onWatch, now: base.addingTimeInterval(7200)))
        XCTAssertNil(WatchPause.effective(computer: nil, watch: nil, now: soon))
    }

    /// The system does not deliver a context equal to the last one, so each carried ask changes it.
    func testEachCarriedAskMakesTheContextDiffer() {
        let id = WatchRecordingWire.newRecordingID()
        let first = WatchRecordingWire.context([id: .onComputer], pause: WatchPause(), pauseSerial: 1)
        let second = WatchRecordingWire.context([id: .onComputer], pause: WatchPause(), pauseSerial: 2)
        XCTAssertNotEqual(first[WatchRecordingWire.pauseSerialKey] as? Int, second[WatchRecordingWire.pauseSerialKey] as? Int)
        XCTAssertNil(WatchRecordingWire.context([id: .onComputer])[WatchRecordingWire.pauseSerialKey])
        XCTAssertEqual(WatchRecordingWire.pause(fromContext: second), WatchPause())
    }

    /// The watch recorded 10:00 to 10:45 in three parts; the first already reached the phone. A
    /// pause began on the computer at 10:20. What the watch still holds from 10:20 on is deleted
    /// before it leaves the watch; the hour is closed with what is left; an earlier hour and a
    /// part the phone already has are not touched.
    func testTheWatchDeletesWhatItRecordedSinceThePauseBegan() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let library = WatchRecordingLibrary(directory: directory)
        let earlier = WatchRecordingWire.newRecordingID(), hour = WatchRecordingWire.newRecordingID(), after = WatchRecordingWire.newRecordingID()
        func add(_ id: String, _ offsets: [(Double, Double)], finish: Bool) {
            library.begin(id: id, at: base.addingTimeInterval(offsets[0].0))
            for (index, offset) in offsets.enumerated() {
                library.addPart(recordingID: id, startedAt: base.addingTimeInterval(offset.0), durationSeconds: offset.1,
                                last: finish && index == offsets.count - 1)
                try? Data("audio".utf8).write(to: library.fileURL(recordingID: id, part: index))
            }
        }
        add(earlier, [(-3600, 3600)], finish: true)                       // 9:00 to 10:00
        add(hour, [(0, 900), (900, 900), (1800, 900)], finish: true)      // 10:00, 10:15, 10:30
        add(after, [(2700, 300)], finish: true)                           // 10:45 to 10:50
        library.markSent(recordingID: hour, part: 0)

        let dropped = library.dropUnsent(since: base.addingTimeInterval(1200)) // the pause began at 10:20
        XCTAssertEqual(dropped, 3, "10:15 to 10:30 (it runs past 10:20), 10:30 to 10:45 and 10:45 to 10:50")
        let entries = library.all()
        XCTAssertNil(entries.first { $0.id == after }, "a recording made wholly inside the pause is gone")
        let kept = try XCTUnwrap(entries.first { $0.id == hour })
        XCTAssertEqual(kept.parts.map(\.part), [0])
        XCTAssertTrue(kept.finished)
        XCTAssertFalse(FileManager.default.fileExists(atPath: library.fileURL(recordingID: hour, part: 1).path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: library.fileURL(recordingID: hour, part: 2).path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: library.fileURL(recordingID: after, part: 0).path))
        XCTAssertEqual(entries.first { $0.id == earlier }?.parts.count, 1, "the hour before the pause is untouched")
        XCTAssertTrue(FileManager.default.fileExists(atPath: library.fileURL(recordingID: earlier, part: 0).path))
        XCTAssertEqual(library.unsentParts().map(\.info.recordingID), [earlier])
        XCTAssertEqual(library.dropUnsent(since: base.addingTimeInterval(1200)), 0, "nothing is left to drop")
    }
}

// Marked moments (PROTOCOL.md 24.9): a tap while recording is a time that travels with the part.
final class WatchMarksTests: XCTestCase {
    func testMarksAreKeptInsideThePartInOrderWithoutRepeats() {
        XCTAssertEqual(WatchRecordingWire.cleanMarks([120.04, 5, 5.01, -1, 901, .nan, .infinity, 900], durationSeconds: 900), [5, 120, 900])
        XCTAssertNil(WatchRecordingWire.cleanMarks([], durationSeconds: 900))
        XCTAssertNil(WatchRecordingWire.cleanMarks([-3, 9999], durationSeconds: 900))
        XCTAssertEqual(WatchRecordingWire.cleanMarks((0..<200).map(Double.init), durationSeconds: 900)?.count, WatchRecordingWire.maxMarks)
    }

    func testMarksSurviveTheTransferToThePhone() throws {
        let info = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 0, last: true,
                                          startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 600,
                                          marks: [431.5, 12, 9000])
        XCTAssertEqual(info.marks, [12, 431.5])
        let read = try XCTUnwrap(WatchRecordingPartInfo(metadata: info.metadata))
        XCTAssertEqual(read, info)
        // A part without marks says nothing about them, and an older phone's stored part still reads.
        let plain = WatchRecordingPartInfo(recordingID: info.recordingID, part: 1, last: true, startedAt: info.startedAt, durationSeconds: 60)
        XCTAssertNil(plain.metadata["marks"])
        let stored = try JSONEncoder().encode(plain)
        XCTAssertNil(try JSONDecoder().decode(WatchRecordingPartInfo.self, from: stored).marks)
    }

    func testTheWatchKeepsAPartsMarksUntilThePhoneHasThePart() {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let library = WatchRecordingLibrary(directory: directory)
        let id = WatchRecordingWire.newRecordingID()
        library.begin(id: id, at: Date(timeIntervalSince1970: 1_800_000_000))
        library.addPart(recordingID: id, startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 600, last: true, marks: [30, 30, 700])
        XCTAssertEqual(library.unsentParts().first?.info.marks, [30])
        XCTAssertEqual(WatchRecordingLibrary(directory: directory).unsentParts().first?.info.marks, [30], "they survive a relaunch")
        library.markSent(recordingID: id, part: 0)
        XCTAssertNil(library.all().first?.parts.first?.marks, "gone from the watch with the audio")
    }
}

// "Your watch" on the phone's Today: what the watch is doing now and its last hour, one tap from
// the main screen whenever Isoform is on the watch.
final class WatchTodayCardTests: XCTestCase {
    func testTheWatchsStatusInAPersonsWords() {
        func status(paused: Bool = false, onWatch: Bool = false, recording: Bool = false, waiting: Bool = false,
                    denied: Bool = false, stopped: Bool = false) -> WatchLiveStatus {
            WatchLiveStatus.of(paused: paused, pauseStartedOnWatch: onWatch, recording: recording,
                               waitingForMicrophone: waiting, microphoneDenied: denied, stoppedToday: stopped)
        }
        XCTAssertEqual(status(recording: true), .recording)
        XCTAssertEqual(status(recording: true, waiting: true), .waitingForMicrophone)
        XCTAssertEqual(status(paused: true), .pausedOnComputer)
        XCTAssertEqual(status(paused: true, onWatch: true), .pausedOnWatch)
        XCTAssertEqual(status(paused: true, recording: true, stopped: true), .pausedOnComputer, "a pause comes first")
        XCTAssertEqual(status(stopped: true), .stoppedToday)
        XCTAssertEqual(status(denied: true, stopped: true), .microphoneOff)
        XCTAssertEqual(status(), .notRecording)
    }

    func testTheStatusTravelsWithWhatTheWatchStillHolds() {
        let held = [WatchRecordingSummary(id: WatchRecordingWire.newRecordingID(), startedAt: Date(timeIntervalSince1970: 1_800_000_000),
                                          durationSeconds: 60, unsentParts: 1, finished: true)]
        let context = WatchRecordingWire.summaryContext(held, status: .stoppedToday)
        XCTAssertEqual(WatchRecordingWire.status(fromContext: context), .stoppedToday)
        XCTAssertEqual(WatchRecordingWire.summaries(fromContext: context), held)
        // An older watch app says nothing; a word this phone does not know reads as nothing.
        XCTAssertNil(WatchRecordingWire.status(fromContext: WatchRecordingWire.summaryContext(held)))
        var odd = context
        odd[WatchRecordingWire.statusKey] = "levitating"
        XCTAssertNil(WatchRecordingWire.status(fromContext: odd))
    }

    /// Bo, 2026-11-18: "Why the phone app don't have watch screen sometime?" The tab showed only
    /// after the system reported the watch, so it was missing at launch and whenever no report
    /// came. A phone that has had a watch keeps the tab; only "no watch is paired" removes it.
    @MainActor
    func testTheWatchTabStaysForAPhoneThatHasHadAWatch() throws {
        let suite = "watch-tab-\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }

        let first = WatchRecordingsModel(defaults: defaults)
        XCTAssertFalse(first.showsWatchTab, "a phone that never had a watch shows no tab")
        first.connection = .noWatch
        XCTAssertFalse(first.showsWatchTab)
        first.connection = .appMissing
        XCTAssertTrue(first.showsWatchTab)
        first.connection = .ready(reachable: false)
        XCTAssertTrue(first.showsWatchTab)

        // The next launch, before the system has said anything, and if it never says.
        let next = WatchRecordingsModel(defaults: defaults)
        XCTAssertEqual(next.connection, .unknown)
        XCTAssertTrue(next.showsWatchTab, "the tab is there at launch")
        next.connection = .ready(reachable: true)
        XCTAssertTrue(next.showsWatchTab)
        // Only a plain report of no paired watch takes it away, and a later report brings it back.
        next.connection = .noWatch
        XCTAssertFalse(next.showsWatchTab)
        next.connection = .unknown
        XCTAssertTrue(next.showsWatchTab)
    }

    @MainActor
    func testTheCardShowsWheneverIsoformIsOnTheWatch() {
        let model = WatchRecordingsModel()
        XCTAssertFalse(model.showsTodayCard, "no watch is known yet")
        model.connection = .noWatch
        XCTAssertFalse(model.showsTodayCard)
        model.connection = .appMissing
        XCTAssertFalse(model.showsTodayCard, "a watch without Isoform has nothing to show")
        model.connection = .ready(reachable: false)
        XCTAssertTrue(model.showsTodayCard, "Isoform is on the watch: the card shows before any recording arrives")
        model.connection = .unknown
        model.showOnWatch([WatchRecordingSummary(id: WatchRecordingWire.newRecordingID(), startedAt: Date(), durationSeconds: 60,
                                                 unsentParts: 1, finished: true)])
        XCTAssertTrue(model.showsTodayCard, "recordings are known, so the card shows even while the watch's state is not")
    }

    func testTheCardsWords() {
        XCTAssertEqual(WatchRecordingsCopy.status(.recording), String(localized: "Recording"))
        XCTAssertEqual(WatchRecordingsCopy.status(.pausedOnComputer), String(localized: "Paused on your computer"))
        XCTAssertEqual(WatchRecordingsCopy.status(.stoppedToday), String(localized: "Stopped for today"))
        XCTAssertEqual(WatchRecordingsCopy.status(nil), String(localized: "Open Isoform on your watch to start"))
        XCTAssertEqual(WatchRecordingsCopy.lastHour([]), String(localized: "Nothing recorded yet."))
        let row = WatchRecordingRow(id: WatchRecordingWire.newRecordingID(), startedAt: Date(timeIntervalSince1970: 1_800_000_000),
                                    durationSeconds: 48 * 60, stage: .transcribed, names: ["Pricing with Sam"])
        let line = WatchRecordingsCopy.lastHour([row])
        XCTAssertTrue(line.contains("48"), line)
        XCTAssertTrue(line.hasSuffix("Pricing with Sam"), line)
    }
}

// The Watch tab (design's phone-watch v1.0.0): whether the watch listens, and what it heard as
// conversations.
final class WatchTabTests: XCTestCase {
    private let utc = TimeZone(identifier: "UTC")!
    private let english = Locale(identifier: "en_US")

    private func decode(_ json: String) throws -> WatchConversationsAnswer {
        try JSONDecoder().decode(WatchConversationsAnswer.self, from: Data(json.utf8))
    }

    private let answer = """
    {"dayStart":"2026-11-15T08:00:00Z",
     "hours":[{"start":"2026-11-15T17:00:00Z","recordedSeconds":3600,"talkSeconds":1800,"pausedSeconds":0},
              {"start":"2026-11-15T18:00:00Z","recordedSeconds":0,"talkSeconds":0,"pausedSeconds":3600},
              {"start":"2026-11-15T19:00:00Z","recordedSeconds":600,"talkSeconds":120,"pausedSeconds":0,"current":true}],
     "closed":[{"startAt":"2026-11-15T18:00:00Z","endAt":"2026-11-15T19:00:00Z"}],
     "conversations":[
       {"id":"dev:2","name":"Pricing with Sam","startAt":"2026-11-15T22:05:00Z","endAt":"2026-11-15T22:41:00Z","talkSeconds":1310,
        "speakers":["you","speaker_1"],"marks":["2026-11-15T22:31:04Z"],"recordingIds":["a"],"summary":"Sam wants 15%.",
        "decisions":["Annual discount at 15%"],"followups":[{"text":"Send the pricing page","owner":"me","person":"","due":"tonight"},
        {"text":"Check with legal","owner":"other","person":"Sam","due":""}],"place":"Mission District","heartRate":"calm"},
       {"id":"dev:1","startAt":"2026-11-14T17:00:00Z","endAt":"2026-11-14T17:01:00Z","talkSeconds":40,
        "speakers":[],"marks":[],"recordingIds":["b"],"decisions":[],"followups":[]}]}
    """

    func testTheComputersAnswerIsRead() throws {
        let got = try decode(answer)
        XCTAssertEqual(got.hours.count, 3)
        XCTAssertEqual(got.conversations.count, 2)
        let pricing = got.conversations[0]
        XCTAssertEqual(pricing.name, "Pricing with Sam")
        XCTAssertTrue(pricing.hasNote)
        XCTAssertEqual(pricing.mine.map(\.text), ["Send the pricing page"], "only what the wearer said they would do is theirs to answer")
        XCTAssertEqual(pricing.unknownSpeakers, ["speaker_1"])
        XCTAssertFalse(got.conversations[1].hasNote)
        // An older computer's shorter answer still reads.
        XCTAssertEqual(try decode("{}").conversations, [])
    }

    func testARowsWords() throws {
        let got = try decode(answer)
        let meta = WatchTabCopy.meta(got.conversations[0], locale: english, timeZone: utc)
        XCTAssertTrue(meta.contains("36 min"), meta)
        XCTAssertTrue(meta.contains("You, Speaker 1"), meta)
        XCTAssertTrue(meta.hasSuffix("1 mark"), meta)
        XCTAssertTrue(meta.contains("10:05") && meta.contains("10:41"), meta)
        XCTAssertEqual(WatchTabCopy.title(got.conversations[0]), "Pricing with Sam")
        XCTAssertEqual(WatchTabCopy.title(got.conversations[1]), String(localized: "Talk · \(WatchTabCopy.duration(60))"), "no name under two minutes")
        XCTAssertEqual(WatchTabCopy.duration(8220), String(localized: "\(2) h \(17) min"))
        XCTAssertEqual(WatchTabCopy.duration(20), String(localized: "\(1) min"))
        XCTAssertNil(WatchTabCopy.marks(0))
        XCTAssertEqual(WatchTabCopy.speaker("Sam"), "Sam")
    }

    func testTodayAndEarlierDays() throws {
        let got = try decode(answer)
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = utc
        let now = try XCTUnwrap(WatchConversationDates.parse("2026-11-15T23:00:00Z"))
        let split = WatchTabModel.split(got.conversations, now: now, calendar: calendar)
        XCTAssertEqual(split.today.map(\.id), ["dev:2"])
        XCTAssertEqual(split.earlier.count, 1)
        XCTAssertEqual(split.earlier[0].conversations.map(\.id), ["dev:1"])
        XCTAssertEqual(WatchTabCopy.earlierRow(split.earlier[0]), String(localized: "1 conversation") + " · " + WatchTabCopy.duration(40))
        let summary = WatchTabCopy.todaySummary(got, today: split.today)
        XCTAssertTrue(summary.contains("32 min") && summary.hasSuffix("1 mark"), summary)
    }

    func testTheHoursStrip() throws {
        let columns = WatchHourColumn.columns(try decode(answer).hours)
        XCTAssertEqual(columns.map(\.kind), [.talk, .closed, .current])
        XCTAssertEqual(columns[0].height, 0.5, accuracy: 0.001)
        XCTAssertEqual(columns[1].height, 0)
    }

    func testNowSaysWhatTheWatchIsDoing() {
        let until = Date(timeIntervalSince1970: 1_800_000_000)
        func now(_ connection: WatchConnection, _ status: WatchLiveStatus?, until: Date? = nil) -> WatchNow {
            WatchTabCopy.now(connection: connection, status: status, pauseUntil: until, lastHeard: nil, locale: english)
        }
        XCTAssertEqual(now(.ready(reachable: true), .recording).kind, .listening)
        XCTAssertEqual(now(.ready(reachable: false), .recording).kind, .notNear, "out of reach, it keeps listening")
        XCTAssertEqual(now(.ready(reachable: false), .stoppedToday).kind, .offToday, "what it last said still stands when it is away")
        let closed = now(.ready(reachable: true), .pausedOnComputer, until: until)
        XCTAssertEqual(closed.kind, .closedEyes)
        XCTAssertTrue(closed.word.hasPrefix("Closed Eyes until"), closed.word)
        XCTAssertTrue(closed.line.contains(String(localized: "Closed Eyes on your computer")), closed.line)
        XCTAssertFalse(now(.ready(reachable: true), .pausedOnWatch).line.contains(String(localized: "Closed Eyes on your computer")))
        XCTAssertEqual(now(.ready(reachable: true), .stoppedToday).word, String(localized: "Off for today"))
        XCTAssertEqual(now(.ready(reachable: true), .notRecording).kind, .notListening)
        XCTAssertEqual(now(.appMissing, nil).kind, .setUp)
        // A watch that has had Isoform and does not answer as having it: not "put it on your watch".
        let away = now(.appAway, .recording)
        XCTAssertEqual(away.kind, .unknown)
        XCTAssertNotEqual(away.word, now(.appMissing, nil).word)
        XCTAssertFalse(away.line.contains(String(localized: "Isoform isn't on it yet.")), away.line)
        XCTAssertEqual(now(.ready(reachable: true), nil).kind, .unknown)
    }

    /// 2026-11-21: twice right after an update the system said the watch app was not installed,
    /// and the page said "Isoform isn't on it yet" and hid the hours and conversations. A watch
    /// that has had Isoform is "away", keeps its tab, and is never told to set up again.
    func testAWatchThatHadIsoformIsAwayNotMissing() {
        XCTAssertEqual(WatchConnection.from(paired: false, installed: false, reachable: false, hadApp: true), .noWatch)
        XCTAssertEqual(WatchConnection.from(paired: true, installed: false, reachable: false, hadApp: false), .appMissing)
        XCTAssertEqual(WatchConnection.from(paired: true, installed: false, reachable: false, hadApp: true), .appAway)
        XCTAssertEqual(WatchConnection.from(paired: true, installed: true, reachable: true, hadApp: false), .ready(reachable: true))
        XCTAssertEqual(WatchConnection.from(paired: true, installed: true, reachable: false, hadApp: true), .ready(reachable: false))
        XCTAssertTrue(WatchConnection.appAway.hasWatch)
        XCTAssertTrue(WatchConnection.showsTab(.appAway, seenBefore: false))
        XCTAssertFalse(WatchRecordingsCopy.connection(.appAway).isEmpty)
    }

    /// v1.5.0 W2: the system says only that Isoform is not on the watch, not why. Missing and away
    /// say the same three lines, in design's words; "isn't on your watch yet" is never said.
    func testAWatchWithoutIsoformSaysThreeLinesOfWhatThePhoneKnows() {
        let three = ["This phone can't reach Isoform on your watch right now.",
                     "After an update this can happen for a while. It comes back by itself.",
                     "If it doesn't, open the Watch app on this phone and install Isoform under Available Apps."]
        XCTAssertEqual(WatchRecordingsCopy.connection(.appMissing), three)
        XCTAssertEqual(WatchRecordingsCopy.connection(.appAway), three)
        XCTAssertEqual(String(localized: "watch.cantReach"), three[0])
        XCTAssertEqual(String(localized: "watch.afterUpdate"), three[1])
        XCTAssertEqual(String(localized: "watch.installHow"), three[2])
        for state in [WatchConnection.appMissing, .appAway] {
            XCTAssertFalse(WatchRecordingsCopy.connection(state).joined().contains("isn't on your watch yet"))
        }
        // The other states keep their one line, and an unknown pairing says nothing.
        XCTAssertEqual(WatchRecordingsCopy.connection(.noWatch), ["No watch is paired with this phone."])
        XCTAssertEqual(WatchRecordingsCopy.connection(.ready(reachable: true)), ["Your watch is connected."])
        XCTAssertEqual(WatchRecordingsCopy.connection(.ready(reachable: false)), ["Your watch sends recordings when it is near this phone."])
        XCTAssertEqual(WatchRecordingsCopy.connection(.unknown), [])
    }

    /// Heart rate and Place are one choice for the computer, the phone and the watch (24.15).
    /// The watch is told only what someone chose; until then, and with an older computer, its
    /// own switches decide.
    func testTheWatchIsToldOnlyWhatWasChosen() {
        let unset = WatchContextSettingsAnswer(heartRate: WatchNotesSetting(on: false, chosen: false),
                                               place: WatchNotesSetting(on: false, chosen: false))
        XCTAssertTrue(unset.choices.isEmpty, "nobody chose: the watch keeps its own switches")
        let mixed = WatchContextSettingsAnswer(heartRate: WatchNotesSetting(on: true, chosen: true),
                                               place: WatchNotesSetting(on: false, chosen: nil))
        XCTAssertEqual(mixed.choices, WatchContextChoices(heartRate: true, place: nil))
        let off = WatchContextSettingsAnswer(heartRate: WatchNotesSetting(on: false, chosen: true),
                                             place: WatchNotesSetting(on: false, chosen: true))
        XCTAssertEqual(off.choices, WatchContextChoices(heartRate: false, place: false), "an explicit off is told")

        // In the context the phone sends: left out when empty, read back as chosen.
        let plain = WatchRecordingWire.context([:], choices: unset.choices)
        XCTAssertNil(plain[WatchRecordingWire.choicesKey])
        XCTAssertNil(WatchRecordingWire.choices(fromContext: plain))
        XCTAssertNil(WatchRecordingWire.choices(fromContext: WatchRecordingWire.context([:])), "an older phone says nothing")
        let told = WatchRecordingWire.context([:], choices: mixed.choices)
        XCTAssertEqual(WatchRecordingWire.choices(fromContext: told), WatchContextChoices(heartRate: true, place: nil))
        XCTAssertEqual(WatchRecordingWire.choices(fromContext: WatchRecordingWire.context([:], choices: off.choices)),
                       WatchContextChoices(heartRate: false, place: false))

        // A switch changed on the watch travels to the phone as one field.
        let fromWatch = WatchContextChoices(place: true)
        XCTAssertEqual(WatchContextChoices(userInfo: fromWatch.userInfo), fromWatch)
        XCTAssertNil(WatchContextChoices(userInfo: [:]))
        XCTAssertNil(WatchContextChoices(userInfo: [WatchContextChoices.requestKey: [String: Bool]()]))
        XCTAssertNil(WatchPauseRequest(userInfo: fromWatch.userInfo), "it is not mistaken for a pause")
        // Your data names Apple while Place is on, and for a week after it was last on.
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        XCTAssertTrue(WatchTabModel.namesApple(placeOn: true, lastOn: nil, now: now))
        XCTAssertFalse(WatchTabModel.namesApple(placeOn: false, lastOn: nil, now: now))
        XCTAssertFalse(WatchTabModel.namesApple(placeOn: nil, lastOn: nil, now: now), "an older computer: the phone cannot know")
        XCTAssertTrue(WatchTabModel.namesApple(placeOn: false, lastOn: now.addingTimeInterval(-6 * 24 * 3600), now: now))
        XCTAssertFalse(WatchTabModel.namesApple(placeOn: false, lastOn: now.addingTimeInterval(-8 * 24 * 3600), now: now))
        // v1.5.0 W9: the row is design's one line, split at its first " · " into title and sub-line.
        XCTAssertEqual(String(localized: "data.appleArea"), "Apple, to name the area")
        XCTAssertEqual(String(localized: "data.appleAreaSub"),
                       "Where you were, rounded to about a kilometre, once for each new place · sent from your watch, while Place is on and it's listening")
        XCTAssertEqual(String(localized: "watch.set.placeSub"), "Adds the area, like \"Mission District\". Never the address.")
        XCTAssertEqual(String(localized: "watch.set.heartSub"), "Adds \"heart rate raised\" beside what was said. Never the numbers.")
    }

    func testTheAmberLineOnlyWhenItMatters() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        func row(_ stage: WatchRecordingStage, endedAgo minutes: Double) -> WatchRecordingRow {
            WatchRecordingRow(id: WatchRecordingWire.newRecordingID(), startedAt: now.addingTimeInterval(-minutes * 60 - 600),
                              durationSeconds: 600, stage: stage)
        }
        XCTAssertNil(WatchTabCopy.waitingLine([row(.onPhone, endedAgo: 10), row(.transcribed, endedAgo: 300)], now: now),
                     "an hour that has waited ten minutes is not worth a line")
        XCTAssertEqual(WatchTabCopy.waitingLine([row(.onPhone, endedAgo: 45), row(.onWatch, endedAgo: 100)], now: now),
                       String(localized: "\(2) hours are waiting for your computer."))
        XCTAssertEqual(WatchTabCopy.waitingLine([row(.failedToSend, endedAgo: 1), row(.onPhone, endedAgo: 45)], now: now),
                       String(localized: "1 hour couldn't be sent."), "a failure comes first")
    }

    @MainActor
    func testWatchIsATabOfItsOwn() {
        let router = NotificationRouter()
        router.selectedTab = .watch
        XCTAssertEqual(router.selectedTab, .watch, "the Watch tab is a place of its own, not sent back to Today")
        router.selectedTab = .tasks
        XCTAssertEqual(router.selectedTab, .today, "Tasks still lands on Today")
        XCTAssertEqual(RootView.tabTitle(.watch, locale: Locale(identifier: "en_US")), "Watch")
        XCTAssertTrue(WatchConnection.appMissing.hasWatch, "a paired watch without Isoform still gets the tab, for its setup card")
        XCTAssertFalse(WatchConnection.noWatch.hasWatch)
        XCTAssertFalse(WatchConnection.unknown.hasWatch)
    }

    // Names, notes and follow-ups are kept in memory only, and never outlive the computer they came from.
    @MainActor
    func testWhatWasHeardGoesWithThePairing() throws {
        let model = WatchTabModel()
        model.show(try decode(answer))
        model.notesOn = true
        XCTAssertEqual(model.answer?.conversations.count, 2)
        // A computer that does not serve conversations answers nothing: the old list is dropped.
        model.show(nil)
        XCTAssertNil(model.answer)
        XCTAssertFalse(model.served)
        XCTAssertTrue(model.split().earlier.isEmpty)
        model.show(try decode(answer))
        model.clear()
        XCTAssertNil(model.answer, "unpairing forgets what was heard")
        XCTAssertNil(model.notesOn)
        XCTAssertTrue(model.served)
    }

    func testTheConversationRequestsAreWellFormed() {
        XCTAssertEqual(HubRoute.recordingConversations.path, "/recordings/conversations")
        XCTAssertEqual(HubRoute.recordingConversations.method, "GET")
        let one = HubRoute.recordingConversation(id: "0b9d6e2c-3f4a:1800000000000 x")
        XCTAssertEqual(one.path, "/recordings/conversation")
        XCTAssertEqual(one.query, "id=0b9d6e2c-3f4a%3A1800000000000%20x", "the id is escaped, never pasted")
        XCTAssertEqual(HubRoute.watchNotes.path, "/settings/watch-notes")
    }
}

// "Listen on your watch": the phone can turn it off, never on; the watch acts on each off once and
// stays off until the wearer starts it there.
final class WatchListenOffTests: XCTestCase {
    func testTheOffTravelsInThePhonesContext() {
        let since = Date(timeIntervalSince1970: 1_800_000_000)
        let context = WatchRecordingWire.context([:], listenOff: since)
        XCTAssertEqual(WatchRecordingWire.listenOff(fromContext: context), since)
        XCTAssertNil(WatchRecordingWire.listenOff(fromContext: WatchRecordingWire.context([:])), "no off, no word")
        var odd = context
        odd[WatchRecordingWire.listenOffKey] = "now"
        XCTAssertNil(WatchRecordingWire.listenOff(fromContext: odd))
    }

    func testTheWatchActsOnEachOffOnceAndHoldsUntilTheWearerStarts() throws {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "watch-listen-off-\(UUID().uuidString)"))
        let first = Date(timeIntervalSince1970: 1_800_000_000)
        XCTAssertFalse(WatchAutoListen.offFromPhone(in: defaults))
        XCTAssertTrue(WatchAutoListen.takesOff(since: first, in: defaults))
        WatchAutoListen.setOffFromPhone(true, in: defaults)
        XCTAssertTrue(WatchAutoListen.offFromPhone(in: defaults), "off holds, across days, until the wearer starts it")
        // The wearer starts on the watch; the same off, still in the phone's context, must not stop it again.
        WatchAutoListen.setOffFromPhone(false, in: defaults)
        XCTAssertFalse(WatchAutoListen.takesOff(since: first, in: defaults), "an off already acted on is not acted on twice")
        XCTAssertFalse(WatchAutoListen.takesOff(since: first.addingTimeInterval(-60), in: defaults))
        XCTAssertTrue(WatchAutoListen.takesOff(since: first.addingTimeInterval(60), in: defaults), "a later off is a new one")
    }

    func testTheComputersSettingIsRead() throws {
        let off = try JSONDecoder().decode(WatchListeningSetting.self, from: Data(#"{"off":true,"since":"2026-11-15T22:31:00Z"}"#.utf8))
        XCTAssertEqual(off.offSince, WatchConversationDates.parse("2026-11-15T22:31:00Z"))
        let on = try JSONDecoder().decode(WatchListeningSetting.self, from: Data(#"{"off":false}"#.utf8))
        XCTAssertNil(on.offSince)
    }

    @MainActor
    func testUnpairingForgetsTheSwitch() {
        let model = WatchTabModel()
        model.listening = false
        model.clear()
        XCTAssertNil(model.listening, "no switch is shown until a computer says it has the setting")
    }
}

// A conversation's page: its words, who spoke, and the guess to judge.
final class WatchConversationPageTests: XCTestCase {
    private let utc = TimeZone(identifier: "UTC")!
    private let english = Locale(identifier: "en_US")

    private func conversation(_ extra: String = "") throws -> WatchConversation {
        let json = """
        {"id":"dev:2","name":"Pricing with Sam","startAt":"2026-11-15T22:05:00Z","endAt":"2026-11-15T22:41:00Z","talkSeconds":1310,
         "speakers":["you","speaker_1","speaker_2"],"marks":["2026-11-15T22:31:04Z"],"place":"Mission District","heartRate":"calm"\(extra)}
        """
        return try JSONDecoder().decode(WatchConversation.self, from: Data(json.utf8))
    }

    func testNamesAndTheGuessAreRead() throws {
        let plain = try conversation()
        XCTAssertEqual(plain.speakerNames, [:])
        XCTAssertNil(plain.speakerGuess)
        XCTAssertEqual(plain.unknownSpeakers, ["speaker_1", "speaker_2"])
        let named = try conversation("""
        ,"itemIds":["followup:abc"],"speakerNames":{"speaker_1":"Sam Li"},
         "speakerGuess":{"speaker":"speaker_2","name":"Priya Shah","reason":"calendar","eventStartAt":1794780000000}
        """)
        XCTAssertEqual(named.itemIds, ["followup:abc"])
        XCTAssertEqual(named.unknownSpeakers, ["speaker_2"], "a named speaker is no longer asked about")
        XCTAssertEqual(WatchConversationCopy.speaker("speaker_1", in: named), "Sam Li")
        XCTAssertEqual(WatchConversationCopy.speaker("speaker_2", in: named), String(localized: "Speaker \(2)"))
        XCTAssertEqual(WatchConversationCopy.speaker("you", in: named), String(localized: "You"))
        XCTAssertTrue(WatchTabCopy.meta(named, locale: english, timeZone: utc).contains("You, Sam Li, Speaker 2"))
        let guess = WatchConversationCopy.guess(try XCTUnwrap(named.speakerGuess), locale: english, timeZone: utc)
        XCTAssertTrue(guess.hasPrefix("Speaker 2 might be Priya Shah: your "), guess)
        XCTAssertTrue(guess.hasSuffix("meeting was with them."), guess)
    }

    func testTheSubtitleAndTheNoteLine() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = utc
        let now = try XCTUnwrap(WatchConversationDates.parse("2026-11-15T23:00:00Z"))
        let subtitle = WatchConversationCopy.subtitle(try conversation(), now: now, calendar: calendar, locale: english, timeZone: utc)
        let lines = subtitle.components(separatedBy: "\n")
        XCTAssertEqual(lines.count, 2, subtitle)
        XCTAssertTrue(lines[0].hasPrefix(String(localized: "Today")), subtitle)
        XCTAssertTrue(lines[0].contains("36 min"), subtitle)
        XCTAssertEqual(lines[1], "at Mission District · heart rate calm")
        XCTAssertNil(WatchConversationCopy.heartRate("104"), "only the three words are shown, never a number")
        XCTAssertEqual(WatchConversationCopy.noNote(try conversation()), String(localized: "No note for this conversation yet."))
        let short = try JSONDecoder().decode(WatchConversation.self, from: Data("""
        {"id":"dev:1","startAt":"2026-11-15T22:05:00Z","endAt":"2026-11-15T22:07:00Z"}
        """.utf8))
        XCTAssertEqual(WatchConversationCopy.noNote(short), String(localized: "Notes come for conversations over 5 minutes."))
    }

    func testAMarkedLineIsRead() throws {
        let detail = try JSONDecoder().decode(WatchConversationDetail.self, from: Data("""
        {"conversation":{"id":"dev:2","startAt":"2026-11-15T22:05:00Z","endAt":"2026-11-15T22:41:00Z"},
         "lines":[{"at":"2026-11-15T22:05:00Z","speaker":"speaker_1","text":"What is the number"},
                  {"at":"2026-11-15T22:31:00Z","speaker":"you","text":"Fifteen percent","marked":true}],"truncated":true}
        """.utf8))
        XCTAssertEqual(detail.lines.map { $0.marked == true }, [false, true])
        XCTAssertEqual(detail.truncated, true)
    }
}

// The phone lists the watch's hours under their day (watch v1.1.0): a day, then "10 AM · 48 min".
final class WatchRecordingDayTests: XCTestCase {
    private var calendar: Calendar = {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        return calendar
    }()

    private func at(_ day: Int, _ hour: Int, _ minute: Int = 0) -> Date {
        calendar.date(from: DateComponents(year: 2026, month: 11, day: day, hour: hour, minute: minute))!
    }

    private func row(_ date: Date, seconds: Double = 3600) -> WatchRecordingRow {
        WatchRecordingRow(id: WatchRecordingWire.newRecordingID(), startedAt: date, durationSeconds: seconds, stage: .transcribed)
    }

    func testHoursAreListedUnderTheirDayNewestFirst() {
        let rows = [row(at(14, 9, 40), seconds: 1200), row(at(15, 8)), row(at(14, 23)), row(at(15, 10)), row(at(13, 17))]
        let days = WatchRecordingDay.days(rows, calendar: calendar)
        XCTAssertEqual(days.map(\.day), [at(15, 0), at(14, 0), at(13, 0)])
        XCTAssertEqual(days.map { $0.rows.map(\.startedAt) }, [[at(15, 10), at(15, 8)], [at(14, 23), at(14, 9, 40)], [at(13, 17)]])
        XCTAssertTrue(WatchRecordingDay.days([], calendar: calendar).isEmpty)
    }

    func testADayIsNamedTodayYesterdayOrByItsDate() {
        let now = at(15, 11)
        XCTAssertEqual(WatchRecordingsCopy.dayTitle(at(15, 0), now: now, calendar: calendar), String(localized: "Today"))
        XCTAssertEqual(WatchRecordingsCopy.dayTitle(at(14, 0), now: now, calendar: calendar), String(localized: "Yesterday"))
        let earlier = WatchRecordingsCopy.dayTitle(at(12, 0), now: now, calendar: calendar)
        XCTAssertTrue(earlier.contains("12"), earlier)
        XCTAssertNotEqual(earlier, String(localized: "Today"))
    }

    func testAnHourShowsWholeMinutesNotAClock() {
        XCTAssertFalse(WatchRecordingsCopy.minutes(48 * 60).contains(":"))
        XCTAssertTrue(WatchRecordingsCopy.minutes(48 * 60).contains("48"))
        XCTAssertTrue(WatchRecordingsCopy.minutes(5).contains("1"))
    }
}

final class WatchRecordingLibraryTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    }

    override func tearDown() {
        try? FileManager.default.removeItem(at: directory)
    }

    private func writeAudio(_ library: WatchRecordingLibrary, _ id: String, _ part: Int) throws {
        try Data("audio".utf8).write(to: library.fileURL(recordingID: id, part: part))
    }

    func testARecordingTravelsFromTheWatchToIsoform() throws {
        let library = WatchRecordingLibrary(directory: directory)
        let id = WatchRecordingWire.newRecordingID()
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        library.begin(id: id, at: start)
        XCTAssertEqual(library.all().first?.display, .recording)

        try writeAudio(library, id, 0)
        let first = try XCTUnwrap(library.addPart(recordingID: id, startedAt: start, durationSeconds: 900, last: false))
        XCTAssertEqual(first.part, 0)
        XCTAssertFalse(first.last)
        try writeAudio(library, id, 1)
        let second = try XCTUnwrap(library.addPart(recordingID: id, startedAt: start.addingTimeInterval(900),
                                                   durationSeconds: 30, last: true))
        XCTAssertEqual(second.part, 1)
        XCTAssertEqual(library.all().first?.display, .onWatch)
        XCTAssertEqual(library.all().first?.durationSeconds, 930)
        XCTAssertEqual(library.unsentParts().map(\.info.part), [0, 1])
        XCTAssertNil(library.addPart(recordingID: id, startedAt: start, durationSeconds: 5, last: true),
                     "a stopped recording takes no more parts")

        library.markSent(recordingID: id, part: 0)
        XCTAssertFalse(FileManager.default.fileExists(atPath: library.fileURL(recordingID: id, part: 0).path),
                       "audio the phone holds leaves the watch")
        XCTAssertEqual(library.all().first?.display, .sending(sent: 1, total: 2))
        library.markSent(recordingID: id, part: 1)
        XCTAssertEqual(library.all().first?.display, .remote(.onPhone))
        XCTAssertTrue(library.unsentParts().isEmpty)

        library.apply(states: [id: .transcribed])
        XCTAssertEqual(library.all().first?.display, .remote(.transcribed))
        library.apply(states: [id: .onPhone])
        XCTAssertEqual(library.all().first?.display, .remote(.transcribed), "a state never moves backwards")

        // The index survives a relaunch.
        XCTAssertEqual(WatchRecordingLibrary(directory: directory).all().first?.display, .remote(.transcribed))
    }

    func testAPartsContextLeavesTheWatchWithItsAudio() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let library = WatchRecordingLibrary(directory: directory)
        let id = WatchRecordingWire.newRecordingID()
        library.begin(id: id, at: Date())
        let context = try XCTUnwrap(WatchRecordingContext(heartRate: .init(readings: [90, 110]), location: nil))
        try Data("audio".utf8).write(to: library.fileURL(recordingID: id, part: 0))
        let info = try XCTUnwrap(library.addPart(recordingID: id, startedAt: Date(), durationSeconds: 30, last: true,
                                                 context: context))
        XCTAssertEqual(info.context, context)
        // A resend after a restart still carries it.
        XCTAssertEqual(WatchRecordingLibrary(directory: directory).unsentParts().first?.info.context, context)

        library.markSent(recordingID: id, part: 0)
        XCTAssertNil(library.all().first?.parts.first?.context)
        let index = try String(contentsOf: directory.appendingPathComponent("index.json"), encoding: .utf8)
        XCTAssertFalse(index.contains("heartRate"), "the heart rate is gone from the watch once the phone holds the part")
    }

    func testADeleteFromThePhoneRemovesTheRecordingAndItsAudioFromTheWatch() throws {
        let library = WatchRecordingLibrary(directory: directory)
        let gone = WatchRecordingWire.newRecordingID(), kept = WatchRecordingWire.newRecordingID()
        let running = WatchRecordingWire.newRecordingID()
        for id in [gone, kept] {
            library.begin(id: id, at: Date(timeIntervalSince1970: 1_800_000_000))
            try writeAudio(library, id, 0)
            library.addPart(recordingID: id, startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 60, last: true)
        }
        library.begin(id: running, at: Date(timeIntervalSince1970: 1_800_000_100))
        XCTAssertEqual(library.summaries().map(\.id), [running, gone, kept].sorted { a, b in
            library.all().firstIndex { $0.id == a }! < library.all().firstIndex { $0.id == b }!
        })
        XCTAssertEqual(library.summaries().first { $0.id == gone }?.unsentParts, 1)

        library.apply(states: [gone: .deleted, running: .deleted])
        XCTAssertFalse(FileManager.default.fileExists(atPath: library.fileURL(recordingID: gone, part: 0).path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: library.fileURL(recordingID: kept, part: 0).path))
        // A recording still running is not removed under the recorder.
        XCTAssertEqual(Set(library.all().map(\.id)), [kept, running])
        XCTAssertNil(library.all().first { $0.id == running }?.remote)
        // Once the phone holds a part the watch no longer lists the recording as held.
        library.markSent(recordingID: kept, part: 0)
        XCTAssertEqual(library.summaries().map(\.id), [running])
    }

    func testARecordingCutShortByAKillIsClosedAndKept() throws {
        let library = WatchRecordingLibrary(directory: directory)
        let withAudio = WatchRecordingWire.newRecordingID()
        let empty = WatchRecordingWire.newRecordingID()
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        library.begin(id: withAudio, at: start)
        try writeAudio(library, withAudio, 0)
        library.addPart(recordingID: withAudio, startedAt: start, durationSeconds: 900, last: false)
        try writeAudio(library, withAudio, 1)   // was being written when the app was killed
        library.begin(id: empty, at: start.addingTimeInterval(5))

        let reopened = WatchRecordingLibrary(directory: directory)
        let recovered = reopened.recoverInterrupted { _ in 42 }
        XCTAssertEqual(recovered.map(\.part), [1])
        XCTAssertEqual(recovered.first?.last, true)
        XCTAssertEqual(recovered.first?.startedAt, start.addingTimeInterval(900))
        XCTAssertEqual(reopened.all().map(\.id), [withAudio], "a recording with no audio is dropped")
        XCTAssertEqual(reopened.all().first?.finished, true)
    }

    func testStoppingBetweenPartsMakesTheNewestUnsentPartTheLast() throws {
        let library = WatchRecordingLibrary(directory: directory)
        let id = WatchRecordingWire.newRecordingID()
        library.begin(id: id, at: Date())
        try writeAudio(library, id, 0)
        library.addPart(recordingID: id, startedAt: Date(), durationSeconds: 900, last: false)
        library.close(recordingID: id)
        XCTAssertEqual(library.unsentParts().map(\.info.last), [true])
        XCTAssertEqual(library.all().first?.finished, true)

        // Stopped before anything was kept: no row is left behind.
        let empty = WatchRecordingWire.newRecordingID()
        library.begin(id: empty, at: Date())
        library.close(recordingID: empty)
        XCTAssertEqual(library.all().map(\.id), [id])
    }

    func testTheSixtyFourthPartClosesTheRecording() throws {
        let library = WatchRecordingLibrary(directory: directory)
        let id = WatchRecordingWire.newRecordingID()
        library.begin(id: id, at: Date())
        var last: WatchRecordingPartInfo?
        for _ in 0..<WatchRecordingWire.maxParts {
            last = library.addPart(recordingID: id, startedAt: Date(), durationSeconds: 1, last: false)
        }
        XCTAssertEqual(last?.part, 63)
        XCTAssertEqual(last?.last, true)
        XCTAssertEqual(library.all().first?.finished, true)
    }
}

/// Counts how often the forwarder had something new for the watch.
private final class Told: @unchecked Sendable {
    private let lock = NSLock()
    private var value = 0
    var count: Int { lock.withLock { value } }
    func bump() { lock.withLock { value += 1 } }
}

/// What the forwarder last told the watch about names.
private final class Pushed: @unchecked Sendable {
    private let lock = NSLock()
    private var value: [String: [String]] = [:]
    var names: [String: [String]] { lock.withLock { value } }
    func set(_ names: [String: [String]]) { lock.withLock { value = names } }
}

/// Set when a hand-over waited in vain.
private final class Late: @unchecked Sendable {
    private let lock = NSLock()
    private var late = false
    var value: Bool { lock.withLock { late } }
    func set() { lock.withLock { late = true } }
}

private actor StubRecordingHub: RecordingHubAPI {
    var available = true
    var posted: [RecordingPartRequest] = []
    var postError: Error?
    var answerState = "received"
    var listed: [RecordingView] = []

    func set(available: Bool) { self.available = available }
    func set(postError: Error?) { self.postError = postError }
    func set(answerState: String) { self.answerState = answerState }
    func set(listed: [RecordingView]) { self.listed = listed }

    var takesContext = false
    func set(takesContext: Bool) { self.takesContext = takesContext }
    var availableError: Error?
    func set(availableError: Error?) { self.availableError = availableError }
    /// How often the computer was asked what it takes.
    var asked = 0
    func recordingAbilities() async throws -> RecordingAbilities {
        asked += 1
        guard try await recordingsAvailable() else { return RecordingAbilities() }
        return RecordingAbilities(recordings: true, context: takesContext, marks: takesMarks, relay: takesRelay, ahead: takesAhead,
                                  inWake: takesInWake ? true : nil)
    }
    var takesInWake = false
    func set(takesInWake: Bool) { self.takesInWake = takesInWake }
    func recordingsAvailable() async throws -> Bool {
        if let availableError { throw availableError }
        return available
    }
    func recordingContextAvailable() async throws -> Bool { takesContext }
    /// Parts whose request fails with an answer of the relay's.
    var failingParts: Set<Int> = []
    func set(failingParts: Set<Int>) { self.failingParts = failingParts }
    /// Part requests that reached the computer's door, answered or not.
    var attempts = 0
    /// Parts whose request gets no answer.
    var unansweredParts: Set<Int> = []
    func set(unansweredParts: Set<Int>) { self.unansweredParts = unansweredParts }
    func postRecording(_ request: RecordingPartRequest) async throws -> RecordingView {
        attempts += 1
        if let postError { throw postError }
        if unansweredParts.contains(request.part) { throw RelayClientError.timedOut }
        if failingParts.contains(request.part) { throw RelayClientError.server(status: 502, message: "") }
        posted.append(request)
        return RecordingView(id: request.recordingId, state: answerState)
    }
    func recordingStates(ids: [String]) async throws -> [RecordingView] { listed.filter { ids.contains($0.id) } }

    var controlsPause = false
    var pausedMinutes: [Int] = []
    var resumes = 0
    var resumeError: Error?
    func set(controlsPause: Bool) { self.controlsPause = controlsPause }
    func set(resumeError: Error?) { self.resumeError = resumeError }
    func observingPauseControlAvailable() async throws -> Bool { controlsPause }
    func pauseObserving(minutes: Int) async throws {
        pausedMinutes.append(minutes)
        pauseSaid = WatchPause(since: Date(timeIntervalSince1970: 1_800_000_000), until: Date(timeIntervalSince1970: 1_800_000_000 + Double(minutes) * 60))
    }
    func resumeObserving() async throws {
        resumes += 1
        if let resumeError { throw resumeError }
        pauseSaid = nil
    }

    var takesMarks = false
    func set(takesMarks: Bool) { self.takesMarks = takesMarks }
    func recordingMarksAvailable() async throws -> Bool { takesMarks }

    var takesRelay = false
    func set(takesRelay: Bool) { self.takesRelay = takesRelay }
    func recordingRelayAvailable() async throws -> Bool { takesRelay }
    var takesAhead = false
    func set(takesAhead: Bool) { self.takesAhead = takesAhead }
    func recordingAheadAvailable() async throws -> Bool { takesAhead }
    /// Parts told ahead of their upload, and what the computer answers to them.
    var told: [RecordingPartRequest] = []
    var aheadState = "expected"
    func set(aheadState: String) { self.aheadState = aheadState }
    /// Runs inside every tell before it answers: an answer that is slow to come back.
    var whileTelling: (@Sendable () async -> Void)?
    func set(whileTelling: (@Sendable () async -> Void)?) { self.whileTelling = whileTelling }
    func tellAhead(_ request: RecordingPartRequest) async throws -> RecordingView {
        if let postError { throw postError }
        await whileTelling?()
        told.append(request)
        return RecordingView(id: request.recordingId, state: aheadState)
    }

    /// Pass records the phone told, one array per telling.
    var takesPasses = false
    func set(takesPasses: Bool) { self.takesPasses = takesPasses }
    var passesTold: [[RecordingPassRecord]] = []
    func reportPasses(_ passes: [RecordingPassRecord]) async throws -> Bool {
        guard takesPasses else { return false }
        passesTold.append(passes)
        return true
    }

    var pauseSaid: WatchPause?
    var pauseError: Error?
    func set(pause: WatchPause?) { pauseSaid = pause }
    func set(pauseError: Error?) { self.pauseError = pauseError }
    func observingPause() async throws -> WatchPause? {
        if let pauseError { throw pauseError }
        return pauseSaid
    }

    var detail = true
    var deleted: [String] = []
    var deleteError: Error?
    func set(detail: Bool) { self.detail = detail }
    func set(deleteError: Error?) { self.deleteError = deleteError }
    func recordingDetailAvailable() async throws -> Bool { detail }
    func recordingTranscript(id: String) async throws -> RecordingTranscript {
        RecordingTranscript(id: id, lines: [.init(at: "2026-11-13T09:00:02Z", speaker: "you", text: "Call Sam")])
    }
    func deleteRecording(id: String) async throws -> RecordingView {
        if let deleteError { throw deleteError }
        deleted.append(id)
        return RecordingView(id: id, state: "deleted")
    }
}

private final class StubUploads: RecordingUploadTransport, RecordingUploadAhead, @unchecked Sendable {
    /// Parts left with the system before the computer was asked: (host, upload id).
    private(set) var began: [(String, String)] = []
    var takesAhead = true
    /// Runs inside every hand-over before it ends: a slow hand-over.
    var whileBeginning: (@Sendable () async -> Void)?
    func begin(_ credentials: RelayCredentials, id: String, sealed: Data) async -> Bool {
        await beginKnowing(credentials, id: id, sealed: sealed) != .no
    }
    /// Upload ids the relay holds whole although the phone was never told.
    var wholeOnTheRelay: Set<String> = []
    /// The system had held every part for minutes: each hand-over starts it afresh.
    var startsAfresh = false
    /// The relay does not answer: nothing is handed over.
    var handsNothing = false
    /// How each hand-over was asked for: with the send in a wake allowed, or not.
    private(set) var inWakeAllowed: [Bool] = []
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data) async -> RecordingBegun {
        await beginKnowing(credentials, id: id, sealed: sealed, inWake: false)
    }
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool) async -> RecordingBegun {
        await beginKnowing(credentials, id: id, sealed: sealed, inWake: inWake, taken: nil)
    }
    /// The relay holds nothing of any part: the computer's word is waited for, as on the phone.
    var relayHoldsNothing = false
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool,
                      taken: (@Sendable () async -> Bool)?) async -> RecordingBegun {
        guard takesAhead else { return .no }
        await whileBeginning?()
        if relayHoldsNothing, let taken, await taken() { return .no }
        return lockAhead.withLock {
            inWakeAllowed.append(inWake)
            if handsNothing { return .no }
            began.append((credentials.origin.host ?? "", id))
            if inWake { return .sentInWake }
            if wholeOnTheRelay.contains(id) { return .whole }
            return startsAfresh ? .handedAgain : .handed
        }
    }
    private let lockAhead = NSLock()

    /// What the relay says it holds of this phone's uploads; nil for a relay that cannot say.
    var relayHolds: Set<String>?
    /// How often the relay was asked what it holds.
    private(set) var askedOnRelay = 0
    func onRelay(_ credentials: RelayCredentials) async -> Set<String>? {
        lockAhead.withLock {
            askedOnRelay += 1
            return relayHolds
        }
    }
    /// The relay has no place for an upload it holds nothing of: it answers 429 to it.
    var fullForNew = false

    private let lock = NSLock()
    private var stored: [String: Data] = [:]
    var error: Error?

    /// Uploads that arrive at the relay although the phone is told they failed.
    var arrivesAnyway = false
    /// Ids the phone handed over, in order, repeats included.
    private(set) var handed: [String] = []
    /// The relay each try went to, by host, in order.
    private(set) var hosts: [String] = []
    /// Relays that fail, by host: nothing sent there is stored.
    var down: [String: Error] = [:]
    /// What each relay holds, by host.
    private(set) var storedAt: [String: Set<String>] = [:]

    /// Runs inside every upload before it is stored.
    var whileStoring: (@Sendable () async -> Void)?
    func putUpload(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        await whileStoring?()
        let host = credentials.origin.host ?? ""
        let failure = lock.withLock { () -> Error? in
            hosts.append(host)
            if fullForNew, relayHolds?.contains(id) != true {
                handed.append(id)
                return RelayClientError.rateLimited(retryAfter: 600)
            }
            if let failure = down[host] { handed.append(id); return failure }
            storedAt[host, default: []].insert(id)
            return nil
        }
        if let failure { throw failure }
        let held = lock.withLock { () -> Bool in
            handed.append(id)
            if let before = stored[id] { precondition(before == sealed, "the same id must carry the same bytes") }
            let held = stored[id] != nil
            if error == nil || arrivesAnyway { stored[id] = sealed }
            return held
        }
        if let error { throw error }
        if held { throw RelayClientError.conflict }
    }
    func body(_ id: String) -> Data? { lock.withLock { stored[id] } }
    var count: Int { lock.withLock { stored.count } }
}

final class WatchRecordingForwarderTests: XCTestCase {
    private var directory: URL!
    private let hubID = "6f1c2c9e-3b1a-4c55-9f4e-2a7b8c9d0e1f"
    private let deviceID = "0b1c2d3e-4f50-4a61-8b72-9c8d7e6f5a4b"

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDown() {
        try? FileManager.default.removeItem(at: directory)
    }

    private func context() -> SessionContext {
        SessionContext(credentials: RelayCredentials(origin: URL(string: "https://relay.example")!, bearer: "b"),
                       hubID: hubID, deviceID: deviceID,
                       keys: RelayCrypto.SessionKeys(d2h: RelayCrypto.randomBytes(32), h2d: RelayCrypto.randomBytes(32)))
    }

    /// The forwarder's clock in these tests; a test moves it to let a refused part's wait pass.
    private let clock = TestClock()

    private func make(_ hub: StubRecordingHub, _ uploads: StubUploads) -> (WatchRecordingInbox, WatchRecordingForwarder) {
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("inbox"))
        let context = context()
        let clock = clock
        return (inbox, WatchRecordingForwarder(inbox: inbox, hub: hub, relay: uploads, now: { clock.now }, session: { context }))
    }

    /// Passes until the part was refused `times` times, letting each wait pass.
    private func refuse(_ forwarder: WatchRecordingForwarder, times: Int) async {
        for _ in 0..<times {
            _ = await forwarder.drain()
            clock.advance(WatchRecordingInbox.retryDelay(refusals: .max) + 1)
        }
    }

    private func deliver(_ inbox: WatchRecordingInbox, _ forwarder: WatchRecordingForwarder, id: String, part: Int,
                         last: Bool, audio: String, at base: Double = 1_800_000_000) async throws -> WatchRecordingPartInfo {
        let source = directory.appendingPathComponent(UUID().uuidString)
        try Data(audio.utf8).write(to: source)
        let info = WatchRecordingPartInfo(recordingID: id, part: part, last: last,
                                          startedAt: Date(timeIntervalSince1970: base + Double(part) * 900),
                                          durationSeconds: 900, model: "Apple Watch")
        try inbox.accept(file: source, info: info)
        await forwarder.received(info)
        return info
    }

    func testAPartIsSealedUploadedAndRemovedOnceTheComputerHoldsIt() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "the audio")
        let onPhone = await forwarder.currentStates()
        XCTAssertEqual(onPhone[id], .onPhone)

        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        let posted = await hub.posted
        let request = try XCTUnwrap(posted.first)
        XCTAssertEqual(request.recordingId, id)
        XCTAssertEqual(request.part, 0)
        XCTAssertTrue(request.last)
        XCTAssertEqual(request.source, "watch")
        XCTAssertEqual(request.mime, "audio/mp4")
        XCTAssertEqual(request.bytes, 9)
        XCTAssertEqual(request.sha256, RelayCrypto.sha256Hex(Data("the audio".utf8)))
        XCTAssertEqual(request.startedAt, "2027-01-15T08:00:00Z")
        XCTAssertEqual(request.idempotencyKey, "rec-\(id)-0")

        // What reached the relay is ciphertext that opens only with the key in the sealed request.
        let sealed = try XCTUnwrap(uploads.body(request.upload.id))
        XCTAssertNil(sealed.range(of: Data("the audio".utf8)))
        let opened = try RelayCrypto.unseal(sealed, key: RelayCrypto.base64URLDecode(request.upload.key),
                                            nonce: RelayCrypto.base64URLDecode(request.upload.nonce),
                                            aad: "yansu-upload-v1|\(hubID)|\(deviceID)|\(request.upload.id)")
        XCTAssertEqual(opened, Data("the audio".utf8))

        XCTAssertFalse(FileManager.default.fileExists(atPath: inbox.audioURL(info).path),
                       "audio the computer holds leaves the phone")
        XCTAssertTrue(inbox.pending().isEmpty)
        let states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .onComputer)
    }

    func testAnUnreachableComputerKeepsThePartOnThePhone() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")

        // The relay cannot be reached: nothing is uploaded, the part stays.
        uploads.error = RelayClientError.offline
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))

        uploads.error = nil
        await hub.set(postError: HubRequestError.noAnswer)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))

        // Paused observing on the computer answers 409: also kept.
        await hub.set(postError: HubRequestError.conflict(message: "observing is paused on this computer"))
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(inbox.pending().count, 1)

        // The copy already on the relay is reused, so the relay being offline now does not matter.
        uploads.error = RelayClientError.offline
        await hub.set(postError: nil)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
        XCTAssertEqual(uploads.count, 1)
    }

    func testContextGoesOnlyToAComputerThatTakesItAndLeavesThePhoneWithThePart() async throws {
        let context = try XCTUnwrap(WatchRecordingContext(
            heartRate: .init(readings: [88, 96]), location: .init(latitude: 48.8566, longitude: 2.3522, place: "Le Marais")))
        for takes in [false, true] {
            let hub = StubRecordingHub()
            await hub.set(takesContext: takes)
            let uploads = StubUploads()
            let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("inbox-\(takes)"))
            let session = self.context()
            let forwarder = WatchRecordingForwarder(inbox: inbox, hub: hub, relay: uploads, session: { session })
            let source = directory.appendingPathComponent(UUID().uuidString)
            try Data("audio".utf8).write(to: source)
            let info = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 0, last: true,
                                              startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 60,
                                              context: context)
            try inbox.accept(file: source, info: info)
            await forwarder.received(info)
            let outcome = await forwarder.drain()
            XCTAssertEqual(outcome, .drained)

            let posted = await hub.posted
            let request = try XCTUnwrap(posted.first)
            XCTAssertEqual(request.context, takes ? context : nil)
            let body = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
            XCTAssertEqual(body.contains("\"context\""), takes, "an older computer refuses fields it does not know")
            if takes {
                XCTAssertTrue(body.contains("\"heartRate\""))
                XCTAssertTrue(body.contains("\"place\":\"Le Marais\""))
                XCTAssertFalse(body.contains("48.8566"), "only the coarse position is sent")
            }
            // Nothing about the part stays on the phone once the computer holds it.
            let left = try FileManager.default.contentsOfDirectory(atPath: inbox.directory.path)
            XCTAssertEqual(left.filter { $0 != "states.json" }, [])
        }
    }

    func testAnOlderComputerLeavesRecordingsOnThePhone() async throws {
        let hub = StubRecordingHub()
        await hub.set(available: false)
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "audio")
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .unsupported)
        XCTAssertEqual(uploads.count, 0, "nothing is uploaded for a computer that cannot take it")
        XCTAssertEqual(inbox.pending().count, 1)
    }

    func testPartsGoInOrderAndTheStateFollowsTheLastOne() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 1, last: true, audio: "two")
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: false, audio: "one")
        await hub.set(listed: [RecordingView(id: id, state: "transcribed")])
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        let parts = await hub.posted.map(\.part)
        XCTAssertEqual(parts, [0, 1])
        let states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .transcribed, "the computer's list moves an arrived recording on")
    }

    /// The watch asks to pause; the phone asks the computer, reads what holds and tells the
    /// watch. A computer that does not take the ask is not asked. A resume the computer refuses
    /// (the pause was made there) leaves the pause, and the watch is still told.
    func testThePhoneCarriesTheWatchsPauseToTheComputer() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (_, forwarder) = make(hub, uploads)
        let told = Told()
        await forwarder.onStatesChanged { _, _ in told.bump() }

        var asked = await forwarder.carry(.pause(minutes: 60))
        XCTAssertFalse(asked, "an older computer is not asked")
        var minutes = await hub.pausedMinutes
        XCTAssertEqual(minutes, [])
        XCTAssertEqual(told.count, 1, "the watch still hears that nothing holds on the computer")

        await hub.set(controlsPause: true)
        asked = await forwarder.carry(.pause(minutes: 60))
        XCTAssertTrue(asked)
        minutes = await hub.pausedMinutes
        XCTAssertEqual(minutes, [60])
        var pause = await forwarder.currentPause()
        XCTAssertEqual(pause?.until?.timeIntervalSince(pause?.since ?? Date()), 3600)

        await hub.set(resumeError: HubRequestError.conflict(message: "this pause was made on the computer; resume it there"))
        let before = told.count
        await forwarder.carry(.resume)
        pause = await forwarder.currentPause()
        XCTAssertNotNil(pause, "a pause the computer keeps is not ended on the phone's say-so")
        XCTAssertGreaterThan(told.count, before, "the watch is told the pause still holds")

        await hub.set(resumeError: nil)
        await forwarder.carry(.resume)
        pause = await forwarder.currentPause()
        XCTAssertNil(pause)
        let serial = await forwarder.pauseSerial
        XCTAssertEqual(serial, 4)
    }

    /// Marks go to a computer that says it takes them, and are left out for one that does not
    /// (it would refuse the whole part); the audio goes either way.
    func testMarksAreSentOnlyToAComputerThatTakesThem() async throws {
        for takes in [false, true] {
            let hub = StubRecordingHub()
            await hub.set(takesMarks: takes)
            let uploads = StubUploads()
            let (inbox, forwarder) = make(hub, uploads)
            let info = WatchRecordingPartInfo(recordingID: WatchRecordingWire.newRecordingID(), part: 0, last: true,
                                              startedAt: Date(timeIntervalSince1970: 1_800_000_000), durationSeconds: 600, marks: [12, 431.5])
            let source = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".m4a")
            try Data("audio".utf8).write(to: source)
            try inbox.accept(file: source, info: info)
            await forwarder.received(info)
            let outcome = await forwarder.drain()
            XCTAssertEqual(outcome, .drained)
            let posted = await hub.posted
            XCTAssertEqual(posted.count, 1)
            XCTAssertEqual(posted.first?.marks, takes ? [12, 431.5] : nil)
        }
    }

    /// The phone learns of a pause on the computer, tells the watch, keeps the last word when the
    /// computer cannot be reached, and tells the watch again when the pause is over.
    func testThePhoneTellsTheWatchWhenTheComputerPausesAndWhenItResumes() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (_, forwarder) = make(hub, uploads)
        let told = Told()
        await forwarder.onStatesChanged { _, _ in told.bump() }
        await forwarder.refreshPause()
        var pause = await forwarder.currentPause()
        XCTAssertNil(pause)
        XCTAssertEqual(told.count, 0, "nothing changed, nothing is sent")

        let said = WatchPause(since: Date(timeIntervalSince1970: 1_800_000_000), until: nil)
        await hub.set(pause: said)
        await forwarder.refreshPause()
        pause = await forwarder.currentPause()
        XCTAssertEqual(pause, said)
        XCTAssertEqual(told.count, 1)

        await hub.set(pauseError: HubRequestError.noAnswer)
        await forwarder.refreshPause()
        pause = await forwarder.currentPause()
        XCTAssertEqual(pause, said, "an unreachable computer does not end the pause")
        XCTAssertEqual(told.count, 1)

        await hub.set(pauseError: nil)
        await hub.set(pause: nil)
        await forwarder.refreshPause()
        pause = await forwarder.currentPause()
        XCTAssertNil(pause)
        XCTAssertEqual(told.count, 2, "the watch is told the pause is over")
    }

    /// The computer names an hour's conversations well after it is transcribed. The phone picks
    /// the names up at a later check, hands them to the watch, and drops them when the computer
    /// no longer gives them (the hour's words were withdrawn there).
    func testTheNamesOfAnHourArriveLaterAndGoWhenTheComputerWithdrawsThem() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "hour")
        await hub.set(listed: [RecordingView(id: id, state: "transcribed")])
        _ = await forwarder.drain()
        var rows = await forwarder.recordings()
        XCTAssertEqual(rows.first?.names, [])

        let pushed = Pushed()
        await forwarder.onStatesChanged { _, names in pushed.set(names) }
        await hub.set(listed: [RecordingView(id: id, state: "transcribed", conversations: ["Pricing with Sam", " "])])
        await forwarder.refreshStates()
        rows = await forwarder.recordings()
        XCTAssertEqual(rows.first?.names, ["Pricing with Sam"])
        XCTAssertEqual(pushed.names[id], ["Pricing with Sam"], "the watch is told")
        let kept = await forwarder.currentNames()
        XCTAssertEqual(kept[id], ["Pricing with Sam"])

        await hub.set(listed: [RecordingView(id: id, state: "transcribed")])
        await forwarder.refreshStates()
        rows = await forwarder.recordings()
        XCTAssertEqual(rows.first?.names, [])
        XCTAssertNil(pushed.names[id], "the watch is told they are gone")
    }

    func testThePhoneListShowsEachRecordingOnceWithItsWholeLengthAndState() async throws {
        let hub = StubRecordingHub()
        await hub.set(available: false)
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let long = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: long, part: 0, last: false, audio: "one")
        _ = try await deliver(inbox, forwarder, id: long, part: 1, last: true, audio: "two")
        _ = try await deliver(inbox, forwarder, id: long, part: 1, last: true, audio: "two")   // delivered twice
        var list = await forwarder.recordings()
        XCTAssertEqual(list.count, 1)
        XCTAssertEqual(list.first?.durationSeconds, 1800)
        XCTAssertEqual(list.first?.startedAt, Date(timeIntervalSince1970: 1_800_000_000))
        XCTAssertEqual(list.first?.state, .onPhone)

        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .unsupported)
        XCTAssertNotNil(WatchRecordingsCopy.waiting(outcome, anyOnPhone: true))
        XCTAssertNil(WatchRecordingsCopy.waiting(outcome, anyOnPhone: false))

        await hub.set(available: true)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        list = await forwarder.recordings()
        XCTAssertEqual(list.first?.state, .onComputer)

        // The list survives a relaunch of the phone app.
        let context = context()
        let reopened = WatchRecordingForwarder(inbox: inbox, hub: hub, relay: uploads, session: { context })
        let again = await reopened.recordings()
        XCTAssertEqual(again, list)
        // Lengths are whole minutes now, never a clock (watch v1.1.0).
        XCTAssertTrue(WatchRecordingsCopy.minutes(1800).contains("30"))
        XCTAssertFalse(WatchRecordingsCopy.minutes(3725).contains(":"))
    }

    func testDeletingARecordingThatNeverLeftThePhoneAsksTheComputerNothing() async throws {
        let hub = StubRecordingHub()
        await hub.set(available: false)
        let (inbox, forwarder) = make(hub, StubUploads())
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "one")
        let listed = await forwarder.recordings()
        XCTAssertEqual(listed.first?.partsOnPhone, 1)
        XCTAssertEqual(listed.first?.bytesOnPhone, 3)
        let urls = await forwarder.audioOnPhone(id)
        XCTAssertEqual(urls, [inbox.audioURL(info)])

        await forwarder.delete(id)
        XCTAssertFalse(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))
        let after = await forwarder.recordings()
        XCTAssertTrue(after.isEmpty)
        // The watch reads the state and removes its copy.
        let states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .deleted)
        let asked = await hub.deleted
        XCTAssertTrue(asked.isEmpty)
        // A part that was still on its way from the watch is not kept.
        _ = try await deliver(inbox, forwarder, id: id, part: 1, last: true, audio: "late")
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    func testDeletingARecordingTheComputerHasReachesTheComputerEvenLater() async throws {
        let hub = StubRecordingHub()
        let (inbox, forwarder) = make(hub, StubUploads())
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "one")
        _ = await forwarder.drain()

        // The computer cannot be reached: the delete is done here and owed there.
        await hub.set(deleteError: URLError(.notConnectedToInternet))
        await forwarder.delete(id)
        var owed = await forwarder.deletesWaiting()
        XCTAssertEqual(owed, 1)
        let listed = await forwarder.recordings()
        XCTAssertTrue(listed.isEmpty)

        // It survives a relaunch and goes out with the next pass.
        await hub.set(deleteError: nil)
        let context = context()
        let reopened = WatchRecordingForwarder(inbox: inbox, hub: hub, relay: StubUploads(), session: { context })
        _ = await reopened.drain()
        owed = await reopened.deletesWaiting()
        XCTAssertEqual(owed, 0)
        let asked = await hub.deleted
        XCTAssertEqual(asked, [id])
    }

    func testARecordingDeletedOnTheComputerIsDeletedHereAndOnTheWatch() async throws {
        let hub = StubRecordingHub()
        await hub.set(answerState: "transcribed")
        let (inbox, forwarder) = make(hub, StubUploads())
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "one")
        _ = await forwarder.drain()
        var states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .transcribed)
        let transcript = try await forwarder.transcript(id)
        XCTAssertEqual(transcript?.lines.first?.text, "Call Sam")

        await hub.set(listed: [RecordingView(id: id, state: "deleted")])
        await forwarder.refreshStates()
        states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .deleted)
        let listed = await forwarder.recordings()
        XCTAssertTrue(listed.isEmpty)
        let asked = await hub.deleted
        XCTAssertTrue(asked.isEmpty)

        // An older computer serves no transcript: the page says so instead of failing.
        await hub.set(detail: false)
        let none = try await forwarder.transcript(id)
        XCTAssertNil(none)
    }

    func testTryAgainSendsARefusedRecordingOnceTheComputerTakesIt() async throws {
        let hub = StubRecordingHub()
        await hub.set(postError: HubRequestError.rejected(status: 400, message: "bad"))
        let (inbox, forwarder) = make(hub, StubUploads())
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "one")
        await refuse(forwarder, times: WatchRecordingInbox.maxRefusals)
        var listed = await forwarder.recordings()
        XCTAssertEqual(listed.first?.state, .failed)
        XCTAssertEqual(listed.first?.partsOnPhone, 1)

        await hub.set(postError: nil)
        let outcome = await forwarder.retry(id)
        XCTAssertEqual(outcome, .drained)
        listed = await forwarder.recordings()
        XCTAssertEqual(listed.first?.state, .onComputer)
        XCTAssertEqual(listed.first?.partsOnPhone, 0)
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    func testRetriesReuseTheCopyOnTheRelay() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")

        // Observing is paused on the computer for a while: every pass asks again, but the relay
        // keeps one sealed copy, not one per pass (each would wait 48 hours on the device quota).
        await hub.set(postError: HubRequestError.conflict(message: "observing is paused on this computer"))
        for _ in 0..<4 { _ = await forwarder.drain() }
        XCTAssertEqual(uploads.count, 1)

        await hub.set(postError: nil)
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.count, 1)
        let posted = await hub.posted
        let sent = try XCTUnwrap(posted.first)
        XCTAssertNotNil(uploads.body(sent.upload.id), "the request names the copy that is on the relay")
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    func testAnUploadTheRelayLostIsSealedAndSentAgain() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        await hub.set(postError: HubRequestError.rejected(status: 404, message: "upload_missing"))
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(uploads.count, 1)
        await hub.set(postError: nil)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.count, 2, "a lost copy is replaced by a fresh one")
        let posted = await hub.posted
        let sent = try XCTUnwrap(posted.first)
        XCTAssertNotNil(uploads.body(sent.upload.id))
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// 2026-11-16: parts the computer refused were given up after five tries and waited for a
    /// tap that never came. A refused part now waits, longer each time, and is sent again by
    /// itself for as long as it is on the phone.
    func testARefusedPartKeepsItsAudioAndIsSentAgainByItselfUntilTheComputerTakesIt() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        await hub.set(postError: HubRequestError.rejected(status: 400, message: "the recording is not valid"))

        _ = await forwarder.drain()
        let before = uploads.count
        _ = await forwarder.drain()
        XCTAssertEqual(uploads.count, before, "a refused part waits its turn; it is not sent in a loop")
        let due = await forwarder.nextRetry()
        XCTAssertEqual(due, clock.now.addingTimeInterval(WatchRecordingInbox.retryDelay(refusals: 1)))

        // Well past the old limit of five, it is still tried, and its audio is still here.
        clock.advance(WatchRecordingInbox.retryDelay(refusals: 1) + 1)
        await refuse(forwarder, times: WatchRecordingInbox.maxRefusals + 3)
        XCTAssertGreaterThan(uploads.count, WatchRecordingInbox.maxRefusals + 1)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))
        var states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .failed)
        XCTAssertLessThanOrEqual(WatchRecordingInbox.retryDelay(refusals: 99), 6 * 60 * 60)

        // The computer was updated: the next pass by itself delivers it. Nobody tapped anything.
        await hub.set(postError: nil)
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
        states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .onComputer)
        let nothing = await forwarder.nextRetry()
        XCTAssertNil(nothing)
    }

    /// The night of 2026-11-21: parts half sent in the evening held every place on the relay.
    /// The watch then handed over older parts, which sort first. Each was refused with 429 and
    /// the pass ended at the first refusal, so the half-sent parts were never finished and the
    /// places never freed. The relay is asked what it holds: those parts go first, and a full
    /// relay stops only the parts it holds nothing of.
    func testAFullRelayStopsOnlyNewPartsAndBegunPartsGoFirst() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        uploads.takesAhead = false
        let (inbox, forwarder) = make(hub, uploads)
        // The evening: two parts of a later recording are begun and do not arrive.
        let later = WatchRecordingWire.newRecordingID()
        let begunInfos = [try await deliver(inbox, forwarder, id: later, part: 0, last: false, audio: "evening one"),
                          try await deliver(inbox, forwarder, id: later, part: 1, last: true, audio: "evening two")]
        uploads.error = RelayClientError.offline
        _ = await forwarder.drain()
        let begun = Set(inbox.pending().compactMap { $0.uploading?.upload.id })
        XCTAssertEqual(begun.count, 2, "both parts were handed over once and keep their upload ids")
        // The night: the watch hands over three older parts.
        let older = WatchRecordingWire.newRecordingID()
        var olderInfos: [WatchRecordingPartInfo] = []
        for part in 0..<3 {
            olderInfos.append(try await deliver(inbox, forwarder, id: older, part: part, last: part == 2,
                                                audio: "older \(part)", at: 1_700_000_000))
        }
        XCTAssertEqual(inbox.pending().first?.info.recordingID, older, "the older parts sort first")
        // The morning: the relay holds the two begun uploads and has no place for a new one.
        uploads.error = nil
        uploads.relayHolds = begun
        uploads.fullForNew = true
        let before = uploads.handed.count
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .relayFull, "the phone still says the relay is full")
        XCTAssertGreaterThanOrEqual(uploads.askedOnRelay, 1)
        let morning = Array(uploads.handed.dropFirst(before))
        XCTAssertTrue(morning.prefix(2).allSatisfy(begun.contains), "the begun parts go first: \(morning)")
        XCTAssertEqual(morning.filter { !begun.contains($0) }.count, 1, "one refusal is enough to stop the new parts")
        let left = inbox.pending().map(\.info.recordingID)
        XCTAssertEqual(left, [older, older, older], "the begun parts reached the computer; the older ones wait")
        for info in olderInfos {
            XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path), "nothing waiting is deleted")
        }
        for info in begunInfos {
            XCTAssertFalse(FileManager.default.fileExists(atPath: inbox.audioURL(info).path), "taken by the computer")
        }
        XCTAssertEqual(inbox.pending().first?.refusals, 0, "a full relay is not a refusal of the part")

        // A place is free: the older parts go at the next pass.
        uploads.fullForNew = false
        uploads.relayHolds = []
        let next = await forwarder.drain()
        XCTAssertEqual(next, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// Without the relay's word (an older relay) nothing changes: the order stays, and a full
    /// relay ends the pass as before.
    func testWithoutTheRelaysWordTheOrderStays() {
        func item(_ id: String, uploading: String?) -> WatchRecordingInbox.Pending {
            let info = WatchRecordingPartInfo(recordingID: id, part: 0, last: true, startedAt: Date(timeIntervalSince1970: 1_800_000_000),
                                              durationSeconds: 900, model: "Apple Watch")
            var pending = WatchRecordingInbox.Pending(info: info, refusals: 0)
            if let uploading {
                pending.uploading = WatchRecordingInbox.HeldUpload(
                    upload: RecordingPartRequest.Upload(id: uploading, key: "k", nonce: "n"), hubID: hubID, deviceID: deviceID, relay: "pinned")
            }
            return pending
        }
        let list = [item("a", uploading: nil), item("b", uploading: "u-b"), item("c", uploading: "u-c"), item("d", uploading: nil)]
        XCTAssertEqual(WatchRecordingForwarder.begunFirst(list, onRelay: nil).map(\.info.recordingID), ["a", "b", "c", "d"])
        XCTAssertEqual(WatchRecordingForwarder.begunFirst(list, onRelay: []).map(\.info.recordingID), ["a", "b", "c", "d"])
        XCTAssertEqual(WatchRecordingForwarder.begunFirst(list, onRelay: ["u-c"]).map(\.info.recordingID), ["c", "a", "b", "d"],
                       "a part the phone marked and the relay holds nothing of is not begun")
        XCTAssertEqual(WatchRecordingForwarder.begunFirst(list, onRelay: ["u-b", "u-c", "other"]).map(\.info.recordingID), ["b", "c", "a", "d"])
    }

    /// The relay keeps no more of this phone's recordings (48 refused copies did that on
    /// 2026-11-16): the phone says so, keeps the audio, and tries again by itself.
    func testAFullRelayIsSaidAndTriedAgain() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        uploads.error = RelayClientError.rateLimited(retryAfter: 600)
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .relayFull)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))
        XCTAssertEqual(inbox.pending().first?.refusals, 0, "a full relay is not a refusal of the part")
        let due = await forwarder.nextRetry()
        XCTAssertEqual(due, clock.now.addingTimeInterval(WatchRecordingForwarder.waitingRetrySeconds))
        XCTAssertNotNil(WatchRecordingsCopy.waiting(.relayFull, anyOnPhone: true))

        uploads.error = nil
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
    }
}

extension WatchRecordingForwarderTests {
    private func directContext(_ pinned: SessionContext) -> SessionContext {
        SessionContext(credentials: RelayCredentials(origin: URL(string: "http://127.0.0.1:4321")!, bearer: "d"),
                       hubID: pinned.hubID, deviceID: pinned.deviceID, keys: pinned.keys, route: .direct)
    }

    /// A forwarder for a pairing with two paths. `prefersDirect` is the path requests use now;
    /// `directOpen` whether the direct tunnel is up.
    private func makeTwoPaths(_ hub: StubRecordingHub, _ uploads: StubUploads, prefersDirect: Bool = false,
                              directOpen: Bool = true) -> (WatchRecordingInbox, WatchRecordingForwarder) {
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("inbox"))
        let pinned = context()
        let direct = directContext(pinned)
        let clock = clock
        let forwarder = WatchRecordingForwarder(
            inbox: inbox, hub: hub, relay: uploads, now: { clock.now },
            session: { prefersDirect && directOpen ? direct : pinned },
            otherSession: { current in
                switch current.route {
                case .direct: return pinned
                case .relay: return directOpen ? direct : nil
                }
            })
        return (inbox, forwarder)
    }

    /// 2026-11-18: the public tunnel stopped answering twice in one afternoon and every time the
    /// recordings stopped, although the phone still reached the computer over the direct path.
    /// A part takes the path that is up, and the computer is told which relay holds it.
    /// 2026-11-20: the pinned relay comes first. That day nothing from the phone arrived over
    /// the direct path, and a part spent half a minute on it in every pass.
    func testAPartGoesToThePinnedRelayFirstAndNamesItsRelay() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesRelay: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.hosts, ["relay.example"], "the pinned relay first, and the direct path is not tried")
        let posted = await hub.posted
        XCTAssertEqual(posted.first?.upload.relay, "pinned")
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    func testWhenOnePathIsDownThePartTakesTheOther() async throws {
        // The direct path is down: the pinned relay carries the part.
        var hub = StubRecordingHub()
        await hub.set(takesRelay: true)
        var uploads = StubUploads()
        uploads.down["127.0.0.1"] = RelayClientError.offline
        var (inbox, forwarder) = makeTwoPaths(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.hosts, ["relay.example"], "the direct path is not even tried")
        var posted = await hub.posted
        XCTAssertEqual(posted.first?.upload.relay, "pinned")

        // The pinned relay is down (the tunnel), or refuses the phone: the direct path carries it.
        for failure in [RelayClientError.timedOut, .unauthorized, .unavailable] {
            try? FileManager.default.removeItem(at: directory.appendingPathComponent("inbox"))
            hub = StubRecordingHub()
            await hub.set(takesRelay: true)
            uploads = StubUploads()
            uploads.down["relay.example"] = failure
            (inbox, forwarder) = makeTwoPaths(hub, uploads, directOpen: true)
            _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
            outcome = await forwarder.drain()
            XCTAssertEqual(outcome, .drained, "\(failure)")
            posted = await hub.posted
            XCTAssertEqual(posted.first?.upload.relay, "direct")
        }

        // Both down: the part waits, with its audio, and says the pinned relay's reason.
        try? FileManager.default.removeItem(at: directory.appendingPathComponent("inbox"))
        hub = StubRecordingHub()
        await hub.set(takesRelay: true)
        uploads = StubUploads()
        uploads.down["127.0.0.1"] = RelayClientError.offline
        uploads.down["relay.example"] = RelayClientError.unauthorized
        (inbox, forwarder) = makeTwoPaths(hub, uploads)
        let info = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .pairAgain)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))
        XCTAssertEqual(inbox.pending().first?.refusals, 0)

        // A part that began on the direct path, and the direct path now fails in a way that is
        // not "the path is down": the pinned relay carries it in the same pass.
        try? FileManager.default.removeItem(at: directory.appendingPathComponent("inbox"))
        hub = StubRecordingHub()
        await hub.set(takesRelay: true)
        uploads = StubUploads()
        uploads.down["relay.example"] = RelayClientError.timedOut
        uploads.down["127.0.0.1"] = RelayClientError.badRequest(message: "")
        (inbox, forwarder) = makeTwoPaths(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting, "both failed: the pinned relay's reason stands")
        XCTAssertEqual(inbox.pending().first?.uploading?.relay, "direct")
        uploads.down["relay.example"] = nil
        clock.advance(900)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained, "an error on the direct path does not end the pass")
        posted = await hub.posted
        XCTAssertEqual(posted.first?.upload.relay, "pinned")
        XCTAssertEqual(Array(uploads.hosts.suffix(2)), ["127.0.0.1", "relay.example"])
    }

    /// A computer that only looks on the pinned relay gets every part there, also from a phone
    /// whose requests go over the direct path now. Before, such a part went where the computer
    /// did not look and was answered "upload missing".
    func testAComputerThatDoesNotNameRelaysGetsThePartOnThePinnedRelay() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.hosts, ["relay.example"])
        let posted = await hub.posted
        XCTAssertNil(posted.first?.upload.relay, "no field an older computer does not know")
        let body = try JSONEncoder().encode(XCTUnwrap(posted.first))
        XCTAssertFalse(String(decoding: body, as: UTF8.self).contains("\"relay\""))
    }

    /// A part that began on one relay goes on there while that path is up, and the copy the
    /// relay holds is named to the computer when the computer was the one that could not answer.
    func testAPartStaysOnTheRelayWhereItBeganAndAHeldCopyKeepsItsRelay() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesRelay: true)
        let uploads = StubUploads()
        // The direct path is down at first, so the part begins on the pinned relay and stays
        // in the system's hands there.
        uploads.down["127.0.0.1"] = RelayClientError.offline
        uploads.down["relay.example"] = RecordingUploadInFlight()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .sending, "with the system, on its way: nothing is in the way")
        XCTAssertEqual(inbox.pending().first?.uploading?.relay, "pinned")
        // Both paths are up again: it goes on where it began, not from zero on the other.
        uploads.down = [:]
        await hub.set(postError: HubRequestError.rejected(status: 503, message: "the computer could not fetch the recording yet"))
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(uploads.hosts.last, "relay.example")
        XCTAssertEqual(inbox.pending().first?.held?.relay, "pinned")
        // The computer answers: the held copy is posted with its relay, nothing is uploaded again.
        let tries = uploads.hosts.count
        await hub.set(postError: nil)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.hosts.count, tries)
        let posted = await hub.posted
        XCTAssertEqual(posted.first?.upload.relay, "pinned")
    }

    /// 2026-11-18, 15:00: the app asked the computer three things over a slow relay before its
    /// first upload, was gone after 25 seconds, and had handed nothing over. Parts go to the
    /// system first, on every wake, for a computer that last said it takes recordings; and they
    /// go also while the computer cannot be asked, because the upload needs only the relay.
    func testWaitingPartsAreLeftWithTheSystemBeforeTheComputerIsAsked() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        // The first part ever: nothing is known about the computer, so nothing goes ahead.
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(uploads.began.isEmpty)

        // Now the computer is known to take recordings. It cannot be reached, and two more
        // parts wait than go to the system at once.
        await hub.set(availableError: RelayClientError.timedOut)
        let id = WatchRecordingWire.newRecordingID()
        let all = WatchRecordingForwarder.uploadsAtOnce + 2
        for part in 0..<all {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == all - 1, audio: "part \(part)")
        }
        let before = uploads.began.count
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting, "the computer was not reached")
        let handed = uploads.began.dropFirst(before)
        XCTAssertEqual(handed.count, WatchRecordingForwarder.uploadsAtOnce, "the first parts are with the system all the same")
        XCTAssertEqual(Set(handed.map(\.0)), ["relay.example"], "on the pinned relay, where every computer looks")
        XCTAssertEqual(inbox.pending().count, all, "nothing leaves the phone before the computer has it")
        XCTAssertEqual(inbox.pending().first?.uploading?.relay, "pinned")
        // The same ids on the next wake: the same bytes, nothing sealed anew.
        let ids = handed.map(\.1)
        _ = await forwarder.drain()
        // The hand-overs run side by side, so their order is not fixed.
        XCTAssertEqual(Set(uploads.began.suffix(WatchRecordingForwarder.uploadsAtOnce).map(\.1)), Set(ids))

        // What the computer said is remembered across launches.
        let clock = clock
        let pinned = context()
        let reopened = WatchRecordingForwarder(inbox: inbox, hub: hub, relay: uploads, now: { clock.now }, session: { pinned })
        let count = uploads.began.count
        _ = await reopened.drain()
        XCTAssertEqual(uploads.began.count, count + WatchRecordingForwarder.uploadsAtOnce)
    }

    /// 2026-11-19: four parts were whole on the relay and in seven wakes of 30 seconds the
    /// computer was never told. The hand-overs came first, one after the other, and one took 25
    /// seconds. A part the relay holds is told to the computer while hand-overs are still under
    /// way, and every hand-over begun still ends before the pass does.
    func testTheComputerIsToldOfAHeldPartWhileOthersAreStillHandedOver() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)

        // Three parts wait. The first reaches the relay, and the computer does not answer.
        await hub.set(postError: RelayClientError.timedOut)
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<3 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 2, audio: "part \(part)")
        }
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(inbox.pending().filter { $0.held != nil }.count, 1, "one part is whole on the relay")
        let toldBefore = await hub.posted.count

        // Next wake: every hand-over is slow. It ends only once the computer was told of the
        // held part, or after five seconds.
        await hub.set(postError: nil)
        let late = Late()
        uploads.whileBeginning = {
            for _ in 0..<100 {
                if await hub.posted.count > toldBefore { return }
                try? await Task.sleep(nanoseconds: 50_000_000)
            }
            late.set()
        }
        let began = uploads.began.count
        outcome = await forwarder.drain()
        XCTAssertFalse(late.value, "the computer was told while the hand-overs were under way")
        XCTAssertEqual(uploads.began.count, began + 2, "both waiting parts were handed over before the pass ended")
        XCTAssertEqual(outcome, .drained)
        let parts = await hub.posted.suffix(3).map(\.part)
        XCTAssertEqual(parts.first, 0, "the held part first")
        XCTAssertEqual(Set(parts), [0, 1, 2])
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// 2026-11-19: the phone had to be awake once more after each upload to tell the computer,
    /// and four whole parts waited on the relay for five hours. With a computer that takes it,
    /// the phone tells the computer when it hands a part to the system; the computer fetches the
    /// part by itself. "Expected" never takes a part off the phone; only the computer's own copy does.
    func testTheComputerIsToldWhenAPartIsHandedOverAndThePartStaysUntilItHasIt() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesAhead: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        let toldAtFirst = await hub.told.count
        XCTAssertEqual(toldAtFirst, 0, "nothing is told ahead before the computer said it takes it")

        // Two parts are handed to the system; their uploads are still on the way.
        uploads.error = RecordingUploadInFlight()
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<2 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 1, audio: "part \(part)")
        }
        let posted = await hub.posted.count
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .sending)
        let told = await hub.told
        XCTAssertEqual(Set(told.map(\.part)), [0, 1])
        XCTAssertTrue(told.allSatisfy { $0.ahead == true && $0.upload.relay == "pinned" && $0.recordingId == id })
        XCTAssertEqual(inbox.pending().count, 2, "expected is not received: both parts stay on the phone")
        XCTAssertTrue(inbox.pending().allSatisfy { $0.told == true })
        let postedAfter = await hub.posted.count
        XCTAssertEqual(postedAfter, posted, "nothing was said in the usual way while the uploads were on the way")
        let body = String(decoding: try JSONEncoder().encode(told[0]), as: UTF8.self)
        XCTAssertTrue(body.contains("\"ahead\":true"))

        // The phone slept, the uploads arrived, the computer fetched them and removed the relay's
        // copies. Next wake: the computer is asked first, nothing goes up again, the parts leave.
        await hub.set(aheadState: "received")
        uploads.relayHoldsNothing = true
        let began = uploads.began.count
        let handed = uploads.handed.count
        // A wake within the minute leaves the parts alone: nothing is told, asked or sent again.
        let toldSoFar = await hub.told.count
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .sending)
        let toldWithinTheMinute = await hub.told.count
        XCTAssertEqual(toldWithinTheMinute, toldSoFar, "a part told a moment ago is not told again")
        XCTAssertEqual(uploads.began.count, began)
        XCTAssertEqual(uploads.handed.count, handed)
        clock.advance(WatchRecordingForwarder.tellAgainAfter + 1)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.began.count, began, "no part was handed over again")
        XCTAssertEqual(uploads.handed.count, handed, "no part was uploaded again")
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// 2026-11-20, 7:46 PM: four parts became whole on the relay in the second the app left.
    /// The computer had not been told of them, since the tell came after the hand-over, and
    /// it asked for none until the phone woke again. The tell goes out beside the hand-over.
    func testTheComputerIsToldWhileAPartIsStillBeingHandedOver() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesAhead: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        uploads.error = RecordingUploadInFlight()
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "b")
        let toldBefore = await hub.told.count
        let seen = Late()
        uploads.whileBeginning = {
            // The hand-over has not ended; the computer hears of the part all the same.
            for _ in 0..<200 {
                if await hub.told.count > toldBefore { seen.set(); return }
                try? await Task.sleep(nanoseconds: 10_000_000)
            }
        }
        _ = await forwarder.drain()
        XCTAssertTrue(seen.value, "the computer was told before the hand-over ended")
        let told = await hub.told.filter { $0.recordingId == id }
        XCTAssertEqual(told.count, 1, "and it is told once, not again after the hand-over")
        XCTAssertEqual(inbox.pending().count, 1, "the part stays on the phone until the computer has it")
        XCTAssertTrue(inbox.pending().allSatisfy { $0.told == true })
    }

    /// The tell went out and the hand-over then failed. Nothing is lost by it: the part stays
    /// on the phone, is not marked as told, and goes up at the next pass.
    func testATellWhoseHandOverFailsLeavesThePartWaiting() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesAhead: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        uploads.error = RecordingUploadInFlight()
        uploads.handsNothing = true
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "b")
        _ = await forwarder.drain()
        XCTAssertEqual(inbox.pending().count, 1, "the part is still on the phone")
        XCTAssertTrue(inbox.pending().allSatisfy { $0.told != true }, "and it does not count as told")
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(inbox.pending()[0].info).path), "with its file")

        uploads.handsNothing = false
        clock.advance(WatchRecordingForwarder.tellAgainAfter + 1)
        _ = await forwarder.drain()
        XCTAssertTrue(inbox.pending().allSatisfy { $0.told == true }, "the next pass hands it over and tells it")
    }

    /// A pass says how many held parts it started afresh, and a computer from before that
    /// count gets a record without it.
    func testAPassCountsTheHeldPartsItStartedAfresh() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesAhead: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        uploads.error = RecordingUploadInFlight()
        uploads.startsAfresh = true
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<2 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 1, audio: "part \(part)")
        }
        _ = await forwarder.drain()
        let passes = await forwarder.passes
        let record = try XCTUnwrap(passes.last)
        XCTAssertEqual(record.restarted, 2)
        XCTAssertEqual(record.beganHanded, 2)
        XCTAssertEqual(inbox.pending().count, 2, "both parts are still on the phone")

        XCTAssertEqual(RecordingPassRecord.forComputer([record], takesRestarts: true)[0].restarted, 2)
        let older = RecordingPassRecord.forComputer([record], takesRestarts: false)[0]
        XCTAssertNil(older.restarted)
        XCTAssertFalse(String(decoding: try JSONEncoder().encode(older), as: UTF8.self).contains("restarted"))
    }

    /// 2026-11-20: the computer answered a second tell in under a second, the answer reached
    /// the phone 8 and 13 seconds later, and the hand-over waited for it; the wake was over
    /// before a piece went. The hand-over of a part told before does not wait for that answer.
    func testAPartToldBeforeIsHandedOverWithoutWaitingForTheComputer() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesAhead: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()
        uploads.error = RecordingUploadInFlight()
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        _ = await forwarder.drain()
        XCTAssertTrue(inbox.pending().allSatisfy { $0.told == true })

        clock.advance(WatchRecordingForwarder.tellAgainAfter + 1)
        let began = uploads.began.count
        let handedFirst = Flag(false)
        // The answer comes back only after the hand-over, or after two seconds if it never does.
        await hub.set(whileTelling: {
            for _ in 0..<200 where uploads.began.count == began {
                try? await Task.sleep(nanoseconds: 10_000_000)
            }
            handedFirst.value = uploads.began.count > began
        })
        _ = await forwarder.drain()
        XCTAssertTrue(handedFirst.value, "the hand-over went ahead while the answer was on its way")
        XCTAssertEqual(inbox.pending().count, 1, "the part stays until the computer has it")
    }

    /// The send in a wake is the computer's to allow: a computer that does not say so gets
    /// the hand-over as before and a record without the count.
    func testTheSendInAWakeIsTheComputersToAllow() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesAhead: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()
        XCTAssertFalse(uploads.inWakeAllowed.contains(true), "not allowed: never asked for")

        await hub.set(takesInWake: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        _ = await forwarder.drain()
        // The computer's word is known from the pass before: the next wake uses it.
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "c")
        _ = await forwarder.drain()
        XCTAssertEqual(uploads.inWakeAllowed.last, true)
        let passes = await forwarder.passes
        let record = try XCTUnwrap(passes.last)
        XCTAssertGreaterThanOrEqual(record.sentInWake ?? 0, 1)

        let older = RecordingPassRecord.forComputer([record], takesRestarts: true, takesInWake: false)[0]
        XCTAssertNil(older.sentInWake)
        XCTAssertFalse(String(decoding: try JSONEncoder().encode(older), as: UTF8.self).contains("sentInWake"))
        XCTAssertNotNil(RecordingPassRecord.forComputer([record], takesRestarts: false, takesInWake: true)[0].sentInWake)
    }

    /// A computer that does not say it takes parts told ahead is never sent one: an older
    /// computer refuses a request with a field it does not know.
    func testAnOlderComputerIsNeverToldAhead() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<2 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 1, audio: "part \(part)")
        }
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        let told = await hub.told
        XCTAssertTrue(told.isEmpty)
        let posted = await hub.posted
        XCTAssertEqual(posted.count, 3)
        for request in posted {
            XCTAssertNil(request.ahead)
            XCTAssertFalse(String(decoding: try JSONEncoder().encode(request), as: UTF8.self).contains("ahead"))
        }
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// 2026-11-19, 6:48 PM: the phone was awake for eight seconds. A pass asked the computer for
    /// its status five times before it told it of the first part, and one part was taken. A pass
    /// asks what the computer takes once, however many parts wait.
    func testAPassAsksTheComputerWhatItTakesOnce() async throws {
        let hub = StubRecordingHub()
        await hub.set(takesContext: true)
        await hub.set(takesMarks: true)
        await hub.set(takesRelay: true)
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<3 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 2, audio: "part \(part)")
        }
        let before = await hub.asked
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        let asked = await hub.asked
        XCTAssertEqual(asked - before, 1)
        let posted = await hub.posted
        XCTAssertEqual(posted.count, 3)
    }

    /// 2026-11-19, 3:12 PM: four parts were whole on the relay, the computer had not taken them,
    /// and they held all four places, so the phone uploaded nothing new. A part the relay holds
    /// whole takes no place: in the same wake the next parts go to the system, also while the
    /// computer cannot be asked. Recording everything comes first.
    func testPartsTheRelayHoldsWholeTakeNoPlaceFromNewOnes() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = makeTwoPaths(hub, uploads, prefersDirect: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)

        // Two more parts wait than go to the system at once, and the computer cannot be reached.
        let atOnce = WatchRecordingForwarder.uploadsAtOnce, all = atOnce + 2
        await hub.set(availableError: RelayClientError.timedOut)
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<all {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == all - 1, audio: "part \(part)")
        }
        var before = uploads.began.count
        _ = await forwarder.drain()
        let first = uploads.began.dropFirst(before).map(\.1)
        XCTAssertEqual(first.count, atOnce)

        // They arrive while the phone sleeps. Next wake, still no computer: the phone learns the
        // relay holds those, and the last two go to the system in the same wake.
        uploads.wholeOnTheRelay = Set(first)
        before = uploads.began.count
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        let second = uploads.began.dropFirst(before).map(\.1)
        XCTAssertEqual(Set(second).subtracting(first).count, 2, "both new parts went to the system")
        XCTAssertEqual(inbox.pending().filter { $0.held != nil }.count, atOnce, "they are remembered as whole on the relay")
        XCTAssertEqual(inbox.pending().count, all, "nothing leaves the phone before the computer has it")

        // A third wake: the held parts are not asked about again and take no place.
        before = uploads.began.count
        _ = await forwarder.drain()
        XCTAssertTrue(Set(uploads.began.dropFirst(before).map(\.1)).isDisjoint(with: first))

        // The computer is back: every part crosses.
        await hub.set(availableError: nil)
        uploads.wholeOnTheRelay = []
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
        let parts = await hub.posted.suffix(all).map(\.part)
        XCTAssertEqual(Set(parts), Set(0..<all))
    }

    /// The 08:30 wake on 2026-11-20: ten parts the relay holds whole, eight of them asked
    /// about in that wake, and no part request reached the computer. The phone said nothing of
    /// what its pass did. Each pass now leaves a record of counts, and the computer is told.
    func testAPassLeavesARecordOfWhatItCameTo() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        await hub.set(takesAhead: true)
        await hub.set(takesPasses: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)

        // Ten parts wait; the computer stops answering part requests. Wake after wake the parts
        // go to the system, four at a time, and arrive on the relay while the phone sleeps.
        await hub.set(postError: RelayClientError.timedOut)
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<10 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 9, audio: "part \(part)")
        }
        for _ in 0..<4 {
            clock.advance(900)
            _ = await forwarder.drain()
            uploads.wholeOnTheRelay = Set(uploads.began.map(\.1))
        }
        clock.advance(900)
        XCTAssertEqual(inbox.pending().count, 10, "nothing leaves the phone before the computer has it")

        // The wake of 08:30: every part is whole on the relay and the computer hears of none.
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        var passes = await forwarder.passes
        var record = try XCTUnwrap(passes.last)
        XCTAssertEqual(record.pending, 10)
        XCTAssertEqual(record.whole, 10, "the relay holds all ten")
        XCTAssertEqual(record.taken, 0)
        XCTAssertEqual(record.posted, 10, "every whole part was told, side by side")
        XCTAssertEqual(record.outcome, "waiting")
        XCTAssertEqual(record.stoppedBy, "part-relay-timed-out")
        XCTAssertNotNil(record.seconds)
        let told = await hub.passesTold
        XCTAssertEqual(told.last?.last?.startedAt, record.startedAt, "the computer was told of this pass")
        XCTAssertEqual(told.flatMap { $0 }.filter { $0.startedAt == record.startedAt && $0.seconds != nil }.count, 1,
                       "an ended pass is told once")

        // The computer answers again: the next pass says ten taken.
        await hub.set(postError: nil)
        uploads.wholeOnTheRelay = []
        clock.advance(900)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        passes = await forwarder.passes
        record = try XCTUnwrap(passes.last)
        XCTAssertEqual(record.taken, 10)
        XCTAssertEqual(record.outcome, "drained")
        XCTAssertNil(record.stoppedBy)

        // The records are kept across a restart, the newest twenty.
        let (_, again) = make(hub, uploads)
        let kept = await again.passes
        XCTAssertEqual(kept.last, record)
        XCTAssertLessThanOrEqual(kept.count, WatchRecordingForwarder.passesKept)
    }

    /// One part request without an answer used to end the pass: a wake of seconds told at most
    /// one part. Whole parts are told side by side when the pass starts, with what the computer
    /// said it takes the last time, and the others cross although one gets no answer.
    func testWholePartsAreToldSideBySideAndOneWithoutAnAnswerDoesNotStopTheOthers() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained, "the computer said what it takes; the phone remembers")

        // Ten parts arrive whole on the relay while the computer cannot be reached.
        await hub.set(postError: RelayClientError.offline)
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<10 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 9, audio: "part \(part)")
        }
        for _ in 0..<4 {
            clock.advance(900)
            _ = await forwarder.drain()
            uploads.wholeOnTheRelay = Set(uploads.began.map(\.1))
        }
        clock.advance(900)
        _ = await forwarder.drain()
        XCTAssertEqual(inbox.pending().filter { $0.held != nil }.count, 10)

        // The computer is back, and the request for the first part gets no answer. The computer
        // cannot say what it takes either: the phone tells with what it remembered.
        await hub.set(postError: nil)
        await hub.set(unansweredParts: [0])
        await hub.set(availableError: RelayClientError.timedOut)
        var before = await hub.attempts
        clock.advance(900)
        outcome = await forwarder.drain()
        var after = await hub.attempts
        XCTAssertEqual(after - before, 10, "all ten requests went out although nothing could be asked first")
        XCTAssertEqual(inbox.pending().map(\.info.part), [0], "nine crossed; the unanswered one stays")
        XCTAssertEqual(outcome, .waiting)
        var passes = await forwarder.passes
        XCTAssertEqual(passes.last?.taken, 9)
        XCTAssertEqual(passes.last?.posted, 10)

        // Next wake the first part gets its answer.
        await hub.set(unansweredParts: [])
        await hub.set(availableError: nil)
        before = await hub.attempts
        clock.advance(900)
        outcome = await forwarder.drain()
        after = await hub.attempts
        XCTAssertEqual(after - before, 1, "only the part that is left is told again")
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
        passes = await forwarder.passes
        XCTAssertEqual(passes.last?.taken, 1)
    }

    /// On 2026-11-20 one part kept failing while the app was open. Every answer from the
    /// computer asks for a pass, a pass that ended on the failure started the next at once, and
    /// passes ran every two to three seconds for minutes. A pass that did not send everything
    /// does not start the next one by itself, and one failing part does not keep the parts
    /// behind it from their turn.
    func testAFailingPartNeitherLoopsThePassNorHoldsUpTheOthers() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)

        // Three parts wait. The request for the first fails with an error of the relay's.
        uploads.takesAhead = false
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<3 {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: part == 2, audio: "part \(part)")
        }
        await hub.set(failingParts: [0])
        // While the pass runs, another one is asked for, as an open app does all the time.
        uploads.whileStoring = { _ = await forwarder.drain() }
        let passesBefore = await forwarder.passes.count
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        let passesAfter = await forwarder.passes.count
        XCTAssertEqual(passesAfter - passesBefore, 1, "the pass that ended waiting did not start another")
        XCTAssertEqual(inbox.pending().map(\.info.part), [0], "the two parts behind the failing one crossed")
        let passes = await forwarder.passes
        let record = try XCTUnwrap(passes.last)
        XCTAssertEqual(record.taken, 2)
        XCTAssertGreaterThan(record.skipped, 0)
    }

    /// A part whose new uploads are refused too does not start afresh for ever: each start
    /// leaves a half-done upload on the relay. Two starts per part, then it keeps its upload
    /// until the next version of the app.
    func testAPartStartsAfreshAtMostTwiceInOneVersion() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        uploads.takesAhead = false
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("inbox"))
        let pinned = context()
        let clock = clock
        func launch(_ build: String) -> WatchRecordingForwarder {
            WatchRecordingForwarder(inbox: inbox, hub: hub, relay: uploads, now: { clock.now }, session: { pinned }, build: build)
        }
        var forwarder = launch("100")
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        uploads.error = RelayClientError.server(status: 500, message: "")
        var ids: [String] = []
        // Twelve refused tries: three for the first upload, three for each fresh one, and more.
        for _ in 0..<12 {
            clock.advance(900)
            _ = await forwarder.drain()
            if let id = inbox.pending().first?.uploading?.upload.id, ids.last != id { ids.append(id) }
        }
        XCTAssertEqual(ids.count, 3, "the first upload and two fresh ones, no more")
        XCTAssertEqual(inbox.pending().first?.uploading?.upload.id, ids.last, "the part keeps its last upload")
        XCTAssertEqual(inbox.pending().count, 1, "and its audio")

        // The same version, started again: still no further fresh start.
        forwarder = launch("100")
        for _ in 0..<4 {
            clock.advance(900)
            _ = await forwarder.drain()
        }
        XCTAssertEqual(inbox.pending().first?.uploading?.upload.id, ids.last)

        // A new version of the app gets its own tries.
        forwarder = launch("101")
        for _ in 0..<3 {
            clock.advance(900)
            _ = await forwarder.drain()
        }
        XCTAssertNotEqual(inbox.pending().first?.uploading?.upload.id, ids.last, "the new version starts the part afresh")
        XCTAssertEqual(inbox.pending().count, 1)
    }

    /// The count of refusals is kept across launches: a phone that starts the app afresh for
    /// each wake still reaches three, and the stuck part heals.
    func testRefusalsAreCountedAcrossLaunches() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        uploads.takesAhead = false
        var (inbox, forwarder) = make(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        uploads.error = RelayClientError.server(status: 500, message: "")
        var first: String?
        for launch in 0..<3 {
            // Each wake is a new run of the app.
            (inbox, forwarder) = make(hub, uploads)
            clock.advance(900)
            _ = await forwarder.drain()
            if launch == 0 { first = inbox.pending().first?.uploading?.upload.id }
            if launch < 2 { XCTAssertEqual(inbox.pending().first?.uploading?.upload.id, first, "the same upload so far") }
        }
        XCTAssertNotNil(first)
        XCTAssertNil(inbox.pending().first?.uploading, "the third refusal, in the third run, starts the part afresh")
        uploads.error = nil
        (inbox, forwarder) = make(hub, uploads)
        clock.advance(900)
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// One upload failed every try for two hours on 2026-11-20. After three refusals in a row
    /// the part starts a new upload under a new id; the audio never leaves the phone meanwhile.
    func testAnUploadTheRelayKeepsRefusingStartsAfresh() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        uploads.takesAhead = false
        let (inbox, forwarder) = make(hub, uploads)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        let info = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        uploads.error = RelayClientError.badRequest(message: "")
        var ids: Set<String> = []
        for _ in 0..<3 {
            clock.advance(900)
            let outcome = await forwarder.drain()
            XCTAssertEqual(outcome, .waiting)
            if let id = inbox.pending().first?.uploading?.upload.id { ids.insert(id) }
        }
        XCTAssertEqual(ids.count, 1, "the same upload was tried each time")
        XCTAssertNil(inbox.pending().first?.uploading, "after three refusals the part forgets that upload")
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path), "the audio stays on the phone")

        // The next try is a new upload, and it goes through.
        uploads.error = nil
        clock.advance(900)
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
        let fresh = try XCTUnwrap(uploads.handed.last)
        XCTAssertFalse(ids.contains(fresh), "under a new id")

        // Being offline is not a refusal: the upload is kept however often that happens.
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "c")
        uploads.error = RelayClientError.offline
        for _ in 0..<5 {
            clock.advance(900)
            _ = await forwarder.drain()
        }
        XCTAssertNotNil(inbox.pending().first?.uploading, "an upload that only could not be reached is kept")
    }

    /// A wake that finds a pass still running starts none. That is counted and told    /// A wake that finds a pass still running starts none. That is counted and told, with the
    /// running pass's record, so a pass that hangs does not hide itself.
    func testAWakeThatFindsAPassRunningIsCountedAndTold() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        await hub.set(takesPasses: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        let release = Late()
        uploads.whileBeginning = {
            for _ in 0..<200 where !release.value { try? await Task.sleep(nanoseconds: 20_000_000) }
        }
        let stuck = Task { await forwarder.drain() }
        for _ in 0..<200 where await forwarder.passes.count < 2 { try await Task.sleep(nanoseconds: 10_000_000) }
        let outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        let now = await forwarder.passes
        let running = try XCTUnwrap(now.last)
        XCTAssertNil(running.seconds, "the pass has not ended")
        XCTAssertEqual(running.skipped, 1)
        let told = await hub.passesTold
        XCTAssertEqual(told.last?.last?.skipped, 1, "the computer hears of the running pass and the skipped wake")
        XCTAssertNil(told.last?.last?.seconds)

        release.set()
        _ = await stuck.value
        let later = await forwarder.passes
        let ended = try XCTUnwrap(later.first { $0.startedAt == running.startedAt })
        XCTAssertNotNil(ended.seconds)
        let after = await hub.passesTold
        XCTAssertEqual(after.last?.last?.seconds, ended.seconds, "and again when it ends")
    }

    /// A pass the app was closed under leaves a record with no end. The next run closes it as
    /// cut off and tells it once; on 2026-11-20 three such records went out with every report.
    func testAPassCutOffByTheAppClosingIsToldOnce() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        await hub.set(takesPasses: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        // A pass hangs in a hand-over, and the app is closed: the record on disk has no end.
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        let release = Late()
        uploads.whileBeginning = {
            for _ in 0..<400 where !release.value { try? await Task.sleep(nanoseconds: 20_000_000) }
        }
        let stuck = Task { await forwarder.drain() }
        for _ in 0..<200 where await forwarder.passes.count < 2 { try await Task.sleep(nanoseconds: 10_000_000) }
        let open = await forwarder.passes
        XCTAssertNil(open.last?.seconds)

        // The app starts again.
        uploads.whileBeginning = nil
        let (_, next) = make(hub, uploads)
        let read = await next.passes
        XCTAssertEqual(read.last?.outcome, WatchRecordingForwarder.cutOff)
        XCTAssertEqual(read.last?.seconds, 0)
        XCTAssertEqual(read.last?.startedAt, open.last?.startedAt)
        let before = await hub.passesTold.flatMap { $0 }.filter { $0.outcome == WatchRecordingForwarder.cutOff }.count
        clock.advance(900)
        _ = await next.drain()
        clock.advance(900)
        _ = await next.drain()
        let after = await hub.passesTold.flatMap { $0 }.filter { $0.outcome == WatchRecordingForwarder.cutOff }.count
        XCTAssertEqual(after - before, 1, "the cut-off pass is told once")
        release.set()
        _ = await stuck.value
    }

    func testAnErrorsWordCarriesItsStatus() {
        XCTAssertEqual(RecordingPassRecord.word(RelayClientError.server(status: 500, message: "x")), "relay-500")
        XCTAssertEqual(RecordingPassRecord.word(RelayClientError.notFound), "relay-404")
        XCTAssertEqual(RecordingPassRecord.word(RelayClientError.timedOut), "relay-timed-out")
        XCTAssertEqual(RecordingPassRecord.word(HubRequestError.rejected(status: 409, message: "x")), "computer-409")
        XCTAssertEqual(RecordingPassRecord.word(HubRequestError.noAnswer), "computer-no-answer")
        XCTAssertEqual(RecordingPassRecord.word(RecordingUploadInFlight()), "upload-on-the-way")
    }

    /// A suspended app keeps "a pass is running" for as long as the system keeps the app. A pass
    /// that has done nothing for three minutes no longer holds back the next wake: that wake
    /// sends the parts itself, and its record says a stale pass was replaced.
    func testAPassThatDidNothingForThreeMinutesIsReplaced() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        await hub.set(takesPasses: true)
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "a")
        _ = await forwarder.drain()

        // The next pass hangs in its first hand-over.
        _ = try await deliver(inbox, forwarder, id: WatchRecordingWire.newRecordingID(), part: 0, last: true, audio: "b")
        let release = Late()
        let hung = Late()
        uploads.whileBeginning = {
            guard !hung.value else { return }
            hung.set()
            for _ in 0..<400 where !release.value { try? await Task.sleep(nanoseconds: 20_000_000) }
        }
        let stuck = Task { await forwarder.drain() }
        for _ in 0..<200 where !hung.value { try await Task.sleep(nanoseconds: 10_000_000) }

        // A wake two minutes later still waits for it: a pass may wait that long for one answer.
        clock.advance(120)
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(inbox.pending().count, 1)

        // A wake after three more minutes of nothing does not.
        clock.advance(WatchRecordingForwarder.staleAfter + 1)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty, "the part crossed without the hung pass")
        var passes = await forwarder.passes
        let fresh = try XCTUnwrap(passes.last)
        XCTAssertEqual(fresh.replacedStale, 1)
        XCTAssertEqual(fresh.taken, 1)
        XCTAssertEqual(fresh.outcome, "drained")
        let stale = try XCTUnwrap(passes.dropLast().last)
        XCTAssertNil(stale.seconds, "the hung pass has not ended")
        XCTAssertEqual(stale.skipped, 1)
        let posted = await hub.posted.count

        // The hung pass comes back: it ends, sends nothing more and does not undo the outcome.
        release.set()
        _ = await stuck.value
        passes = await forwarder.passes
        XCTAssertNotNil(passes.dropLast().last?.seconds)
        XCTAssertEqual(passes.last, fresh.reported == true ? fresh : passes.last)
        XCTAssertEqual(passes.last?.taken, 1)
        let after = await hub.posted.count
        XCTAssertEqual(after, posted, "the replaced pass sent no part request of its own")
        let last = await forwarder.lastOutcome
        XCTAssertEqual(last, .drained)
        // And the next wake runs as usual.
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
    }

    /// Try again sends refused parts now. On 2026-11-18 parts an older computer had refused were
    /// waiting hours for their turn after the computer was updated; the card said "1 hour couldn't
    /// reach your computer" and Try again did not touch them.
    func testTryAgainSendsRefusedPartsNow() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        await hub.set(postError: HubRequestError.rejected(status: 400, message: "the recording is not valid"))
        await refuse(forwarder, times: WatchRecordingInbox.maxRefusals - 1)
        // The last refusal, with no time passing after it: the part's next turn is hours away.
        _ = await forwarder.drain()
        var rows = await forwarder.recordings()
        XCTAssertEqual(rows.first?.state, .failed)
        XCTAssertGreaterThan(try XCTUnwrap(inbox.pending().first?.nextTry), clock.now)

        // The computer was updated. Without waiting for the part's turn:
        await hub.set(postError: nil)
        var outcome = await forwarder.drain()
        XCTAssertFalse(inbox.pending().isEmpty, "its turn has not come, so a pass leaves it")
        await forwarder.retryRefused()
        XCTAssertEqual(inbox.pending().first?.refusals, 0)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
        rows = await forwarder.recordings()
        XCTAssertNotEqual(rows.first?.state, .failed)
    }

    func testThePathsAPartMayTake() {
        let pinned = context()
        let direct = directContext(pinned)
        func names(_ first: SessionContext, _ other: SessionContext?, _ takesRelay: Bool) -> [String] {
            WatchRecordingForwarder.paths(first: first, other: other, takesRelay: takesRelay).map(WatchRecordingForwarder.relayName)
        }
        XCTAssertEqual(names(pinned, direct, true), ["pinned", "direct"])
        XCTAssertEqual(names(direct, pinned, true), ["pinned", "direct"])
        XCTAssertEqual(names(pinned, direct, false), ["pinned"])
        XCTAssertEqual(names(direct, pinned, false), ["pinned"])
        // One path only: a pinned pairing with the tunnel closed, or a pairing that is direct only.
        XCTAssertEqual(names(pinned, nil, true), ["pinned"])
        XCTAssertEqual(names(direct, nil, false), ["direct"])
        // The direct path's address is inside the app; iOS cannot upload to it with the app closed.
        XCTAssertTrue(RecordingPieceUploads.isInsideTheApp(direct.credentials.origin))
        XCTAssertFalse(RecordingPieceUploads.isInsideTheApp(pinned.credentials.origin))
    }

    /// The relay refused this phone's credentials (2026-11-16 to 11-17: every upload got 401 and
    /// the Watch tab only said it was waiting). The phone says to pair again, keeps the audio,
    /// counts nothing against the part, and sends it once the relay accepts the phone again.
    func testARefusedPairingIsSaidAndThePartGoesAfterPairingAgain() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        uploads.error = RelayClientError.unauthorized
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .pairAgain)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))
        XCTAssertEqual(inbox.pending().first?.refusals, 0, "a refused pairing is not a refusal of the part")
        let due = await forwarder.nextRetry()
        XCTAssertEqual(due, clock.now.addingTimeInterval(WatchRecordingForwarder.waitingRetrySeconds))
        XCTAssertNotNil(WatchRecordingsCopy.waiting(.pairAgain, anyOnPhone: true))
        XCTAssertNotEqual(WatchRecordingsCopy.waiting(.pairAgain, anyOnPhone: true),
                          WatchRecordingsCopy.waiting(.waiting, anyOnPhone: true))

        uploads.error = nil
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
    }

    /// An upload that goes on in the background is asked about again with the same id and the
    /// same bytes, and the parts behind it are not held back.
    func testAnUploadStillInFlightKeepsItsIDAndDoesNotHoldTheOthersBack() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: false, audio: "first")
        uploads.error = RecordingUploadInFlight()
        _ = try await deliver(inbox, forwarder, id: id, part: 1, last: true, audio: "second")

        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .sending, "with the system, on its way: nothing is in the way")
        XCTAssertEqual(Set(uploads.handed).count, 2, "both parts were handed over in one pass")
        XCTAssertEqual(inbox.pending().count, 2)
        XCTAssertTrue(inbox.pending().allSatisfy { $0.refusals == 0 && $0.held == nil && $0.uploading != nil })
        let first = uploads.handed

        uploads.error = nil
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(Set(uploads.handed), Set(first), "no part got a second upload id")
        XCTAssertTrue(inbox.pending().isEmpty)
        let posted = await hub.posted
        XCTAssertEqual(posted.map(\.part), [0, 1])
    }

    /// The upload arrived but the phone heard a failure (the app was closed in between). The next
    /// try hands over the same id; the relay says it holds it, and the part goes on to the computer.
    func testAnUploadThatArrivedUnheardIsNotStoredTwice() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        _ = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        uploads.error = RelayClientError.offline
        uploads.arrivesAnyway = true
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(uploads.count, 1)

        uploads.error = nil
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertEqual(uploads.count, 1, "one copy on the relay")
        XCTAssertTrue(inbox.pending().isEmpty)
        let posted = await hub.posted
        XCTAssertEqual(posted.first?.upload.id, uploads.handed.first)
    }

    /// Only a few uploads are with the system at a time.
    func testOnlyAFewUploadsAreInFlightAtOnce() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        for part in 0..<(WatchRecordingForwarder.uploadsAtOnce + 3) {
            _ = try await deliver(inbox, forwarder, id: id, part: part, last: false, audio: "part \(part)")
        }
        uploads.error = RecordingUploadInFlight()
        let before = uploads.handed.count
        _ = await forwarder.drain()
        XCTAssertEqual(Set(uploads.handed.dropFirst(before)).count, WatchRecordingForwarder.uploadsAtOnce)
    }

    func testHowABackgroundUploadEndedIsRead() {
        XCTAssertNoThrow(try RecordingBackgroundUploads.check(.init(status: 201, retryAfter: nil)))
        XCTAssertNoThrow(try RecordingBackgroundUploads.check(.init(status: 409, retryAfter: nil)), "the relay already holds it")
        XCTAssertThrowsError(try RecordingBackgroundUploads.check(.init(status: 429, retryAfter: 600))) {
            XCTAssertEqual($0 as? RelayClientError, .rateLimited(retryAfter: 600))
        }
        XCTAssertThrowsError(try RecordingBackgroundUploads.check(.init(status: 0, retryAfter: nil))) {
            XCTAssertEqual($0 as? RelayClientError, .offline)
        }
        XCTAssertThrowsError(try RecordingBackgroundUploads.check(.init(status: 413, retryAfter: nil))) {
            XCTAssertEqual($0 as? RelayClientError, .payloadTooLarge)
        }
    }
}

/// A clock a test moves by hand.
private final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value = Date(timeIntervalSince1970: 1_800_100_000)
    var now: Date { lock.withLock { value } }
    func advance(_ seconds: TimeInterval) { lock.withLock { value = value.addingTimeInterval(seconds) } }
}

private final class Flag: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: Bool
    init(_ value: Bool) { stored = value }
    var value: Bool {
        get { lock.withLock { stored } }
        set { lock.withLock { stored = newValue } }
    }
}
