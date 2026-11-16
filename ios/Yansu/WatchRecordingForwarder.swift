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
    /// Heart rate and coarse location of the part (24.4), only for a computer that takes it.
    var context: WatchRecordingContext?
    /// The moments the wearer marked in the part (24.9), only for a computer that takes them.
    var marks: [Double]?
}

/// The computer's view of a recording (24.3).
struct RecordingView: Decodable, Equatable, Sendable {
    let id: String
    let state: String
    var partsReceived: Int? = nil
    var parts: Int? = nil
    var words: Int? = nil
    /// The names of the conversations heard in it, once the computer has named any (24.7).
    var conversations: [String]? = nil
}

struct RecordingList: Decodable, Equatable, Sendable {
    let recordings: [RecordingView]
}

/// What one recording heard (PROTOCOL.md 24.5). The phone shows it and does not keep it.
struct RecordingTranscript: Decodable, Equatable, Sendable {
    struct Line: Decodable, Equatable, Sendable {
        let at: String
        var speaker: String? = nil
        let text: String
    }
    let id: String
    var state: String? = nil
    let lines: [Line]
    var truncated: Bool? = nil
}

protocol RecordingHubAPI: Sendable {
    func recordingsAvailable() async throws -> Bool
    /// The computer takes the heart rate and location of a part (PROTOCOL.md 24.4).
    func recordingContextAvailable() async throws -> Bool
    func postRecording(_ request: RecordingPartRequest) async throws -> RecordingView
    func recordingStates(ids: [String]) async throws -> [RecordingView]
    /// The computer serves a recording's transcript and its delete (PROTOCOL.md 24.5).
    func recordingDetailAvailable() async throws -> Bool
    func recordingTranscript(id: String) async throws -> RecordingTranscript
    func deleteRecording(id: String) async throws -> RecordingView
    /// Whether observing is paused on the computer, and since when (PROTOCOL.md 24.8).
    func observingPause() async throws -> WatchPause?
    /// The computer takes the moments the wearer marked in a part (PROTOCOL.md 24.9).
    func recordingMarksAvailable() async throws -> Bool
    /// The phone may pause the computer (PROTOCOL.md 24.10).
    func observingPauseControlAvailable() async throws -> Bool
    func pauseObserving(minutes: Int) async throws
    func resumeObserving() async throws
    /// What the watch heard as conversations (PROTOCOL.md 24.11); nil from a computer that does not serve them.
    func recordingConversations() async throws -> WatchConversationsAnswer?
    func recordingConversation(id: String) async throws -> WatchConversationDetail?
    /// "Notes for long talks" on the computer (PROTOCOL.md 24.12); nil when it has no such setting.
    func watchNotes() async throws -> Bool?
    func setWatchNotes(_ on: Bool) async throws -> Bool
    /// Says who a speaker is (PROTOCOL.md 24.13); nil from a computer that does not take it.
    func answerSpeaker(conversation: String, speaker: String, answer: String, name: String) async throws -> WatchConversation?
    /// "Listen on your watch" (PROTOCOL.md 24.14); nil from a computer that has no such setting.
    func watchListening() async throws -> WatchListeningSetting?
    func turnWatchListeningOff() async throws -> WatchListeningSetting
    func watchListeningStarted(at: Date) async throws -> WatchListeningSetting
}

extension RecordingHubAPI {
    func observingPauseControlAvailable() async throws -> Bool { false }
    func pauseObserving(minutes: Int) async throws { throw HubRequestError.invalidIdentifier }
    func resumeObserving() async throws { throw HubRequestError.invalidIdentifier }
    func recordingMarksAvailable() async throws -> Bool { false }
    func observingPause() async throws -> WatchPause? { nil }
    func recordingDetailAvailable() async throws -> Bool { false }
    func recordingTranscript(id: String) async throws -> RecordingTranscript { throw HubRequestError.invalidIdentifier }
    func deleteRecording(id: String) async throws -> RecordingView { throw HubRequestError.invalidIdentifier }
}

extension HubChannel: RecordingHubAPI {
    func recordingsAvailable() async throws -> Bool { try await status().recordings == true }
    func recordingContextAvailable() async throws -> Bool { try await status().recordingContext == true }
    func observingPause() async throws -> WatchPause? { try await status().observingPause?.pause }
    func recordingMarksAvailable() async throws -> Bool { try await status().recordingMarks == true }
    func observingPauseControlAvailable() async throws -> Bool { try await status().observingPauseControl == true }

    func pauseObserving(minutes: Int) async throws {
        _ = try await perform(.write(path: "/observing/pause", body: "{\"minutes\":\(minutes)}"))
    }

    func resumeObserving() async throws {
        _ = try await perform(.write(path: "/observing/resume", body: "{}"))
    }

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

