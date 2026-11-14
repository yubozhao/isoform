import Foundation

enum HubRoute: Equatable, Sendable {
    case yourAgent
    case status
    case today
    case todayMeetingPrep(id: String)
    case findTime
    case findTimeLegacy
    case holdProposal(kind: String, id: String)
    case holdStatus(id: String)
    case todayPart(version: UInt64, part: Int)
    case latestBrief
    case briefList(limit: Int)
    case brief(id: String)
    /// The whole package with ?package=1: part 0 and the part count (PROTOCOL.md 8.1).
    case briefPackage(id: String)
    case briefPart(id: String, version: String, part: Int)
    case briefReceived(id: String, version: String)
    case briefAudio(id: String, version: String)
    case briefAskAnswer(id: String, version: String, paragraph: Int, text: String)
    case briefCardAskAnswer(id: String, version: String, cardID: String, message: String)
    case briefWeekDecline(id: String, version: String)
    case briefWeekAskAnswer(id: String, version: String, topicID: String, message: String)
    case refreshBrief
    case chats(limit: Int)
    case chat(id: String, after: Int?, limit: Int)
    case notifications
    case keepAwake
    case tasks
    case task(id: String)
    case goals(limit: Int, cursor: String?)
    case goal(id: String)
    case offers
    case offersWithHolds
    case topics
    case privacy
    case topic(id: String, cursor: String?, limit: Int)
    case write(path: String, body: String)
    /// The state of this phone's recordings on the computer (PROTOCOL.md 24.3).
    case recordings(ids: [String])

    var method: String {
        switch self {
        case .yourAgent, .recordings: return "GET"
        case .refreshBrief, .write, .briefReceived: return "POST"
        case .status, .today, .todayMeetingPrep, .findTime, .findTimeLegacy, .holdProposal, .holdStatus, .todayPart, .latestBrief, .briefList, .brief, .briefPackage, .briefPart, .briefAudio, .briefAskAnswer, .briefCardAskAnswer, .briefWeekDecline, .briefWeekAskAnswer, .chats, .chat, .notifications, .keepAwake, .tasks, .task, .goals, .goal, .offers, .offersWithHolds,
             .topics, .topic, .privacy: return "GET"
        }
    }

    var path: String {
        switch self {
        case .yourAgent: return "/your-agent"
        case .recordings: return "/recordings"
        case .status: return "/status"
        case .today, .todayPart: return "/today"
        case .todayMeetingPrep: return "/today/meeting-prep"
        case .findTime: return "/calendar/free-time-details"
        case .findTimeLegacy: return "/calendar/free-time"
        case .holdProposal: return "/holds/propose"
        case .holdStatus: return "/holds/status"
        case .latestBrief: return "/brief/latest"
        case .briefList: return "/briefs"
        case .brief(let id): return "/briefs/\(id)"
        case .briefPackage(let id), .briefPart(let id, _, _): return "/briefs/\(id)"
        case .briefReceived(let id, _): return "/briefs/\(id)/received"
        case .briefAudio(let id, _): return "/briefs/\(id)/audio"
        case .briefAskAnswer(let id, _, _, _): return "/briefs/\(id)/ask-answer"
        case .briefCardAskAnswer(let id, _, _, _): return "/briefs/\(id)/ask-answer"
        case .briefWeekDecline(let id, _): return "/briefs/\(id)/week-decline"
        case .briefWeekAskAnswer(let id, _, _, _): return "/briefs/\(id)/ask-answer"
        case .refreshBrief: return "/brief/refresh"
        case .chats: return "/chats"
        case .chat(let id, _, _): return "/chats/\(id)"
        case .notifications: return "/notifications"
        case .keepAwake: return "/settings/keep-awake"
        case .tasks: return "/tasks"
        case .task(let id): return "/tasks/\(id)"
        case .goals: return "/goals"
        case .goal(let id): return "/goals/\(id)"
        case .offers, .offersWithHolds: return "/offers"
        case .topics: return "/topics"
        case .privacy: return "/privacy"
        case .topic(let id, _, _): return "/topics/\(id)"
        case .write(let path, _): return path
        }
    }

