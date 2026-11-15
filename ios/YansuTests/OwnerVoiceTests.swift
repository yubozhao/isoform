import XCTest
@testable import Yansu

private actor StubVoiceHub: OwnerVoiceHubAPI {
    var list = VoiceList(modelReady: true, samples: [])
    var posted: [VoiceSampleRequest] = []
    var forgotten: [Int64?] = []
    var addErrors: [Error] = []
    var listError: Error?
    private var nextID: Int64 = 40

    func setList(_ list: VoiceList) { self.list = list }
    func failAdding(_ errors: [Error]) { addErrors = errors }
    func failListing(_ error: Error?) { listError = error }

    func voice() async throws -> VoiceList {
        if let listError { throw listError }
        return list
    }

    func addVoiceSample(_ request: VoiceSampleRequest) async throws -> VoiceSample {
        posted.append(request)
        if !addErrors.isEmpty { throw addErrors.removeFirst() }
        nextID += 1
        let sample = VoiceSample(id: nextID, audioSeconds: request.durationSeconds,
                                 capturedAt: "2027-01-15T08:00:00Z", fromPhone: true)
        list = VoiceList(modelReady: list.modelReady, samples: [sample] + list.samples)
        return sample
    }

    func forgetVoice(id: Int64?, idempotencyKey: String) async throws -> VoiceForgetResult {
        forgotten.append(id)
        let kept = id == nil ? [] : list.samples.filter { $0.id != id }
        let removed = list.samples.count - kept.count
        list = VoiceList(modelReady: list.modelReady, samples: kept)
        return VoiceForgetResult(removed: removed, samples: kept)
    }
}

private final class StubVoiceUploads: RecordingUploadTransport, @unchecked Sendable {
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

@MainActor
private final class StubVoiceRecorder: OwnerVoiceRecording {
    var startError: Error?
    var next: (file: URL, seconds: Double)?
    var cancelled = 0
    var onLimit: (@MainActor () -> Void)?

    func start(onLimit: @escaping @MainActor () -> Void) async throws {
        if let startError { throw startError }
        self.onLimit = onLimit
    }
    func stop() -> (file: URL, seconds: Double)? { next }
    func cancel() { cancelled += 1 }
}

final class OwnerVoiceTests: XCTestCase {
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

    private func enroller(_ hub: StubVoiceHub, _ uploads: StubVoiceUploads) -> OwnerVoiceEnroller {
        let context = SessionContext(
            credentials: RelayCredentials(origin: URL(string: "https://relay.example")!, bearer: "b"),
            hubID: hubID, deviceID: deviceID,
            keys: RelayCrypto.SessionKeys(d2h: RelayCrypto.randomBytes(32), h2d: RelayCrypto.randomBytes(32)))
        return OwnerVoiceEnroller(hub: hub, relay: uploads, session: { context })
    }

    private func sample(_ audio: String = "my voice") throws -> URL {
        let file = directory.appendingPathComponent(UUID().uuidString + ".m4a")
        try Data(audio.utf8).write(to: file)
        return file
    }

