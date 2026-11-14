import Foundation
import OSLog

/// Debug timing only: phase names contain no chat text, credentials, or record identifiers.
enum FirstLoadTrace {
    private static let logger = Logger(subsystem: "com.yansu.companion", category: "FirstLoad")

    static var now: TimeInterval { ProcessInfo.processInfo.systemUptime }

    static func record(_ phase: String, since startedAt: TimeInterval? = nil) {
#if DEBUG
        let timestamp = now
        let elapsed = startedAt.map { String(format: " elapsed_ms=%.1f", (timestamp - $0) * 1_000) } ?? ""
        let line = String(format: "uptime_ms=%.1f ", timestamp * 1_000) + phase + elapsed
        logger.debug("\(line, privacy: .public)")
#endif
    }
}

// Device side of the relay HTTP API (mobile-protocol/PROTOCOL.md section 5). The relay stores
// ciphertext only. This client never sends a plaintext brief or request, never sends an Origin
// header, and always sends Content-Type: application/json with POST and PUT.
//
// Status codes are mapped one-to-one here; each caller decides what a code means for its flow
// (for example 403 on a claim is an invalid code, while 403 on a request is a foreign device).
enum RelayClientError: LocalizedError, Equatable {
    case badRequest(message: String)              // 400
    case unauthorized                             // 401
    case forbidden                                // 403
    case notFound                                 // 404
    case conflict                                 // 409
    case gone                                     // 410
    case payloadTooLarge                          // 413
    case unsupportedMediaType                     // 415
    case rateLimited(retryAfter: TimeInterval?)   // 429
    case unavailable                              // 503
    case offline
    case timedOut
    case invalidResponse
    case server(status: Int, message: String)

    var errorDescription: String? {
        switch self {
        case .badRequest(let message):
            return message
        case .unauthorized:
            return "This phone is no longer paired with your computer. Pair again in Settings."
        case .forbidden:
            return "This phone does not have permission for that. Pair again in Settings."
        case .notFound:
            return "The relay has nothing stored for that."
        case .conflict:
            return "The relay could not accept that request. Try again."
        case .gone:
            return "That is no longer available on the relay."
        case .payloadTooLarge:
            return "Isoform sent more data than the relay accepts."
        case .unsupportedMediaType:
            return "The relay could not read the request Isoform sent."
        case .rateLimited:
            return "Isoform asked too often. Try again in a few minutes."
        case .unavailable:
            return "The relay cannot serve requests right now. Try again later."
        case .offline:
            return "Isoform could not reach the relay. Check your connection and try again."
        case .timedOut:
            return "The relay took too long to respond. Try again."
        case .invalidResponse:
            return "The relay sent a response Isoform does not understand."
        case .server(_, let message):
            return message
        }
    }
}

// Where a paired device talks to, and the bearer it presents: the exact 43-character base64url
// text of 32 random bytes. The relay hashes that text, so it is never re-encoded.
struct RelayCredentials: Equatable, Sendable {
    let origin: URL
    let bearer: String
}

// Envelope as the relay stores and forwards it (section 4).
struct RelayEnvelope: Codable, Equatable, Sendable {
    let v: Int
    let hub: String
    let dev: String
    let dir: String
    let seq: UInt64
    let kind: String
    let ct: String
}

struct RelayBriefRecord: Decodable, Equatable, Sendable {
    let version: UInt64
    let asOf: String
    let env: RelayEnvelope
}

struct RelayPresence: Decodable, Equatable, Sendable {
    let hubOnline: Bool
    let lastSeenAt: String?
    var goodbyeCause: String? = nil
    var goodbyeAt: String? = nil
    /// Why the computer went to sleep: battery_floor, low_power, lid_closed, user_sleep or unknown. Optional.
    var goodbyeReason: String? = nil
}

struct PushTokenReceipt: Decodable, Equatable, Sendable {
    let enabled: Bool
    let registeredAt: String?
}

struct PairingClaimBody: Codable, Equatable, Sendable {
    let hub: String
    let claimToken: String
    let deviceId: String
    let devicePub: String
    let ct: String
}

struct PairingPoll: Decodable, Equatable, Sendable {
    struct Approval: Decodable, Equatable, Sendable {
        let ct: String
    }