    var query: String {
        switch self {
        case .yourAgent: return ""
        case .recordings(let ids): return ids.isEmpty ? "" : "ids=" + ids.joined(separator: ",")
        case .todayPart(let version, let part): return "version=\(version)&part=\(part)"
        case .todayMeetingPrep(let id):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "id", value: id)]
            return components.percentEncodedQuery ?? ""
        case .holdProposal(let kind, let id):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "source_kind", value: kind), URLQueryItem(name: "source_id", value: id)]
            return components.percentEncodedQuery ?? ""
        case .holdStatus(let id):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "id", value: id)]
            return components.percentEncodedQuery ?? ""
        case .offersWithHolds: return "holds=1"
        case .briefPackage: return "package=1"
        case .briefPart(_, let version, let part): return "version=\(version)&part=\(part)"
        case .briefAudio(_, let version):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "version", value: version)]
            return (components.percentEncodedQuery ?? "").replacingOccurrences(of: "+", with: "%2B")
        case .briefAskAnswer(_, let version, let paragraph, let text):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "version", value: version),
                                     URLQueryItem(name: "paragraph", value: String(paragraph)),
                                     URLQueryItem(name: "text", value: text)]
            return (components.percentEncodedQuery ?? "").replacingOccurrences(of: "+", with: "%2B")
        case .briefCardAskAnswer(_, let version, let cardID, let message):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "version", value: version),
                                     URLQueryItem(name: "cardId", value: cardID),
                                     URLQueryItem(name: "cardMessage", value: message)]
            return (components.percentEncodedQuery ?? "").replacingOccurrences(of: "+", with: "%2B")
        case .briefWeekDecline(_, let version):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "version", value: version)]
            return components.percentEncodedQuery ?? ""
        case .briefWeekAskAnswer(_, let version, let topicID, let message):
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "version", value: version), URLQueryItem(name: "weekTopicId", value: topicID), URLQueryItem(name: "weekMessage", value: message)]
            return (components.percentEncodedQuery ?? "").replacingOccurrences(of: "+", with: "%2B")
        case .briefList(let limit): return "limit=\(min(max(limit, 1), 14))"
        case .chats(let limit): return "limit=\(min(max(limit, 1), 50))"
        case .chat(_, let after, let limit):
            // Without `after` the Mac answers with its newest page. `after=0` is the start of the chat.
            var items = ["limit=\(min(max(limit, 1), 50))", "firstRead=1"]
            if let after { items.insert("after=\(max(after, 0))", at: 0) }
            return items.joined(separator: "&")
        case .goals(let limit, let cursor):
            // The cursor is base64url from the Mac, so it needs no escaping (PROTOCOL.md 20.5).
            var items = ["limit=\(min(max(limit, 1), 100))"]
            if let cursor, !cursor.isEmpty { items.append("cursor=\(cursor)") }
            return items.joined(separator: "&")
        case .topic(_, let cursor, let limit):
            // The cursor is base64url from the computer and opaque to the phone (PROTOCOL.md 15.8).
            var items = ["limit=\(min(max(limit, 1), 50))"]
            if let cursor, !cursor.isEmpty { items.insert("cursor=\(cursor)", at: 0) }
            return items.joined(separator: "&")
        case .status, .today, .findTime, .findTimeLegacy, .latestBrief, .brief, .briefReceived, .refreshBrief, .notifications, .keepAwake, .tasks, .task, .goal, .offers, .topics, .privacy, .write: return ""
        }
    }

    var body: String {
        switch self {
        case .yourAgent, .recordings: return ""
        case .refreshBrief: return "{}"
        case .write(_, let body): return body
        case .briefReceived(_, let version): return "{\"version\":\"\(version)\"}"
        case .status, .today, .todayMeetingPrep, .findTime, .findTimeLegacy, .holdProposal, .holdStatus, .todayPart, .latestBrief, .briefList, .brief, .briefPackage, .briefPart, .briefAudio, .briefAskAnswer, .briefCardAskAnswer, .briefWeekDecline, .briefWeekAskAnswer, .chats, .chat, .notifications, .keepAwake, .tasks, .task, .goals, .goal, .offers, .offersWithHolds,
             .topics, .topic, .privacy: return ""
        }
    }

    var traceName: String {
        switch self {
        case .yourAgent: return "your-agent"
        case .recordings: return "recordings.list"
        case .status: return "status"
        case .today, .todayPart: return "today"
        case .todayMeetingPrep: return "today.meeting-prep"
        case .findTime, .findTimeLegacy: return "calendar.free-time"
        case .holdProposal: return "holds.propose"
        case .holdStatus: return "holds.status"
        case .latestBrief: return "brief.latest"
        case .briefList: return "brief.list"
        case .brief: return "brief.detail"
        case .briefPackage, .briefPart: return "brief.package"
        case .briefReceived: return "brief.received"
        case .briefAudio: return "brief.audio"
        case .briefAskAnswer, .briefCardAskAnswer, .briefWeekAskAnswer: return "brief.ask-answer"
        case .briefWeekDecline: return "brief.week-decline"
        case .refreshBrief: return "brief.refresh"
        case .chats: return "chat.list"
        case .chat: return "chat.detail"
        case .notifications: return "notifications"
        case .keepAwake: return "settings.keepAwake"
        case .tasks: return "tasks.list"
        case .task: return "tasks.detail"
        case .goals: return "goals.list"
        case .goal: return "goals.detail"
        case .offers, .offersWithHolds: return "offers.list"
        case .topics: return "topics.list"
        case .privacy: return "privacy.sees"
        case .topic: return "topics.detail"
        case .write: return "write"
        }
    }
}

struct HubResponse: Equatable, Sendable {
    let status: Int
    let body: String

