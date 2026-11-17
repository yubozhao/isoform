import SwiftUI
import UIKit

// An agent joining from the phone (phone-agent v1.2.0 §4 and rule 14; PROTOCOL.md 27.4; agents-join v1.0.0 §2).
// The phone makes the join link, the agent answers it, and the last yes is checked here: the agent shows a
// four-digit code and the person taps Yes only if this sheet shows the same one. Only Yes connects it.

struct AgentJoinStart: Codable, Equatable, Sendable {
    let joinId: String
    /// What to give the agent, in the computer's words (the address and how to use it).
    var message: String? = nil
    var link: String? = nil
    /// Unix milliseconds.
    var expiresAt: Double? = nil
    /// The link only works on the computer itself.
    var local: Bool? = nil
}

struct AgentJoinState: Codable, Equatable, Identifiable, Sendable {
    let joinId: String
    /// `waiting`, `requested`, `allowed`, `denied` or `expired`.
    let state: String
    var expiresAt: Double? = nil
    var agentName: String? = nil
    var from: String? = nil
    var toOpen: String? = nil
    var code: String? = nil
    var agentId: String? = nil

    var id: String { joinId }
    var isOver: Bool { state == "allowed" || state == "denied" || state == "expired" }
    var expiresDate: Date? { expiresAt.map { Date(timeIntervalSince1970: $0 / 1000) } }
}

struct AgentJoinRequest: Codable, Equatable, Sendable {
    let idempotencyKey: String
    var reconnect: String? = nil
}

struct AgentJoinAnswer: Codable, Equatable, Sendable {
    let idempotencyKey: String
    let answer: String
}

protocol AgentJoinAPI: Sendable {
    func startJoin(_ request: AgentJoinRequest) async throws -> AgentJoinStart
    func join(id: String) async throws -> AgentJoinState
    func answerJoin(id: String, _ answer: AgentJoinAnswer) async throws -> AgentJoinState
}

enum AgentJoinPath {
    static func join(_ id: String) -> String? {
        guard Identifiers.isSafePathID(id) else { return nil }
        return "/agents/join/" + id
    }
}

extension HubChannel: AgentJoinAPI {
    func startJoin(_ request: AgentJoinRequest) async throws -> AgentJoinStart {
        let body = try JSONEncoder().encode(request)
        let response = try await perform(.write(path: "/agents/join", body: String(decoding: body, as: UTF8.self)))
        // 409 names why in `data.code`: `not_taking_work` or `no_address`.
        if response.status == 409, let code = AgentJoinConflict.code(in: response.body) {
            throw HubRequestError.conflict(message: code)
        }
        return try response.payload(AgentJoinStart.self)
    }

    func join(id: String) async throws -> AgentJoinState {
        guard let path = AgentJoinPath.join(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.agentJoin(path: path)).payload(AgentJoinState.self)
    }

    func answerJoin(id: String, _ answer: AgentJoinAnswer) async throws -> AgentJoinState {
        guard let path = AgentJoinPath.join(id) else { throw HubRequestError.invalidIdentifier }
        let body = try JSONEncoder().encode(answer)
        return try await perform(.write(path: path + "/answer", body: String(decoding: body, as: UTF8.self)))
            .payload(AgentJoinState.self)
    }
}

enum AgentJoinConflict {
    private struct Body: Decodable { struct Data: Decodable { let code: String? }; let data: Data? }

    static func code(in body: String) -> String? {
        (try? JSONDecoder().decode(Body.self, from: Foundation.Data(body.utf8)))?.data?.code
    }
}

/// A join the phone opens from a notification ("{agent} is asking to join"), by its id.
struct AgentJoinRoute: Identifiable, Equatable {
    let joinId: String
    var id: String { joinId }
}

