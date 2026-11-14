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

    func testOlderComputerOmitsTheRecordingsCapability() throws {
        XCTAssertNil(try JSONDecoder().decode(HubStatus.self, from: Data("{}".utf8)).recordings)
    }

    func testRecordingsRouteIsAGetWithItsIDs() {
        let route = HubRoute.recordings(ids: ["a", "b"])
        XCTAssertEqual(route.method, "GET")
        XCTAssertEqual(route.path, "/recordings")
        XCTAssertEqual(route.query, "ids=a,b")
        XCTAssertEqual(HubRoute.recordings(ids: []).query, "")
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

    func recordingsAvailable() async throws -> Bool { available }
    func postRecording(_ request: RecordingPartRequest) async throws -> RecordingView {
        if let postError { throw postError }
        posted.append(request)
        return RecordingView(id: request.recordingId, state: answerState)
    }
    func recordingStates(ids: [String]) async throws -> [RecordingView] { listed.filter { ids.contains($0.id) } }
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

        await hub.set(postError: HubRequestError.noAnswer)
        var outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertTrue(FileManager.default.fileExists(atPath: inbox.audioURL(info).path))

        // Paused observing on the computer answers 409: also kept.
        await hub.set(postError: HubRequestError.conflict(message: "observing is paused on this computer"))
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(inbox.pending().count, 1)

        uploads.error = RelayClientError.offline
        await hub.set(postError: nil)
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .waiting)

        uploads.error = nil
        outcome = await forwarder.drain()
        XCTAssertEqual(outcome, .drained)
        XCTAssertTrue(inbox.pending().isEmpty)
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
