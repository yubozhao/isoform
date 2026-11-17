import SwiftUI

/// Closed Eyes on the phone (phone-settings rule 3): one switch that stops seeing on every device. Turning it on asks
/// for how long; on, it says until when. It is the computer's observing pause (PROTOCOL.md 24.10), which the watch
/// follows (24.8).
struct ClosedEyesRow: View {
    @Binding var status: HubStatus?
    let hub: HubChannel
    @State private var asking = false
    @State private var notice: String?
    @Environment(\.locale) private var locale

    /// How long, as the phone offers it: a number of minutes, or until the person ends it.
    enum Length: Equatable, Hashable {
        case minutes(Int)
        case untilEnded

        var key: String {
            switch self {
            case .minutes(let minutes): return "set.closed.\(minutes)"
            case .untilEnded: return "set.closed.untilEnded"
            }
        }

        var body: String {
            switch self {
            case .minutes(let minutes): return "{\"minutes\":\(minutes)}"
            case .untilEnded: return "{\"untilEnded\":true}"
            }
        }
    }

    /// 15 minutes, an hour, until I end it (phone-settings rule 3). A computer without `untilEnded` gets its longest
    /// timed pause instead, four hours.
    static func lengths(untilEnded: Bool) -> [Length] {
        [.minutes(15), .minutes(60), untilEnded ? .untilEnded : .minutes(240)]
    }

    private var pause: ObservingPause? { status?.observingPause }

    var body: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 8) {
                Toggle(isOn: Binding(get: { pause != nil }, set: { on in
                    if on { asking = true } else { Task { await resume() } }
                })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("set.closed").font(.body).foregroundStyle(YansuTheme.ink)
                        Text(subline).font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityIdentifier("settings.closedEyes.line")
                    }
                }
                .tint(YansuTheme.accent)
                .accessibilityIdentifier("settings.closedEyes")
                if let notice {
                    Text(verbatim: notice).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
            }
        }
        .confirmationDialog(Text("set.closed.howLong"), isPresented: $asking, titleVisibility: .visible) {
            ForEach(Self.lengths(untilEnded: status?.observingPauseUntilEnded == true), id: \.self) { length in
                Button(String(localized: String.LocalizationValue(length.key))) { Task { await start(length) } }
            }
            Button(String(localized: "Cancel"), role: .cancel) {}
        }
    }

    private var subline: String {
        guard let pause else { return String(localized: "set.closedSub") }
        guard let until = pause.untilDate else { return String(localized: "set.closed.on") }
        return String.localizedStringWithFormat(String(localized: "set.closedUntil"), BriefClock.text(until, locale: locale))
    }

    private struct PauseAnswer: Decodable { let observingPause: ObservingPause? }

    private func start(_ length: Length) async {
        do {
            let answer = try await hub.perform(.write(path: "/observing/pause", body: length.body))
                .payload(PauseAnswer.self)
            status?.observingPause = answer.observingPause
            notice = nil
        } catch {
            notice = String(localized: "set.closed.failed")
        }
    }

    private func resume() async {
        do {
            _ = try await hub.perform(.write(path: "/observing/resume", body: "{}")).payload(PauseAnswer.self)
            status?.observingPause = nil
            notice = nil
        } catch HubRequestError.conflict {
            // A pause made at the computer is ended at the computer (24.10).
            notice = String(localized: "set.closed.fromComputer")
        } catch {
            notice = String(localized: "set.closed.failed")
        }
    }
}

/// Settings › Your agents (phone-agent v1.2.0 rules 13 and 14; board artboard 11, frame 7): the two jobs on top, each
/// with its agent; then the other agents, each Connected or not, where it runs and when it last worked; then how to
/// connect one, and Sent to your agents.
struct YourAgentsView: View {
    let hub: AgentsHubAPI
    /// The hand-off log, when the computer serves it (PROTOCOL.md 27.2).
    var sentHub: AgentsHubAPI? = nil
    /// Connect an agent from the phone, when the computer takes joins (PROTOCOL.md 27.4).
    var joinAPI: AgentJoinAPI? = nil
    @State private var agents: PhoneAgents?
    @State private var joining: JoinTarget?

