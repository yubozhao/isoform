import Foundation

// What the watch and the phone say to each other about recordings. Both targets compile this
// file, so the two sides cannot drift. The phone's tests cover it.
//
// Watch to phone: one WCSession file transfer per finished part, with WatchRecordingPartInfo as
// its metadata. Phone to watch: the application context carries the state of recent recordings.
enum WatchRecordingWire {
    static let version = 1
    /// A long recording is cut into parts of this length; each part is a complete audio file.
    static let partSeconds: TimeInterval = 5 * 60
    /// PROTOCOL.md section 24: parts are numbered 0 to 63.
    static let maxParts = 64
    static let mime = "audio/mp4"
    static let contextKey = "recordingStates"
    static let contextVersionKey = "v"
}

/// One finished part of a recording, as the watch describes it to the phone.
struct WatchRecordingPartInfo: Codable, Equatable, Sendable {
    let recordingID: String
    let part: Int
    /// The final part. A recording still running does not know its part count.
    let last: Bool
    let startedAt: Date
    let durationSeconds: Double
    var model: String?
    /// The wearer's heart rate and coarse location during this part, when they allowed each.
    var context: WatchRecordingContext?
    /// The moments the wearer marked during this part, in seconds from its start (PROTOCOL.md 24.9).
    var marks: [Double]?

    /// The metadata dictionary of a WCSession file transfer (property-list values only).
    var metadata: [String: Any] {
        var out: [String: Any] = [
            "v": WatchRecordingWire.version, "recordingId": recordingID, "part": part, "last": last,
            "startedAt": startedAt.timeIntervalSince1970, "durationSeconds": durationSeconds,
        ]
        if let model { out["model"] = model }
        context?.write(into: &out)
        if let marks, !marks.isEmpty { out["marks"] = marks }
        return out
    }

    init(recordingID: String, part: Int, last: Bool, startedAt: Date, durationSeconds: Double, model: String? = nil,
         context: WatchRecordingContext? = nil, marks: [Double]? = nil) {
        self.marks = marks.flatMap { WatchRecordingWire.cleanMarks($0, durationSeconds: durationSeconds) }
        self.recordingID = recordingID
        self.part = part
        self.last = last
        self.startedAt = startedAt
        self.durationSeconds = durationSeconds
        self.model = model
        self.context = context
    }

    /// Reads a transfer's metadata. Anything malformed is refused, so a file is never forwarded
    /// under a guessed identity.
    init?(metadata: [String: Any]?) {
        guard let metadata, metadata["v"] as? Int == WatchRecordingWire.version,
              let id = metadata["recordingId"] as? String, WatchRecordingWire.isRecordingID(id),
              let part = metadata["part"] as? Int, (0..<WatchRecordingWire.maxParts).contains(part),
              let last = metadata["last"] as? Bool,
              let started = metadata["startedAt"] as? Double, started > 0,
              let duration = metadata["durationSeconds"] as? Double, duration > 0, duration <= 4 * 60 * 60 else {
            return nil
        }
        self.init(recordingID: id, part: part, last: last, startedAt: Date(timeIntervalSince1970: started),
                  durationSeconds: duration, model: (metadata["model"] as? String).map { String($0.prefix(40)) },
                  context: WatchRecordingContext(metadata: metadata), marks: metadata["marks"] as? [Double])
    }
}

extension WatchRecordingWire {
    /// A lowercase UUID, the only form the computer accepts.
    static func isRecordingID(_ text: String) -> Bool {
        guard let uuid = UUID(uuidString: text) else { return false }
        return uuid.uuidString.lowercased() == text
    }

    static func newRecordingID() -> String { UUID().uuidString.lowercased() }
}

/// Where a recording is, in the order it travels. Raw values cross the watch link.
enum WatchRecordingRemoteState: String, Codable, Sendable, Comparable {
    case onPhone
    case onComputer
    case transcribed
    case failed
    /// Deleted by the person on any device (PROTOCOL.md 24.5): every device removes its copy.
    case deleted

    private var rank: Int {
        switch self {
        case .onPhone: return 0
        case .onComputer: return 1
        case .transcribed: return 2
        case .failed: return 3
        case .deleted: return 4
        }
    }

