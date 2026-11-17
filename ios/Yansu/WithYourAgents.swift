import SwiftUI

// With your agents (phone-agent v1.2.1, board artboard 16; PROTOCOL.md 27.2): one row per hand-off with its state in
// the computer's own words and the time, ordered by what needs you; a row opens with Ask again and Take it back.
// The phone says only what the computer knows; it never estimates.

struct HandoffStatus: Codable, Equatable, Identifiable, Sendable {
    let id: String
    var agentId: String? = nil
    var agentName: String? = nil
    var title: String? = nil
    /// Unix milliseconds.
    var sentAt: Double? = nil
    /// `sent`, `opened`, `has_it`, `working`, `back`, `no_word`, `couldnt_start`, `taken_back`; an unknown one reads as
    /// `sent`.
    var state: String
    var line: String? = nil
    /// Unix milliseconds; 0 or absent when the computer doesn't know.
    var since: Double? = nil

    var name: String { agentName ?? agentId ?? "" }

    var known: String {
        switch state {
        case "sent", "opened", "has_it", "working", "back", "no_word", "couldnt_start", "taken_back": return state
        default: return "sent"
        }
    }

    /// Still with the agent: back and taken back leave the group (the result, or the work, is in Your work).
    var isWithAgent: Bool { known != "back" && known != "taken_back" }

    static func date(_ ms: Double?) -> Date? {
        guard let ms, ms > 0 else { return nil }
        return Date(timeIntervalSince1970: ms / 1000)
    }
}

struct HandoffStatusList: Codable, Equatable, Sendable {
    var handoffs: [HandoffStatus]
}

protocol HandoffStatusAPI: Sendable {
    func handoffStatuses() async throws -> [HandoffStatus]
    func askAgain(id: String, idempotencyKey: String) async throws -> HandoffStatus
    func takeBack(id: String, idempotencyKey: String) async throws -> HandoffStatus
}

extension HubChannel: HandoffStatusAPI {
    func handoffStatuses() async throws -> [HandoffStatus] {
        try await perform(.handoffStatuses).payload(HandoffStatusList.self).handoffs
    }

    func askAgain(id: String, idempotencyKey: String) async throws -> HandoffStatus {
        try await handoffWrite(id: id, action: "ask-again", key: idempotencyKey)
    }

    func takeBack(id: String, idempotencyKey: String) async throws -> HandoffStatus {
        try await handoffWrite(id: id, action: "take-back", key: idempotencyKey)
    }

    private func handoffWrite(id: String, action: String, key: String) async throws -> HandoffStatus {
        let escaped = id.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-._~"))) ?? id
        let body = try JSONEncoder().encode(["idempotencyKey": key])
        return try await perform(.write(path: "/agents/handoffs/\(escaped)/\(action)", body: String(decoding: body, as: UTF8.self)))
            .payload(HandoffStatus.self)
    }
}

/// The words and tone of one row (v1.2.1 rule 1).
enum WithAgentWords {
    enum Tone: Equatable { case plain, working, amber }

    static func line(_ status: HandoffStatus, computerAsleep: Bool, locale: Locale) -> (text: String, tone: Tone) {
        let name = status.name
        let at = { (ms: Double?) -> String in HandoffStatus.date(ms).map { BriefClock.text($0, locale: locale) } ?? "" }
        switch status.known {
        case "working":
            return (String.localizedStringWithFormat(String(localized: "with.working"), name), .working)
        case "has_it":
            return (String.localizedStringWithFormat(String(localized: "with.hasIt"), name, at(status.since ?? status.sentAt)), .plain)
        case "opened":
            return (String.localizedStringWithFormat(String(localized: "with.openedIn"), name, at(status.since ?? status.sentAt)), .plain)
        case "no_word":
            return (String.localizedStringWithFormat(String(localized: "with.noWord"), name, at(status.since ?? status.sentAt)), .amber)
        case "couldnt_start":
            return (String.localizedStringWithFormat(String(localized: "with.couldntStart"), name), .amber)
        default:
            if computerAsleep { return (String(localized: "with.waitingComputer"), .amber) }
            return (String.localizedStringWithFormat(String(localized: "with.sent"), name, at(status.sentAt)), .plain)
        }
    }

