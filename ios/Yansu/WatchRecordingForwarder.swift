import CryptoKit
import Foundation

// Recordings from the watch, on their way to the computer (PROTOCOL.md section 24).
//
// The watch hands each finished part to the phone as a file. The phone keeps it in its own
// folder until the computer answers that it holds it: it seals the part with a fresh key, puts
// the ciphertext on the relay, and sends the key to the computer inside the sealed
// POST /recordings request. Nothing is transcribed or read on the phone, and the audio leaves
// the phone only as ciphertext.

/// The body of POST /recordings.
struct RecordingPartRequest: Encodable, Equatable, Sendable {
    struct Upload: Codable, Equatable, Sendable {
        let id: String
        let key: String
        let nonce: String
    }
    struct Device: Encodable, Equatable, Sendable {
        let model: String
    }
    let idempotencyKey: String
    let recordingId: String
    let part: Int
    let last: Bool
    let upload: Upload
    let sha256: String
    let bytes: Int
    let mime: String
    let startedAt: String
    let durationSeconds: Double
    let source: String
    var device: Device?
}

/// The computer's view of a recording (24.3).
struct RecordingView: Decodable, Equatable, Sendable {
    let id: String
    let state: String
    var partsReceived: Int? = nil
    var parts: Int? = nil
    var words: Int? = nil
}

struct RecordingList: Decodable, Equatable, Sendable {
    let recordings: [RecordingView]
}

protocol RecordingHubAPI: Sendable {
    func recordingsAvailable() async throws -> Bool
    func postRecording(_ request: RecordingPartRequest) async throws -> RecordingView
    func recordingStates(ids: [String]) async throws -> [RecordingView]
}

extension HubChannel: RecordingHubAPI {
    func recordingsAvailable() async throws -> Bool { try await status().recordings == true }

    func postRecording(_ request: RecordingPartRequest) async throws -> RecordingView {
        let body = try JSONEncoder().encode(request)
        // The computer fetches the part from the relay before it answers.
        return try await perform(.write(path: "/recordings", body: String(decoding: body, as: UTF8.self)), timeout: 150)
            .payload(RecordingView.self)
    }

    func recordingStates(ids: [String]) async throws -> [RecordingView] {
        guard ids.allSatisfy(WatchRecordingWire.isRecordingID) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.recordings(ids: ids)).payload(RecordingList.self).recordings
    }
}

protocol RecordingUploadTransport: Sendable {
    func putUpload(_ credentials: RelayCredentials, id: String, sealed: Data) async throws
}

extension RelayClient: RecordingUploadTransport {}

enum RecordingSeal {
    static func aad(hubID: String, deviceID: String, uploadID: String) -> String {
        "yansu-upload-v1|\(hubID)|\(deviceID)|\(uploadID)"
    }

    /// Seals one part for the computer. Returns the ciphertext and the coordinates the request carries.
    static func seal(_ plaintext: Data, hubID: String, deviceID: String,
                     uploadID: String = UUID().uuidString.lowercased(),
                     key: Data = RelayCrypto.randomBytes(32),
                     nonce: Data = RelayCrypto.randomBytes(12)) throws -> (sealed: Data, upload: RecordingPartRequest.Upload) {
        let sealed = try RelayCrypto.seal(plaintext, key: key, nonce: nonce,
                                          aad: aad(hubID: hubID, deviceID: deviceID, uploadID: uploadID))
        return (sealed, .init(id: uploadID, key: RelayCrypto.base64URLEncode(key), nonce: RelayCrypto.base64URLEncode(nonce)))
    }
}

/// The parts waiting on the phone: each is an audio file with its description beside it.
final class WatchRecordingInbox: @unchecked Sendable {
    struct Pending: Codable, Equatable, Sendable {
        var info: WatchRecordingPartInfo
        /// How many times the computer refused this part as invalid.
        var refusals: Int
        /// The sealed copy already on the relay, reused until the computer takes it or the relay
        /// no longer has it. Without it every retry (observing paused, the computer asleep, a lost
        /// answer) would put another copy on the relay, each kept 48 hours against the device quota.
        var held: HeldUpload? = nil
    }

    /// A part's upload on the relay and the pairing it was sealed for.
    struct HeldUpload: Codable, Equatable, Sendable {
        var upload: RecordingPartRequest.Upload
        var hubID: String
        var deviceID: String
    }

    static let maxRefusals = 5

    let directory: URL
    private let fileManager: FileManager
    private let lock = NSLock()