    func payload<T: Decodable>(_ type: T.Type) throws -> T {
        let envelope = try? JSONDecoder().decode(HubEnvelope<T>.self, from: Data(body.utf8))
        guard (200..<300).contains(status) else {
            if status == 409, let held = try? JSONDecoder().decode(ConfirmSendBody.self, from: Data(body.utf8)),
               held.error == "confirm_send" {
                throw HubRequestError.confirmSend(person: held.data?.person)
            }
            throw HubRequestError.rejection(status: status, message: envelope?.error)
        }
        guard let envelope else {
            throw HubRequestError.malformedResponse
        }
        guard envelope.ok else {
            throw HubRequestError.rejection(status: status, message: envelope.error)
        }
        guard let payload = envelope.data else {
            throw HubRequestError.malformedResponse
        }
        return payload
    }
}

/// The head of GET /briefs/{id}?package=1: the first part and how many parts the package has.
struct BriefPackageHead: Decodable, Equatable, Sendable {
    let briefId: String?
    let version: String
    let parts: Int
    let data: String
}

/// One later part of a package, GET /briefs/{id}?version=V&part=I.
struct BriefPackagePart: Decodable, Equatable, Sendable {
    let version: String
    let part: Int
    let data: String
}

struct BriefReceiptAnswer: Decodable, Equatable, Sendable {
    let received: Bool
}

/// The brief bodies the phone reads from the Mac. BriefStore's library sync is the only caller that fetches
/// them (design section 6); the hub conforms.
protocol BriefBodyAPI: Sendable {
    func briefPackage(id: String) async throws -> (version: String, package: Data)
    /// The brief as one object, from a Mac before 1.0, which serves no package.
    func brief(id: String) async throws -> MobileBriefV1
    func briefReceived(id: String, version: String) async throws
}

extension HubChannel: BriefBodyAPI {}

/// The computer's 409 for a send it held: the error names it, and data names the person it is for.
struct ConfirmSendBody: Decodable {
    struct Details: Decodable {
        let person: String?
    }
    let error: String?
    let data: Details?
}

struct HubEnvelope<Payload: Decodable>: Decodable {
    let ok: Bool
    let data: Payload?
    let error: String?
}

struct PhoneFreeTimeRange: Decodable, Equatable, Sendable {
    let start: String
    let end: String
}

struct PhoneFreeTimeDetails: Decodable, Equatable, Sendable {
    let ranges: [PhoneFreeTimeRange]
    let checkedCalendars: [String]
    let browserOnly: [String]
}

struct HubStatus: Decodable, Equatable, Sendable {
    let hubName: String?
    let appVersion: String?
    let protocolVersion: Int?
    let narrativeAvailable: Bool?
    let lastBriefAt: String?
    var schedule: MobileBriefSchedule? = nil
    /// The computer has the RC 1 topic routes (PROTOCOL.md 15.8). Older computers omit it.
    var topics: Bool? = nil
    var firstRead: Bool? = nil
    var todayMeetingPrep: Bool? = nil
    /// The computer sends to its main chat when the phone lists none (PROTOCOL.md 15.5a). Older computers omit it.
    var chatMainSend: Bool? = nil
    var holds: Bool? = nil
    var briefRelevanceFeedback: Bool? = nil
    var briefAsk: Bool? = nil
    var briefWeekDecline: Bool? = nil
    var notificationTest: Bool? = nil
    var keepAwake: Bool? = nil
    var instantPush: Bool? = nil
    var brainDump: Bool? = nil
    /// The computer takes watch and phone recordings (PROTOCOL.md 24). Older computers omit it.
    var recordings: Bool? = nil
    var briefSchedule: [ScheduledPhoneBrief]? = nil
}

struct ScheduledPhoneBrief: Codable, Equatable, Sendable {
    let slot: String
    let at: String
    var date: Date? { RelayTime.parse(at) }
}

struct MobileBriefSchedule: Codable, Equatable, Sendable {
    let morning: String?
    let evening: String?
}

struct BriefRefreshResult: Decodable, Equatable, Sendable {
    let status: String?
    let nextAllowedAt: String?
    var readyAt: String? = nil
}

/// The reply to POST /remote-access: the computer's iroh ticket and a bearer for its own relay.
struct RemoteAccessGrant: Decodable, Equatable, Sendable {
    let ticket: String
    let bearer: String
    let expiresAt: String?
}

/// Which way a session reaches the computer: through the pairing's relay, or over the computer's direct path.
enum SessionRoute: Sendable, Equatable {
    case relay, direct
}

struct SessionContext: Sendable {
    let credentials: RelayCredentials
    let hubID: String
    let deviceID: String
    let keys: RelayCrypto.SessionKeys
    var route: SessionRoute = .relay

    /// A failure on the way to the computer, where the other path may still work. A refusal the relay
    /// answered on purpose (a conflict, a full queue, a bad request) is not one.
    static func isPathFailure(_ error: Error) -> Bool {
        guard let error = error as? RelayClientError else { return false }
        switch error {
        case .offline, .timedOut, .invalidResponse, .unauthorized, .unavailable, .server:
            return true
        default:
            return false
        }
    }
}

