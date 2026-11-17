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
    /// Unix milliseconds: disconnected within 30 days and not connected again (PROTOCOL.md 27.4).
    var disconnectedAt: Double? = nil

    var isDisconnected: Bool { disconnectedAt != nil && !isConnected }

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

/// One hand-off as the computer keeps it (PROTOCOL.md 27.2): exactly what went, when, to whom, and from where.
struct AgentHandoffEntry: Codable, Equatable, Identifiable, Sendable {
    struct Source: Codable, Equatable, Sendable {
        var kind: String? = nil
        var id: String? = nil
    }
    let id: String
    var agentId: String? = nil
    var agentName: String? = nil
    var kind: String? = nil
    var method: String? = nil
    var via: String? = nil
    let text: String
    /// What left, when it differs from `text` (the one line of a task link).
    var sent: String? = nil
    let sentAt: String
    var surface: String? = nil
    var source: Source? = nil

    var date: Date? { RelayTime.parse(sentAt) }
}

struct AgentHandoffsPage: Codable, Equatable, Sendable {
    var handoffs: [AgentHandoffEntry]
    var next: String? = nil
}

protocol AgentsHubAPI: Sendable {
    func phoneAgents() async throws -> PhoneAgents
    func chooseAgent(_ request: PhoneAgentChoiceRequest) async throws -> PhoneAgents
    func handOff(_ request: PhoneHandOffRequest) async throws -> PhoneHandOffResult
    func handoffs(limit: Int, before: String?) async throws -> AgentHandoffsPage
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

    func handoffs(limit: Int, before: String?) async throws -> AgentHandoffsPage {
        try await perform(.agentHandoffs(limit: limit, before: before)).payload(AgentHandoffsPage.self)
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

/// The hand-off's one button, by what the agent is (phone-agent v1.2.0 §3): it says what will really happen.
enum HandOffButton: Equatable {
    /// An agent Isoform runs on the computer: "Send to {agent} on your computer".
    case onComputer
    /// An agent in its own app: "Open in {agent}", the app opening with the task written in.
    case openIn(scheme: String)
    /// "Write it in Messages".
    case messages
    /// A cloud agent, or one with no app on this phone: "Send to {agent}".
    case send

    static func button(for agent: PhoneAgent) -> HandOffButton {
        let scheme = WorkOnIt.catalog.first { $0.id == agent.id }?.scheme ?? HandOffRoute.knownSchemes[agent.id]
        switch agent.reach {
        case "connected_computer": return .onComputer
        case "on_computer": return agent.isConnected ? .onComputer : .send
        case "by_text": return .messages
        default:
            if let scheme { return .openIn(scheme: scheme) }
            return .send
        }
    }

    func title(_ name: String) -> String {
        switch self {
        case .onComputer: return String.localizedStringWithFormat(String(localized: "agent.sendOnComputer"), name)
        case .openIn: return String.localizedStringWithFormat(String(localized: "agent.openIn"), name)
        case .messages: return String(localized: "agent.writeMessages")
        case .send: return String.localizedStringWithFormat(String(localized: "agent.send"), name)
        }
    }

    /// The line under the button, when there is one.
    func foot(_ agent: PhoneAgent) -> String? {
        switch self {
        case .onComputer:
            return agent.kind == "code" ? String.localizedStringWithFormat(String(localized: "agent.newBranch"), agent.name) : nil
        case .openIn: return String.localizedStringWithFormat(String(localized: "agent.openInFoot"), agent.name)
        case .messages, .send: return nil
        }
    }
}

/// Settings › Your agents words for one agent (phone-agent v1.2.0 rule 14): Connected or not, and where it runs.
enum AgentRowWords {
    static func state(_ agent: PhoneAgent) -> String {
        if agent.isDisconnected { return String(localized: "agents.state.disconnected") }
        let connected = String(localized: agent.isConnected ? "agents.state.connected" : "agents.state.notConnected")
        let place: String
        switch agent.reach {
        case "on_computer", "connected_computer": place = String(localized: "agents.where.computer")
        case "by_text": place = String(localized: "agents.where.text")
        default: place = String(localized: "agents.where.app")
        }
        return connected + " · " + place
    }

    static func lastWorked(_ agent: PhoneAgent, now: Date = Date(), locale: Locale) -> String? {
        guard let at = agent.lastWorkedAt else { return nil }
        // The computer sends milliseconds; tolerate seconds too.
        let date = Date(timeIntervalSince1970: at > 10_000_000_000 ? at / 1000 : at)
        let formatter = RelativeDateTimeFormatter()
        formatter.locale = locale
        formatter.unitsStyle = .full
        return String.localizedStringWithFormat(String(localized: "agents.lastWorked"),
                                                formatter.localizedString(for: date, relativeTo: now))
    }
}