    /// No word first, then waiting for the computer (sent while it sleeps), then the rest, newest first (rule 2).
    static func ordered(_ statuses: [HandoffStatus], computerAsleep: Bool) -> [HandoffStatus] {
        func rank(_ status: HandoffStatus) -> Int {
            switch status.known {
            case "no_word": return 0
            case "sent" where computerAsleep: return 1
            default: return 2
            }
        }
        return statuses.filter(\.isWithAgent).enumerated().sorted { lhs, rhs in
            let (l, r) = (rank(lhs.element), rank(rhs.element))
            if l != r { return l < r }
            let (ls, rs) = (lhs.element.sentAt ?? 0, rhs.element.sentAt ?? 0)
            if ls != rs { return ls > rs }
            return lhs.offset < rhs.offset
        }.map(\.element)
    }
}

/// The With your agents group, read from the computer's statuses. Nil statuses: the computer doesn't serve them, and
/// the caller shows its own rows.
struct WithYourAgentsGroup: View {
    let statuses: [HandoffStatus]
    let computerAsleep: Bool
    var open: (HandoffStatus) -> Void
    @Environment(\.locale) private var locale

    var body: some View {
        let rows = WithAgentWords.ordered(statuses, computerAsleep: computerAsleep)
        if !rows.isEmpty {
            YourWorkGroup(title: String(localized: "work.withAgent"), identifier: "work.withAgent") {
                ForEach(rows) { status in
                    Button { open(status) } label: { row(status) }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("with.row.\(status.id)")
                    if status.id != rows.last?.id { Divider().overlay(YansuTheme.borderSubtle) }
                }
            }
        }
    }

    private func row(_ status: HandoffStatus) -> some View {
        let words = WithAgentWords.line(status, computerAsleep: computerAsleep, locale: locale)
        return HStack(spacing: 10) {
            Text(verbatim: String(status.name.prefix(1)))
                .font(.caption.weight(.bold))
                .foregroundStyle(YansuTheme.accent)
                .frame(width: 26, height: 26)
                .background(YansuTheme.accentWash, in: Circle())
            VStack(alignment: .leading, spacing: 3) {
                Text(verbatim: status.title ?? "")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(1)
                HStack(spacing: 5) {
                    if words.tone == .working {
                        Circle().fill(YansuTheme.success).frame(width: 6, height: 6)
                    }
                    Text(verbatim: words.text)
                        .font(.footnote)
                        .foregroundStyle(words.tone == .amber ? YansuTheme.warning : YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Image(systemName: "chevron.right").font(.caption).foregroundStyle(YansuTheme.secondary)
        }
        .padding(.vertical, 12)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

/// A hand-off, opened (v1.2.1 rule 3): its state, the agent's own line, when it went, Ask again and Take it back.
struct HandoffStatusSheet: View {
    let status: HandoffStatus
    let hub: HandoffStatusAPI
    let computerAsleep: Bool
    /// After Take it back or Ask again: the new status.
    var changed: (HandoffStatus) -> Void
    @Environment(\.dismiss) private var dismiss
    @Environment(\.locale) private var locale
    @State private var working = false
    @State private var notice: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Text(verbatim: status.title ?? "")
                        .font(.title2.weight(.bold))
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                    let words = WithAgentWords.line(status, computerAsleep: computerAsleep, locale: locale)
                    Text(verbatim: words.text)
                        .font(.subheadline)
                        .foregroundStyle(words.tone == .amber ? YansuTheme.warning : YansuTheme.secondary)
                        .accessibilityIdentifier("with.sheet.state")
                    if let line = status.line, !line.isEmpty {
                        Text(verbatim: line)
                            .font(.body)
                            .foregroundStyle(YansuTheme.ink)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(14)
                            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 14))
                    }
                    if let sent = HandoffStatus.date(status.sentAt) {
                        Text(String.localizedStringWithFormat(String(localized: "with.sentAt"), BriefClock.text(sent, locale: locale)))
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                    }
                    if let notice {
                        Text(verbatim: notice).font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .accessibilityIdentifier("with.sheet.notice")
                    }
                    Button(String(localized: "with.askAgain")) { Task { await act(ask: true) } }
                        .buttonStyle(YansuPrimaryButtonStyle())
                        .disabled(working)
                        .accessibilityIdentifier("with.askAgain")
                    Button(String(localized: "with.takeBack")) { Task { await act(ask: false) } }
                        .buttonStyle(YansuSecondaryButtonStyle())
                        .disabled(working)
                        .accessibilityIdentifier("with.takeBack")
                    Text(String.localizedStringWithFormat(String(localized: "with.takeBackNote"), status.name))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(YansuTheme.gutter)
            }
            .yansuScreen()
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(String(localized: "Done")) { dismiss() }.accessibilityIdentifier("with.sheet.done")
                }
            }
        }
        .accessibilityIdentifier("with.sheet")
    }

    private func act(ask: Bool) async {
        working = true
        defer { working = false }
        let key = UUID().uuidString.lowercased()
        do {
            let updated = ask ? try await hub.askAgain(id: status.id, idempotencyKey: key)
                              : try await hub.takeBack(id: status.id, idempotencyKey: key)
            changed(updated)
            dismiss()
        } catch HubRequestError.conflict {
            // Already back or taken back, or Closed Eyes is on (409): the computer's word stands; refresh shows it.
            notice = String(localized: "with.alreadyChanged")
        } catch {
            notice = String(localized: "agents.unreachable")
        }
    }
}