enum HubRequestError: LocalizedError, Equatable {
    case notPaired
    case noAnswer
    case queueFull(retryAfter: TimeInterval?)
    case rejectedEnvelope
    case rejected(status: Int, message: String)
    case conflict(message: String)
    /// The computer held a send that waited over an hour; the person must say yes again (PROTOCOL.md section 23).
    case confirmSend(person: String?)
    case forbidden
    case malformedResponse
    case invalidIdentifier

    /// Maps a hub answer that is not 2xx to a typed error. 403 means the paired scopes do not allow the
    /// route, 409 means the target changed or was resolved, and 400 is a bad request.
    static func rejection(status: Int, message: String?) -> HubRequestError {
        switch status {
        case 403:
            return .forbidden
        case 409:
            if message == "confirm_send" { return .confirmSend(person: nil) }
            return .conflict(message: message ?? "Your computer could not apply that because it changed.")
        default:
            return .rejected(status: status, message: message ?? "Your computer could not finish that request.")
        }
    }

    var errorDescription: String? {
        switch self {
        case .confirmSend:
            return "Your computer was away for over an hour. Confirm before sending."
        case .notPaired:
            return "This phone is not paired with a computer yet."
        case .noAnswer:
            return "Your computer has not answered yet. It may be asleep or offline."
        case .queueFull:
            return "Requests to your computer are queued. Try again in a minute."
        case .rejectedEnvelope:
            return "Isoform could not send that request. Try again."
        case .rejected(_, let message):
            return message
        case .conflict(let message):
            return message
        case .forbidden:
            return "Your computer did not allow that from this phone. Pair this phone again in Settings, then try again."
        case .malformedResponse:
            return "Your computer sent a response Isoform could not read."
        case .invalidIdentifier:
            return "That brief is not available on this phone."
        }
    }
}

private struct RequestPlaintext: Encodable {
    let id: String
    let method: String
    let path: String
    let query: String
    let body: String
}

private struct ResponsePlaintext: Decodable {
    let id: String
    let status: Int
    let body: String
}

protocol HubTransport: Sendable {
    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws
    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope]
    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws
}

extension RelayClient: HubTransport {}

enum HubRequestScope {
    @TaskLocal static var hubID: String?
    @TaskLocal static var deviceID: String?
}

