import Foundation

// What the watch and the phone say to each other about recordings. Both targets compile this
// file, so the two sides cannot drift. The phone's tests cover it.
//
// Watch to phone: one WCSession file transfer per finished part, with WatchRecordingPartInfo as
// its metadata. Phone to watch: the application context carries the state of recent recordings.
enum WatchRecordingWire {
    static let version = 1
    /// A long recording is cut into parts of this length; each part is a complete audio file.
    static let partSeconds: TimeInterval = 15 * 60
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

    /// The metadata dictionary of a WCSession file transfer (property-list values only).
    var metadata: [String: Any] {
        var out: [String: Any] = [
            "v": WatchRecordingWire.version, "recordingId": recordingID, "part": part, "last": last,
            "startedAt": startedAt.timeIntervalSince1970, "durationSeconds": durationSeconds,
        ]
        if let model { out["model"] = model }
        return out
    }

    init(recordingID: String, part: Int, last: Bool, startedAt: Date, durationSeconds: Double, model: String? = nil) {
        self.recordingID = recordingID
        self.part = part
        self.last = last
        self.startedAt = startedAt
        self.durationSeconds = durationSeconds
        self.model = model
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
                  durationSeconds: duration, model: (metadata["model"] as? String).map { String($0.prefix(40)) })
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

    private var rank: Int {
        switch self {
        case .onPhone: return 0
        case .onComputer: return 1
        case .transcribed: return 2
        case .failed: return 3
        }
    }

    static func < (lhs: Self, rhs: Self) -> Bool { lhs.rank < rhs.rank }

    /// Maps the computer's state (PROTOCOL.md 24.3). An unknown state reads as "on your computer".
    init(computerState: String) {
        switch computerState {
        case "transcribed": self = .transcribed
        case "failed": self = .failed
        default: self = .onComputer
        }
    }
}

extension WatchRecordingWire {
    /// The application context the phone sends: recording id to state.
    static func context(_ states: [String: WatchRecordingRemoteState]) -> [String: Any] {
        [contextVersionKey: version, contextKey: states.mapValues(\.rawValue)]
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