#if DEBUG && targetEnvironment(simulator)
/// The stub of GET /agents/handoffs/status and its writes, one hand-off in each state the phone draws.
actor HandoffStatusUITestHub: HandoffStatusAPI {
    static let shared = HandoffStatusUITestHub()

    private var statuses: [HandoffStatus] = {
        let now = Date().timeIntervalSince1970 * 1000
        return [
            HandoffStatus(id: "s-working", agentId: "codex", agentName: "Codex", title: "Fix the failing release build",
                          sentAt: now - 1_200_000, state: "working", line: "Running the release tests."),
            HandoffStatus(id: "s-has", agentId: "claude", agentName: "Claude", title: "Draft the launch note",
                          sentAt: now - 3_600_000, state: "has_it", since: now - 3_000_000),
            HandoffStatus(id: "s-noword", agentId: "chatgpt", agentName: "ChatGPT", title: "Write the launch checklist",
                          sentAt: now - 9_000_000, state: "no_word", since: now - 8_400_000),
            HandoffStatus(id: "s-opened", agentId: "grok", agentName: "Grok", title: "Summarize the pricing thread",
                          sentAt: now - 600_000, state: "opened", since: now - 600_000),
            HandoffStatus(id: "s-back", agentId: "codex", agentName: "Codex", title: "Done already", sentAt: now - 20_000_000,
                          state: "back"),
        ]
    }()

    func handoffStatuses() async throws -> [HandoffStatus] { statuses }

    func askAgain(id: String, idempotencyKey: String) async throws -> HandoffStatus {
        guard let index = statuses.firstIndex(where: { $0.id == id }) else { throw HubRequestError.notPaired }
        statuses[index].state = "sent"
        statuses[index].sentAt = Date().timeIntervalSince1970 * 1000
        return statuses[index]
    }

    func takeBack(id: String, idempotencyKey: String) async throws -> HandoffStatus {
        guard let index = statuses.firstIndex(where: { $0.id == id }) else { throw HubRequestError.notPaired }
        statuses[index].state = "taken_back"
        return statuses[index]
    }
}
#endif

/// A result's text file, read-only (phone-agent v1.2.0 rule 3): fetched when opened, kept only while it's open.
struct WorkFileSheet: View {
    let item: WorkItem
    let file: WorkFile
    let api: WorkFileAPI
    @Environment(\.dismiss) private var dismiss
    @State private var text: WorkFileText?
    @State private var failed: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    Text(verbatim: WorkRequestWords.fileWhere(file))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                    if let text {
                        Text(verbatim: text.text)
                            .font(.body)
                            .foregroundStyle(YansuTheme.ink)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .accessibilityIdentifier("work.file.text")
                    } else if let failed {
                        Text(verbatim: failed).font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    } else {
                        ProgressView().frame(maxWidth: .infinity)
                    }
                }
                .padding(YansuTheme.gutter)
            }
            .yansuScreen()
            .navigationTitle(file.name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(String(localized: "Done")) { dismiss() }.accessibilityIdentifier("work.file.done")
                }
            }
        }
        .task {
            do {
                text = try await api.workFile(itemID: item.id, fileID: file.id)
            } catch HubRequestError.rejected(let status, _) where status == 413 || status == 415 {
                // Too big, or not plain text after all: it opens on the computer.
                failed = String(localized: "work.file.onComputerOnly")
            } catch {
                failed = String(localized: "agents.unreachable")
            }
        }
        .accessibilityIdentifier("work.file.sheet")
    }
}

#if DEBUG && targetEnvironment(simulator)
/// The stub of GET /work/{id}/file.
struct WorkFileUITestHub: WorkFileAPI {
    func workFile(itemID: String, fileID: String) async throws -> WorkFileText {
        WorkFileText(name: "test-plan.md", text: "Test plan\n\n1. The 40,000-row file imports in under a minute.\n2. Both failing date formats parse.")
    }
}
#endif
