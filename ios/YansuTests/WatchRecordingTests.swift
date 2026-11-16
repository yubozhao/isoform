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
        XCTAssertEqual(now(.ready(reachable: true), nil).kind, .unknown)
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
    func recordingsAvailable() async throws -> Bool { available }
    func recordingContextAvailable() async throws -> Bool { takesContext }
    func postRecording(_ request: RecordingPartRequest) async throws -> RecordingView {
        if let postError { throw postError }
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

private final class StubUploads: RecordingUploadTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var stored: [String: Data] = [:]
    var error: Error?

    func putUpload(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        if let error { throw error }
        lock.withLock { stored[id] = sealed }
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
                         last: Bool, audio: String) async throws -> WatchRecordingPartInfo {
        let source = directory.appendingPathComponent(UUID().uuidString)
        try Data(audio.utf8).write(to: source)
        let info = WatchRecordingPartInfo(recordingID: id, part: part, last: last,
                                          startedAt: Date(timeIntervalSince1970: 1_800_000_000 + Double(part) * 900),
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

/// A clock a test moves by hand.
private final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value = Date(timeIntervalSince1970: 1_800_100_000)
    var now: Date { lock.withLock { value } }
    func advance(_ seconds: TimeInterval) { lock.withLock { value = value.addingTimeInterval(seconds) } }
}