actor HubChannel {
    static let pollWait = 25
    static let defaultTimeout: TimeInterval = 45
    static let pageLimit = 50
    static let maxPagesPerPoll = 20
    static let maxSendAttempts = 3
    private static let eventLimit = 32
    /// How long a poll that stopped at an envelope the vault could not save waits before asking the relay
    /// again. The relay answers at once while it still holds that envelope.
    static let unsavedRetryNanoseconds: UInt64 = 1_000_000_000

    /// What became of one envelope from a poll.
    private enum Receipt {
        /// Its sequence number is saved as the receive cursor.
        case saved
        /// Safe to acknowledge unsaved: a replay, or an envelope that is not for this iPhone.
        case skipped
        /// Readable but not saved. The relay must keep it, so nothing from it onwards is acknowledged.
        case notSaved
    }

    private let vault: PairingVault
    private let relay: HubTransport
    private var parked: [String: HubResponse] = [:]
    /// Requests still waiting for an answer. An answer to any other id belongs to a request that was cancelled
    /// or timed out: it is accepted for its sequence number and dropped, so it never takes a waiting one's place.
    private var waiting: Set<String> = []
    /// Waiting requests whose reply arrived but could not be saved. Only these fail; the others keep waiting.
    private var unsaved: [String: Error] = [:]
    private var pendingChatTurns: [MobileChatTurnEvent] = []
    private var pendingTodayEvents: [MobileTodayEvent] = []
    private var presence: MobilePresence?
    private var presenceCleared = true
    private var inFlightPoll: Task<Void, Error>?
    private var inFlightAcknowledgement: Task<Void, Never>?
    private var pendingAcknowledgement: UInt64?
    private var pairingChangeDepth = 0
    private var pairingGeneration = UUID()
    /// Called each time an answer or event from the computer arrives. The channel is the one place that hears the
    /// computer, so whatever waits for the computer to be back (queued chat messages) listens here.
    private var heardFromComputer: (@Sendable () -> Void)?

    init(vault: PairingVault, relay: HubTransport = RelayClient.shared) {
        self.vault = vault
        self.relay = relay
    }

    func onHeardFromComputer(_ handler: @escaping @Sendable () -> Void) {
        heardFromComputer = handler
    }

    /// Stop the old long-poll before the caller commits a replacement pairing. While this awaits the
    /// transport, new requests are refused and outstanding sends cannot start another poll or retry.
    func prepareForPairingChange() async {
        pairingChangeDepth += 1
        pairingGeneration = UUID()
        if let poll = inFlightPoll {
            poll.cancel()
            _ = await poll.result
        }
        if let acknowledgement = inFlightAcknowledgement {
            acknowledgement.cancel()
            await acknowledgement.value
        }
        pendingAcknowledgement = nil
        parked.removeAll()
        unsaved.removeAll()
        pendingChatTurns.removeAll()
        pendingTodayEvents.removeAll()
    }

    /// Resume after either committing the new pairing or retaining the old one after a failed save.
    func finishPairingChange() {
        pairingChangeDepth = max(0, pairingChangeDepth - 1)
    }

    func status() async throws -> HubStatus {
        try await perform(.status).payload(HubStatus.self)
    }

    func today() async throws -> MobileTodaySnapshot {
        try await today(timeout: HubChannel.defaultTimeout)
    }

    func findTime() async throws -> PhoneFreeTimeDetails {
        let response = try await perform(.findTime)
        if response.status == 404 {
            let ranges = try await perform(.findTimeLegacy).payload([PhoneFreeTimeRange].self)
            return PhoneFreeTimeDetails(ranges: ranges, checkedCalendars: [], browserOnly: [])
        }
        return try response.payload(PhoneFreeTimeDetails.self)
    }

    func today(timeout: TimeInterval) async throws -> MobileTodaySnapshot {
        let response = try await perform(.today, timeout: timeout).payload(MobileTodayResponse.self)
        if let snapshot = response.snapshot { return snapshot }
        guard let version = response.version, let count = response.parts, count > 0, count <= 512 else {
            throw HubRequestError.malformedResponse
        }
        var bytes = Data()
        for index in 0..<count {
            let piece = try await perform(.todayPart(version: version, part: index), timeout: timeout).payload(MobileTodayPart.self)
            guard piece.version == version, piece.part == index,
                  let data = try? RelayCrypto.base64URLDecode(piece.data), data.count <= 24 * 1024 else {
                throw HubRequestError.malformedResponse
            }
            bytes.append(data)
        }
        return try JSONDecoder().decode(MobileTodaySnapshot.self, from: bytes)
    }

    func latestBrief() async throws -> MobileBriefV1? {
        let response = try await perform(.latestBrief)
        if response.status == 404 { return nil }
        return try response.payload(MobileBriefV1.self)
    }

    func briefList(limit: Int = 14) async throws -> [BriefSummary] {
        try await perform(.briefList(limit: limit)).payload([BriefSummary].self)
    }

    func brief(id: String) async throws -> MobileBriefV1 {
        try await perform(.brief(id: id)).payload(MobileBriefV1.self)
    }

    /// The brief's whole package and the version the Mac named for it. Parts are fetched with that version;
    /// a 409 means the package changed, so the fetch starts again from the head (PROTOCOL.md 8.1).
    func briefPackage(id: String) async throws -> (version: String, package: Data) {
        try await briefPackage(id: id, restartsLeft: Self.briefRestarts)
    }

    private func briefPackage(id: String, restartsLeft: Int) async throws -> (version: String, package: Data) {
        do {
            let head = try await perform(.briefPackage(id: id)).payload(BriefPackageHead.self)
            guard head.briefId == nil || head.briefId == id, BriefCache.isVersion(head.version),
                  (1...Self.maxBriefParts).contains(head.parts) else {
                throw HubRequestError.malformedResponse
            }
            var package = try Self.briefPartBytes(head.data)
            if head.parts > 1 {
                for index in 1..<head.parts {
                    let piece = try await perform(.briefPart(id: id, version: head.version, part: index))
                        .payload(BriefPackagePart.self)
                    guard piece.version == head.version, piece.part == index else { throw HubRequestError.malformedResponse }
                    package.append(try Self.briefPartBytes(piece.data))
                }
            }
            return (head.version, package)
        } catch HubRequestError.conflict(_) where restartsLeft > 0 {
            return try await briefPackage(id: id, restartsLeft: restartsLeft - 1)
        }
    }

    /// Tells the Mac this phone saved the package with that version, so its outbox stops resending it.
    func briefReceived(id: String, version: String) async throws {
        guard BriefCache.isVersion(version) else { throw HubRequestError.malformedResponse }
        _ = try await perform(.briefReceived(id: id, version: version)).payload(BriefReceiptAnswer.self)
    }

    private static let briefRestarts = 2
    private static let maxBriefParts = 128
    private static let briefPartLimit = 24 * 1024

    private static func briefPartBytes(_ text: String) throws -> Data {
        guard let data = try? RelayCrypto.base64URLDecode(text), data.count <= briefPartLimit else {
            throw HubRequestError.malformedResponse
        }
        return data
    }

    func requestRefresh() async throws -> BriefRefreshResult {
        try await perform(.refreshBrief).payload(BriefRefreshResult.self)
    }

    func perform(_ route: HubRoute, timeout: TimeInterval = HubChannel.defaultTimeout) async throws -> HubResponse {
        let startedAt = FirstLoadTrace.now
        FirstLoadTrace.record("hub.\(route.traceName).start")
        defer { FirstLoadTrace.record("hub.\(route.traceName).finish", since: startedAt) }
        switch route {
        case .brief(let id), .briefPackage(let id), .briefPart(let id, _, _), .briefReceived(let id, _):
            guard Identifiers.isSafeBriefID(id) else { throw HubRequestError.invalidIdentifier }
        default:
            break
        }
        if case .briefAudio(let id, let version) = route,
           !Identifiers.isSafeBriefID(id) || version.isEmpty || version.utf8.count > 256 {
            throw HubRequestError.invalidIdentifier
        }
        if case .briefAskAnswer(let id, let version, let paragraph, let text) = route,
           !Identifiers.isSafeBriefID(id) || version.isEmpty || version.utf8.count > 256 || paragraph < 0 ||
            text.isEmpty || text.utf8.count > 8192 {
            throw HubRequestError.invalidIdentifier
        }
        if case .briefCardAskAnswer(let id, let version, let cardID, let message) = route,
           !Identifiers.isSafeBriefID(id) || version.isEmpty || version.utf8.count > 256 ||
            !Identifiers.isSafePathID(cardID) || message.isEmpty || message.utf8.count > 8192 {
            throw HubRequestError.invalidIdentifier
        }
        if case .briefWeekDecline(let id, let version) = route,
           !Identifiers.isSafeBriefID(id) || version.isEmpty || version.utf8.count > 256 {
            throw HubRequestError.invalidIdentifier
        }
        if case .briefWeekAskAnswer(let id, let version, let topicID, let message) = route,
           !Identifiers.isSafeBriefID(id) || version.isEmpty || version.utf8.count > 256 ||
            !Identifiers.isSafePathID(topicID) || message.isEmpty || message.utf8.count > 8192 {
            throw HubRequestError.invalidIdentifier
        }
        if case .chat(let id, _, _) = route, !Identifiers.isSafePathID(id) {
            throw HubRequestError.invalidIdentifier
        }
        if case .goal(let id) = route, !Identifiers.isSafePathID(id) {
            throw HubRequestError.invalidIdentifier
        }
        if case .goals(_, let cursor?) = route,
           !cursor.unicodeScalars.allSatisfy({ CharacterSet.alphanumerics.contains($0) || $0 == "-" || $0 == "_" }) {
            throw HubRequestError.invalidIdentifier
        }
        if case .topic(let id, let cursor, _) = route {
            guard Identifiers.isSafePathID(id), (cursor ?? "").unicodeScalars.allSatisfy({
                $0.isASCII && (CharacterSet.alphanumerics.contains($0) || $0 == "-" || $0 == "_")
            }) else { throw HubRequestError.invalidIdentifier }
        }
        var context = try makeContext()
        let generation = pairingGeneration
        let requestID = UUID().uuidString.lowercased()
        waiting.insert(requestID)
        defer {
            waiting.remove(requestID)
            parked.removeValue(forKey: requestID)
            unsaved.removeValue(forKey: requestID)
        }
        let plaintext = try JSONEncoder().encode(RequestPlaintext(id: requestID, method: route.method,
                                                                  path: route.path, query: route.query,
                                                                  body: route.body))
        // A pairing with two paths (its relay and the computer's direct path) tries the other one once when the
        // first cannot reach the computer. The computer answers on both, so a reply waits on either.
        // The other path carries the same sequence number: a first copy the relay took after all (a timeout after
        // it queued the request) is then the replay the computer drops, not a second run of the same request.
        var switched = false
        var sentSeq: UInt64?
        do {
            try await send(plaintext, context: context, generation: generation, sentSeq: &sentSeq)
        } catch let error where SessionContext.isPathFailure(error) && vault.switchRoute(after: context) {
            switched = true
            let firstSeq = sentSeq
            context = try makeContext()
            try await send(plaintext, context: context, generation: generation, reuseSeq: firstSeq, sentSeq: &sentSeq)
        }

        let deadline = Date().addingTimeInterval(timeout)
        while true {
            try checkSession(context, generation: generation)
            if let response = parked.removeValue(forKey: requestID) {
                FirstLoadTrace.record("hub.\(route.traceName).response", since: startedAt)
                return response
            }
            if let error = unsaved.removeValue(forKey: requestID) { throw error }
            guard Date() < deadline else { throw HubRequestError.noAnswer }
            do {
                try await pollShared(context, generation: generation)
            } catch let error where !switched && SessionContext.isPathFailure(error) && vault.switchRoute(after: context) {
                switched = true
                context = try makeContext()
            }
        }
    }

    private func makeContext() throws -> SessionContext {
        try Task.checkCancellation()
        guard pairingChangeDepth == 0 else { throw CancellationError() }
        let context: SessionContext
        do {
            context = try vault.session()
        } catch RelayClientError.offline {
            throw RelayClientError.offline
        } catch {
            throw HubRequestError.notPaired
        }
        if let expectedHub = HubRequestScope.hubID, expectedHub != context.hubID {
            throw CancellationError()
        }
        if let expectedDevice = HubRequestScope.deviceID, expectedDevice != context.deviceID {
            throw CancellationError()
        }
        return context
    }

    private func checkSession(_ context: SessionContext, generation: UUID) throws {
        try Task.checkCancellation()
        guard pairingChangeDepth == 0, generation == pairingGeneration else { throw CancellationError() }
        guard vault.current()?.deviceID == context.deviceID else { throw HubRequestError.notPaired }
    }

    /// Sends one request envelope. `reuseSeq` is the sequence number a copy on the other path already used: the
    /// first attempt carries it again, so the computer runs the request once whichever copy arrives first.
    /// `sentSeq` is the last sequence number put on the wire, even when the send then failed.
    private func send(_ plaintext: Data, context: SessionContext, generation: UUID, reuseSeq: UInt64? = nil,
                      sentSeq: inout UInt64?) async throws {
        var attempt = 0
        while true {
            try checkSession(context, generation: generation)
            attempt += 1
            let seq: UInt64
            if attempt == 1, let reuseSeq {
                seq = reuseSeq
            } else {
                seq = try vault.nextSendSeq(deviceID: context.deviceID)
            }
            sentSeq = seq
            let ct = try RelayCrypto.sealEnvelope(plaintext, key: context.keys.d2h, hub: context.hubID,
                                                  dev: context.deviceID, dir: "d2h", seq: seq, kind: "req")
            let envelope = RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                                         dir: "d2h", seq: seq, kind: "req", ct: RelayCrypto.base64URLEncode(ct))
            do {
                let startedAt = FirstLoadTrace.now
                FirstLoadTrace.record("relay.send.start")
                try await relay.sendRequest(context.credentials, envelope: envelope)
                FirstLoadTrace.record("relay.send.accepted", since: startedAt)
                try checkSession(context, generation: generation)
                return
            } catch RelayClientError.conflict where attempt < Self.maxSendAttempts {
                continue
            } catch RelayClientError.conflict {
                throw HubRequestError.rejectedEnvelope
            } catch RelayClientError.rateLimited(let retryAfter) {
                throw HubRequestError.queueFull(retryAfter: retryAfter)
            } catch RelayClientError.badRequest {
                throw HubRequestError.rejectedEnvelope
            }
        }
    }

    /// Runs one long-poll at a time. A caller that arrives while a poll is running waits for it, and every
    /// response that poll receives is parked for whichever request is waiting. A second, idle poll could ack a
    /// response that nobody parked and then hold for the full wait.
    private func pollShared(_ context: SessionContext, generation: UUID) async throws {
        try checkSession(context, generation: generation)
        if let running = inFlightPoll {
            try await running.value
            try checkSession(context, generation: generation)
            return
        }
        let poll = Task {
            defer { self.inFlightPoll = nil }
            try await self.pollOnce(context, generation: generation)
        }
        inFlightPoll = poll
        try await poll.value
        try checkSession(context, generation: generation)
    }

    private func pollOnce(_ context: SessionContext, generation: UUID) async throws {
        var page = 0
        while true {
            try checkSession(context, generation: generation)
            page += 1
            let cursor = try vault.lastAcceptedMessage(deviceID: context.deviceID)
            let wait = page == 1 ? Self.pollWait : 0
            let startedAt = FirstLoadTrace.now
            FirstLoadTrace.record("relay.poll.start")
            let envelopes = try await relay.pollResponses(context.credentials, after: cursor, wait: wait)
            FirstLoadTrace.record("relay.poll.received", since: startedAt)
            try checkSession(context, generation: generation)
            var accepted = cursor
            var highest = cursor
            var stoppedUnsaved = false
            for envelope in envelopes {
                switch accept(envelope, context: context, after: accepted) {
                case .saved:
                    accepted = envelope.seq
                    highest = max(highest, envelope.seq)
                case .skipped:
                    highest = max(highest, envelope.seq)
                case .notSaved:
                    // Saving a later envelope would move the cursor past this one, and the relay deletes what is
                    // acknowledged. Stop here: the next poll reads it, and everything after it, again. A
                    // notSaved envelope is above the cursor, so its seq is at least 1.
                    highest = min(highest, envelope.seq - 1)
                    stoppedUnsaved = true
                }
                if stoppedUnsaved { break }
            }
            if highest > cursor {
                queueAcknowledgement(upTo: highest, context: context, generation: generation)
            }
            if stoppedUnsaved {
                try await Task.sleep(nanoseconds: Self.unsavedRetryNanoseconds)
                return
            }
            guard envelopes.count >= Self.pageLimit, page < Self.maxPagesPerPoll else { return }
        }
    }

    /// Covers only envelopes whose receive cursor is saved, or that are safe to drop: a poll stops before the
    /// first envelope it could not save. So an ACK is cleanup, not a prerequisite to displaying an accepted
    /// answer. Keep one ACK in flight and coalesce later cursors; pairing changes cancel and drain it
    /// alongside the poll before replacing credentials.
    private func queueAcknowledgement(upTo sequence: UInt64, context: SessionContext, generation: UUID) {
        pendingAcknowledgement = max(pendingAcknowledgement ?? 0, sequence)
        guard inFlightAcknowledgement == nil else { return }
        inFlightAcknowledgement = Task {
            defer { self.inFlightAcknowledgement = nil }
            while let sequence = self.pendingAcknowledgement {
                self.pendingAcknowledgement = nil
                do {
                    try self.checkSession(context, generation: generation)
                    let startedAt = FirstLoadTrace.now
                    FirstLoadTrace.record("relay.ack.start")
                    try await self.relay.ackResponses(context.credentials, upTo: sequence)
                    FirstLoadTrace.record("relay.ack.finish", since: startedAt)
                    try self.checkSession(context, generation: generation)
                } catch {
                    // A newer page may have queued while this ACK failed. Give that watermark its own
                    // attempt; do not retry the failed watermark or hold accepted responses behind it.
                }
            }
        }
    }

    /// `cursor` is the receive cursor as this poll has saved it so far.
    private func accept(_ envelope: RelayEnvelope, context: SessionContext, after cursor: UInt64) -> Receipt {
        guard envelope.v == RelayCrypto.protocolVersion, envelope.dir == "h2d",
              envelope.hub == context.hubID, envelope.dev == context.deviceID,
              envelope.kind == "res" || envelope.kind == "event",
              envelope.seq > 0, envelope.seq <= RelayCrypto.maxSequence else { return .skipped }
        guard envelope.seq > cursor else { return .skipped }
        guard let ct = try? RelayCrypto.base64URLDecode(envelope.ct),
              let plaintext = try? RelayCrypto.openEnvelope(ct, key: context.keys.h2d, hub: context.hubID,
                                                           dev: context.deviceID, dir: "h2d",
                                                           seq: envelope.seq, kind: envelope.kind) else {
            return .skipped
        }
        var response: ResponsePlaintext?
        if envelope.kind == "res" {
            response = try? JSONDecoder().decode(ResponsePlaintext.self, from: plaintext)
        }
        do {
            try vault.acceptMessage(seq: envelope.seq, deviceID: context.deviceID)
        } catch {
            if let response, waiting.contains(response.id) { unsaved[response.id] = error }
            return .notSaved
        }
        heardFromComputer?()
        if envelope.kind == "event" {
            receiveEvent(plaintext)
            return .saved
        }
        guard let response, waiting.contains(response.id) else { return .saved }
        parked[response.id] = HubResponse(status: response.status, body: response.body)
        return .saved
    }

    /// Keeps `chat_turn` events for Chat. Any other event (a `notice` event, or a `needs_you` event from an older
    /// Mac) is accepted for its sequence number and otherwise ignored: the phone reads notices from their pushes.
    private func receiveEvent(_ plaintext: Data) {
        if let event = try? JSONDecoder().decode(MobilePresenceEvent.self, from: plaintext), event.event == "presence" {
            presence = event.text.map { MobilePresence(text: $0, at: RelayTime.parse(event.at) ?? Date()) }
            presenceCleared = event.text == nil
            return
        }
        if let event = try? JSONDecoder().decode(MobileTodayEvent.self, from: plaintext),
           event.event == "today_changed" {
            pendingTodayEvents.append(event)
            if pendingTodayEvents.count > Self.eventLimit {
                pendingTodayEvents.removeFirst(pendingTodayEvents.count - Self.eventLimit)
            }
            return
        }
        guard let turn = try? JSONDecoder().decode(MobileChatTurnEvent.self, from: plaintext),
              turn.event == "chat_turn" else { return }
        pendingChatTurns.append(turn)
        if pendingChatTurns.count > Self.eventLimit {
            pendingChatTurns.removeFirst(pendingChatTurns.count - Self.eventLimit)
        }
    }

    /// The newest presence line from the computer; nil once it cleared it.
    func latestPresence() async -> MobilePresence? {
        presenceCleared ? nil : presence
    }

    /// Chat turn events since the last call.
    func pollChatTurns() async throws -> [MobileChatTurnEvent] {
        let context = try makeContext()
        try await pollShared(context, generation: pairingGeneration)
        let drained = pendingChatTurns
        pendingChatTurns.removeAll()
        return drained
    }

    func pollTodayEvents() async throws -> [MobileTodayEvent] {
        try Task.checkCancellation()
        if pendingTodayEvents.isEmpty {
            let context = try makeContext()
            try await pollShared(context, generation: pairingGeneration)
        }
        try Task.checkCancellation()
        let drained = pendingTodayEvents
        pendingTodayEvents.removeAll()
        return drained
    }
}

// MARK: - Writes

extension HubChannel {
    /// Sends one POST over the envelope path and decodes its payload. Every write the phone makes goes through
    /// here, under the idempotency key its body carries.
    /// Asks the computer for its direct path (POST /remote-access). A computer without one answers 503.
    func remoteAccess() async throws -> RemoteAccessGrant {
        try await post("/remote-access", [String: String](), as: RemoteAccessGrant.self)
    }

    func post<Body: Encodable, Payload: Decodable>(_ path: String, _ body: Body,
                                                   as type: Payload.Type) async throws -> Payload {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        guard let text = String(data: try encoder.encode(body), encoding: .utf8) else {
            throw HubRequestError.malformedResponse
        }
        return try await perform(.write(path: path, body: text)).payload(type)
    }
}