    static func < (lhs: Self, rhs: Self) -> Bool { lhs.rank < rhs.rank }

    /// Maps the computer's state (PROTOCOL.md 24.3). An unknown state reads as "on your computer".
    init(computerState: String) {
        switch computerState {
        case "transcribed": self = .transcribed
        case "failed": self = .failed
        case "deleted": self = .deleted
        default: self = .onComputer
        }
    }
}

extension WatchRecordingWire {
    /// The most marks one part carries.
    static let maxMarks = 60

    /// Marks inside the part, in order, without repeats; nil when none is usable.
    static func cleanMarks(_ marks: [Double], durationSeconds: Double) -> [Double]? {
        let kept = Set(marks.filter { $0.isFinite && $0 >= 0 && $0 <= durationSeconds }.map { ($0 * 10).rounded() / 10 })
        let sorted = Array(kept.sorted().prefix(maxMarks))
        return sorted.isEmpty ? nil : sorted
    }

    /// Observing is paused on the computer (PROTOCOL.md 24.8): the watch stops too.
    static let pauseKey = "observingPause"

    static func pause(fromContext context: [String: Any]) -> WatchPause? {
        guard context[contextVersionKey] as? Int == version, let raw = context[pauseKey] as? [String: Double] else { return nil }
        return WatchPause(since: raw["since"].map { Date(timeIntervalSince1970: $0) },
                          until: raw["until"].map { Date(timeIntervalSince1970: $0) })
    }

    /// The names of what a recording heard, as the computer gave them (PROTOCOL.md 24.7).
    static let namesKey = "recordingNames"
    static let maxNames = 4
    static let maxNameLength = 48

    /// Keeps a few short, non-empty names.
    static func cleanNames(_ names: [String]) -> [String] {
        names.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .prefix(maxNames)
            .map { String($0.prefix(maxNameLength)) }
    }

    /// The application context the phone sends: recording id to state, and recording id to the
    /// names of the conversations heard in it, once the computer has named any.
    /// A number the phone raises each time it carried a pause request of the watch to the
    /// computer. The system does not deliver a context equal to the last one, so without it the
    /// watch would never hear "nothing changed" (a pause made at the computer was not ended).
    static let pauseSerialKey = "pauseSerial"

    /// "Listen on your watch" was turned off on the phone at this time (PROTOCOL.md 24.14). The
    /// watch stops, and stays off until the wearer starts it on the watch.
    static let listenOffKey = "listenOff"

    static func listenOff(fromContext context: [String: Any]) -> Date? {
        guard context[contextVersionKey] as? Int == version, let since = context[listenOffKey] as? Double, since > 0 else { return nil }
        return Date(timeIntervalSince1970: since)
    }

    /// Heart rate and Place as the person chose them on the computer or the phone (PROTOCOL.md
    /// 24.15). A field is left out while nobody has chosen: the watch's own switch decides
    /// then, as it does with an older computer.
    static let choicesKey = "contextChoices"

    static func choices(fromContext context: [String: Any]) -> WatchContextChoices? {
        guard context[contextVersionKey] as? Int == version, let raw = context[choicesKey] as? [String: Bool] else { return nil }
        let choices = WatchContextChoices(heartRate: raw["heartRate"], place: raw["place"])
        return choices.isEmpty ? nil : choices
    }

    static func context(_ states: [String: WatchRecordingRemoteState], names: [String: [String]] = [:],
                        pause: WatchPause? = nil, pauseSerial: Int = 0, listenOff: Date? = nil,
                        choices: WatchContextChoices? = nil) -> [String: Any] {
        var context: [String: Any] = [contextVersionKey: version, contextKey: states.mapValues(\.rawValue)]
        if let listenOff { context[listenOffKey] = listenOff.timeIntervalSince1970 }
        if let choices, !choices.isEmpty { context[choicesKey] = choices.raw }
        if pauseSerial > 0 { context[pauseSerialKey] = pauseSerial }
        if let pause {
            var raw: [String: Double] = [:]
            if let since = pause.since { raw["since"] = since.timeIntervalSince1970 }
            if let until = pause.until { raw["until"] = until.timeIntervalSince1970 }
            context[pauseKey] = raw
        }
        let kept = names.filter { states[$0.key] != nil && states[$0.key] != .deleted }
            .mapValues(cleanNames).filter { !$0.value.isEmpty }
        if !kept.isEmpty { context[namesKey] = kept }
        return context
    }

