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
        /// The relay that holds the part: "pinned" or "direct". Only for a computer that fetches
        /// from the relay the phone names; left out otherwise.
        var relay: String? = nil
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
    /// True when this is said before the upload is on the relay (24.2a): the computer keeps the
    /// request and fetches the part when the relay has it. Only for a computer that takes it.
    var ahead: Bool?
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

/// What the computer takes of a recording part, as its status says.
struct RecordingAbilities: Codable, Equatable, Sendable {
    var recordings = false
    var context = false
    var marks = false
    var relay = false
    var ahead = false
    /// The computer allows the send in a wake and takes its count (`recordingInWakeSend`).
    /// Absent in what an older app kept.
    var inWake: Bool? = nil
}

protocol RecordingHubAPI: Sendable {
    /// Everything a pass needs to know of the computer, in one request. On 2026-11-19 a pass
    /// asked for the status five times, one answer after the other, before it told the computer
    /// of the first part; the phone was awake for eight seconds, and one part was taken.
    func recordingAbilities() async throws -> RecordingAbilities
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
    /// The computer fetches a part from the relay the phone names (PROTOCOL.md 24.2).
    func recordingRelayAvailable() async throws -> Bool
    /// Whether the computer keeps a part told ahead of its upload and fetches it by itself (24.2a).
    func recordingAheadAvailable() async throws -> Bool
    /// POST /recordings for a part whose upload is still on its way. The request is with the
    /// relay at once; the answer is not waited for long.
    func tellAhead(_ request: RecordingPartRequest) async throws -> RecordingView
    /// Tells the computer what the last passes came to. False from a computer that does not take them.
    func reportPasses(_ passes: [RecordingPassRecord]) async throws -> Bool
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
    /// Heart rate and Place on the computer (PROTOCOL.md 24.15); nil from a computer that has no
    /// such settings. A field is nil until someone has chosen.
    func watchContextChoices() async throws -> WatchContextSettingsAnswer?
    func setWatchHeartRate(_ on: Bool) async throws -> Bool
    func setWatchPlace(_ on: Bool) async throws -> Bool
    /// Tells the computer whether this phone has a paired watch with Isoform on it. False when
    /// the computer does not take the report.
    func reportWatchPairing(_ paired: Bool) async throws -> Bool
}

/// What the computer says about Heart rate and Place: each as it stands there, and whether
/// someone chose it.
struct WatchContextSettingsAnswer: Equatable, Sendable {
    var heartRate: WatchNotesSetting
    var place: WatchNotesSetting

    /// What the watch is told: only what was chosen.
    var choices: WatchContextChoices {
        WatchContextChoices(heartRate: heartRate.chosen == true ? heartRate.on : nil, place: place.chosen == true ? place.on : nil)
    }
}

extension RecordingHubAPI {
    func watchContextChoices() async throws -> WatchContextSettingsAnswer? { nil }
    func setWatchHeartRate(_ on: Bool) async throws -> Bool { throw HubRequestError.invalidIdentifier }
    func setWatchPlace(_ on: Bool) async throws -> Bool { throw HubRequestError.invalidIdentifier }
    func reportWatchPairing(_ paired: Bool) async throws -> Bool { false }
    /// For a computer asked one thing at a time. Only what it takes at all may fail the pass;
    /// not knowing the rest counts as no.
    func recordingAbilities() async throws -> RecordingAbilities {
        var abilities = RecordingAbilities()
        abilities.recordings = try await recordingsAvailable()
        guard abilities.recordings else { return abilities }
        abilities.context = (try? await recordingContextAvailable()) ?? false
        abilities.marks = (try? await recordingMarksAvailable()) ?? false
        abilities.relay = (try? await recordingRelayAvailable()) ?? false
        abilities.ahead = (try? await recordingAheadAvailable()) ?? false
        return abilities
    }
    func observingPauseControlAvailable() async throws -> Bool { false }
    func pauseObserving(minutes: Int) async throws { throw HubRequestError.invalidIdentifier }
    func resumeObserving() async throws { throw HubRequestError.invalidIdentifier }
    func recordingMarksAvailable() async throws -> Bool { false }
    func recordingRelayAvailable() async throws -> Bool { false }
    func recordingAheadAvailable() async throws -> Bool { false }
    func reportPasses(_ passes: [RecordingPassRecord]) async throws -> Bool { false }
    func tellAhead(_ request: RecordingPartRequest) async throws -> RecordingView { try await postRecording(request) }
    func observingPause() async throws -> WatchPause? { nil }
    func recordingDetailAvailable() async throws -> Bool { false }
    func recordingTranscript(id: String) async throws -> RecordingTranscript { throw HubRequestError.invalidIdentifier }
    func deleteRecording(id: String) async throws -> RecordingView { throw HubRequestError.invalidIdentifier }
}

extension HubChannel: RecordingHubAPI {
    func recordingAbilities() async throws -> RecordingAbilities {
        let status = try await abilities()
        return RecordingAbilities(recordings: status.recordings == true, context: status.recordingContext == true,
                                  marks: status.recordingMarks == true, relay: status.recordingRelay == true,
                                  ahead: status.recordingAhead == true, inWake: status.recordingInWakeSend == true)
    }
    func recordingsAvailable() async throws -> Bool { try await abilities().recordings == true }
    func recordingContextAvailable() async throws -> Bool { try await abilities().recordingContext == true }
    func observingPause() async throws -> WatchPause? { try await status().observingPause?.pause }
    func recordingMarksAvailable() async throws -> Bool { try await abilities().recordingMarks == true }
    func recordingRelayAvailable() async throws -> Bool { try await abilities().recordingRelay == true }
    func recordingAheadAvailable() async throws -> Bool { try await abilities().recordingAhead == true }
    func reportPasses(_ passes: [RecordingPassRecord]) async throws -> Bool {
        let status = try await abilities()
        guard status.recordingPasses == true else { return false }
        struct Body: Encodable { let passes: [RecordingPassRecord] }
        // A computer from before `restarted` refuses a record with a field it does not know.
        let known = RecordingPassRecord.forComputer(passes, takesRestarts: status.recordingPassRestarts == true,
                                                    takesInWake: status.recordingInWakeSend == true)
        let body = try JSONEncoder().encode(Body(passes: known))
        _ = try await perform(.write(path: "/recordings/passes", body: String(decoding: body, as: UTF8.self)), timeout: 20)
            .payload(RecordingPassesAnswer.self)
        return true
    }
    func tellAhead(_ request: RecordingPartRequest) async throws -> RecordingView {
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/recordings", body: String(decoding: body, as: UTF8.self)), timeout: 10)
            .payload(RecordingView.self)
    }
    func observingPauseControlAvailable() async throws -> Bool { try await abilities().observingPauseControl == true }

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