    func testVoiceRouteIsAGetAndOlderComputersOmitTheCapability() throws {
        XCTAssertEqual(HubRoute.voice.method, "GET")
        XCTAssertEqual(HubRoute.voice.path, "/voice")
        XCTAssertEqual(HubRoute.voice.query, "")
        XCTAssertNil(try JSONDecoder().decode(HubStatus.self, from: Data("{}".utf8)).ownerVoice)
        XCTAssertEqual(try JSONDecoder().decode(HubStatus.self, from: Data(#"{"ownerVoice":true}"#.utf8)).ownerVoice, true)
    }

    func testTheComputersListDecodesAsTheProtocolWritesIt() throws {
        let json = #"{"modelReady":true,"samples":[{"id":12,"audioSeconds":8.1,"capturedAt":"2026-11-14T09:30:00Z","fromPhone":true},{"id":3,"audioSeconds":20,"capturedAt":"2026-11-01T09:30:00Z"}]}"#
        let list = try JSONDecoder().decode(VoiceList.self, from: Data(json.utf8))
        XCTAssertTrue(list.modelReady)
        XCTAssertEqual(list.samples.map(\.id), [12, 3])
        XCTAssertEqual(list.samples[0].fromPhone, true)
        XCTAssertNil(list.samples[1].fromPhone)
        XCTAssertNotNil(list.samples[0].date)
    }

    func testAddingAVoiceNeedsBothScopes() {
        XCTAssertTrue(SettingsView.allowsOwnerVoice(["memory:correct", "chat:write"]))
        XCTAssertFalse(SettingsView.allowsOwnerVoice(["memory:correct"]))
        XCTAssertFalse(SettingsView.allowsOwnerVoice(["chat:write", "brief:read"]))
    }

    func testASampleIsSealedSentAndRemovedFromThePhone() async throws {
        let hub = StubVoiceHub()
        let uploads = StubVoiceUploads()
        let file = try sample()

        let added = try await enroller(hub, uploads).send(file: file, durationSeconds: 9.4)
        XCTAssertEqual(added.fromPhone, true)

        let posted = await hub.posted
        let request = try XCTUnwrap(posted.first)
        XCTAssertEqual(posted.count, 1)
        XCTAssertEqual(request.mime, "audio/mp4")
        XCTAssertEqual(request.bytes, 8)
        XCTAssertEqual(request.durationSeconds, 9.4)
        XCTAssertEqual(request.sha256, RelayCrypto.sha256Hex(Data("my voice".utf8)))
        XCTAssertEqual(request.idempotencyKey, "voice-\(request.upload.id)")

        // What reached the relay is ciphertext that opens only with the key in the sealed request.
        let sealed = try XCTUnwrap(uploads.body(request.upload.id))
        XCTAssertNil(sealed.range(of: Data("my voice".utf8)))
        let opened = try RelayCrypto.unseal(sealed, key: RelayCrypto.base64URLDecode(request.upload.key),
                                            nonce: RelayCrypto.base64URLDecode(request.upload.nonce),
                                            aad: "yansu-upload-v1|\(hubID)|\(deviceID)|\(request.upload.id)")
        XCTAssertEqual(opened, Data("my voice".utf8))
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path), "the phone keeps no copy of the sample")
    }

    func testASampleThatCouldNotBeSentAlsoLeavesThePhone() async throws {
        let hub = StubVoiceHub()
        let uploads = StubVoiceUploads()
        uploads.error = URLError(.notConnectedToInternet)
        let file = try sample()
        do {
            _ = try await enroller(hub, uploads).send(file: file, durationSeconds: 10)
            XCTFail("an unreachable relay is an error")
        } catch {
            XCTAssertEqual(error as? OwnerVoiceError, .unreachable)
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path))
        let posted = await hub.posted
        XCTAssertTrue(posted.isEmpty)
    }