    let state: String
    let approval: Approval?
}

// The pairing flow only needs these three calls, so the state machine can be tested
// with a stub instead of a live relay.
protocol PairingTransport: Sendable {
    func claimPairing(origin: URL, pairingID: String, body: PairingClaimBody) async throws
    func pollPairing(origin: URL, pairingID: String, claimToken: String) async throws -> PairingPoll
    func unpair(_ credentials: RelayCredentials) async throws
}

final class RelayClient: PairingTransport, @unchecked Sendable {
    static let shared = RelayClient()

    // Short calls time out after 20 s. Long-polls use 30 s, above the relay's 25 s cap.
    static let standardTimeout: TimeInterval = 20
    static let longPollTimeout: TimeInterval = 30
    static let maxResponseBytes = 8 * 1024 * 1024
    static let maxLongPollWait = 25

    private let session: URLSession
    private let redirects: RedirectRefusingDelegate

    // The notification service extension passes short timeouts: it has seconds, not the app's minutes.
    init(session: URLSession? = nil, requestTimeout: TimeInterval = RelayClient.standardTimeout,
         resourceTimeout: TimeInterval = 35) {
        let delegate = RedirectRefusingDelegate()
        redirects = delegate
        if let session {
            self.session = session
        } else {
            let configuration = URLSessionConfiguration.ephemeral
            configuration.timeoutIntervalForRequest = requestTimeout
            configuration.timeoutIntervalForResource = resourceTimeout
            configuration.httpCookieStorage = nil
            configuration.httpShouldSetCookies = false
            configuration.urlCache = nil
            configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
            configuration.waitsForConnectivity = false
            self.session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
        }
    }

    deinit {
        session.finishTasksAndInvalidate()
    }

    // MARK: Pairing (no bearer yet)

    // A repeat of an identical claim returns 202 again, so the caller may retry after a timeout.
    func claimPairing(origin: URL, pairingID: String, body: PairingClaimBody) async throws {
        let url = try Self.endpoint(origin, ["v1", "pairings", pairingID, "claim"])
        var request = URLRequest(url: url)
        request.httpMethod = Method.POST.rawValue
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.httpBody = try JSONEncoder().encode(body)
        let (data, http) = try await send(request)
        try check(http: http, data: data)
    }

    // Long-poll. The relay answers 401 for a missing or malformed claim header, 403 for a wrong token,
    // 409 while the pairing is still open, and 404 once an expired row was removed.
    func pollPairing(origin: URL, pairingID: String, claimToken: String) async throws -> PairingPoll {
        let url = try Self.endpoint(origin, ["v1", "pairings", pairingID])
        var request = URLRequest(url: url)
        request.httpMethod = Method.GET.rawValue
        request.timeoutInterval = Self.longPollTimeout
        request.setValue(claimToken, forHTTPHeaderField: "X-Yansu-Claim")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        let (data, http) = try await send(request)
        return try decode(PairingPoll.self, from: data, http: http)
    }

    // MARK: Paired device

    func presence(_ credentials: RelayCredentials) async throws -> RelayPresence {
        try await call(.GET, credentials, ["v1", "presence"], as: RelayPresence.self)
    }

    // Returns nil when the relay holds no brief for this device yet.
    func latestBrief(_ credentials: RelayCredentials) async throws -> RelayBriefRecord? {
        do {
            return try await call(.GET, credentials, ["v1", "brief", "latest"], as: RelayBriefRecord.self)
        } catch RelayClientError.notFound {
            return nil
        }
    }

    // The relay accepts limit 1...7 only.
    func briefs(_ credentials: RelayCredentials, limit: Int) async throws -> [RelayBriefRecord] {
        let clamped = min(max(limit, 1), 7)
        let list = try await call(.GET, credentials, ["v1", "briefs"],
                                  query: [URLQueryItem(name: "limit", value: String(clamped))],
                                  as: RelayBriefList.self)
        return list.briefs
    }