    struct JoinTarget: Identifiable { let agent: PhoneAgent?; var id: String { agent?.id ?? "new" } }
    @State private var failed = false
    @Environment(\.locale) private var locale

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if let agents {
                    VStack(spacing: 0) {
                        job(agents, kind: "code", title: String(localized: "agents.forCode"))
                        Divider().overlay(YansuTheme.borderSubtle)
                        job(agents, kind: "else", title: String(localized: "agents.forElse"))
                    }
                    .padding(.horizontal, 16)
                    .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius))
                    let chosen = Set([agents.choices.code, agents.choices.else])
                    let others = agents.agents.filter { !chosen.contains($0.id) }
                    if !others.isEmpty {
                        VStack(alignment: .leading, spacing: 8) {
                            SectionLabel(title: String(localized: "agents.other")).padding(.horizontal, 4)
                            VStack(spacing: 0) {
                                ForEach(Array(others.enumerated()), id: \.element.id) { index, agent in
                                    if index > 0 { Divider().overlay(YansuTheme.borderSubtle) }
                                    otherRow(agent)
                                }
                            }
                            .padding(.horizontal, 16)
                            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius))
                        }
                    }
                    VStack(alignment: .leading, spacing: 8) {
                        SectionLabel(title: String(localized: "agents.connectTitle")).padding(.horizontal, 4)
                        if joinAPI != nil {
                            Button(String(localized: "agents.connectTitle")) { joining = JoinTarget(agent: nil) }
                                .buttonStyle(YansuSecondaryButtonStyle())
                                .accessibilityIdentifier("agents.connect")
                        } else {
                            Text("agents.connectHint").font(.footnote).foregroundStyle(YansuTheme.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                                .padding(.horizontal, 4)
                        }
                    }
                    if let sentHub {
                        NavigationLink {
                            SentToAgentsView(hub: sentHub)
                        } label: {
                            HStack {
                                Text("data.sent").font(.body).foregroundStyle(YansuTheme.ink)
                                Spacer()
                                Image(systemName: "chevron.right").font(.footnote.weight(.semibold))
                                    .foregroundStyle(YansuTheme.secondary)
                            }
                            .padding(.vertical, 12)
                            .padding(.horizontal, 16)
                            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius))
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("agents.sent")
                    }
                    Text("settings.agentsFoot").font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.horizontal, 4)
                } else if failed {
                    Text("agents.unreachable").font(.subheadline).foregroundStyle(YansuTheme.secondary)
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            .padding(YansuTheme.gutter)
        }
        .yansuScreen()
        .navigationTitle(String(localized: "set.agent"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            do { agents = try await hub.phoneAgents() } catch { failed = true }
        }
        .sheet(item: $joining) { target in
            if let joinAPI {
                AgentJoinSheet(api: joinAPI, reconnect: target.agent) {
                    Task { if let read = try? await hub.phoneAgents() { agents = read } }
                }
            }
        }
        .accessibilityIdentifier("settings.yourAgents")
    }

    /// One job: its title, the agent doing it, and a menu to pick another of that kind.
    private func job(_ agents: PhoneAgents, kind: String, title: String) -> some View {
        Menu {
            ForEach(agents.agents(of: kind)) { agent in
                Button {
                    Task { await choose(agent) }
                } label: {
                    if agents.choice(for: kind)?.id == agent.id { Label(agent.name, systemImage: "checkmark") } else { Text(verbatim: agent.name) }
                }
            }
        } label: {
            if let agent = agents.choice(for: kind) {
                agentRow(agent, title: title, name: agent.name, chevron: true)
            } else {
                agentRow(nil, title: title, name: String(localized: "agents.none"), chevron: true)
            }
        }
        .accessibilityIdentifier("agents.job.\(kind)")
    }

    private func agentRow(_ agent: PhoneAgent?, title: String, name: String? = nil, chevron: Bool = false) -> some View {
        HStack(alignment: .center, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                if let name {
                    Text(verbatim: title).font(.footnote).foregroundStyle(YansuTheme.secondary)
                    Text(verbatim: name).font(.body).foregroundStyle(YansuTheme.ink)
                } else {
                    Text(verbatim: title).font(.body).foregroundStyle(YansuTheme.ink)
                }
                if let agent {
                    Text(verbatim: [AgentRowWords.state(agent), AgentRowWords.lastWorked(agent, locale: locale)]
                        .compactMap { $0 }.joined(separator: " · "))
                        .font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, alignment: .leading)
            if chevron {
                Image(systemName: "chevron.up.chevron.down").font(.footnote.weight(.semibold))
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
        .padding(.vertical, 12)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(agent.map { "agents.row.\($0.id)" } ?? "agents.row.none")
    }

    /// An agent that isn't one of the two jobs. Disconnected ones fade, with Connect again, for 30 days (rule 14).
    @ViewBuilder private func otherRow(_ agent: PhoneAgent) -> some View {
        if agent.isDisconnected, joinAPI != nil {
            HStack(spacing: 10) {
                agentRow(agent, title: agent.name).opacity(0.55)
                Button(String(localized: "agents.connectAgain")) { joining = JoinTarget(agent: agent) }
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("agents.connectAgain.\(agent.id)")
            }
        } else {
            agentRow(agent, title: agent.name).opacity(agent.isDisconnected ? 0.55 : 1)
        }
    }

    private func choose(_ chosen: PhoneAgent) async {
        if let updated = try? await hub.chooseAgent(PhoneAgentChoiceRequest(
            idempotencyKey: UUID().uuidString.lowercased(), kind: chosen.kind, agentId: chosen.id)) {
            agents = updated
        }
    }
}

/// Proposals from your topics (phone-settings rule 5, PROTOCOL.md 15.9.1): the computer's one switch, turned on or off
/// from either device. Off, topics stop offering their next piece of work in Your work.
struct TopicsSwitchRow: View {
    let hub: HubChannel
    @State private var enabled: Bool?
    @State private var saving = false
    @State private var failed = false

    private struct TopicsDailyState: Decodable { let enabled: Bool }

    var body: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 8) {
                Toggle(isOn: Binding(get: { enabled ?? false }, set: { on in Task { await set(on) } })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("set.topics").font(.body).foregroundStyle(YansuTheme.ink)
                        Text("set.topicsSub").font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .tint(YansuTheme.accent)
                .disabled(enabled == nil || saving)
                .accessibilityIdentifier("settings.topics")
                if failed {
                    Text("set.closed.failed").font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("settings.topics.failed")
                }
            }
        }
        .task { await load() }
    }

    private func load() async {
        if let state = try? await hub.perform(.topicsDaily).payload(TopicsDailyState.self) { enabled = state.enabled }
    }

    private func set(_ on: Bool) async {
        saving = true
        defer { saving = false }
        do {
            let state = try await hub.perform(.write(path: "/topics-daily/enabled", body: "{\"enabled\":\(on)}"))
                .payload(TopicsDailyState.self)
            enabled = state.enabled
            failed = false
        } catch {
            failed = true
        }
    }
}