    func testAShortSampleIsNeverUploaded() async throws {
        let hub = StubVoiceHub()
        let uploads = StubVoiceUploads()
        let file = try sample()
        do {
            _ = try await enroller(hub, uploads).send(file: file, durationSeconds: 2.2)
            XCTFail("under 3 seconds is refused on the phone")
        } catch {
            XCTAssertEqual(error as? OwnerVoiceError, .tooShort)
        }
        XCTAssertEqual(uploads.count, 0)
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path))
    }

    func testTheComputersRefusalsAreToldApart() async throws {
        let cases: [(HubRequestError, OwnerVoiceError)] = [
            (.rejected(status: 400, message: "too short"), .tooShort),
            (.conflict(message: "voice_model_missing"), .modelMissing),
            (.forbidden, .notAllowed),
            (.rejected(status: 503, message: "not ready"), .unreachable),
        ]
        for (thrown, expected) in cases {
            let hub = StubVoiceHub()
            await hub.failAdding([thrown])
            do {
                _ = try await enroller(hub, StubVoiceUploads()).send(file: try sample(), durationSeconds: 8)
                XCTFail("\(thrown) is an error")
            } catch {
                XCTAssertEqual(error as? OwnerVoiceError, expected, "\(thrown)")
            }
        }
    }

    func testALostAnswerRepeatsTheSameRequestOnce() async throws {
        let hub = StubVoiceHub()
        let uploads = StubVoiceUploads()
        await hub.failAdding([HubRequestError.noAnswer])
        _ = try await enroller(hub, uploads).send(file: try sample(), durationSeconds: 8)
        let posted = await hub.posted
        XCTAssertEqual(posted.count, 2)
        XCTAssertEqual(posted[0], posted[1], "the repeat carries the same idempotency key and upload")
        XCTAssertEqual(uploads.count, 1)
    }

    @MainActor
    func testRecordingAddsTheSampleAndForgettingRemovesEverySample() async throws {
        let hub = StubVoiceHub()
        await hub.setList(VoiceList(modelReady: true, samples: [
            VoiceSample(id: 3, audioSeconds: 20, capturedAt: "2026-11-01T09:30:00Z"),
        ]))
        let recorder = StubVoiceRecorder()
        let model = OwnerVoiceModel(hub: hub, enroller: enroller(hub, StubVoiceUploads()), recorder: recorder)
        XCTAssertFalse(model.canRecord, "nothing is recorded before the computer answered")
        await model.load()
        XCTAssertEqual(model.samples.map(\.id), [3])
        XCTAssertTrue(model.canRecord)

        recorder.next = (try sample(), 12)
        await model.startRecording()
        guard case .recording = model.phase else { return XCTFail("recording") }
        await model.stopAndSend()
        XCTAssertEqual(model.phase, .idle)
        XCTAssertEqual(model.notice, .added)
        XCTAssertEqual(model.samples.count, 2)
        XCTAssertEqual(model.samples.first?.fromPhone, true)

        await model.forget(id: nil)
        XCTAssertTrue(model.samples.isEmpty)
        XCTAssertEqual(model.notice, .removed)
        let forgotten = await hub.forgotten
        XCTAssertEqual(forgotten, [nil])
    }

    @MainActor
    func testOneSampleCanBeRemoved() async throws {
        let hub = StubVoiceHub()
        await hub.setList(VoiceList(modelReady: true, samples: [
            VoiceSample(id: 12, audioSeconds: 8, capturedAt: "2026-11-14T09:30:00Z", fromPhone: true),
            VoiceSample(id: 3, audioSeconds: 20, capturedAt: "2026-11-01T09:30:00Z"),
        ]))
        let model = OwnerVoiceModel(hub: hub, enroller: enroller(hub, StubVoiceUploads()), recorder: StubVoiceRecorder())
        await model.load()
        await model.forget(id: 12)
        XCTAssertEqual(model.samples.map(\.id), [3])
    }

    @MainActor
    func testAComputerWithoutAVoiceModelCannotTakeASample() async throws {
        let hub = StubVoiceHub()
        await hub.setList(VoiceList(modelReady: false, samples: []))
        let recorder = StubVoiceRecorder()
        let model = OwnerVoiceModel(hub: hub, enroller: enroller(hub, StubVoiceUploads()), recorder: recorder)
        await model.load()
        XCTAssertFalse(model.canRecord)
        await model.startRecording()
        XCTAssertEqual(model.phase, .idle)
        XCTAssertNil(recorder.onLimit, "the microphone is not opened")
    }

    @MainActor
    func testADeniedMicrophoneAndLeavingThePageRecordNothing() async throws {
        let hub = StubVoiceHub()
        let recorder = StubVoiceRecorder()
        let model = OwnerVoiceModel(hub: hub, enroller: enroller(hub, StubVoiceUploads()), recorder: recorder)
        await model.load()

        recorder.startError = OwnerVoiceError.microphoneDenied
        await model.startRecording()
        XCTAssertEqual(model.phase, .idle)
        XCTAssertEqual(model.notice, .failed(.microphoneDenied))

        recorder.startError = nil
        await model.startRecording()
        model.leave()
        XCTAssertEqual(model.phase, .idle)
        XCTAssertEqual(recorder.cancelled, 1)
        let posted = await hub.posted
        XCTAssertTrue(posted.isEmpty)
    }

    @MainActor
    func testAnUnreachableComputerSaysSoInsteadOfShowingAnEmptyList() async throws {
        let hub = StubVoiceHub()
        await hub.failListing(HubRequestError.noAnswer)
        let model = OwnerVoiceModel(hub: hub, enroller: enroller(hub, StubVoiceUploads()), recorder: StubVoiceRecorder())
        await model.load()
        XCTAssertFalse(model.loaded)
        XCTAssertFalse(model.canRecord)
        XCTAssertEqual(model.notice, .failed(.unreachable))
    }
}
