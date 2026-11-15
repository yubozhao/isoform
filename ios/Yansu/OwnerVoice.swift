import AVFoundation
import Foundation

// The owner's voice, added from the phone (PROTOCOL.md section 25).
//
// The phone records a short sample, seals it with a fresh key, puts the ciphertext on the relay
// and sends the key to the computer inside the sealed POST /voice/samples request. The computer
// keeps a voice fingerprint and no audio. The phone's own file is removed as soon as the attempt
// ends, whatever the answer, so no copy of the sample stays here; a failed attempt is recorded
// again. The key of a copy the computer never took is dropped with the attempt, so that copy
// cannot be opened by anyone and the relay removes it after 48 hours.

/// One enrolled sample as the computer lists it. No fingerprint and no audio is ever sent.
struct VoiceSample: Decodable, Equatable, Identifiable, Sendable {
    let id: Int64
    let audioSeconds: Double
    let capturedAt: String
    var fromPhone: Bool? = nil

    var date: Date? { RelayTime.parse(capturedAt) }
}

/// The answer to GET /voice.
struct VoiceList: Decodable, Equatable, Sendable {
    /// False while the computer has no speaker model yet; a sample cannot be added until it has.
    let modelReady: Bool
    let samples: [VoiceSample]
}

/// The body of POST /voice/samples.
struct VoiceSampleRequest: Encodable, Equatable, Sendable {
    let idempotencyKey: String
    let upload: RecordingPartRequest.Upload
    let sha256: String
    let bytes: Int
    let mime: String
    let durationSeconds: Double
}

/// The answer to POST /voice/forget.
struct VoiceForgetResult: Decodable, Equatable, Sendable {
    let removed: Int
    let samples: [VoiceSample]
}

protocol OwnerVoiceHubAPI: Sendable {
    func voice() async throws -> VoiceList
    func addVoiceSample(_ request: VoiceSampleRequest) async throws -> VoiceSample
    /// Removes one sample, or every sample of the owner when `id` is nil.
    func forgetVoice(id: Int64?, idempotencyKey: String) async throws -> VoiceForgetResult
}

extension HubChannel: OwnerVoiceHubAPI {
    func voice() async throws -> VoiceList {
        try await perform(.voice).payload(VoiceList.self)
    }

    func addVoiceSample(_ request: VoiceSampleRequest) async throws -> VoiceSample {
        let body = try JSONEncoder().encode(request)
        // The computer fetches the sample from the relay and reads it before it answers.
        return try await perform(.write(path: "/voice/samples", body: String(decoding: body, as: UTF8.self)), timeout: 90)
            .payload(VoiceSample.self)
    }

    func forgetVoice(id: Int64?, idempotencyKey: String) async throws -> VoiceForgetResult {
        struct Body: Encodable {
            let idempotencyKey: String
            let id: Int64
        }
        let body = try JSONEncoder().encode(Body(idempotencyKey: idempotencyKey, id: id ?? 0))
        return try await perform(.write(path: "/voice/forget", body: String(decoding: body, as: UTF8.self)))
            .payload(VoiceForgetResult.self)
    }
}

enum OwnerVoiceError: Error, Equatable {
    /// Under 3 seconds of speech.
    case tooShort
    /// The computer said the sample is not usable.
    case refused
    /// The computer has no speaker model yet.
    case modelMissing
    /// This pairing may not add a voice sample.
    case notAllowed
    /// The computer or the relay could not be reached.
    case unreachable
    case microphoneDenied
}

enum OwnerVoiceLimits {
    static let minSeconds = 3.0
    static let maxSeconds = 30.0
    static let maxBytes = 4 << 20
    static let mime = "audio/mp4"
}

/// Sends one recorded sample to the computer and removes the phone's file.
struct OwnerVoiceEnroller: Sendable {
    let hub: OwnerVoiceHubAPI
    let relay: RecordingUploadTransport
    let session: @Sendable () throws -> SessionContext

