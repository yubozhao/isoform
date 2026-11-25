import SwiftUI

// Your data (phone-settings v1.0.0 rules 12 and 13; board artboard 8, frame 7c): by where the data is. Kept on your
// computer, read here; on this phone, with how long; and what left your devices.

/// One answer in the computer's answers ledger (Your answers).
struct AnswerEntry: Codable, Equatable, Identifiable, Sendable {
    let id: String
    var item: String? = nil
    var kind: String? = nil
    var title: String? = nil
    let action: String
    var reason: String? = nil
    var note: String? = nil
    var edited: WorkEdited? = nil
    let at: String
    var surface: String? = nil
    var source: String? = nil
    var undone: Bool? = nil

    var date: Date? { RelayTime.parse(at) }

    /// "Yes", "No · not now", "Not mine", "Edited", "Useful", "Not useful".
    var actionLine: String {
        let word: String
        switch action {
        case "yes": word = String(localized: "work.yes")
        case "no": word = String(localized: "work.no")
        case "not_mine": word = String(localized: "work.notMine")
        case "edit": word = String(localized: "data.answer.edited")
        case "useful": word = String(localized: "work.useful")
        case "not_useful": word = String(localized: "work.notUseful")
        default: word = action
        }
        guard let reason, let title = WorkReason(rawValue: reason)?.title else { return word }
        return word + " · " + title.lowercased()
    }

    var surfaceWord: String? {
        switch surface {
        case "phone": return String(localized: "data.surface.phone")
        case "computer": return String(localized: "data.surface.computer")
        case "notification": return String(localized: "data.surface.notification")
        case "chat": return String(localized: "data.surface.chat")
        default: return nil
        }
    }
}

struct AnswersPage: Codable, Equatable, Sendable {
    var answers: [AnswerEntry]
    var next: String? = nil
}

protocol AnswersHubAPI: Sendable {
    func answers(limit: Int, before: String?) async throws -> AnswersPage
    func forgetAnswer(id: String) async throws
}

extension HubChannel: AnswersHubAPI {
    func answers(limit: Int, before: String?) async throws -> AnswersPage {
        try await perform(.answers(limit: limit, before: before)).payload(AnswersPage.self)
    }

    func forgetAnswer(id: String) async throws {
        let response = try await perform(.forgetAnswer(id: id))
        // 404: the entry isn't kept any more, which is what the person asked for.
        guard response.status == 404 || (200..<300).contains(response.status) else {
            throw HubRequestError.rejection(status: response.status, message: nil)
        }
    }
}