    init(directory: URL, fileManager: FileManager = .default) {
        self.directory = directory
        self.fileManager = fileManager
        try? fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    private func base(_ info: WatchRecordingPartInfo) -> String { "\(info.recordingID)-\(info.part)" }
    func audioURL(_ info: WatchRecordingPartInfo) -> URL { directory.appendingPathComponent(base(info) + ".m4a") }
    private func metaURL(_ info: WatchRecordingPartInfo) -> URL { directory.appendingPathComponent(base(info) + ".json") }

    /// Takes a file the watch delivered. The transfer's own file is removed by the system when
    /// the delegate returns, so it is copied here first. A part already held is left as it is.
    func accept(file: URL, info: WatchRecordingPartInfo) throws {
        try lock.withLock {
            let target = audioURL(info)
            if !fileManager.fileExists(atPath: target.path) {
                try fileManager.copyItem(at: file, to: target)
                try? (target as NSURL).setResourceValue(URLFileProtection.completeUntilFirstUserAuthentication,
                                                        forKey: .fileProtectionKey)
            }
            try write(Pending(info: info, refusals: 0))
        }
    }

    private func write(_ pending: Pending) throws {
        try JSONEncoder().encode(pending).write(to: metaURL(pending.info),
                                               options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    /// Oldest recording first, parts in order.
    func pending() -> [Pending] {
        lock.withLock {
            let names = (try? fileManager.contentsOfDirectory(atPath: directory.path)) ?? []
            return names.filter { $0.hasSuffix(".json") }.compactMap { name -> Pending? in
                guard let data = try? Data(contentsOf: directory.appendingPathComponent(name)),
                      let pending = try? JSONDecoder().decode(Pending.self, from: data),
                      fileManager.fileExists(atPath: audioURL(pending.info).path) else { return nil }
                return pending
            }.sorted {
                ($0.info.startedAt, $0.info.recordingID, $0.info.part) < ($1.info.startedAt, $1.info.recordingID, $1.info.part)
            }
        }
    }

    func remove(_ info: WatchRecordingPartInfo) {
        lock.withLock {
            try? fileManager.removeItem(at: audioURL(info))
            try? fileManager.removeItem(at: metaURL(info))
        }
    }

    func refused(_ pending: Pending) {
        lock.withLock {
            var next = pending
            next.refusals += 1
            // A refused part is sealed again next time.
            next.held = nil
            try? write(next)
        }
    }

    /// Remembers the upload a part now has on the relay.
    func hold(_ held: HeldUpload?, for info: WatchRecordingPartInfo) {
        lock.withLock {
            guard let data = try? Data(contentsOf: metaURL(info)),
                  var pending = try? JSONDecoder().decode(Pending.self, from: data) else { return }
            pending.held = held
            try? write(pending)
        }
    }
}

/// Sends waiting parts to the computer, one at a time, and keeps the state of recent recordings.
actor WatchRecordingForwarder {
    enum Outcome: Equatable, Sendable {
        /// Nothing was waiting, or everything waiting was sent.
        case drained
        /// The computer or the relay could not be reached, or is not ready; parts stay on the phone.
        case waiting
        /// This computer does not take recordings yet.
        case unsupported
    }

    static let keptStates = 50

    private let inbox: WatchRecordingInbox
    private let hub: RecordingHubAPI
    private let relay: RecordingUploadTransport
    private let session: @Sendable () throws -> SessionContext
    private let statesURL: URL
    private var states: [String: WatchRecordingRemoteState]
    private var order: [String]
    private var running = false
    private var again = false
    private var onStates: (@Sendable ([String: WatchRecordingRemoteState]) -> Void)?

    private struct Stored: Codable {
        var order: [String]
        var states: [String: WatchRecordingRemoteState]
        var details: [String: Detail]?
    }

    /// What the phone knows about a recording beyond its state: when it began and the parts seen.
    private struct Detail: Codable {
        var startedAt: Date
        var parts: [Int: Double]
    }

    private var details: [String: Detail] = [:]
    private(set) var lastOutcome: Outcome = .drained

    init(inbox: WatchRecordingInbox, hub: RecordingHubAPI, relay: RecordingUploadTransport = RelayClient.shared,
         session: @escaping @Sendable () throws -> SessionContext) {
        self.inbox = inbox
        self.hub = hub
        self.relay = relay
        self.session = session
        self.statesURL = inbox.directory.appendingPathComponent("states.json")
        if let data = try? Data(contentsOf: statesURL), let stored = try? JSONDecoder().decode(Stored.self, from: data) {
            states = stored.states
            order = stored.order
            details = stored.details ?? [:]
        } else {
            states = [:]
            order = []
        }
    }

    func onStatesChanged(_ handler: @escaping @Sendable ([String: WatchRecordingRemoteState]) -> Void) {
        onStates = handler
    }

    func currentStates() -> [String: WatchRecordingRemoteState] { states }

    /// A part arrived from the watch.
    func received(_ info: WatchRecordingPartInfo) {
        var detail = details[info.recordingID] ?? Detail(startedAt: info.startedAt, parts: [:])
        detail.startedAt = min(detail.startedAt, info.startedAt)
        detail.parts[info.part] = info.durationSeconds
        details[info.recordingID] = detail
        if states[info.recordingID] == .onPhone { save() }
        set(info.recordingID, .onPhone)
    }

    /// Recent recordings for the phone's list, newest first.
    func recordings() -> [PhoneRecording] {
        order.compactMap { id -> PhoneRecording? in
            guard let state = states[id], let detail = details[id] else { return nil }
            return PhoneRecording(id: id, startedAt: detail.startedAt,
                                  durationSeconds: detail.parts.values.reduce(0, +), state: state)
        }.sorted { $0.startedAt > $1.startedAt }
    }

    /// Sends what is waiting. A call while one runs asks for one more pass and returns.
    @discardableResult
    func drain() async -> Outcome {
        if running {
            again = true
            return .waiting
        }
        running = true
        defer { running = false }
        var outcome = await pass()
        while again {
            again = false
            outcome = await pass()
        }
        lastOutcome = outcome
        return outcome
    }

    private func pass() async -> Outcome {
        let pending = inbox.pending().filter { $0.refusals < WatchRecordingInbox.maxRefusals }
        if !pending.isEmpty {
            do {
                guard try await hub.recordingsAvailable() else { return .unsupported }
            } catch {
                return .waiting
            }
        }
        for item in pending {
            do {
                try await send(item)
            } catch HubRequestError.rejected(let status, _) where status == 400 {
                // The computer says this part is not valid. Count it and move on; the audio stays.
                inbox.refused(item)
                if item.refusals + 1 >= WatchRecordingInbox.maxRefusals { set(item.info.recordingID, .failed) }
            } catch RelayClientError.payloadTooLarge {
                inbox.refused(item)
            } catch {
                // Offline, asleep, paused, a full quota, an upload the relay lost: try again later.
                return .waiting
            }
        }
        await refreshStates()
        return .drained
    }

    private func send(_ item: WatchRecordingInbox.Pending) async throws {
        let context = try session()
        let info = item.info
        let plaintext = try Data(contentsOf: inbox.audioURL(info), options: .mappedIfSafe)
        let upload: RecordingPartRequest.Upload
        if let held = item.held, held.hubID == context.hubID, held.deviceID == context.deviceID {
            upload = held.upload
        } else {
            let (sealed, fresh) = try RecordingSeal.seal(plaintext, hubID: context.hubID, deviceID: context.deviceID)
            try await relay.putUpload(context.credentials, id: fresh.id, sealed: sealed)
            inbox.hold(.init(upload: fresh, hubID: context.hubID, deviceID: context.deviceID), for: info)
            upload = fresh
        }
        let request = RecordingPartRequest(
            idempotencyKey: "rec-\(info.recordingID)-\(info.part)", recordingId: info.recordingID, part: info.part,
            last: info.last, upload: upload, sha256: RelayCrypto.sha256Hex(plaintext), bytes: plaintext.count,
            mime: WatchRecordingWire.mime, startedAt: RecordingTime.format(info.startedAt),
            durationSeconds: info.durationSeconds, source: "watch", device: info.model.map { .init(model: $0) })
        let view: RecordingView
        do {
            view = try await hub.postRecording(request)
        } catch HubRequestError.rejected(let status, _) where status == 404 {
            // upload_missing: the relay no longer has the copy; the next pass uploads a new one.
            inbox.hold(nil, for: info)
            throw HubRequestError.rejected(status: status, message: "upload_missing")
        }
        inbox.remove(info)
        // Parts still waiting here keep the recording "on phone" until the last one is across.
        let more = inbox.pending().contains { $0.info.recordingID == info.recordingID }
        set(info.recordingID, more ? .onPhone : WatchRecordingRemoteState(computerState: view.state))
    }

    /// Asks the computer about recordings that are across but not yet transcribed.
    func refreshStates() async {
        let open = order.filter { states[$0] == .onComputer }
        guard !open.isEmpty, let views = try? await hub.recordingStates(ids: Array(open.prefix(50))) else { return }
        for view in views where states[view.id] == .onComputer {
            set(view.id, WatchRecordingRemoteState(computerState: view.state))
        }
    }

    private func set(_ id: String, _ state: WatchRecordingRemoteState) {
        guard states[id] != state else { return }
        states[id] = state
        order.removeAll { $0 == id }
        order.append(id)
        while order.count > Self.keptStates {
            let dropped = order.removeFirst()
            states.removeValue(forKey: dropped)
            details.removeValue(forKey: dropped)
        }
        save()
        onStates?(states)
    }

    private func save() {
        if let data = try? JSONEncoder().encode(Stored(order: order, states: states, details: details)) {
            try? data.write(to: statesURL, options: .atomic)
        }
    }
}

/// One row of the phone's list of watch recordings.
struct PhoneRecording: Equatable, Identifiable, Sendable {
    let id: String
    let startedAt: Date
    let durationSeconds: Double
    let state: WatchRecordingRemoteState
}

enum RecordingTime {
    /// RFC 3339 UTC with second precision, as the computer parses it.
    static func format(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        formatter.timeZone = TimeZone(identifier: "UTC")
        return formatter.string(from: date)
    }
}