/// Connect an agent, or Connect again: the phone makes the link and shows what to give the agent, then checks the
/// code when the agent answers. `reconnect` names a disconnected agent.
struct AgentJoinSheet: View {
    let api: AgentJoinAPI
    var reconnect: PhoneAgent? = nil
    var joinID: String? = nil
    var onConnected: () -> Void = {}
    @Environment(\.dismiss) private var dismiss
    @State private var start: AgentJoinStart?
    @State private var join: AgentJoinState?
    @State private var failure: String?
    @State private var answering = false
    @State private var copied = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    content
                    if let failure {
                        Text(verbatim: failure).font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityIdentifier("join.failure")
                    }
                }
                .padding(YansuTheme.gutter)
            }
            .yansuScreen()
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(String(localized: join?.state == "allowed" ? "Done" : "Cancel")) { dismiss() }
                        .accessibilityIdentifier("join.close")
                }
            }
        }
        .task { await run() }
        .accessibilityIdentifier("join.sheet")
    }

    @ViewBuilder private var content: some View {
        if let join, join.state == "requested", let code = join.code {
            codeCheck(join, code: code)
        } else if let join, join.state == "allowed" {
            Text(String.localizedStringWithFormat(String(localized: "join.allowed"), join.agentName ?? ""))
                .font(.title2.weight(.bold)).foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("join.allowed")
            Text(String.localizedStringWithFormat(String(localized: "join.whatShould"), join.agentName ?? ""))
                .font(.subheadline).foregroundStyle(YansuTheme.secondary)
        } else if let join, join.state == "denied" {
            Text("join.denied").font(.body).foregroundStyle(YansuTheme.ink).accessibilityIdentifier("join.denied")
        } else if let join, join.state == "expired" {
            Text("join.expired").font(.body).foregroundStyle(YansuTheme.ink).accessibilityIdentifier("join.expired")
        } else if let start {
            waiting(start)
        } else if failure == nil {
            ProgressView().frame(maxWidth: .infinity)
        }
    }

    /// The link, what to give the agent, and that it works once, for ten minutes (v1.2.1 rule 6).
    private func waiting(_ start: AgentJoinStart) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(reconnect.map { String.localizedStringWithFormat(String(localized: "join.again.title"), $0.name) }
                 ?? String(localized: "join.title"))
                .font(.title2.weight(.bold)).foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
            Text("join.give").font(.subheadline).foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let text = start.message ?? start.link {
                Text(verbatim: text)
                    .font(.body).foregroundStyle(YansuTheme.ink).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(14)
                    .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 14))
                    .accessibilityIdentifier("join.message")
                Button(String(localized: copied ? "join.copied" : "join.copy")) {
                    UIPasteboard.general.string = text
                    copied = true
                }
                .buttonStyle(YansuPrimaryButtonStyle())
                .accessibilityIdentifier("join.copy")
            }
            if start.local == true {
                Text("join.localOnly").font(.footnote).foregroundStyle(YansuTheme.warning)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack(spacing: 8) {
                ProgressView()
                Text("join.waiting").font(.footnote).foregroundStyle(YansuTheme.secondary)
            }
            .accessibilityIdentifier("join.waiting")
        }
    }

    /// "{agent} is asking to join" / "From {where} · to open {what}" / "Does your agent show this code?" / the code
    /// (agents-join §2). Only Yes connects it.
    private func codeCheck(_ join: AgentJoinState, code: String) -> some View {
        let name = join.agentName ?? ""
        return VStack(alignment: .leading, spacing: 14) {
            Text(String.localizedStringWithFormat(String(localized: "join.asking"), name))
                .font(.title2.weight(.bold)).foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("join.asking")
            if let line = AgentJoinWords.fromLine(join) {
                Text(verbatim: line).font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Text("join.showsCode").font(.body).foregroundStyle(YansuTheme.ink)
            Text(verbatim: AgentJoinWords.spaced(code))
                .font(.system(size: 44, weight: .bold, design: .rounded))
                .foregroundStyle(YansuTheme.ink)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 12)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
                .accessibilityLabel(Text(verbatim: code.map(String.init).joined(separator: " ")))
                .accessibilityIdentifier("join.code")
            Button(String.localizedStringWithFormat(String(localized: "join.yes"), code)) {
                Task { await answer("yes") }
            }
            .buttonStyle(YansuPrimaryButtonStyle())
            .disabled(answering)
            .accessibilityIdentifier("join.answer.yes")
            Button(String(localized: "work.no")) { Task { await answer("no") } }
                .buttonStyle(YansuSecondaryButtonStyle())
                .disabled(answering)
                .accessibilityIdentifier("join.answer.no")
            Text("join.onlyYes").font(.footnote).foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// Makes the link (or opens the one named), then reads the join every two seconds until it's answered or over.
    private func run() async {
        var id = joinID
        if id == nil {
            do {
                let made = try await api.startJoin(AgentJoinRequest(idempotencyKey: UUID().uuidString.lowercased(),
                                                                    reconnect: reconnect?.id))
                start = made
                id = made.joinId
            } catch HubRequestError.conflict(let message) {
                // 409: not taking work (Closed Eyes, task links off), or agents can't reach this computer yet.
                failure = String(localized: message.contains("no_address") ? "join.noAddress" : "join.notTaking")
                return
            } catch {
                failure = String(localized: "agents.unreachable")
                return
            }
        }
        guard let id else { return }
        while !Task.isCancelled {
            do {
                let read = try await api.join(id: id)
                if read != join { join = read }
                if read.isOver {
                    if read.state == "allowed" { onConnected() }
                    return
                }
            } catch HubRequestError.rejected(let status, _) where status == 404 {
                join = AgentJoinState(joinId: id, state: "expired")
                return
            } catch {
                // The computer may be out of reach for a moment; keep asking while the sheet is open.
            }
            try? await Task.sleep(for: .seconds(2))
        }
    }

    private func answer(_ value: String) async {
        guard let id = join?.joinId else { return }
        answering = true
        defer { answering = false }
        do {
            join = try await api.answerJoin(id: id, AgentJoinAnswer(idempotencyKey: UUID().uuidString.lowercased(), answer: value))
            if join?.state == "allowed" { onConnected() }
        } catch HubRequestError.rejected(let status, _) where status == 410 {
            join = AgentJoinState(joinId: id, state: "expired")
        } catch HubRequestError.conflict {
            // Answered on the computer first: the first answer wins; the next read shows which.
            if let read = try? await api.join(id: id) { join = read }
        } catch {
            failure = String(localized: "agents.unreachable")
        }
    }
}

enum AgentJoinWords {
    /// "From Claude Code on Bo's MacBook · to open only what each hand-off sends".
    static func fromLine(_ join: AgentJoinState) -> String? {
        guard let from = join.from, !from.isEmpty else { return nil }
        guard let toOpen = join.toOpen, !toOpen.isEmpty else {
            return String.localizedStringWithFormat(String(localized: "join.from"), from)
        }
        return String.localizedStringWithFormat(String(localized: "join.fromToOpen"), from, toOpen)
    }

    /// "4 8 1 7", wide enough to compare at a glance.
    static func spaced(_ code: String) -> String { code.map(String.init).joined(separator: " ") }
}

#if DEBUG && targetEnvironment(simulator)
/// The stub of the join routes: the link, then the agent answering with its code.
actor AgentJoinUITestHub: AgentJoinAPI {
    static let shared = AgentJoinUITestHub()
    private var reads = 0
    private var answered: String?

    func startJoin(_ request: AgentJoinRequest) async throws -> AgentJoinStart {
        reads = 0
        answered = nil
        return AgentJoinStart(joinId: "join-ui", message: "Connect to Isoform: https://example.invalid/join/ui",
                              link: "https://example.invalid/join/ui",
                              expiresAt: (Date().timeIntervalSince1970 + 600) * 1000, local: false)
    }

    func join(id: String) async throws -> AgentJoinState {
        reads += 1
        if let answered { return AgentJoinState(joinId: id, state: answered == "yes" ? "allowed" : "denied", agentName: "Claude Code", agentId: "claude-code-2") }
        guard reads > 1 else { return AgentJoinState(joinId: id, state: "waiting") }
        return AgentJoinState(joinId: id, state: "requested", agentName: "Claude Code", from: "Claude Code on this Mac",
                              toOpen: "Only what each hand-off sends", code: "4817")
    }

    func answerJoin(id: String, _ answer: AgentJoinAnswer) async throws -> AgentJoinState {
        answered = answer.answer
        return try await join(id: id)
    }
}
#endif
