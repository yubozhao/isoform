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

    private func make(_ hub: StubRecordingHub, _ uploads: StubUploads) -> (WatchRecordingInbox, WatchRecordingForwarder) {
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("inbox"))
        let context = context()
        return (inbox, WatchRecordingForwarder(inbox: inbox, hub: hub, relay: uploads, session: { context }))
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
        XCTAssertEqual(WatchRecordingsCopy.length(1800), "30:00")
        XCTAssertEqual(WatchRecordingsCopy.length(3725), "1:02:05")
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
        for _ in 0..<WatchRecordingInbox.maxRefusals { _ = await forwarder.drain() }
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

    func testAPartTheComputerCallsInvalidStopsBeingRetriedButKeepsItsAudio() async throws {
        let hub = StubRecordingHub()
        let uploads = StubUploads()
        let (inbox, forwarder) = make(hub, uploads)
        let id = WatchRecordingWire.newRecordingID()
        let info = try await deliver(inbox, forwarder, id: id, part: 0, last: true, audio: "audio")
        await hub.set(postError: HubRequestError.rejected(status: 400, message: "the recording is not valid"))
        for _ in 0..<WatchRecordingInbox.maxRefusals { _ = await forwarder.drain() }
        let before = uploads.count
        _ = await forwarder.drain()
        XCTAssertEqual(uploads.count, before, "a refused part is not uploaded for ever")
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))
        let states = await forwarder.currentStates()
        XCTAssertEqual(states[id], .failed)
    }
}