    func recordingDetailAvailable() async throws -> Bool { try await abilities().recordingDetail == true }

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
        /// A part the computer refused is sent again by itself, not before this time. The wait
        /// grows with each refusal and never ends: the audio stays on the phone until the
        /// computer stores it or the person deletes it.
        var nextTry: Date? = nil
        /// The upload this part was last handed over as, set before the bytes leave. Every try
        /// until the relay holds it uses the same id, key and nonce, so the same bytes go up: an
        /// upload that arrived without the phone hearing of it is not stored a second time (the
        /// relay answers 409 for an id it holds).
        var uploading: HeldUpload? = nil
        /// The computer was told of this part ahead of its upload and answered that it expects
        /// it (24.2a). It may have fetched the part since, so it is asked before anything goes up
        /// again.
        var told: Bool? = nil
    }

    /// A part's upload on the relay and the pairing it was sealed for.
    struct HeldUpload: Codable, Equatable, Sendable {
        var upload: RecordingPartRequest.Upload
        var hubID: String
        var deviceID: String
        /// The relay it is on, or going to: "pinned" or "direct". Nil from a build that knew one.
        var relay: String? = nil
    }

    /// After this many refusals the recording reads "could not be sent"; it is still sent again.
    static let maxRefusals = 5

    /// How long a refused part waits before it is sent again: a minute, then longer, six hours
    /// at most. A computer that was updated meanwhile takes it without anyone tapping anything.
    static func retryDelay(refusals: Int) -> TimeInterval {
        let steps: [TimeInterval] = [60, 5 * 60, 15 * 60, 60 * 60, 3 * 60 * 60, 6 * 60 * 60]
        return steps[min(max(refusals, 1), steps.count) - 1]
    }

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

    func refused(_ pending: Pending, now: Date = Date()) {
        lock.withLock {
            var next = pending
            next.refusals += 1
            // A refused part is sealed again next time.
            next.held = nil
            next.uploading = nil
            next.nextTry = now.addingTimeInterval(Self.retryDelay(refusals: next.refusals))
            try? write(next)
        }
    }

    /// Removes every part of every recording from the phone.
    func removeEverything() {
        for item in pending() { remove(item.info) }
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
                next.nextTry = nil
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
            if held == nil { pending.uploading = nil }
            try? write(pending)
        }
    }

    /// Remembers that the computer expects this part.
    func told(_ info: WatchRecordingPartInfo) {
        lock.withLock {
            guard let data = try? Data(contentsOf: metaURL(info)),
                  var pending = try? JSONDecoder().decode(Pending.self, from: data) else { return }
            pending.told = true
            try? write(pending)
        }
    }

    /// Remembers the upload a part is about to be sent as.
    func uploading(_ upload: HeldUpload, for info: WatchRecordingPartInfo) {
        lock.withLock {
            guard let data = try? Data(contentsOf: metaURL(info)),
                  var pending = try? JSONDecoder().decode(Pending.self, from: data) else { return }
            pending.uploading = upload
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
        /// The relay holds as many of this phone's recordings as it keeps and takes no more
        /// until the computer has fetched some. Parts stay on the phone.
        case relayFull
        /// The relay no longer knows this phone's pairing (it answered 401). Parts stay on the
        /// phone and go up with the new credentials once the person has paired again.
        case pairAgain
        /// Parts are with the system, on their way: nothing is in the way.
        case sending
    }

    /// How soon a pass that could not finish is tried again while the app runs.
    static let waitingRetrySeconds: TimeInterval = 5 * 60

    static let keptStates = 50

    private let inbox: WatchRecordingInbox
    private let hub: RecordingHubAPI
    private let relay: RecordingUploadTransport
    private let session: @Sendable () throws -> SessionContext
    /// The pairing's second path, when it has one and it is open now.
    private let otherSession: @Sendable (SessionContext) -> SessionContext?
    /// The computer last said it takes recordings; remembered across launches.
    private var takesRecordings = false
    /// The computer last said it keeps a part told ahead of its upload (24.2a).
    private var takesAhead = false
    /// Parts the computer said it has while they were handed over in this pass.
    private var confirmedAhead: Set<String> = []
    /// The hand-overs of this pass, by part, and the parts still waiting for a place.
    private var handingTasks: [String: Task<Void, Never>] = [:]
    private var aheadWaiting: [WatchRecordingInbox.Pending] = []
    /// What the computer last said it takes, kept across wakes.
    private var remembered: RecordingAbilities?
    /// Part requests for whole parts, sent side by side when the pass starts, by part name.
    private var wholeTasks: [String: Task<Result<Void, Error>, Never>] = [:]
    /// How many whole parts are told at once. The requests are small and the computer takes the
    /// parts one after the other, also after the phone sleeps again.
    static let wholeAtOnce = 16
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
        /// The computer last said it takes recordings. Parts go to the relay ahead of asking again.
        var takesRecordings: Bool?
        var takesAhead: Bool?
        var passes: [RecordingPassRecord]?
        /// What the computer last said it takes: the next wake tells whole parts with it at once.
        var takes: RecordingAbilities?
        /// How often each part's upload was refused in a row, kept across launches.
        var refusals: [String: Int]?
        /// How often each part started a new upload, and the app version that counted.
        var freshStarts: [String: Int]?
        var freshStartsBuild: String?
        var edgeRefusals: [String: Int]?
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

    /// What the last passes came to, oldest first; the last one is the pass that runs now.
    private(set) var passes: [RecordingPassRecord] = []
    static let passesKept = 20
    private var reportingPasses = false

    /// The number of the pass the current task belongs to, and of the pass that runs now. They
    /// differ only for a stale pass that was replaced and is still finishing something.
    @TaskLocal private static var taskPass = 0
    private var passNumber = 0
    /// The pass number of each kept record, beside `passes`; 0 for records read from the file.
    private var passNumbers: [Int] = []
    /// When the running pass last did something.
    private var progressAt: Date?
    /// A running pass that did nothing for this long no longer holds back the next wake. It is
    /// longer than the longest wait for one answer (150 seconds, a part request), so a pass that
    /// only waits is left alone. A suspended app keeps "a pass is running" for as long as the
    /// system keeps the app, and a pass waiting for an answer that never comes then stopped
    /// every later wake.
    static let staleAfter: TimeInterval = 180
    /// When a skipped wake was last written down and told, and how often at most.
    private var skipToldAt: Date?
    static let skipToldEvery: TimeInterval = 60

    /// This task's pass was replaced: it finishes what it waits for and changes nothing shared.
    private var replaced: Bool { Self.taskPass != passNumber }

    /// The outcome of a pass that never ended: the app was closed or stopped under it.
    static let cutOff = "cut-off"

    /// Records read at launch. One with no end belongs to a pass of an earlier run of the app,
    /// which cannot end any more: it is closed as cut off, so it is told once and not with
    /// every later report. On 2026-11-20 three such records were sent again each time.
    static func closingCutOff(_ stored: [RecordingPassRecord]) -> [RecordingPassRecord] {
        stored.map { record in
            guard record.seconds == nil else { return record }
            var closed = record
            closed.seconds = 0
            closed.outcome = cutOff
            closed.reported = nil
            return closed
        }
    }

    /// Changes the record of this task's pass, or of the given one.
    private func note(pass number: Int? = nil, _ change: (inout RecordingPassRecord) -> Void) {
        let number = number ?? Self.taskPass
        guard number != 0, let index = passNumbers.lastIndex(of: number), index < passes.count else { return }
        change(&passes[index])
        if number == passNumber { progressAt = now() }
    }

    /// Tells the computer of every record it has not heard of. A pass that has not ended is told
    /// too, and told again when it ends: a pass that hangs must not hide itself.
    func reportPasses() async {
        guard !reportingPasses else { return }
        let unsent = passes.filter { $0.reported != true }
        guard !unsent.isEmpty else { return }
        reportingPasses = true
        defer { reportingPasses = false }
        guard (try? await hub.reportPasses(unsent)) == true else { return }
        for index in passes.indices where unsent.contains(where: { $0.startedAt == passes[index].startedAt }) {
            // Only a record that did not change meanwhile, and has ended, needs no second telling.
            if passes[index].seconds != nil, unsent.contains(passes[index]) { passes[index].reported = true }
        }
        save()
    }

    private let now: @Sendable () -> Date

    init(inbox: WatchRecordingInbox, hub: RecordingHubAPI, relay: RecordingUploadTransport = RelayClient.shared,
         now: @escaping @Sendable () -> Date = { Date() },
         session: @escaping @Sendable () throws -> SessionContext,
         otherSession: @escaping @Sendable (SessionContext) -> SessionContext? = { _ in nil },
         build: String = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "") {
        self.build = build
        self.now = now
        self.inbox = inbox
        self.hub = hub
        self.relay = relay
        self.session = session
        self.otherSession = otherSession
        self.statesURL = inbox.directory.appendingPathComponent("states.json")
        if let data = try? Data(contentsOf: statesURL), let stored = try? JSONDecoder().decode(Stored.self, from: data) {
            states = stored.states
            order = stored.order
            details = stored.details ?? [:]
            pendingDeletes = stored.pendingDeletes ?? []
            names = stored.names ?? [:]
            takesRecordings = stored.takesRecordings ?? false
            takesAhead = stored.takesAhead ?? false
            passes = Self.closingCutOff(stored.passes ?? [])
            remembered = stored.takes
            uploadRefusals = stored.refusals ?? [:]
            edgeRefusals = stored.edgeRefusals ?? [:]
            // A new version of the app gets its own tries.
            freshStarts = stored.freshStartsBuild == build ? (stored.freshStarts ?? [:]) : [:]
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

    /// Forget everything: every recording's audio and state on this phone. The computer is forgetting its own copy,
    /// so no delete goes to it.
    func forgetAll() {
        inbox.removeEverything()
        states.removeAll()
        order.removeAll()
        details.removeAll()
        pendingDeletes.removeAll()
        names.removeAll()
        save()
        onStates?(states, names)
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

    /// The person tapped Try again: every part the computer refused gets another round now,
    /// without waiting its turn. On 2026-11-18 parts refused by an older computer waited hours
    /// for their turn after the computer was updated, and Try again did not touch them.
    func retryRefused() {
        for recordingID in Set(inbox.pending().filter { $0.refusals > 0 }.map(\.info.recordingID)) {
            inbox.resetRefusals(recordingID: recordingID)
            if states[recordingID] == .failed, !inbox.audio(recordingID: recordingID).urls.isEmpty { set(recordingID, .onPhone) }
        }
    }

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

    /// When the next pass is due by itself: the earliest refused part's turn, or a few minutes
    /// after a pass that could not finish. Nil when nothing waits.
    func nextRetry() -> Date? {
        let waiting = inbox.pending()
        guard !waiting.isEmpty else { return nil }
        let moment = now()
        let soon = moment.addingTimeInterval(Self.waitingRetrySeconds)
        if waiting.contains(where: { $0.nextTry == nil }) {
            // Something has not been refused: it waits for the computer or the relay.
            return lastOutcome == .drained ? nil : soon
        }
        return waiting.compactMap(\.nextTry).min().map { max($0, moment) }
    }

    /// Sends what is waiting. A call while one runs asks for one more pass and returns.
    @discardableResult
    func drain() async -> Outcome {
        var replacing = false
        if running {
            if let progressAt, now().timeIntervalSince(progressAt) > Self.staleAfter {
                // The running pass has done nothing for too long: this wake does not wait for it.
                replacing = true
            } else {
                again = true
                let before = progressAt
                note(pass: passNumber) { $0.skipped += 1 }
                // A skipped wake is not something the pass did.
                progressAt = before
                // Told at most once a minute: on 2026-11-20 an open app skipped 88 times in two
                // minutes (every piece that ends asks for a pass), and each skip wrote the file
                // and asked the computer.
                let moment = now()
                if let last = skipToldAt, moment.timeIntervalSince(last) < Self.skipToldEvery { return .waiting }
                skipToldAt = moment
                save()
                await reportPasses()
                return .waiting
            }
        }
        running = true
        again = false
        passNumber += 1
        let number = passNumber
        defer { if passNumber == number { running = false } }
        return await Self.$taskPass.withValue(number) {
            var outcome = await recordedPass(replacing: replacing)
            // Another pass was asked for meanwhile. It runs at once only after a pass that sent
            // everything. On 2026-11-20 one part kept failing, every answer from the computer
            // asked for a pass, and passes ran every two to three seconds for minutes: 182 part
            // requests for the same few parts and no progress. After any other pass the next one
            // comes at its retry time, or when an upload ends.
            while again, !replaced, outcome == .drained {
                again = false
                outcome = await recordedPass(replacing: false)
            }
            if !replaced { again = false }
            if !replaced { lastOutcome = outcome }
            await reportPasses()
            return outcome
        }
    }

    /// Before anything is asked of the computer, the first waiting parts are left with the
    /// system. The upload needs only the relay, and handing over takes milliseconds; asking the
    /// computer took 20 seconds on 2026-11-18, and the app was gone before the first upload.
    /// Only for a computer that last said it takes recordings, and only to the pinned relay,
    /// where every computer looks.
    ///
    /// Each part is handed over by itself, all at the same time, and the pass goes on at once:
    /// it waits for a part's hand-over only when it comes to send that part. On 2026-11-19 the
    /// parts were handed over one after the other before anything else; one took 25 seconds, the
    /// app was awake for 30, and in seven wakes the computer was never told of four parts the
    /// relay already held whole.
    ///
    /// Four parts are with the system at a time, and only a part that is on its way takes one
    /// of the four places. A part the relay holds whole, or the computer already has, gives its
    /// place to the next waiting part at once. On 2026-11-19 at 3:12 PM four whole parts the
    /// computer had not taken held all four places, and the phone uploaded nothing new: old
    /// parts waiting for the computer must never stop new audio from going up.
    private func handAhead(_ pending: [WatchRecordingInbox.Pending]) {
        handingTasks = [:]
        aheadWaiting = []
        guard takesRecordings, let ahead = relay as? RecordingUploadAhead, let context = try? session() else { return }
        let pinned = Self.paths(first: context, other: otherSession(context), takesRelay: false).first ?? context
        guard pinned.route == .relay else { return }
        // A part that began on the direct path goes on there.
        aheadWaiting = pending.filter { !isHeld($0, for: context) && $0.uploading?.relay != "direct" }
        let run = AheadRun(ahead: ahead, context: context, pinned: pinned)
        for _ in 0..<Self.uploadsAtOnce { handNextAhead(run) }
    }

    /// What one pass hands ahead with.
    private struct AheadRun {
        let ahead: RecordingUploadAhead
        let context: SessionContext
        let pinned: SessionContext
    }

    /// Starts the hand-over of the next waiting part. When that part turns out to need no
    /// place, the one after it is started in its stead.
    private func handNextAhead(_ run: AheadRun) {
        while !aheadWaiting.isEmpty {
            let item = aheadWaiting.removeFirst()
            // The relay has no place for a new part in this pass.
            if relayFull, !isOnRelay(item) { continue }
            guard let plaintext = try? Data(contentsOf: inbox.audioURL(item.info), options: .mappedIfSafe),
                  let (sealed, coordinates) = try? sealedBytes(item, plaintext: plaintext, context: run.context) else { continue }
            let name = Self.relayName(run.pinned)
            let uploading = WatchRecordingInbox.HeldUpload(upload: coordinates, hubID: run.context.hubID,
                                                           deviceID: run.context.deviceID, relay: name)
            inbox.uploading(uploading, for: item.info)
            let info = item.info
            var upload = coordinates
            upload.relay = name
            let request = takesAhead ? Self.request(info, upload: upload, plaintext: plaintext, context: info.context,
                                                    marks: info.marks, ahead: true) : nil
            let told = item.told == true
            handingTasks[Self.name(info)] = Task {
                let takesAPlace = await handOne(run, info: info, uploading: uploading, sealed: sealed, request: request, told: told)
                if !takesAPlace, !replaced { handNextAhead(run) }
            }
            return
        }
    }

    /// Hands one part over. False when the part needs no place among those on their way: the
    /// relay holds it whole, or the computer has it.
    private func handOne(_ run: AheadRun, info: WatchRecordingPartInfo, uploading: WatchRecordingInbox.HeldUpload,
                         sealed: Data, request: RecordingPartRequest?, told: Bool) async -> Bool {
        // A part the computer expects may be with it by now.
        var expected = false
        // Told a moment ago: the computer is fetching it, or the upload is on its way. It is
        // left alone for this pass, and asked about again when the minute is over.
        if told, let at = toldAt[Self.name(info)], now().timeIntervalSince(at) < Self.tellAgainAfter { return true }
        // A part the computer was told of before is asked about again beside the hand-over, not
        // before it. On 2026-11-20 the computer answered in under a second and the answers
        // reached the phone 8 and 13 seconds later; the hand-over waited for them, and a wake
        // of 20 seconds was over before a piece went. The answer is waited for only where it
        // decides something: when the relay holds nothing of a part that went up before, which
        // is how a part looks once the computer has taken it.
        var again: Task<RecordingView?, Never>?
        if told, let request { again = Task { await tell(request) } }
        let expectedWord = Self.expected
        let asked = again
        let taken: (@Sendable () async -> Bool)? = asked.map { asked in
            { @Sendable in
                guard let view = await asked.value else { return false }
                return view.state != expectedWord
            }
        }
        // A part the computer has not been told of is told now, at the same moment as it is
        // handed over, not after. On 2026-11-20 four parts became whole on the relay in the
        // second the app left; the tell had not gone out yet, and the computer asked for none
        // of them until the phone woke again. A tell whose hand-over then fails does no harm:
        // the computer keeps asking the relay for a while and then lets it go (24.2a).
        async let first: RecordingView? = tellEarly(told ? nil : request)
        // The switch is the computer's: one that does not say so gets the hand-over as before.
        let begun = await run.ahead.beginKnowing(run.pinned.credentials, id: uploading.upload.id, sealed: sealed,
                                                 inWake: remembered?.inWake == true, taken: taken)
        // A part the system holds and that was left as it is says why, as a short word.
        let heldWord = await run.ahead.takeHeldWord(uploading.upload.id)
        note {
            if let heldWord { $0.stoppedBy = heldWord }
            $0.began += 1
            switch begun {
            case .no: $0.beganNo += 1
            case .whole: $0.beganWhole += 1
            case .sentInWake:
                $0.beganWhole += 1
                $0.sentInWake = ($0.sentInWake ?? 0) + 1
            case .handed: $0.beganHanded += 1
            case .handedAgain:
                $0.beganHanded += 1
                $0.restarted = ($0.restarted ?? 0) + 1
            }
        }
        let early = await first
        // The hand-over is done; the computer's word on a part it was told of before is read now.
        if let again, let view = await again.value {
            if view.state != Self.expected {
                confirmAhead(info, view)
                return false
            }
            expected = true
        }
        switch begun {
        case .no:
            // Not handed over (the relay did not answer, or takes no pieces). It keeps its place,
            // so a relay that is down is not asked about every waiting part.
            if let early, early.state != Self.expected {
                // The computer has this part already.
                confirmAhead(info, early)
                return false
            }
            return true
        case .whole, .sentInWake:
            // Only the computer is left to tell. Remembered, so the next wake tells it first.
            inbox.hold(uploading, for: info)
            if !expected, let request, let view = await tellUnless(early, request), view.state != Self.expected {
                confirmAhead(info, view)
            }
            return false
        case .handed, .handedAgain:
            // The part is with the system. The computer was told as it was handed over, or is
            // told now, while the app is awake: it keeps this and fetches the part when the
            // relay has it, so nothing waits for the phone to wake again after the upload.
            guard !expected, let request, let view = await tellUnless(early, request) else { return true }
            if view.state == Self.expected {
                inbox.told(info)
                return true
            }
            confirmAhead(info, view)
            return false
        }
    }

    /// When each part was last told ahead, by part name, and how long before it is told again.
    private var toldAt: [String: Date] = [:]
    static let tellAgainAfter: TimeInterval = 60
    /// How many parts may fail in one pass before it ends: one bad part does not hold up the
    /// parts behind it, and a path that is down is not tried for every part.
    static let failuresInAPass = 2

    /// How often each part's upload was refused by the relay in a row, by part name. Kept in the
    /// state file: an app that is started afresh often would otherwise never count to three.
    private var uploadRefusals: [String: Int] = [:]
    /// Passes in a row in which a tunnel's edge, not the relay, refused this part (a 403 the
    /// relay did not write). Once is about the moment. On 2026-11-21 the same parts were refused
    /// in every pass for a day, by what their bytes happened to be, and no record said so.
    private var edgeRefusals: [String: Int] = [:]
    static let edgeRefusalsThatAreNotTheMoment = 3

    /// The word for a part's failed try. A part the edge refused in three passes in a row or
    /// more says so with the count: it cannot pass as it is sent.
    private func partWord(_ item: WatchRecordingInbox.Pending, after error: Error) -> String {
        let name = Self.name(item.info)
        guard error is RelayEdgeForbidden else {
            if edgeRefusals.removeValue(forKey: name) != nil { save() }
            return RecordingPassRecord.word(error)
        }
        let count = min((edgeRefusals[name] ?? 0) + 1, 99)
        edgeRefusals[name] = count
        save()
        return Self.edgeWord(passes: count)
    }

    static func edgeWord(passes: Int) -> String {
        passes >= edgeRefusalsThatAreNotTheMoment ? "edge-403-in-\(passes)-passes" : "edge-403"
    }
    /// After this many refusals in a row the part starts a new upload under a new id.
    static let refusalsBeforeAFreshUpload = 3
    /// How often one part may start a new upload in one version of the app. Each start leaves
    /// a half-done upload on the relay for 48 hours; a part whose new uploads are refused too
    /// would fill the phone's room there. After this the part keeps its upload and waits for
    /// the next version of the app.
    static let freshStartsPerPart = 2
    private var freshStarts: [String: Int] = [:]
    /// The app's version, as the count of fresh starts knows it.
    private let build: String

    /// An upload the relay refuses try after try never heals by itself: on 2026-11-20 one
    /// upload failed every try for two hours, and the part behind it never reached the
    /// computer. After three refusals in a row the part forgets that upload and its next try
    /// seals it anew, under a new id. The audio stays on the phone throughout; what the relay
    /// holds of the old upload runs out by itself (48 hours). Only an answer of the relay's
    /// counts: not being offline, a timeout, "too many" or a refused pairing.
    private func startAfreshIfStuck(_ item: WatchRecordingInbox.Pending, after error: Error) {
        let name = Self.name(item.info)
        let word = RecordingPassRecord.word(error)
        let refused = (word.hasPrefix("relay-4") || word.hasPrefix("relay-5")) && word != "relay-401" && word != "relay-429"
        guard refused, item.held == nil else {
            if !refused, uploadRefusals.removeValue(forKey: name) != nil { save() }
            return
        }
        let count = (uploadRefusals[name] ?? 0) + 1
        guard count >= Self.refusalsBeforeAFreshUpload else {
            uploadRefusals[name] = count
            save()
            return
        }
        guard (freshStarts[name] ?? 0) < Self.freshStartsPerPart else {
            // No more fresh starts in this version: the count stays at its limit.
            uploadRefusals[name] = Self.refusalsBeforeAFreshUpload
            save()
            return
        }
        freshStarts[name, default: 0] += 1
        uploadRefusals[name] = nil
        toldAt[name] = nil
        inbox.hold(nil, for: item.info)
        save()
    }

    /// Tells the computer of a part ahead of its upload, and counts how it went.
    /// The tell that goes out beside the hand-over; nil when there is nothing to tell.
    private func tellEarly(_ request: RecordingPartRequest?) async -> RecordingView? {
        guard let request else { return nil }
        return await tell(request)
    }

    /// The early answer when there is one; otherwise the computer is told now.
    private func tellUnless(_ early: RecordingView?, _ request: RecordingPartRequest) async -> RecordingView? {
        if let early { return early }
        return await tell(request)
    }

    private func tell(_ request: RecordingPartRequest) async -> RecordingView? {
        do {
            let view = try await hub.tellAhead(request)
            note { $0.told += 1 }
            toldAt["\(request.recordingId)-\(request.part)"] = now()
            return view
        } catch {
            note {
                $0.toldFailed += 1
                $0.stoppedBy = "tell-" + RecordingPassRecord.word(error)
            }
            return nil
        }
    }

    /// Whether the relay holds this part whole for this pairing: only the computer is left to tell.
    /// The ids of this phone's uploads the relay holds something of, as it said at the start
    /// of this pass; nil when it could not say (an older relay, no answer).
    private var onRelay: Set<String>?
    /// The relay refused a new upload in this pass: it has no place for one. Parts it already
    /// holds something of still go, since their pieces need no new place.
    private var relayFull = false

    /// On 2026-11-22 uploads half sent the evening before held every place on the relay. The
    /// watch had since handed over older parts, which sort first; each was refused, and the
    /// pass ended at the first refusal, before it reached a part that would have freed a
    /// place. The relay is asked only when a part was begun and is not known to be whole.
    private func askOnRelay(_ waiting: [WatchRecordingInbox.Pending]) async -> Set<String>? {
        guard takesRecordings, waiting.contains(where: { $0.uploading != nil && $0.held == nil }),
              let ahead = relay as? RecordingUploadAhead, let context = try? session() else { return nil }
        let pinned = Self.paths(first: context, other: otherSession(context), takesRelay: false).first ?? context
        guard pinned.route == .relay else { return nil }
        return await ahead.onRelay(pinned.credentials)
    }

    private func isOnRelay(_ item: WatchRecordingInbox.Pending) -> Bool {
        Self.isOnRelay(item, onRelay)
    }

    static func isOnRelay(_ item: WatchRecordingInbox.Pending, _ onRelay: Set<String>?) -> Bool {
        guard let onRelay, let id = (item.held ?? item.uploading)?.upload.id else { return false }
        return onRelay.contains(id)
    }

    /// Parts the relay holds something of first, each group in the order it had (oldest
    /// first). Without the relay's word the order is unchanged.
    static func begunFirst(_ pending: [WatchRecordingInbox.Pending], onRelay: Set<String>?) -> [WatchRecordingInbox.Pending] {
        guard let onRelay, !onRelay.isEmpty else { return pending }
        return pending.filter { isOnRelay($0, onRelay) } + pending.filter { !isOnRelay($0, onRelay) }
    }

    private func isHeld(_ item: WatchRecordingInbox.Pending, for context: SessionContext) -> Bool {
        guard let held = item.held else { return false }
        return held.hubID == context.hubID && held.deviceID == context.deviceID
    }

    private static func name(_ info: WatchRecordingPartInfo) -> String { "\(info.recordingID)-\(info.part)" }

    /// The computer's word for a part it was told of and has not fetched yet (24.2a). It never
    /// means the computer has the part.
    static let expected = "expected"

    /// The computer answered a part told ahead with its own copy of it.
    private func confirmAhead(_ info: WatchRecordingPartInfo, _ view: RecordingView) {
        confirmedAhead.insert(Self.name(info))
        crossed(info, view)
    }

    private static func request(_ info: WatchRecordingPartInfo, upload: RecordingPartRequest.Upload, plaintext: Data,
                                context: WatchRecordingContext?, marks: [Double]?, ahead: Bool? = nil) -> RecordingPartRequest {
        RecordingPartRequest(
            idempotencyKey: "rec-\(info.recordingID)-\(info.part)", recordingId: info.recordingID, part: info.part,
            last: info.last, upload: upload, sha256: RelayCrypto.sha256Hex(plaintext), bytes: plaintext.count,
            mime: WatchRecordingWire.mime, startedAt: RecordingTime.format(info.startedAt),
            durationSeconds: info.durationSeconds, source: "watch", device: info.model.map { .init(model: $0) },
            context: context, marks: marks, ahead: ahead)
    }

    /// The computer has the part: it leaves the phone, and the recording's state follows.
    private func crossed(_ info: WatchRecordingPartInfo, _ view: RecordingView) {
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

    /// The sealed bytes of a part: the same id, key and nonce as its last try, so the same bytes go up.
    private func sealedBytes(_ item: WatchRecordingInbox.Pending, plaintext: Data,
                        context: SessionContext) throws -> (Data, RecordingPartRequest.Upload) {
        if let earlier = item.uploading, earlier.hubID == context.hubID, earlier.deviceID == context.deviceID,
           let key = try? RelayCrypto.base64URLDecode(earlier.upload.key),
           let nonce = try? RelayCrypto.base64URLDecode(earlier.upload.nonce) {
            return try RecordingSeal.seal(plaintext, hubID: context.hubID, deviceID: context.deviceID,
                                          uploadID: earlier.upload.id, key: key, nonce: nonce)
        }
        return try RecordingSeal.seal(plaintext, hubID: context.hubID, deviceID: context.deviceID)
    }

    /// A pass, with what it came to written down: before it starts, so a pass that never ends
    /// still leaves its beginning, and when it ends.
    private func recordedPass(replacing: Bool) async -> Outcome {
        let began = now()
        var record = RecordingPassRecord(startedAt: RecordingTime.format(began))
        if replacing { record.replacedStale = 1 }
        passes.append(record)
        while passNumbers.count < passes.count - 1 { passNumbers.insert(0, at: 0) }
        passNumbers.append(Self.taskPass)
        if passes.count > Self.passesKept {
            passNumbers.removeFirst(passes.count - Self.passesKept)
            passes.removeFirst(passes.count - Self.passesKept)
        }
        progressAt = began
        save()
        let outcome = await pass()
        let seconds = (now().timeIntervalSince(began) * 10).rounded() / 10
        note {
            $0.seconds = seconds
            $0.outcome = "\(outcome)"
        }
        save()
        return outcome
    }

    private func pass() async -> Outcome {
        // A refused part waits its turn (WatchRecordingInbox.retryDelay) and is then sent again,
        // however often it was refused.
        let moment = now()
        let waiting = inbox.pending()
        let due = waiting.filter { ($0.nextTry ?? .distantPast) <= moment }
        note {
            $0.pending = due.count
            $0.notDue = waiting.count - due.count
        }
        confirmedAhead = []
        // What the relay holds of this phone's uploads, asked once. Parts it holds something of
        // go first: finishing them is what frees a place there.
        relayFull = false
        onRelay = await askOnRelay(waiting)
        let pending = Self.begunFirst(due, onRelay: onRelay)
        handAhead(pending)
        // Parts the relay holds whole come first: telling the computer is all they need, and it
        // is what lets the computer take them. They wait for no hand-over.
        let context = try? session()
        let whole = pending.filter { item in context.map { isHeld(item, for: $0) } ?? false }
        let rest = pending.filter { item in !(context.map { isHeld(item, for: $0) } ?? false) }
        note { $0.whole = whole.count }
        // Whole parts are told side by side, before anything is asked, with what the computer
        // said it takes the last time. On 2026-11-20 a wake lasted 4 to 60 seconds, a part
        // request waits up to 150 for its answer, and the parts were told one after the other:
        // at most one a wake. Once a request is with the relay the computer takes the part
        // whether or not the phone is still awake for the answer.
        wholeTasks = [:]
        if let remembered, remembered.recordings {
            for item in whole.prefix(Self.wholeAtOnce) {
                wholeTasks[Self.name(item.info)] = Task {
                    do {
                        try await send(item, withContext: remembered.context, withMarks: remembered.marks,
                                       takesRelay: remembered.relay)
                        return .success(())
                    } catch {
                        return .failure(error)
                    }
                }
            }
        }
        let outcome = await send(whole + rest)
        // The hand-overs now belong to the pass that replaced this one.
        if replaced { return outcome }
        // Requests already sent are waited for, however the pass ended.
        while let name = wholeTasks.keys.first, let task = wholeTasks.removeValue(forKey: name) {
            _ = await task.value
        }
        // Whatever the pass came to, every hand-over begun is finished before it ends, those a
        // freed place started meanwhile included.
        while let name = handingTasks.keys.first, let task = handingTasks.removeValue(forKey: name) {
            await task.value
        }
        aheadWaiting = []
        return outcome
    }

    private func send(_ pending: [WatchRecordingInbox.Pending]) async -> Outcome {
        await flushDeletes()
        // What the computer takes, asked once. An older computer refuses a request with fields
        // it does not know, so the context and the marks go only to one that says it takes
        // them; with one that does not the part goes without, the words matter more. A part
        // may go over the direct path only to a computer that fetches from the relay the phone
        // names: the pinned relay is where every computer looks.
        var abilities = RecordingAbilities()
        if !pending.isEmpty {
            do {
                abilities = try await hub.recordingAbilities()
            } catch {
                note { $0.stoppedBy = "asking-" + RecordingPassRecord.word(error) }
                return .waiting
            }
            // Remembered for the next wake, where parts are handed over and told ahead before
            // anything is asked.
            if abilities.recordings != takesRecordings || (abilities.recordings && abilities.ahead != takesAhead)
                || abilities != remembered {
                takesRecordings = abilities.recordings
                if abilities.recordings { takesAhead = abilities.ahead }
                remembered = abilities
                save()
            }
            guard abilities.recordings else { return .unsupported }
        }
        let takesContext = abilities.context, takesMarks = abilities.marks, takesRelay = abilities.relay
        // Parts that could not be finished in this pass; the pass goes on with the others.
        var unfinished = false
        var held = false
        var inFlight = 0
        // A part request got no answer: no further one is started in this pass.
        var unanswered = false
        var failures = 0
        for item in pending {
            // A newer pass took over while this one waited: it sends what is left.
            if replaced { return .waiting }
            // The relay is full: a part it holds nothing of waits for the next pass.
            if relayFull, !isOnRelay(item) {
                unfinished = true
                continue
            }
            do {
                sending = item.info.recordingID
                onActivity?()
                defer {
                    sending = nil
                    onActivity?()
                }
                // The hand-over of this part, if one is under way, ends before it is sent.
                let name = Self.name(item.info)
                // Its turn to be sent came before a place for it: the pass takes it from here.
                aheadWaiting.removeAll { Self.name($0.info) == name }
                await handingTasks[name]?.value
                if replaced { return .waiting }
                // The computer answered the hand-over with its own copy: nothing is left to send.
                if confirmedAhead.contains(Self.name(item.info)) { continue }
                // Told a moment ago and left alone by the hand-over: it is on its way, and
                // nothing of it is sent or asked in this pass.
                if item.told == true, let at = toldAt[name], now().timeIntervalSince(at) < Self.tellAgainAfter,
                   wholeTasks[name] == nil {
                    unfinished = true
                    inFlight += 1
                    note { $0.inFlight += 1 }
                    if inFlight >= Self.uploadsAtOnce { return held ? .waiting : .sending }
                    continue
                }
                if let told = wholeTasks.removeValue(forKey: name) {
                    // Its request went out when the pass started.
                    try await told.value.get()
                } else if unanswered {
                    unfinished = true
                    continue
                } else {
                    try await send(item, withContext: takesContext, withMarks: takesMarks, takesRelay: takesRelay)
                }
            } catch HubRequestError.rejected(let status, _) where status == 400 {
                // The computer says this part is not valid. Count it and move on; the audio stays.
                note { $0.refused += 1 }
                inbox.refused(item, now: now())
                if item.refusals + 1 >= WatchRecordingInbox.maxRefusals { set(item.info.recordingID, .failed) }
            } catch RelayClientError.payloadTooLarge {
                inbox.refused(item, now: now())
            } catch is RecordingUploadInFlight {
                // The upload is with the system and goes on by itself. A few at a time, so the
                // relay's room for this phone is not used up by parts the computer has not fetched.
                unfinished = true
                inFlight += 1
                note { $0.inFlight += 1 }
                // Nothing is in the way: the parts are going.
                if inFlight >= Self.uploadsAtOnce { return held ? .waiting : .sending }
            } catch HubRequestError.rejected {
                // The computer answered, and could not take this part now (it could not fetch
                // it yet). The other parts are not held back by it.
                note { $0.stoppedBy = "part-computer-refused" }
                unfinished = true
                held = true
            } catch let error where Self.unanswered(error) {
                // No answer to this part's request. The request may still be with the relay, and
                // the computer takes the part when it gets it. The requests sent beside it are
                // still waited for; the pass does not end here.
                note { $0.stoppedBy = "part-" + RecordingPassRecord.word(error) }
                unfinished = true
                held = true
                unanswered = true
            } catch RelayClientError.unauthorized {
                // The relay refused this phone's credentials: no part can go up until the phone
                // is paired again. Nothing is counted against the part; the audio stays.
                note { $0.stoppedBy = "part-relay-401" }
                return .pairAgain
            } catch RelayClientError.rateLimited {
                // The relay keeps no more of this phone's recordings until the computer fetched
                // some. Said as it is, so a full relay never looks like a sleeping computer.
                note { $0.stoppedBy = "part-relay-429" }
                // No place for a new part. The parts the relay already holds something of
                // need none, and finishing them is what frees one: the pass goes on with
                // those. A refusal of such a part is about the minute, and ends the pass.
                guard let onRelay, !onRelay.isEmpty, !isOnRelay(item) else { return .relayFull }
                relayFull = true
                unfinished = true
            } catch {
                // Offline, asleep, paused, a full quota, an upload the relay lost: try again later.
                // The parts behind this one still get their turn, up to a second failure.
                let word = partWord(item, after: error)
                note { $0.stoppedBy = "part-" + word }
                startAfreshIfStuck(item, after: error)
                failures += 1
                if failures >= Self.failuresInAPass { return .waiting }
                unfinished = true
                held = true
            }
        }
        await refreshStates()
        if relayFull { return .relayFull }
        guard unfinished else { return .drained }
        return held || inFlight == 0 ? .waiting : .sending
    }

    /// A request that was sent and got no answer in time.
    private static func unanswered(_ error: Error) -> Bool {
        switch error {
        case RelayClientError.timedOut, HubRequestError.noAnswer: return true
        default: return false
        }
    }

    /// How many uploads are with the system at a time.
    /// Eight since 2026-11-20: with four, a phone holding 255 parts (about 64 hours) sent them
    /// four at a time while the computer took each as soon as it was whole, so the relay held
    /// next to nothing and the phone's queue was what took the time. Eight parts are about
    /// 22 MB on their way; the relay keeps up to 48 parts and 192 MB for one phone, 600 new
    /// parts and 16000 pieces an hour.
    static let uploadsAtOnce = 8

    /// The name the computer knows a path's relay by (PROTOCOL.md 24.2).
    static func relayName(_ context: SessionContext) -> String {
        context.route == .direct ? "direct" : "pinned"
    }

    /// The paths a part may take, best first (docs/design/watch-parts-reach-the-computer.md).
    /// `first` is the path requests use now, `other` the pairing's second path if it has one and
    /// it is open. With a computer that fetches from the named relay: the pinned relay, then
    /// the direct path when the pinned relay cannot be reached. With one that does not: only
    /// where it looks, which is the pinned relay, or the one path of a pairing that has no other.
    ///
    /// Pinned first since 2026-11-20. Until then the direct path came first. That day nothing
    /// from the phone arrived over the direct path, a part the pass sent itself spent 30 to 40
    /// seconds on it in every pass, and the pass ended there. The pinned relay is also the one
    /// the system can upload to while the app sleeps.
    static func paths(first: SessionContext, other: SessionContext?, takesRelay: Bool) -> [SessionContext] {
        guard let other else { return [first] }
        let direct = first.route == .direct ? first : other
        let pinned = first.route == .direct ? other : first
        return takesRelay ? [pinned, direct] : [pinned]
    }

    private func send(_ item: WatchRecordingInbox.Pending, withContext: Bool, withMarks: Bool = false,
                      takesRelay: Bool = false) async throws {
        let context = try session()
        var paths = Self.paths(first: context, other: otherSession(context), takesRelay: takesRelay)
        let info = item.info
        let plaintext = try Data(contentsOf: inbox.audioURL(info), options: .mappedIfSafe)
        var upload: RecordingPartRequest.Upload
        // A copy on a relay this computer does not look at is not held for it.
        let held = item.held.flatMap { held in
            held.hubID == context.hubID && held.deviceID == context.deviceID
                && (takesRelay || held.relay != "direct" || paths.contains { Self.relayName($0) == "direct" }) ? held : nil
        }
        if let held {
            upload = held.upload
            // A copy from a build that did not note its relay goes unnamed: the computer then
            // looks on its usual relay first and on its own after.
            upload.relay = takesRelay ? held.relay : nil
        } else {
            // The same id, key and nonce as the last try of this part, so the same bytes go up.
            let sealed: Data
            var coordinates: RecordingPartRequest.Upload
            if let earlier = item.uploading, earlier.hubID == context.hubID, earlier.deviceID == context.deviceID,
               let key = try? RelayCrypto.base64URLDecode(earlier.upload.key),
               let nonce = try? RelayCrypto.base64URLDecode(earlier.upload.nonce) {
                (sealed, coordinates) = try RecordingSeal.seal(plaintext, hubID: context.hubID, deviceID: context.deviceID,
                                                               uploadID: earlier.upload.id, key: key, nonce: nonce)
                // The part stays on the relay where it began, while that path is there.
                if let began = earlier.relay, let index = paths.firstIndex(where: { Self.relayName($0) == began }) {
                    paths.insert(paths.remove(at: index), at: 0)
                }
            } else {
                (sealed, coordinates) = try RecordingSeal.seal(plaintext, hubID: context.hubID, deviceID: context.deviceID)
            }
            var landed: String?
            // What the first path said, when the part moved on to the other.
            var before: Error?
            for (index, path) in paths.enumerated() {
                let name = Self.relayName(path)
                inbox.uploading(WatchRecordingInbox.HeldUpload(upload: coordinates, hubID: context.hubID,
                                                               deviceID: context.deviceID, relay: name), for: info)
                do {
                    try await relay.putUpload(path.credentials, id: coordinates.id, sealed: sealed)
                    landed = name
                } catch RelayClientError.conflict {
                    // The relay already holds this id: an earlier try arrived without the phone hearing of it.
                    landed = name
                } catch where index < paths.count - 1 && (name == "direct" || SessionContext.isPathFailure(error)) {
                    // The pinned relay is down or refuses the phone: the direct path carries the
                    // part. The direct path failed, in whatever way: the pinned relay carries it.
                    // An error on the direct path never ends the pass.
                    before = error
                    continue
                } catch where name == "direct" && before != nil {
                    // The direct path was the way out and failed too: the pinned relay's reason
                    // is the one that says what is wrong.
                    throw before ?? error
                }
                break
            }
            guard let landed else { throw RelayClientError.offline }
            coordinates.relay = takesRelay ? landed : nil
            inbox.hold(WatchRecordingInbox.HeldUpload(upload: coordinates, hubID: context.hubID,
                                                      deviceID: context.deviceID, relay: landed), for: info)
            upload = coordinates
        }
        let request = Self.request(info, upload: upload, plaintext: plaintext, context: withContext ? info.context : nil,
                                   marks: withMarks ? info.marks : nil)
        let view: RecordingView
        note { $0.posted += 1 }
        do {
            view = try await hub.postRecording(request)
        } catch HubRequestError.rejected(let status, _) where status == 404 {
            // upload_missing: the relay no longer has the copy; the next pass uploads a new one.
            inbox.hold(nil, for: info)
            throw HubRequestError.rejected(status: status, message: "upload_missing")
        }
        // "expected" is not "received": the part stays here until the computer has it.
        if view.state == Self.expected { throw RecordingUploadInFlight() }
        // The part is across: nothing is counted against it any more.
        uploadRefusals[Self.name(info)] = nil
        edgeRefusals[Self.name(info)] = nil
        freshStarts[Self.name(info)] = nil
        note { $0.taken += 1 }
        crossed(info, view)
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
        await refreshContextChoices()
        guard let setting = try? await hub.watchListening() else { return }
        listeningKnown = true
        listenOffSince = setting.offSince
    }

    /// Heart rate and Place as the computer last said (PROTOCOL.md 24.15); nil from a computer
    /// that has no such settings, where the watch's own switches decide.
    private(set) var contextSettings: WatchContextSettingsAnswer?

    /// What the watch is told about the two; nil when the computer has no such settings.
    var contextChoices: WatchContextChoices? { contextSettings?.choices }

    func refreshContextChoices() async {
        guard let answer = try? await hub.watchContextChoices() else { return }
        contextSettings = answer
    }

    /// Saves one choice on the computer. It returns what holds there, or nil when it was not taken.
    func setContext(heartRate: Bool? = nil, place: Bool? = nil) async -> WatchContextSettingsAnswer? {
        do {
            if let heartRate { _ = try await hub.setWatchHeartRate(heartRate) }
            if let place { _ = try await hub.setWatchPlace(place) }
        } catch {
            return nil
        }
        await refreshContextChoices()
        // The watch hears of it with the next context.
        onStates?(states, names)
        return contextSettings
    }

    /// The last word the computer took on whether a watch with Isoform is paired.
    private var reportedPairing: Bool?

    func reportPairing(_ paired: Bool) async {
        guard reportedPairing != paired, (try? await hub.reportWatchPairing(paired)) == true else { return }
        reportedPairing = paired
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
                                                       pendingDeletes: pendingDeletes, names: names,
                                                       takesRecordings: takesRecordings, takesAhead: takesAhead,
                                                       passes: passes, takes: remembered,
                                                       refusals: uploadRefusals.isEmpty ? nil : uploadRefusals,
                                                       freshStarts: freshStarts.isEmpty ? nil : freshStarts,
                                                       freshStartsBuild: freshStarts.isEmpty ? nil : build,
                                                       edgeRefusals: edgeRefusals.isEmpty ? nil : edgeRefusals)) {
            try? data.write(to: statesURL, options: .atomic)
        }
    }
}

struct RecordingPassesAnswer: Decodable, Equatable, Sendable {
    let received: Int
}

/// What one recording pass came to: counts and the outcome, never anything of a recording.
/// On 2026-11-20 four wakes in a row sent no part request, and nothing on the phone said
/// whether the pass ran, what it found waiting or where it stopped. The last passes are kept
/// and told to a computer that takes them (`recordingPasses`), which writes them to its log.
struct RecordingPassRecord: Codable, Equatable, Sendable {
    /// When the pass began.
    var startedAt: String
    /// How long it ran; nil for a pass that has not ended (the app was put to sleep, or it hangs).
    var seconds: Double?
    /// Parts waiting whose turn it was, those among them the relay already held whole, and
    /// parts left out because a refusal made them wait.
    var pending = 0
    var whole = 0
    var notDue = 0
    /// Parts given to the system for upload, and what the relay said of each.
    var began = 0
    var beganWhole = 0
    var beganHanded = 0
    var beganNo = 0
    /// Parts the computer was told of ahead of their upload, and tells that got no answer.
    var told = 0
    var toldFailed = 0
    /// Part requests sent, parts the computer took, parts still on their way, parts it refused.
    var posted = 0
    var taken = 0
    var inFlight = 0
    var refused = 0
    /// How the pass ended, and the kind of error that ended it, if one did.
    var outcome: String?
    var stoppedBy: String?
    /// Wakes that found this pass still running and so started none.
    var skipped = 0
    /// 1 when this pass replaced one that was still running and had done nothing for
    /// `WatchRecordingForwarder.staleAfter`.
    var replacedStale: Int?
    /// Parts iOS had held for minutes without an end, started afresh in this pass. Sent only to
    /// a computer that takes it (`recordingPassRestarts`).
    var restarted: Int?
    /// Parts the app sent itself in a wake until the relay held them whole. Sent only to a
    /// computer that takes it (`recordingInWakeSend`).
    var sentInWake: Int?
    /// The computer has been told of this record.
    var reported: Bool?

    /// The records as a computer takes them: one from before `restarted` gets none.
    static func forComputer(_ passes: [RecordingPassRecord], takesRestarts: Bool,
                            takesInWake: Bool = false) -> [RecordingPassRecord] {
        guard !takesRestarts || !takesInWake else { return passes }
        return passes.map { pass in
            var pass = pass
            if !takesRestarts { pass.restarted = nil }
            if !takesInWake { pass.sentInWake = nil }
            return pass
        }
    }

    /// A short word for an error: its kind, never its text.
    static func word(_ error: Error) -> String {
        switch error {
        case RelayClientError.offline: return "relay-offline"
        case RelayClientError.timedOut: return "relay-timed-out"
        case RelayClientError.rateLimited: return "relay-429"
        case RelayClientError.unauthorized: return "relay-401"
        case RelayClientError.unavailable: return "relay-503"
        case RelayClientError.server(let status, _): return "relay-\(status)"
        case RelayClientError.badRequest: return "relay-400"
        case RelayClientError.forbidden: return "relay-403"
        case is RelayEdgeForbidden: return "edge-403"
        case RelayClientError.notFound: return "relay-404"
        case RelayClientError.conflict: return "relay-409"
        case RelayClientError.gone: return "relay-410"
        case RelayClientError.payloadTooLarge: return "relay-413"
        case RelayClientError.unsupportedMediaType: return "relay-415"
        case RelayClientError.invalidResponse: return "relay-invalid-answer"
        case HubRequestError.rejected(let status, _): return "computer-\(status)"
        case HubRequestError.noAnswer: return "computer-no-answer"
        case HubRequestError.queueFull: return "relay-queue-full"
        case is RecordingUploadInFlight: return "upload-on-the-way"
        case is CancellationError: return "cancelled"
        default:
            return String("\(type(of: error))".unicodeScalars.filter { CharacterSet.alphanumerics.contains($0) }.prefix(40))
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
