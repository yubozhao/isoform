import SwiftUI
import UIKit

/// The hand-off (phone-agent v1.0.0 rules 4 to 10, v1.1.0 §1 to §3; board artboard 4). Reusable: Today's Yes on
/// offered work and Chat's hand-off card open it with their source. The first time, it asks "Who does your work?".
struct HandOffSheet: View {
    let source: HandOffSource
    let hub: AgentsHubAPI
    /// Today's Yes already went through B3, which hands the work to a connected agent on the computer: Send then
    /// doesn't send it again, and only an agent reached another way needs the person's step.
    var alreadyHandedOver = false
    /// Send through B3 (`action: send`, PROTOCOL.md 26.1): the computer hands it to the agent and keeps one record.
    /// A throw (`agent_not_connected`) falls back to the agent's own way in.
    var sendThroughWork: ((PhoneAgent) async throws -> Void)? = nil
    /// After Send: the agent's name, for "Sent to {agent}".
    var onSent: (String) -> Void = { _ in }
    /// The computer said goodbye for sleep. A Yes already recorded (B3) waits for it, so the sheet says so.
    var computerAsleep = false

    @Environment(\.dismiss) private var dismiss
    @State private var agents: PhoneAgents?
    @State private var failed = false
    /// The agent for this one hand-off, when Change agent picked another.
    @State private var override: PhoneAgent?
    @State private var picking = false
    @State private var sending = false
    @State private var copiedFor: PhoneAgent?
    @State private var notice: String?

    private var agent: PhoneAgent? {
        override ?? source.agentID.flatMap { id in agents?.agents.first { $0.id == id } } ?? agents?.choice(for: source.workKind)
    }

    var body: some View {
        NavigationStack {
            Group {
                if let agents, picking || (agents.choice(for: source.workKind) == nil && source.agentID == nil) {
                    WhoDoesYourWork(agents: agents, kind: source.workKind) { chosen in
                        Task { await choose(chosen) }
                    }
                } else if let agent {
                    sheet(agent)
                } else if failed {
                    Text("agents.unreachable").font(.subheadline).foregroundStyle(YansuTheme.secondary).padding()
                } else {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            .yansuScreen()
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(String(localized: "Cancel")) { dismiss() }.accessibilityIdentifier("handoff.cancel")
                }
            }
        }
        .task { await load() }
        .accessibilityIdentifier("handoff.sheet")
    }