    func recordingDetailAvailable() async throws -> Bool { try await status().recordingDetail == true }

    func recordingTranscript(id: String) async throws -> RecordingTranscript {
        guard WatchRecordingWire.isRecordingID(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.recordingTranscript(id: id)).payload(RecordingTranscript.self)
    }

    func deleteRecording(id: String) async throws -> RecordingView {
        guard WatchRecordingWire.isRecordingID(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.write(path: "/recordings/delete", body: "{\"recordingId\":\"\(id)\"}"))
            .payload(RecordingView.self)
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

    /// Removes every part of a recording from the phone.
    func removeAll(recordingID: String) {
        for item in pending() where item.info.recordingID == recordingID { remove(item.info) }
    }

    /// The person asked to try again: parts the computer refused get another round.
    func resetRefusals(recordingID: String) {
        for item in pending() where item.info.recordingID == recordingID && item.refusals > 0 {
            lock.withLock {
                var next = item
                next.refusals = 0
                try? write(next)
            }
        }
    }

    /// The audio of a recording still on the phone, in order, and its size.
    func audio(recordingID: String) -> (urls: [URL], bytes: Int) {
        let urls = pending().filter { $0.info.recordingID == recordingID }.map { audioURL($0.info) }
        let bytes = urls.reduce(0) { $0 + (((try? fileManager.attributesOfItem(atPath: $1.path))?[.size] as? Int) ?? 0) }
        return (urls, bytes)
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
    private var onStates: (@Sendable ([String: WatchRecordingRemoteState], [String: [String]]) -> Void)?
    /// What the computer named the conversations in each recording.
    private var names: [String: [String]] = [:]
    /// Observing is paused on the computer, as it last said. Not kept across launches: a pause is
    /// asked about again rather than remembered.
    private(set) var pause: WatchPause?

    private struct Stored: Codable {
        var order: [String]
        var states: [String: WatchRecordingRemoteState]
        var details: [String: Detail]?
        var pendingDeletes: [String]?
        var names: [String: [String]]?
    }

    /// What the phone knows about a recording beyond its state: when it began and the parts seen.
    private struct Detail: Codable {
        var startedAt: Date
        var parts: [Int: Double]
        /// Some part of it reached the computer, so a delete has to reach the computer too.
        var crossed: Bool?
    }

    /// Recordings deleted here that the computer has not been told about yet.
    private var pendingDeletes: [String] = []
    /// The recording a part of which is being sent right now.
    private(set) var sending: String?
    private var onActivity: (@Sendable () -> Void)?

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
            pendingDeletes = stored.pendingDeletes ?? []
            names = stored.names ?? [:]
        } else {
            states = [:]
            order = []
        }
    }

    func onStatesChanged(_ handler: @escaping @Sendable ([String: WatchRecordingRemoteState], [String: [String]]) -> Void) {
        onStates = handler
    }

    /// Called when a send starts or ends, so the list can say "Sending to your computer".
    func onActivityChanged(_ handler: @escaping @Sendable () -> Void) {
        onActivity = handler
    }

    /// What the computer named the conversations in each recording, for the watch.
    func currentNames() -> [String: [String]] { names }

    func currentStates() -> [String: WatchRecordingRemoteState] { states }

    /// A part arrived from the watch.
    func received(_ info: WatchRecordingPartInfo) {
        if states[info.recordingID] == .deleted {
            // Deleted while this part was on its way from the watch: it is not kept.
            inbox.remove(info)
            return
        }
        var detail = details[info.recordingID] ?? Detail(startedAt: info.startedAt, parts: [:])
        detail.startedAt = min(detail.startedAt, info.startedAt)
        detail.parts[info.part] = info.durationSeconds
        details[info.recordingID] = detail
        if states[info.recordingID] == .onPhone { save() }
        set(info.recordingID, .onPhone)
    }

    /// Recent recordings for the phone's list, newest first. Deleted ones are left out.
    func recordings() -> [PhoneRecording] {
        order.compactMap { id -> PhoneRecording? in
            guard let state = states[id], state != .deleted, let detail = details[id] else { return nil }
            let audio = inbox.audio(recordingID: id)
            return PhoneRecording(id: id, startedAt: detail.startedAt,
                                  durationSeconds: detail.parts.values.reduce(0, +), state: state,
                                  partsOnPhone: audio.urls.count, bytesOnPhone: audio.bytes, sending: sending == id,
                                  names: names[id] ?? [])
        }.sorted { $0.startedAt > $1.startedAt }
    }

    /// The audio files of a recording that are still on the phone, in order.
    func audioOnPhone(_ id: String) -> [URL] { inbox.audio(recordingID: id).urls }

