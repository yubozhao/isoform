#if DEBUG && targetEnvironment(simulator)
import Foundation

enum YourAgentUITestSeed {
    static let states = ["opened", "learning", "taken", "sent", "offline", "placeholder", "late", "failed", "rate",
                         "code", "wrongCode", "yours", "choice", "ranOut", "older"]

    @MainActor
    static func makeStore(directory: URL, secrets: SecretStore, hubID: @escaping () -> String?) -> YourAgentStore? {
        let environment = ProcessInfo.processInfo.environment
        guard environment["YANSU_UITEST_SETTINGS_STATE"] == "paired",
              let state = environment["YANSU_UITEST_YOUR_AGENT_STATE"],
              states.contains(state),
              let hub = hubID() else { return nil }
        var copy = YourAgentCopy(hub: hub, token: "ui-test-token")
        copy.stillLearning = state == "learning"
        copy.nameCandidates = ["alex.rivera", "alexrivera", "a.rivera"]
        copy.specialties = [.init(name: "Weekly updates", seen: 22), .init(name: "Client SOWs", seen: 14),
                            .init(name: "Hiring plans", seen: 6)]
        copy.taste = ["Lead the weekly update with what changed, not what happened.",
                      "Put the price and the date in the first paragraph of an SOW.", "Name one owner for every open item."]
        switch state {
        case "sent":
            copy.reservation = AgentReservation(name: "alex.rivera", email: "alex.rivera@example.com",
                                                reservedAt: "2026-10-27T20:00:00Z")
        case "code", "wrongCode", "ranOut":
            copy.reservation = AgentReservation(name: "alex.rivera", email: "alex.rivera@example.com",
                                                reservedAt: "2026-10-30T13:00:00Z", status: .pending,
                                                expiresAt: "2099-01-01T00:00:00Z")
        case "choice":
            copy.reservation = AgentReservation(name: "alex.rivera", email: "alex.rivera@example.com",
                                                reservedAt: "2026-10-30T13:00:00Z", status: .choice,
                                                expiresAt: "2099-01-01T00:00:00Z", oldName: "arivera")
        case "yours":
            copy.reservation = AgentReservation(name: "alex.rivera", email: "alex.rivera@example.com",
                                                reservedAt: "2026-10-27T20:00:00Z", status: .verified, place: 214)
        case "older":
            copy.reservation = AgentReservation(name: "alex.rivera", email: "alex.rivera@example.com",
                                                reservedAt: "2026-10-20T20:00:00Z", status: .unconfirmed)
        default: break
        }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [YourAgentUITestProtocol.self]
        let api = AgentsClient(session: URLSession(configuration: configuration),
                               baseURL: URL(string: "https://your-agent-ui.invalid/\(state)")!)
        return YourAgentStore(hub: YourAgentUITestHub(copy: copy, placeholder: state == "placeholder", delayed: state == "late"), api: api,
            cache: YourAgentCache(directory: directory.appendingPathComponent("your-agent"), secrets: secrets),
            hubID: hubID, canRead: { true }, monitorNetwork: false)
    }
}

/// The Keychain is not usable by the Agent of You UI-test runs on this Simulator (ad-hoc builds), so their pairing
/// lives in memory for the length of the launch. DEBUG and simulator only.
final class YourAgentUITestSecrets: SecretStore, @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: String] = [:]

    func accountNames() throws -> [String] {
        lock.lock(); defer { lock.unlock() }
        return Array(values.keys)
    }

    func read(_ account: String) throws -> String? {
        lock.lock(); defer { lock.unlock() }
        return values[account]
    }

    func save(_ value: String, account: String) throws {
        lock.lock(); defer { lock.unlock() }
        values[account] = value
    }

    func delete(_ account: String) throws {
        lock.lock(); defer { lock.unlock() }
        values[account] = nil
    }
}

private struct YourAgentUITestHub: YourAgentHubAPI {
    let copy: YourAgentCopy
    let placeholder: Bool
    /// The copy arrives a few seconds after the page opens (P146: the sections appear when it does).
    var delayed = false
    func yourAgent() async throws -> YourAgentCopy {
        if delayed { try? await Task.sleep(for: .seconds(4)) }
        if placeholder { throw HubRequestError.noAnswer }
        return copy
    }
}

private final class YourAgentUITestProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "your-agent-ui.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let state = request.url!.pathComponents[1]
        if state == "offline" {
            client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
            return
        }
        var data = request.httpBody ?? Data()
        if let stream = request.httpBodyStream {
            stream.open(); defer { stream.close() }
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let count = stream.read(&buffer, maxLength: buffer.count)
                if count <= 0 { break }
                data.append(contentsOf: buffer.prefix(count))
            }
        }
        let body = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        let name = body["name"] as? String ?? "alex.rivera"
        let email = body["email"] as? String ?? "alex.rivera@example.com"
        let pending: [String: Any] = ["name": name, "email": email, "reservedAt": "2026-10-30T13:00:00Z",
                                      "status": "pending", "expiresAt": "2099-01-01T00:00:00Z"]
        let verified: [String: Any] = ["name": name, "email": email, "reservedAt": "2026-10-27T20:00:00Z",
                                       "status": "verified", "place": 214]
        var status = 200
        let answer: [String: Any]
        switch request.url!.lastPathComponent {
        case "check":
            status = 200
            answer = state == "taken" && name == "alex"
                ? ["name": name, "available": false, "reason": "taken", "suggestion": "alex.rivera"]
                : ["name": name, "available": true]
        case "reserve":
            if state == "failed" { status = 500; answer = ["error": "internal"] }
            else if state == "rate" { status = 429; answer = ["error": "rate"] }
            else if state == "sent" || state == "yours" { status = 200; answer = verified }
            else { status = 200; answer = pending }
        case "reservation":
            switch state {
            case "code", "wrongCode":
                answer = pending
            case "yours", "sent":
                answer = verified
            case "choice":
                answer = ["name": "alex.rivera", "email": email, "reservedAt": "2026-10-30T13:00:00Z",
                          "status": "choice", "expiresAt": "2099-01-01T00:00:00Z", "oldName": "arivera"]
            case "older":
                answer = ["name": "alex.rivera", "email": email, "reservedAt": "2026-10-20T20:00:00Z", "status": "unconfirmed"]
            default:
                status = 404; answer = ["error": "none"]
            }
        case "verify":
            if state == "wrongCode" { status = 400; answer = ["error": "wrong_code"] }
            else { status = 200; answer = verified }
        case "resend":
            status = 200; answer = pending
        case "choose":
            status = 200
            answer = ["name": (body["keep"] as? Bool ?? false) ? "arivera" : "alex.rivera", "email": email,
                      "reservedAt": "2026-10-30T13:00:00Z", "status": "verified", "place": 214]
        case "cancel":
            status = 200; answer = ["reservation": NSNull()]
        default:
            status = 404; answer = ["error": "none"]
        }
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: status,
            httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! JSONSerialization.data(withJSONObject: answer))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
#endif