    private func sheet(_ agent: PhoneAgent) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(String.localizedStringWithFormat(String(localized: "agent.sheet.title"), agent.name))
                    .font(.title2.weight(.bold))
                    .foregroundStyle(YansuTheme.ink)
                    .accessibilityIdentifier("handoff.title")
                Text("agent.exact").font(.subheadline).foregroundStyle(YansuTheme.secondary)
                Text(verbatim: source.text)
                    .font(.body)
                    .foregroundStyle(YansuTheme.ink)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(14)
                    .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 14))
                    .accessibilityIdentifier("handoff.text")
                if !source.gets.isEmpty {
                    SectionLabel(title: String(localized: "agent.gets"))
                    VStack(spacing: 0) {
                        ForEach(Array(source.gets.enumerated()), id: \.offset) { index, piece in
                            if index > 0 { Divider().overlay(YansuTheme.borderSubtle) }
                            Text(verbatim: piece).font(.subheadline).foregroundStyle(YansuTheme.ink)
                                .frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 11)
                        }
                    }
                    .padding(.horizontal, 14)
                    .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 14))
                }
                if let copied = copiedFor {
                    copiedPanel(copied)
                }
                if let notice {
                    Text(verbatim: notice).font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("handoff.notice")
                }
                let button = HandOffButton.button(for: agent)
                if button == .onComputer, computerAsleep, alreadyHandedOver {
                    Text("agent.asleepLine")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.warning)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("handoff.asleep")
                }
                Button {
                    Task { await send(agent) }
                } label: {
                    Text(verbatim: button.title(agent.name))
                }
                .buttonStyle(YansuPrimaryButtonStyle())
                .disabled(sending)
                .accessibilityIdentifier("handoff.send")
                if let foot = button.foot(agent) {
                    Text(verbatim: foot)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("handoff.foot")
                }
                VStack(spacing: 6) {
                    Text("agent.ownAccount").font(.footnote).foregroundStyle(YansuTheme.secondary)
                    Menu {
                        ForEach(agents?.agents(of: source.workKind) ?? []) { other in
                            Button(other.name) { override = other; copiedFor = nil; notice = nil }
                        }
                    } label: {
                        Text("agent.change").font(.subheadline).foregroundStyle(YansuTheme.accent)
                    }
                    .accessibilityIdentifier("handoff.change")
                }
                .frame(maxWidth: .infinity)
            }
            .padding(YansuTheme.gutter)
        }
    }

    private func copiedPanel(_ agent: PhoneAgent) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(String.localizedStringWithFormat(String(localized: "agent.copied"), agent.name))
                .font(.subheadline).foregroundStyle(YansuTheme.ink)
            Text("agent.askTomorrow").font(.footnote).foregroundStyle(YansuTheme.secondary)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(YansuTheme.accentWash, in: RoundedRectangle(cornerRadius: 12))
        .accessibilityIdentifier("handoff.copied")
    }

    private func load() async {
        do { agents = try await hub.phoneAgents() } catch { failed = true }
    }

    private func choose(_ chosen: PhoneAgent) async {
        do {
            agents = try await hub.chooseAgent(PhoneAgentChoiceRequest(idempotencyKey: UUID().uuidString.lowercased(),
                                                                       kind: chosen.kind, agentId: chosen.id))
            picking = false
        } catch {
            notice = String(localized: "agents.unreachable")
        }
    }

    /// "Open in {agent}" for a connected app agent: the computer has the hand-off, and the app opens with the task
    /// written in (on the clipboard) for the person to press send there (v1.2.0 rule 6).
    private func openAppIfAsked(_ agent: PhoneAgent) async {
        guard case .openIn(let scheme) = HandOffButton.button(for: agent), let url = URL(string: "\(scheme)://") else { return }
        UIPasteboard.general.string = source.text
        await UIApplication.shared.open(url)
    }

    /// Send: straight to a connected agent; otherwise the agent's own way in, with the person sending it there.
    private func send(_ agent: PhoneAgent) async {
        sending = true
        defer { sending = false }
        var route = HandOffRoute.route(for: agent)
        if let sendThroughWork {
            do {
                try await sendThroughWork(agent)
                await openAppIfAsked(agent)
                onSent(agent.name)
                dismiss()
                return
            } catch {
                if route == .direct { route = .app(scheme: WorkOnIt.catalog.first { $0.id == agent.id }?.scheme) }
            }
        } else if route == .direct, alreadyHandedOver, override == nil {
            await openAppIfAsked(agent)
            onSent(agent.name)
            dismiss()
            return
        }
        if route == .direct {
            do {
                _ = try await hub.handOff(PhoneHandOffRequest(idempotencyKey: UUID().uuidString.lowercased(), agentId: agent.id,
                                                              kind: source.workKind, sourceKind: source.kind, sourceId: source.id,
                                                              title: source.title, detail: source.detail, done: source.done))
                await openAppIfAsked(agent)
                onSent(agent.name)
                dismiss()
                return
            } catch {
                // 409: not connected after all, or the computer isn't taking agent work. The agent's own way in stands.
                route = .app(scheme: WorkOnIt.catalog.first { $0.id == agent.id }?.scheme)
            }
        }
        switch route {
        case .messages:
            if let url = HandOffRoute.messagesURL(text: source.text) { await UIApplication.shared.open(url) }
            onSent(agent.name)
        case .computer:
            UIPasteboard.general.string = source.text
            copiedFor = agent
            notice = String(localized: "agents.connectOnComputer")
        case .app(let scheme):
            UIPasteboard.general.string = source.text
            copiedFor = agent
            if let scheme, let url = URL(string: "\(scheme)://") { await UIApplication.shared.open(url) }
        case .direct:
            break
        }
    }
}

/// "Who does your work?": two groups, for code and for everything else, each row saying how it's reached
/// (v1.1.0 rule 2). Tapping one makes it the person's agent for that kind.
struct WhoDoesYourWork: View {
    let agents: PhoneAgents
    let kind: String
    var choose: (PhoneAgent) -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text("agents.pick.title").font(.title2.weight(.bold)).foregroundStyle(YansuTheme.ink)
                    .accessibilityIdentifier("handoff.pick.title")
                Text("agents.pick.body").font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                group("code", title: String(localized: "agents.forCode"))
                group("else", title: String(localized: "agents.forElse"))
                Text("agents.connectHint").font(.footnote).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(YansuTheme.gutter)
        }
    }

    private func group(_ groupKind: String, title: String) -> some View {
        let rows = agents.agents(of: groupKind)
        let chosen = agents.choice(for: groupKind)?.id
        return VStack(alignment: .leading, spacing: 8) {
            SectionLabel(title: title).padding(.horizontal, 4)
            VStack(spacing: 0) {
                ForEach(Array(rows.enumerated()), id: \.element.id) { index, agent in
                    if index > 0 { Divider().overlay(YansuTheme.borderSubtle) }
                    Button { choose(agent) } label: {
                        HStack(spacing: 10) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(verbatim: agent.name).font(.body).foregroundStyle(YansuTheme.ink)
                                Text(Self.reachLine(agent)).font(.footnote).foregroundStyle(YansuTheme.secondary)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            if chosen == agent.id {
                                Image(systemName: "checkmark").font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.accent)
                            }
                        }
                        .padding(.vertical, 11)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("handoff.agent.\(agent.id)")
                }
            }
            .padding(.horizontal, 14)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
        }
    }

    static func reachLine(_ agent: PhoneAgent) -> String {
        switch agent.reach {
        case "connected": return String(localized: "agents.connected")
        case "connected_computer": return String(localized: "agents.connectedComputer")
        case "on_computer": return String(localized: "agents.onComputer")
        case "by_text": return String(localized: "agents.byText")
        default: return String(localized: "agents.itsApp")
        }
    }
}