    init(hub: OwnerVoiceHubAPI, relay: RecordingUploadTransport = RelayClient.shared,
         session: @escaping @Sendable () throws -> SessionContext) {
        self.hub = hub
        self.relay = relay
        self.session = session
    }

    func send(file: URL, durationSeconds: Double) async throws -> VoiceSample {
        // The sample leaves the phone's storage when this returns or throws.
        defer { try? FileManager.default.removeItem(at: file) }
        guard durationSeconds >= OwnerVoiceLimits.minSeconds else { throw OwnerVoiceError.tooShort }
        guard let plaintext = try? Data(contentsOf: file), !plaintext.isEmpty,
              plaintext.count <= OwnerVoiceLimits.maxBytes else { throw OwnerVoiceError.refused }
        do {
            let context = try session()
            let (sealed, upload) = try RecordingSeal.seal(plaintext, hubID: context.hubID, deviceID: context.deviceID)
            try await relay.putUpload(context.credentials, id: upload.id, sealed: sealed)
            let request = VoiceSampleRequest(
                idempotencyKey: "voice-\(upload.id)", upload: upload, sha256: RelayCrypto.sha256Hex(plaintext),
                bytes: plaintext.count, mime: OwnerVoiceLimits.mime,
                durationSeconds: min(durationSeconds, OwnerVoiceLimits.maxSeconds))
            do {
                return try await hub.addVoiceSample(request)
            } catch HubRequestError.noAnswer {
                // The request is receipted: a repeat after a lost answer replays the stored sample.
                return try await hub.addVoiceSample(request)
            }
        } catch let error as HubRequestError {
            switch error {
            case .rejected(let status, _) where status == 400: throw OwnerVoiceError.tooShort
            case .rejected(let status, _) where status == 404: throw OwnerVoiceError.unreachable
            case .conflict: throw OwnerVoiceError.modelMissing
            case .forbidden: throw OwnerVoiceError.notAllowed
            default: throw OwnerVoiceError.unreachable
            }
        } catch RelayClientError.payloadTooLarge {
            throw OwnerVoiceError.refused
        } catch let error as OwnerVoiceError {
            throw error
        } catch {
            throw OwnerVoiceError.unreachable
        }
    }
}

/// What records the sample. The app uses the microphone; tests replace it.
@MainActor
protocol OwnerVoiceRecording: AnyObject {
    /// Asks for the microphone if needed and starts. `onLimit` runs when the longest sample is reached.
    func start(onLimit: @escaping @MainActor () -> Void) async throws
    /// Stops and hands over the file and its length.
    func stop() -> (file: URL, seconds: Double)?
    /// Stops and removes what was recorded.
    func cancel()
}

@MainActor
final class OwnerVoiceMicrophone: NSObject, OwnerVoiceRecording, AVAudioRecorderDelegate {
    private var recorder: AVAudioRecorder?
    private var startedAt: Date?
    private var onLimit: (@MainActor () -> Void)?