struct YourDataView: View {
    let answersHub: AnswersHubAPI
    /// The hand-off log, when the computer serves it; without it the row says where the log is.
    var sentHub: AgentsHubAPI? = nil
    /// Your voice's page, when the computer keeps a voice sample from this phone (rule 12).
    var voicePage: AnyView? = nil
    /// What Isoform sees, the first row under "Kept on your computer" (phone-duo v1.1.0 rule 1).
    var seesHub: SeesHubAPI? = nil
    @ObservedObject private var watchRecordings = WatchRecordingsModel.shared
    @ObservedObject private var watchTab = WatchTabModel.shared

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                group(String(localized: "data.computer")) {
                    NavigationLink {
                        IsoformSeesPage(hub: seesHub)
                    } label: {
                        row(String(localized: "data.seen"), sub: String(localized: "data.seenSub"), chevron: true)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("data.seen")
                    Divider().overlay(YansuTheme.borderSubtle)
                    NavigationLink {
                        YourAnswersView(hub: answersHub)
                    } label: {
                        row(String(localized: "data.answers"), sub: String(localized: "data.answersSub"), chevron: true)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("data.answers")
                    if let voicePage {
                        Divider().overlay(YansuTheme.borderSubtle)
                        NavigationLink {
                            voicePage
                        } label: {
                            row(String(localized: "set.voice"), sub: String(localized: "data.voiceSub"), chevron: true)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("data.voice")
                    }
                }
                group(String(localized: "data.phone")) {
                    row(String(localized: "data.today"), sub: String(localized: "data.todaySub"))
                    Divider().overlay(YansuTheme.borderSubtle)
                    row(String(localized: "Past briefs"), sub: String(localized: "data.pastSub"))
                    if watchRecordings.worthShowing {
                        Divider().overlay(YansuTheme.borderSubtle)
                        // How much is on its way sits under the title, so neither is cut short.
                        row(String(localized: "data.watchAudio"),
                            sub: [String(localized: "data.watchAudioSub"), WatchRecordingsCopy.summary(watchRecordings.rows)]
                                .filter { !$0.isEmpty }.joined(separator: " · "))
                    }
                }
                group(String(localized: "data.left")) {
                    // The computer keeps each hand-off with exactly what went (PROTOCOL.md 27.2). An older computer
                    // doesn't serve the log, so the row says where it is.
                    if let sentHub {
                        NavigationLink {
                            SentToAgentsView(hub: sentHub)
                        } label: {
                            row(String(localized: "data.sent"), sub: String(localized: "data.sentSub"), chevron: true)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("data.sent")
                    } else {
                        row(String(localized: "data.sent"), sub: String(localized: "data.sentWhere"))
                    }
                    // With Place on, the watch asks Apple to name the area (design's
                    // data.appleArea, v1.5.0 W9): the part before the first " · " is the title,
                    // the rest the sub-line. Shown while Place is on and for a week after.
                    if watchTab.namesAppleForTheArea {
                        Divider().overlay(YansuTheme.borderSubtle)
                        row(String(localized: "data.appleArea"), sub: String(localized: "data.appleAreaSub"))
                            .accessibilityIdentifier("data.appleArea")
                    }
                }
                Text("data.foot")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 4)
            }
            .padding(YansuTheme.gutter)
        }
        .yansuScreen()
        .navigationTitle(String(localized: "set.data"))
        .navigationBarTitleDisplayMode(.inline)
        .accessibilityIdentifier("data.screen")
    }

    private func group<Content: View>(_ title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            SectionLabel(title: title).padding(.horizontal, 4).padding(.top, 4)
            VStack(spacing: 0) { content() }
                .padding(.horizontal, 16)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius))
        }
    }

    private func row(_ title: String, sub: String, trailing: String? = nil, chevron: Bool = false) -> some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: title).font(.body).foregroundStyle(YansuTheme.ink)
                Text(verbatim: sub).font(.footnote).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if let trailing, !trailing.isEmpty {
                Text(verbatim: trailing).font(.subheadline).foregroundStyle(YansuTheme.secondary).lineLimit(1)
            }
            if chevron {
                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.secondary)
            }
        }
        .padding(.vertical, 12)
        .contentShape(Rectangle())
    }
}

/// Your answers: every Yes, No, Not mine and edit, newest first, read from the computer, each with Forget.
struct YourAnswersView: View {
    let hub: AnswersHubAPI
    @State private var answers: [AnswerEntry] = []
    @State private var next: String?
    @State private var loaded = false
    @State private var failed = false
    @State private var forgetting: AnswerEntry?
    @Environment(\.locale) private var locale