    static func names(fromContext context: [String: Any]) -> [String: [String]] {
        guard context[contextVersionKey] as? Int == version,
              let raw = context[namesKey] as? [String: [String]] else { return [:] }
        var out: [String: [String]] = [:]
        for (id, names) in raw where isRecordingID(id) {
            let clean = cleanNames(names)
            if !clean.isEmpty { out[id] = clean }
        }
        return out
    }

    static func states(fromContext context: [String: Any]) -> [String: WatchRecordingRemoteState] {
        guard context[contextVersionKey] as? Int == version,
              let raw = context[contextKey] as? [String: String] else { return [:] }
        var out: [String: WatchRecordingRemoteState] = [:]
        for (id, value) in raw where isRecordingID(id) {
            if let state = WatchRecordingRemoteState(rawValue: value) { out[id] = state }
        }
        return out
    }
}

/// A recording that still has audio on the watch, as the watch tells the phone, so the phone can
/// show "On your watch" for something it has not received yet.
struct WatchRecordingSummary: Equatable, Identifiable, Sendable {
    let id: String
    let startedAt: Date
    let durationSeconds: Double
    /// Parts whose audio is still on the watch.
    let unsentParts: Int
    /// False while the recording is still running.
    let finished: Bool
}

extension WatchRecordingWire {
    static let summariesKey = "watchRecordings"
    static let maxSummaries = 30
    /// A message the phone sends when the person taps Sync now: queue everything again.
    static let syncMessageKey = "syncRecordings"
    /// Sent with transferUserInfo when the computer forgets everything: the watch deletes every recording it holds.
    static let forgetAllKey = "forgetAllRecordings"

    /// The application context the watch sends: what it still holds, newest first.
    /// What the watch is doing right now, for the phone's "Your watch" card.
    static let statusKey = "watchStatus"

    static func summaryContext(_ summaries: [WatchRecordingSummary], status: WatchLiveStatus? = nil) -> [String: Any] {
        var context: [String: Any] = [contextVersionKey: version, summariesKey: summaries.prefix(maxSummaries).map { item -> [String: Any] in
            ["id": item.id, "startedAt": item.startedAt.timeIntervalSince1970, "durationSeconds": item.durationSeconds,
             "unsent": item.unsentParts, "finished": item.finished]
        }]
        if let status { context[statusKey] = status.rawValue }
        return context
    }

    /// Nil when the watch did not say (an older watch app), or said something this phone does not know.
    static func status(fromContext context: [String: Any]) -> WatchLiveStatus? {
        guard context[contextVersionKey] as? Int == version, let raw = context[statusKey] as? String else { return nil }
        return WatchLiveStatus(rawValue: raw)
    }

    static func summaries(fromContext context: [String: Any]) -> [WatchRecordingSummary] {
        guard context[contextVersionKey] as? Int == version, let raw = context[summariesKey] as? [[String: Any]] else { return [] }
        return raw.prefix(maxSummaries).compactMap { item in
            guard let id = item["id"] as? String, isRecordingID(id),
                  let started = item["startedAt"] as? Double, started > 0,
                  let duration = item["durationSeconds"] as? Double, duration >= 0,
                  let unsent = item["unsent"] as? Int, unsent >= 0, let finished = item["finished"] as? Bool else { return nil }
            return WatchRecordingSummary(id: id, startedAt: Date(timeIntervalSince1970: started), durationSeconds: duration,
                                         unsentParts: unsent, finished: finished)
        }
    }
}

/// Observing is paused on the computer. One pause covers all of Isoform, so the watch stops
/// recording while it lasts. `since` and `until` are nil when the computer does not know them:
/// a pause with no end, or a start it cannot vouch for.
struct WatchPause: Equatable, Sendable {
    var since: Date?
    var until: Date?

