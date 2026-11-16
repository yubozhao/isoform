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

    /// The lengths offered: 15 minutes, an hour, and the longest pause the computer takes from a phone.
    static let lengths: [(minutes: Int, key: String)] = [(15, "set.closed.15"), (60, "set.closed.60"), (240, "set.closed.240")]

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
            ForEach(Self.lengths, id: \.minutes) { length in
                Button(String(localized: String.LocalizationValue(length.key))) { Task { await start(length.minutes) } }
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

    private func start(_ minutes: Int) async {
        do {
            let answer = try await hub.perform(.write(path: "/observing/pause", body: "{\"minutes\":\(minutes)}"))
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

/// Settings › Your agents (phone-agent v1.1.0 rule 4): the two choices, and every agent with how it's reached.
struct YourAgentsView: View {
    let hub: AgentsHubAPI
    @State private var agents: PhoneAgents?
    @State private var failed = false

    var body: some View {
        Group {
            if let agents {
                WhoDoesYourWork(agents: agents, kind: "else") { chosen in
                    Task {
                        if let updated = try? await hub.chooseAgent(PhoneAgentChoiceRequest(
                            idempotencyKey: UUID().uuidString.lowercased(), kind: chosen.kind, agentId: chosen.id)) {
                            self.agents = updated
                        }
                    }
                }
            } else if failed {
                Text("agents.unreachable").font(.subheadline).foregroundStyle(YansuTheme.secondary).padding()
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .yansuScreen()
        .navigationTitle(String(localized: "set.agent"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            do { agents = try await hub.phoneAgents() } catch { failed = true }
        }
        .accessibilityIdentifier("settings.yourAgents")
    }
}