    // 202 accepted. 409 means the seq conflicts with an earlier request; the caller must bump the seq.
    // 429 means the queue is full and comes with Retry-After.
    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        let body = try JSONEncoder().encode(envelope)
        try await callVoid(.POST, credentials, ["v1", "requests"], body: body)
    }

    // The relay caps wait at 25 seconds and returns at most 50 envelopes per reply.
    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int = 25) async throws -> [RelayEnvelope] {
        let list = try await call(.GET, credentials, ["v1", "responses"],
                                  query: [URLQueryItem(name: "after", value: String(after)),
                                          URLQueryItem(name: "wait", value: String(min(max(wait, 0), Self.maxLongPollWait)))],
                                  timeout: Self.longPollTimeout, as: RelayResponseList.self)
        return list.envelopes
    }

    // Replies {"upTo":N}; the relay deletes delivered h2d envelopes up to N.
    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {
        let body = try JSONEncoder().encode(["upTo": upTo])
        try await callVoid(.POST, credentials, ["v1", "responses", "ack"], body: body)
    }

    func registerPushToken(_ credentials: RelayCredentials, deviceToken: String,
                           environment: String) async throws -> PushTokenReceipt {
        let body = try JSONEncoder().encode(PushTokenBody(deviceToken: deviceToken, environment: environment))
        return try await call(.PUT, credentials, ["v1", "push", "token"], body: body, as: PushTokenReceipt.self)
    }

    func deletePushToken(_ credentials: RelayCredentials) async throws {
        try await callVoid(.DELETE, credentials, ["v1", "push", "token"])
    }

    // Delivery and interaction receipts for a sealed notice (PROTOCOL.md section 14). The body is the
    // receipt JSON; the relay answers with the usual {"ok":true} envelope.
    func postNoticeReceipt(_ credentials: RelayCredentials, noticeID: String, body: Data) async throws {
        try await callVoid(.POST, credentials, ["v1", "notices", noticeID, "receipts"], body: body)
    }

    // Replies {"unpaired":true} once the device has been removed.
    func unpair(_ credentials: RelayCredentials) async throws {
        let receipt = try await call(.DELETE, credentials, ["v1", "devices", "me"], as: UnpairReceipt.self)
        guard receipt.unpaired else { throw RelayClientError.invalidResponse }
    }

    // MARK: Request plumbing

    enum Method: String {
        case GET, POST, PUT, DELETE
    }

    private struct RelayBriefList: Decodable {
        let briefs: [RelayBriefRecord]
    }

    private struct RelayResponseList: Decodable {
        let envelopes: [RelayEnvelope]
    }

    private struct UnpairReceipt: Decodable {
        let unpaired: Bool
    }

    private struct PushTokenBody: Encodable {
        let deviceToken: String
        let environment: String
    }

    private struct Envelope<Payload: Decodable>: Decodable {
        let ok: Bool
        let data: Payload?
        let error: String?
    }

    private struct NoPayload: Decodable {}

    // MARK: Attachments (PROTOCOL.md section 17)

    /// The sealed bytes of an attachment the hub uploaded. The reply is the file itself, not the JSON
    /// envelope, so a 2xx returns the body as it is; every other status maps like the other routes.
    func attachment(_ credentials: RelayCredentials, id: String) async throws -> Data {
        var request = try authorized(credentials, method: .GET, segments: ["v1", "attachments", id])
        request.setValue("application/octet-stream", forHTTPHeaderField: "Accept")
        let (data, http) = try await send(request)
        if (200..<300).contains(http.statusCode) {
            guard !data.isEmpty else { throw RelayClientError.invalidResponse }
            return data
        }
        try check(http: http, data: data)
        throw RelayClientError.invalidResponse
    }

    private func call<T: Decodable>(_ method: Method, _ credentials: RelayCredentials, _ segments: [String],
                                    query: [URLQueryItem] = [], body: Data? = nil,
                                    timeout: TimeInterval? = nil, as type: T.Type) async throws -> T {
        let request = try authorized(credentials, method: method, segments: segments, query: query,
                                     body: body, timeout: timeout)
        let (data, http) = try await send(request)
        return try decode(type, from: data, http: http)
    }

    private func callVoid(_ method: Method, _ credentials: RelayCredentials, _ segments: [String],
                          body: Data? = nil) async throws {
        let request = try authorized(credentials, method: method, segments: segments, body: body)
        let (data, http) = try await send(request)
        try check(http: http, data: data)
    }

    private func authorized(_ credentials: RelayCredentials, method: Method, segments: [String],
                            query: [URLQueryItem] = [], body: Data? = nil,
                            timeout: TimeInterval? = nil) throws -> URLRequest {
        var request = URLRequest(url: try Self.endpoint(credentials.origin, segments, query: query))
        request.httpMethod = method.rawValue
        request.setValue("Bearer \(credentials.bearer)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if method == .POST || method == .PUT {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = body ?? Data("{}".utf8)
        } else if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = body
        }
        if let timeout { request.timeoutInterval = timeout }
        return request
    }

    private func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        // A zrok public share serves an HTML warning page instead of the relay to browser-like clients; this
        // header asks it not to. Other relays ignore it.
        var request = request
        request.setValue("1", forHTTPHeaderField: "skip_zrok_interstitial")
        let phase: String
        switch request.url?.path {
        case "/v1/requests": phase = "relay.http.request"
        case "/v1/responses": phase = "relay.http.poll"
        case "/v1/responses/ack": phase = "relay.http.ack"
        case "/v1/presence": phase = "relay.http.presence"
        case "/v1/brief/latest": phase = "relay.http.brief"
        default: phase = "relay.http.other"
        }
        let startedAt = FirstLoadTrace.now
        FirstLoadTrace.record("\(phase).start")
        defer { FirstLoadTrace.record("\(phase).finish", since: startedAt) }
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse, data.count <= Self.maxResponseBytes else {
                throw RelayClientError.invalidResponse
            }
            return (data, http)
        } catch let error as URLError {
            switch error.code {
            case .cancelled:
                throw CancellationError()
            case .timedOut:
                throw RelayClientError.timedOut
            default:
                throw RelayClientError.offline
            }
        }
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data, http: HTTPURLResponse) throws -> T {
        try check(http: http, data: data)
        guard let envelope = try? JSONDecoder().decode(Envelope<T>.self, from: data),
              envelope.ok, let payload = envelope.data else {
            throw RelayClientError.invalidResponse
        }
        return payload
    }

    // Maps every documented status to its case. Any 2xx must still carry {"ok":true}.
    private func check(http: HTTPURLResponse, data: Data) throws {
        let status = http.statusCode
        let errorBody = try? JSONDecoder().decode(Envelope<NoPayload>.self, from: data)
        if (200..<300).contains(status) {
            guard let envelope = errorBody, envelope.ok else {
                throw RelayClientError.invalidResponse
            }
            return
        }
        let message = errorBody?.error
        switch status {
        case 400:
            throw RelayClientError.badRequest(message: message ?? "The relay could not read that request.")
        case 401: throw RelayClientError.unauthorized
        case 403: throw RelayClientError.forbidden
        case 404: throw RelayClientError.notFound
        case 409: throw RelayClientError.conflict
        case 410: throw RelayClientError.gone
        case 413: throw RelayClientError.payloadTooLarge
        case 415: throw RelayClientError.unsupportedMediaType
        case 429: throw RelayClientError.rateLimited(retryAfter: Self.retryAfter(http))
        case 503: throw RelayClientError.unavailable
        case 500..<600:
            throw RelayClientError.server(status: status, message: message ?? "The relay failed while handling that request.")
        default:
            throw RelayClientError.server(status: status,
                                          message: message ?? "The relay returned an unexpected status (HTTP \(status)).")
        }
    }

    // Retry-After is whole seconds in this relay. Anything else is treated as absent.
    private static func retryAfter(_ http: HTTPURLResponse) -> TimeInterval? {
        guard let value = http.value(forHTTPHeaderField: "Retry-After") else { return nil }
        guard let seconds = Double(value.trimmingCharacters(in: .whitespaces)), seconds >= 0 else { return nil }
        return seconds
    }

    // The origin has no path, query or fragment (validated when the invitation is parsed).
    static func endpoint(_ origin: URL, _ segments: [String], query: [URLQueryItem] = []) throws -> URL {
        var url = origin
        for segment in segments {
            url.appendPathComponent(segment)
        }
        guard !query.isEmpty else { return url }
        guard var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            throw RelayClientError.invalidResponse
        }
        components.queryItems = query
        guard let result = components.url else { throw RelayClientError.invalidResponse }
        return result
    }
}

// Credentials and bearers never follow redirects, including redirects to another path.
final class RedirectRefusingDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}