    /// Deletes a recording everywhere the phone can reach: its audio here, the watch's copy
    /// (through the state the watch reads), and the computer's copy with what was learned from it.
    func delete(_ id: String) async {
        guard WatchRecordingWire.isRecordingID(id), states[id] != .deleted else { return }
        let crossed = details[id]?.crossed == true || (states[id].map { $0 > .onPhone } ?? false)
        inbox.removeAll(recordingID: id)
        if crossed, !pendingDeletes.contains(id) { pendingDeletes.append(id) }
        set(id, .deleted)
        save()
        await flushDeletes()
    }

    /// Tells the computer about deletes it has not heard of. A computer that cannot be reached,
    /// or does not serve deletes yet, is asked again on the next pass.
    func flushDeletes() async {
        guard !pendingDeletes.isEmpty, (try? await hub.recordingDetailAvailable()) == true else { return }
        for id in pendingDeletes {
            do {
                _ = try await hub.deleteRecording(id: id)
            } catch HubRequestError.rejected(let status, _) where status == 404 || status == 400 {
                // The computer never had it, or no longer does.
            } catch {
                continue
            }
            pendingDeletes.removeAll { $0 == id }
            save()
        }
    }

    /// Whether a delete is still owed to the computer.
    func deletesWaiting() -> Int { pendingDeletes.count }

    /// The person asked to send a recording again after the computer refused it.
    func retry(_ id: String) async -> Outcome {
        inbox.resetRefusals(recordingID: id)
        if states[id] == .failed, !inbox.audio(recordingID: id).urls.isEmpty { set(id, .onPhone) }
        return await drain()
    }

    /// What the watch heard as conversations; nil when the computer does not serve them.
    func conversations() async throws -> WatchConversationsAnswer? { try await hub.recordingConversations() }
    func conversation(_ id: String) async throws -> WatchConversationDetail? { try await hub.recordingConversation(id: id) }
    func notesSetting() async throws -> Bool? { try await hub.watchNotes() }
    func setNotes(_ on: Bool) async throws -> Bool { try await hub.setWatchNotes(on) }
    func answerSpeaker(conversation: String, speaker: String, answer: String, name: String) async throws -> WatchConversation? {
        try await hub.answerSpeaker(conversation: conversation, speaker: speaker, answer: answer, name: name)
    }
    /// When the pause on the computer ends by itself; nil with no pause or no end.
    func pauseUntil() async -> Date? { (try? await hub.observingPause())?.until }