    func start(onLimit: @escaping @MainActor () -> Void) async throws {
        let allowed = await withCheckedContinuation { continuation in
            AVAudioSession.sharedInstance().requestRecordPermission { continuation.resume(returning: $0) }
        }
        guard allowed else { throw OwnerVoiceError.microphoneDenied }
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("voice-sample-\(UUID().uuidString).m4a")
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC, AVSampleRateKey: 16_000, AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: 48_000,
        ]
        do {
            try AVAudioSession.sharedInstance().setCategory(.record, mode: .measurement, options: .duckOthers)
            try AVAudioSession.sharedInstance().setActive(true, options: .notifyOthersOnDeactivation)
            let recorder = try AVAudioRecorder(url: file, settings: settings)
            recorder.delegate = self
            guard recorder.record(forDuration: OwnerVoiceLimits.maxSeconds) else { throw OwnerVoiceError.unreachable }
            try? (file as NSURL).setResourceValue(URLFileProtection.complete, forKey: .fileProtectionKey)
            self.recorder = recorder
            self.startedAt = Date()
            self.onLimit = onLimit
        } catch {
            release()
            try? FileManager.default.removeItem(at: file)
            throw OwnerVoiceError.microphoneDenied
        }
    }

    func stop() -> (file: URL, seconds: Double)? {
        guard let recorder, let startedAt else { return nil }
        let file = recorder.url
        recorder.stop()
        release()
        let measured = (try? AVAudioPlayer(contentsOf: file))?.duration
        let seconds = measured ?? Date().timeIntervalSince(startedAt)
        return (file, min(seconds, OwnerVoiceLimits.maxSeconds))
    }

    func cancel() {
        guard let recorder else { return }
        let file = recorder.url
        recorder.stop()
        release()
        try? FileManager.default.removeItem(at: file)
    }

    private func release() {
        recorder?.delegate = nil
        recorder = nil
        startedAt = nil
        onLimit = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    nonisolated func audioRecorderDidFinishRecording(_ recorder: AVAudioRecorder, successfully flag: Bool) {
        Task { @MainActor in
            guard self.recorder === recorder else { return }
            self.onLimit?()
        }
    }
}

/// The state behind the phone's "Your voice" page.
@MainActor
final class OwnerVoiceModel: ObservableObject {
    enum Phase: Equatable {
        case idle
        case recording(since: Date)
        case sending
    }

    enum Notice: Equatable {
        case added
        case removed
        case failed(OwnerVoiceError)
    }

    @Published private(set) var samples: [VoiceSample] = []
    @Published private(set) var loaded = false
    @Published private(set) var modelReady = true
    @Published private(set) var phase: Phase = .idle
    @Published private(set) var notice: Notice?

    private let hub: OwnerVoiceHubAPI
    private let enroller: OwnerVoiceEnroller
    private let recorder: OwnerVoiceRecording

    init(hub: OwnerVoiceHubAPI, enroller: OwnerVoiceEnroller, recorder: OwnerVoiceRecording) {
        self.hub = hub
        self.enroller = enroller
        self.recorder = recorder
    }

    var canRecord: Bool { loaded && modelReady && phase == .idle }

    func load() async {
        do {
            let list = try await hub.voice()
            samples = list.samples
            modelReady = list.modelReady
            loaded = true
        } catch {
            if !loaded { notice = .failed(.unreachable) }
        }
    }

    func startRecording() async {
        guard canRecord else { return }
        notice = nil
        do {
            try await recorder.start(onLimit: { [weak self] in
                Task { await self?.stopAndSend() }
            })
            phase = .recording(since: Date())
        } catch {
            notice = .failed((error as? OwnerVoiceError) ?? .microphoneDenied)
        }
    }

    func stopAndSend() async {
        guard case .recording = phase else { return }
        guard let taken = recorder.stop() else {
            phase = .idle
            return
        }
        phase = .sending
        do {
            let sample = try await enroller.send(file: taken.file, durationSeconds: taken.seconds)
            samples.removeAll { $0.id == sample.id }
            samples.insert(sample, at: 0)
            notice = .added
        } catch {
            notice = .failed((error as? OwnerVoiceError) ?? .unreachable)
        }
        phase = .idle
    }

    /// Leaving the page while recording drops the sample.
    func leave() {
        if case .recording = phase {
            recorder.cancel()
            phase = .idle
        }
    }

    /// Removes one sample, or every sample of the owner when `id` is nil.
    func forget(id: Int64?) async {
        guard phase == .idle else { return }
        do {
            let result = try await hub.forgetVoice(id: id, idempotencyKey: "voice-forget-\(UUID().uuidString.lowercased())")
            samples = result.samples
            notice = .removed
        } catch {
            notice = .failed(.unreachable)
        }
    }
}