    /// A pause with no end lasts until the phone says it is over.
    func isActive(at now: Date) -> Bool { until.map { $0 > now } ?? true }
}

/// What the watch asks of the phone about the pause. The watch cannot reach the computer, so the
/// phone asks for it (PROTOCOL.md 24.10).
enum WatchPauseRequest: Equatable, Sendable {
    /// Pause Isoform for this many minutes.
    case pause(minutes: Int)
    /// End the pause the watch started.
    case resume

    static let key = "pauseRequest"
    /// A pause from the wrist lasts an hour unless changed: the length of a meeting or a visit.
    static let defaultMinutes = 60

    /// The user-info dictionary sent to the phone.
    var userInfo: [String: Any] {
        switch self {
        case .pause(let minutes): return [Self.key: ["minutes": minutes]]
        case .resume: return [Self.key: ["resume": true]]
        }
    }

    init?(userInfo: [String: Any]) {
        guard let raw = userInfo[Self.key] as? [String: Any] else { return nil }
        if raw["resume"] as? Bool == true {
            self = .resume
        } else if let minutes = raw["minutes"] as? Int, (5...240).contains(minutes) {
            self = .pause(minutes: minutes)
        } else {
            return nil
        }
    }
}

extension WatchPause {
    /// The pause that holds on the watch: the computer's when it has one, else the one the wearer
    /// started on the watch while it has not run out. A pause started on the wrist takes effect
    /// there at once and holds even if the phone cannot reach the computer.
    static func effective(computer: WatchPause?, watch: WatchPause?, now: Date) -> WatchPause? {
        if let computer, computer.isActive(at: now) { return computer }
        if let watch, watch.isActive(at: now) { return watch }
        return nil
    }
}

/// What the watch is doing right now, in the words a person would use. The watch says it to the
/// phone with what it still holds, so the phone can show it without asking.
enum WatchLiveStatus: String, Equatable, Sendable {
    case recording
    /// A call or Siri has the microphone; recording comes back by itself.
    case waitingForMicrophone
    /// Observing is paused on the computer, so the watch does not record either.
    case pausedOnComputer
    /// The wearer paused Isoform from the watch.
    case pausedOnWatch
    /// The wearer stopped it; it stays off for the rest of the day.
    case stoppedToday
    /// The microphone is not allowed for Isoform on the watch.
    case microphoneOff
    case notRecording

    /// Works out the status from what the watch knows. A pause comes first: while it holds
    /// nothing is recorded, whatever else is true.
    static func of(paused: Bool, pauseStartedOnWatch: Bool, recording: Bool, waitingForMicrophone: Bool,
                   microphoneDenied: Bool, stoppedToday: Bool) -> WatchLiveStatus {
        if paused { return pauseStartedOnWatch ? .pausedOnWatch : .pausedOnComputer }
        if recording { return waitingForMicrophone ? .waitingForMicrophone : .recording }
        if microphoneDenied { return .microphoneOff }
        return stoppedToday ? .stoppedToday : .notRecording
    }
}

/// The person's choice for Heart rate and Place, one for the computer, the phone and the watch
/// (PROTOCOL.md 24.15). Nil for a field nobody has chosen, or that is not spoken of.
struct WatchContextChoices: Equatable, Sendable {
    var heartRate: Bool?
    var place: Bool?

    init(heartRate: Bool? = nil, place: Bool? = nil) {
        self.heartRate = heartRate
        self.place = place
    }

    var isEmpty: Bool { heartRate == nil && place == nil }

    var raw: [String: Bool] {
        var raw: [String: Bool] = [:]
        if let heartRate { raw["heartRate"] = heartRate }
        if let place { raw["place"] = place }
        return raw
    }

    /// The wearer changed a switch on the watch: the phone carries it to the computer.
    static let requestKey = "contextChoice"

    var userInfo: [String: Any] { [Self.requestKey: raw] }

    init?(userInfo: [String: Any]) {
        guard let raw = userInfo[Self.requestKey] as? [String: Bool] else { return nil }
        self.init(heartRate: raw["heartRate"], place: raw["place"])
        if isEmpty { return nil }
    }
}