    var body: some View {
        List {
            if failed && answers.isEmpty {
                Text("data.answers.unreachable").font(.subheadline).foregroundStyle(YansuTheme.secondary)
            } else if loaded && answers.isEmpty {
                Text("data.answers.none").font(.subheadline).foregroundStyle(YansuTheme.secondary)
            }
            ForEach(answers) { answer in
                VStack(alignment: .leading, spacing: 3) {
                    Text(verbatim: answer.title ?? answer.item ?? "")
                        .font(.body)
                        .foregroundStyle(answer.undone == true ? YansuTheme.secondary : YansuTheme.ink)
                        .strikethrough(answer.undone == true)
                    Text(verbatim: Self.line(answer, locale: locale))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                .swipeActions {
                    Button(String(localized: "data.answers.forget"), role: .destructive) { forgetting = answer }
                }
                .accessibilityIdentifier("data.answer.\(answer.id)")
            }
            if next != nil {
                Button(String(localized: "data.answers.more")) { Task { await load(more: true) } }
                    .foregroundStyle(YansuTheme.accent)
            }
        }
        .scrollContentBackground(.hidden)
        .yansuScreen()
        .navigationTitle(String(localized: "data.answers"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await load(more: false) }
        .confirmationDialog(Text("data.answers.forgetTitle"), isPresented: Binding(get: { forgetting != nil },
                                                                                   set: { if !$0 { forgetting = nil } }),
                            titleVisibility: .visible) {
            Button(String(localized: "data.answers.forget"), role: .destructive) {
                if let answer = forgetting { Task { await forget(answer) } }
            }
            Button(String(localized: "Cancel"), role: .cancel) {}
        } message: {
            Text("data.answers.forgetBody")
        }
        .accessibilityIdentifier("data.answers.screen")
    }

    /// "Yes · not now · on your phone · Nov 15, 6:20 PM".
    static func line(_ answer: AnswerEntry, locale: Locale) -> String {
        var parts = [answer.actionLine]
        if answer.undone == true { parts.append(String(localized: "data.answer.undone")) }
        if let surface = answer.surfaceWord { parts.append(surface) }
        if let date = answer.date {
            parts.append(date.formatted(.dateTime.month(.abbreviated).day().hour().minute().locale(locale)))
        }
        return parts.joined(separator: " · ")
    }

    private func load(more: Bool) async {
        do {
            let page = try await hub.answers(limit: 50, before: more ? next : nil)
            answers = more ? answers + page.answers : page.answers
            next = page.next
            failed = false
        } catch {
            failed = true
        }
        loaded = true
    }

    private func forget(_ answer: AnswerEntry) async {
        do {
            try await hub.forgetAnswer(id: answer.id)
            answers.removeAll { $0.id == answer.id }
        } catch {
            failed = true
        }
    }
}

/// Sent to your agents (phone-settings rule 12): each hand-off, newest first, with the agent, the time and exactly
/// what went. Read from the computer's log; the phone keeps none of it.
struct SentToAgentsView: View {
    let hub: AgentsHubAPI
    @State private var handoffs: [AgentHandoffEntry] = []
    @State private var next: String?
    @State private var loaded = false
    @State private var failed = false
    @Environment(\.locale) private var locale

    var body: some View {
        List {
            if failed && handoffs.isEmpty {
                Text("data.sent.unreachable").font(.subheadline).foregroundStyle(YansuTheme.secondary)
            } else if loaded && handoffs.isEmpty {
                Text("data.sent.none").font(.subheadline).foregroundStyle(YansuTheme.secondary)
            }
            ForEach(handoffs) { handoff in
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: Self.line(handoff, locale: locale))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                    Text(verbatim: handoff.text)
                        .font(.body)
                        .foregroundStyle(YansuTheme.ink)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                    if let sent = handoff.sent, !sent.isEmpty, sent != handoff.text {
                        Text(String.localizedStringWithFormat(String(localized: "data.sent.left"), sent))
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .padding(.vertical, 2)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("data.sent.\(handoff.id)")
            }
            if next != nil {
                Button(String(localized: "data.answers.more")) { Task { await load(more: true) } }
                    .foregroundStyle(YansuTheme.accent)
            }
        }
        .scrollContentBackground(.hidden)
        .yansuScreen()
        .navigationTitle(String(localized: "data.sent"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await load(more: false) }
        .accessibilityIdentifier("data.sent.screen")
    }

    /// "Claude Code · from your phone · Nov 15, 6:20 PM".
    static func line(_ handoff: AgentHandoffEntry, locale: Locale) -> String {
        var parts: [String] = []
        if let name = handoff.agentName ?? handoff.agentId, !name.isEmpty { parts.append(name) }
        switch handoff.surface {
        case "phone": parts.append(String(localized: "data.surface.phone"))
        case "computer": parts.append(String(localized: "data.surface.computer"))
        default: break
        }
        if let date = handoff.date {
            parts.append(date.formatted(.dateTime.month(.abbreviated).day().hour().minute().locale(locale)))
        }
        return parts.joined(separator: " · ")
    }

    private func load(more: Bool) async {
        do {
            let page = try await hub.handoffs(limit: 50, before: more ? next : nil)
            handoffs = more ? handoffs + page.handoffs : page.handoffs
            next = page.next
            failed = false
        } catch {
            failed = true
        }
        loaded = true
    }
}