    /// What the recording heard, from the computer. Nil when the computer does not serve it.
    func transcript(_ id: String) async throws -> RecordingTranscript? {
        guard try await hub.recordingDetailAvailable() else { return nil }
        return try await hub.recordingTranscript(id: id)
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
        await flushDeletes()
        let pending = inbox.pending().filter { $0.refusals < WatchRecordingInbox.maxRefusals }
        if !pending.isEmpty {
            do {
                guard try await hub.recordingsAvailable() else { return .unsupported }
            } catch {
                return .waiting
            }
        }
        // An older computer refuses a request with fields it does not know, so the context is
        // sent only to one that says it takes it. Not knowing counts as no.
        var takesContext = false
        if pending.contains(where: { $0.info.context != nil }) {
            takesContext = (try? await hub.recordingContextAvailable()) ?? false
        }
        // The same holds for marks. With a computer that does not take them the part goes
        // without: the words matter more than the marks.
        var takesMarks = false
        if pending.contains(where: { $0.info.marks != nil }) {
            takesMarks = (try? await hub.recordingMarksAvailable()) ?? false
        }
        for item in pending {
            do {
                sending = item.info.recordingID
                onActivity?()
                defer {
                    sending = nil
                    onActivity?()
                }
                try await send(item, withContext: takesContext, withMarks: takesMarks)
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

    private func send(_ item: WatchRecordingInbox.Pending, withContext: Bool, withMarks: Bool = false) async throws {
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
            durationSeconds: info.durationSeconds, source: "watch", device: info.model.map { .init(model: $0) },
            context: withContext ? info.context : nil, marks: withMarks ? info.marks : nil)
        let view: RecordingView
        do {
            view = try await hub.postRecording(request)
        } catch HubRequestError.rejected(let status, _) where status == 404 {
            // upload_missing: the relay no longer has the copy; the next pass uploads a new one.
            inbox.hold(nil, for: info)
            throw HubRequestError.rejected(status: status, message: "upload_missing")
        }
        inbox.remove(info)
        if states[info.recordingID] == .deleted || view.state == "deleted" {
            // Deleted meanwhile, here or on the computer: nothing more of it is kept or sent.
            inbox.removeAll(recordingID: info.recordingID)
            set(info.recordingID, .deleted)
            return
        }
        if details[info.recordingID]?.crossed != true {
            details[info.recordingID]?.crossed = true
            save()
        }
        // Parts still waiting here keep the recording "on phone" until the last one is across.
        let more = inbox.pending().contains { $0.info.recordingID == info.recordingID }
        set(info.recordingID, more ? .onPhone : WatchRecordingRemoteState(computerState: view.state))
    }

    /// Asks the computer about recordings that are across: whether they are transcribed yet, and
    /// whether one was deleted there (then it is deleted here and on the watch too).
    /// Asks the computer whether observing is paused there, and tells the watch when that
    /// changed. A computer that cannot be reached leaves the last word standing.
    func refreshPause() async {
        let asked: WatchPause?
        do {
            asked = try await hub.observingPause()
        } catch {
            return
        }
        guard asked != pause else { return }
        pause = asked
        onStates?(states, names)
    }

    func currentPause() -> WatchPause? { pause }

    /// When "Listen on your watch" was turned off, as the computer last said; nil while it is on.
    private(set) var listenOffSince: Date?
    /// False when the computer has no such setting, so the phone shows no switch.
    private(set) var listeningKnown = false

    func refreshListening() async {
        guard let setting = try? await hub.watchListening() else { return }
        listeningKnown = true
        listenOffSince = setting.offSince
    }

    /// Turns listening off on the computer. It returns whether the computer took it.
    func turnListeningOff() async -> Bool {
        guard let setting = try? await hub.turnWatchListeningOff() else { return false }
        listeningKnown = true
        listenOffSince = setting.offSince ?? Date()
        return true
    }

    /// The watch is listening again, started there at `date`. Only a start after the off ends it.
    /// It returns whether the off was ended.
    func wearerStartedListening(at date: Date) async -> Bool {
        guard let since = listenOffSince, date > since, let setting = try? await hub.watchListeningStarted(at: date) else { return false }
        listenOffSince = setting.offSince
        return listenOffSince == nil
    }

    /// How many pause requests of the watch were carried; sent along so the watch hears each answer.
    private(set) var pauseSerial = 0

    /// The watch asked to pause Isoform, or to end the pause it started. The phone asks the
    /// computer, then reads what holds there and tells the watch. A computer that does not take
    /// this, or cannot be reached, leaves the watch with its own pause. It returns whether the
    /// computer was asked.
    @discardableResult
    func carry(_ request: WatchPauseRequest) async -> Bool {
        defer {
            // Tell the watch what holds now, also when nothing changed.
            pauseSerial += 1
            onStates?(states, names)
        }
        guard (try? await hub.observingPauseControlAvailable()) == true else { return false }
        do {
            switch request {
            case .pause(let minutes): try await hub.pauseObserving(minutes: minutes)
            case .resume: try await hub.resumeObserving()
            }
        } catch {
            // A pause made at the computer is not the phone's to end (409): what holds is read below.
        }
        await refreshPause()
        return true
    }

    func refreshStates() async {
        await refreshPause()
        await refreshListening()
        let across = order.reversed().filter { id in
            guard let state = states[id] else { return false }
            return state == .onComputer || state == .transcribed || (state == .failed && inbox.audio(recordingID: id).urls.isEmpty)
        }
        guard !across.isEmpty, let views = try? await hub.recordingStates(ids: Array(across.prefix(50))) else { return }
        var namesChanged = false
        for view in views {
            let next = WatchRecordingRemoteState(computerState: view.state)
            // The computer's word on the names is the whole truth: a name it no longer gives
            // (the hour's words were withdrawn there) is dropped here too.
            let named = next == .deleted ? [] : WatchRecordingWire.cleanNames(view.conversations ?? [])
            if (names[view.id] ?? []) != named {
                names[view.id] = named.isEmpty ? nil : named
                namesChanged = true
            }
            if next == .deleted {
                inbox.removeAll(recordingID: view.id)
                pendingDeletes.removeAll { $0 == view.id }
                set(view.id, .deleted)
            } else if states[view.id] == .onComputer {
                set(view.id, next)
            }
        }
        if namesChanged {
            save()
            onStates?(states, names)
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
            names.removeValue(forKey: dropped)
        }
        if state == .deleted { names.removeValue(forKey: id) }
        save()
        onStates?(states, names)
    }

    private func save() {
        if let data = try? JSONEncoder().encode(Stored(order: order, states: states, details: details,
                                                       pendingDeletes: pendingDeletes, names: names)) {
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
    /// Parts whose audio is still on this phone, and their size.
    var partsOnPhone: Int = 0
    var bytesOnPhone: Int = 0
    /// A part is being sent to the computer right now.
    var sending: Bool = false
    /// What the computer named the conversations heard in it.
    var names: [String] = []
}

enum RecordingTime {
    /// RFC 3339 UTC with second precision, as the computer parses it.
    static func format(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        formatter.timeZone = TimeZone(identifier: "UTC")
        return formatter.string(from: date)
    }

    static func parse(_ text: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: text)
    }
}
