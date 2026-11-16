import Foundation
import UIKit

// Your agents and the hand-off (phone-agent v1.0.0 and v1.1.0 §1 to §3; PROTOCOL.md 27 and 27.1). The person picks
// two agents, one for code and one for everything else; the work's kind decides which gets a Yes. The sheet shows
// exactly what goes, then Send: straight to a connected agent, otherwise copied into its app or written in Messages.

struct PhoneAgent: Codable, Equatable, Hashable, Identifiable, Sendable {
    let id: String
    let name: String
    /// `code` or `else`.
    let kind: String
    /// `connected`, `connected_computer`, `on_computer`, `its_app` or `by_text`.
    let reach: String
    var found: Bool? = nil
    var connected: Bool? = nil
    var lastWorkedAt: Double? = nil

    var isConnected: Bool { connected == true || reach == "connected" || reach == "connected_computer" }
}

struct PhoneAgents: Codable, Equatable, Sendable {
    struct Choices: Codable, Equatable, Sendable {
        var code: String
        var `else`: String
    }
    var choices: Choices
    var agents: [PhoneAgent]

    func choice(for kind: String) -> PhoneAgent? {
        let id = kind == "code" ? choices.code : choices.else
        guard !id.isEmpty else { return nil }
        return agents.first { $0.id == id }
    }

    func agents(of kind: String) -> [PhoneAgent] { agents.filter { $0.kind == kind } }
}

struct PhoneAgentChoiceRequest: Codable, Equatable, Sendable {
    let idempotencyKey: String
    let kind: String
    let agentId: String
}

struct PhoneHandOffRequest: Codable, Equatable, Sendable {
    let idempotencyKey: String
    var agentId: String? = nil
    var kind: String? = nil
    let sourceKind: String
    let sourceId: String
    let title: String
    var detail: String? = nil
    var done: String? = nil
}

struct PhoneHandOffResult: Decodable, Equatable, Sendable {
    let recordId: String
    let agentId: String
}

/// What is handed over: an item's own words, and where it came from. Chat opens the same sheet with `chat_card`.
struct HandOffSource: Identifiable, Equatable, Sendable {
    /// `work` (a Your work item) or `chat_card`.
    let kind: String
    let id: String
    let title: String
    var detail: String? = nil
    var done: String? = nil
    /// `code` or `else`: which of the person's two agents gets it.
    var workKind = "else"
    /// The agent the computer named for this item (B2 `yes.agent`), when it did.
    var agentID: String? = nil
    var gets: [String] = []

    var text: String { WorkOnIt.text(title: title, detail: detail, done: done) }

    /// A Your work item: `work_item` as /agents/handoffs takes it, and the kind the computer names for its Yes, so code
    /// work goes to the code agent (review 2026-11-15).
    static func work(_ item: WorkItem) -> HandOffSource {
        HandOffSource(kind: "work_item", id: item.id, title: item.work.title, detail: item.work.text,
                      workKind: item.yes?.kind == "code" ? "code" : "else", agentID: item.yes?.agent,
                      gets: (item.gets ?? []).map(\.label))
    }
}

protocol AgentsHubAPI: Sendable {
    func phoneAgents() async throws -> PhoneAgents
    func chooseAgent(_ request: PhoneAgentChoiceRequest) async throws -> PhoneAgents
    func handOff(_ request: PhoneHandOffRequest) async throws -> PhoneHandOffResult
}

extension HubChannel: AgentsHubAPI {
    func phoneAgents() async throws -> PhoneAgents {
        try await perform(.agents).payload(PhoneAgents.self)
    }

    func chooseAgent(_ request: PhoneAgentChoiceRequest) async throws -> PhoneAgents {
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/agents/choice", body: String(decoding: body, as: UTF8.self)))
            .payload(PhoneAgents.self)
    }

    func handOff(_ request: PhoneHandOffRequest) async throws -> PhoneHandOffResult {
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/agents/handoffs", body: String(decoding: body, as: UTF8.self)))
            .payload(PhoneHandOffResult.self)
    }
}

/// How Send reaches an agent, decided from what the computer says about it (v1.1.0 §2 and §3).
enum HandOffRoute: Equatable {
    /// Straight to its inbox; the result comes back to Your work.
    case direct
    /// Copied, then its app opens: "Copied. Paste it in {agent} and send."
    case app(scheme: String?)
    /// Messages opens with the task written; the person presses send.
    case messages
    /// A coding agent that isn't connected: connect it on the computer first; meanwhile copy.
    case computer

    static func route(for agent: PhoneAgent) -> HandOffRoute {
        if agent.isConnected { return .direct }
        switch agent.reach {
        case "by_text": return .messages
        case "on_computer": return .computer
        default: return .app(scheme: WorkOnIt.catalog.first { $0.id == agent.id }?.scheme ?? knownSchemes[agent.id])
        }
    }

    /// App schemes beyond Work on it's declared list, for agents the catalog names.
    static let knownSchemes: [String: String] = [:]

    /// Messages with the task as its body; the person picks the recipient and sends.
    static func messagesURL(text: String) -> URL? {
        var components = URLComponents()
        components.scheme = "sms"
        components.path = ""
        components.queryItems = [URLQueryItem(name: "body", value: text)]
        guard let query = components.percentEncodedQuery else { return nil }
        return URL(string: "sms:&" + query)
    }
}
