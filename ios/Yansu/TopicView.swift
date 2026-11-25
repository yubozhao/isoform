import SwiftUI

/// Where a topic button or a "Your topics" card leads: the topic's id, with the name to show before the list loads.
struct TopicRoute: Hashable {
    let id: String
    let name: String
}

/// The Topic screen (RC 1): the chat filtered to one topic, never a side chat. Its box sends to the chat
/// with the topic attached, with no owner check.
struct TopicView: View {
    let route: TopicRoute
    @ObservedObject var topics: TopicStore
    let embedded: Bool
    @StateObject private var thread: TopicThread
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var actions: ActionStore
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var chatAvailability: ChatAvailabilityStore
    @FocusState private var composerFocused: Bool
    @Environment(\.tabChromeHeight) private var tabChromeHeight

    init(route: TopicRoute, topics: TopicStore, embedded: Bool = false) {
        self.route = route
        self.topics = topics
        self.embedded = embedded
        _thread = StateObject(wrappedValue: TopicThread(topicID: route.id, store: topics))
    }

    private var topic: MobileTopic? { topics.topic(route.id) }
    private var name: String { topic?.name ?? route.name }

    var body: some View {
        VStack(spacing: 0) {
            if embedded { filterChip } else { header }
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 14) {
                        rows
                        // No space after the scroll target: the end of the topic sits one row spacing above the composer,
                        // as two messages sit apart (P161).
                        Color.clear.frame(height: 0).id(Self.bottom)
                    }
                    .padding(.horizontal, YansuTheme.gutter)
                    .padding(.top, 14)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .refreshable { await thread.load() }
                .onChange(of: thread.items.last?.id) { _, _ in proxy.scrollTo(Self.bottom, anchor: .bottom) }
                .onChange(of: thread.pending) { _, _ in proxy.scrollTo(Self.bottom, anchor: .bottom) }
            }
            // With Chat off a topic message would fail on the computer (`feature_off: chat`), so no box (scope cut A6).
            if showsComposer, chatAvailability.chatShown {
                composer
            }
        }
        .yansuScreen()
        .navigationTitle(embedded ? Text("Chat") : Text("Topic"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .tint(YansuTheme.ink)
        .task {
            async let events: Void = chat.watchForEvents()
            await thread.load()
            await events
        }
        .onChange(of: chat.lastTopicTurn) { _, event in
            guard let event else { return }
            Task { await thread.receiveTurn(event) }
        }
        .onDisappear { thread.stop() }
    }

    private static let bottom = "topic.bottom"

    private var filterChip: some View {
        HStack {
            Button { router.openMainChat() } label: {
                HStack(spacing: 6) {
                    Text(verbatim: name)
                    Image(systemName: "xmark")
                }
                .font(.subheadline.weight(.medium))
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(YansuTheme.surfaceWarm, in: Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("chat.topicFilter.clear")
            Spacer()
        }
        .padding(.horizontal, YansuTheme.gutter)
        .padding(.vertical, 10)
    }

    // MARK: Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let topic {
                TopicStatusChip(topic: topic)
            }
            Text(verbatim: name)
                .font(.title2.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("topic.name")
            if let topic, let line = TopicLines.subtitle(topic) {
                Text(verbatim: line)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, YansuTheme.gutter)
        .padding(.top, 14)
        .padding(.bottom, 12)
        .overlay(alignment: .bottom) {
            Rectangle().fill(YansuTheme.borderSubtle).frame(height: 1)
        }
    }

    // MARK: Rows

    @ViewBuilder
    private var rows: some View {
        if !embedded { scopeLine }
        switch thread.state {
        case .loading:
            ProgressView("Checking with your computer.")
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.vertical, 24)
                .accessibilityIdentifier("topic.loading")
        case .notAllowed:
            notAllowed
        case .unsupported:
            EmptyStateCard(title: String(localized: "Topics need a newer Isoform on your computer."),
                           detail: String(localized: "Update Isoform on your computer to open topics on this phone. Your main chat has everything meanwhile."))
                .accessibilityIdentifier("topic.unsupported")
        case .gone:
            EmptyStateCard(title: String(localized: "This topic is no longer on your computer."),
                           detail: String(localized: "Isoform shows a topic only while it can still read what the topic came from."))
                .accessibilityIdentifier("topic.gone")
        case .paused:
            EmptyStateCard(title: String(localized: "Observing is paused on your computer."),
                           detail: String(localized: "Topics show here again when Isoform observes again. Your main chat has everything meanwhile."))
                .accessibilityIdentifier("topic.paused")
        case .offline:
            retryState(title: String(localized: "Your computer is offline."),
                       detail: String(localized: "This topic shows here when your computer is back."),
                       identifier: "topic.offline")
        case .failed:
            retryState(title: String(localized: "Isoform could not load this topic."),
                       detail: String(localized: "Your main chat still has everything."),
                       identifier: "topic.failed")
        case .loaded:
            loadedRows
        }
        if let notice = thread.notice {
            NoticeBanner(message: notice, dismiss: { thread.notice = nil })
                .accessibilityIdentifier("topic.notice")
        }
    }

    /// "Your chat, showing only this topic." with the way back to all of it.
    private var scopeLine: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text("Your main chat, showing only this topic.")
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 4)
            Button("Show everything") { router.openMainChat() }
                .font(.footnote.weight(.medium))
                .foregroundStyle(YansuTheme.accent)
                .frame(minHeight: 32)
                .accessibilityHint("Opens your main chat")
                .accessibilityIdentifier("topic.showEverything")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(YansuTheme.surfaceWarm, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    @ViewBuilder
    private var loadedRows: some View {
        if thread.hasMore {
            Button {
                Task { await thread.loadEarlier() }
            } label: {
                HStack(spacing: 6) {
                    if thread.loadingEarlier { ProgressView().controlSize(.small) }
                    Text("Show earlier")
                }
                .font(.footnote.weight(.medium))
                .foregroundStyle(YansuTheme.accent)
                .frame(minHeight: 44)
            }
            .disabled(thread.loadingEarlier)
            .accessibilityIdentifier("topic.earlier")
        }
        let shown = thread.items.filter(TopicRow.isShown)
        if shown.isEmpty && !thread.hasMore && thread.pending == nil {
            Text("Nothing about \(name) in your main chat yet. Ask below to start.")
                .font(.subheadline)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("topic.empty")
        }
        ForEach(shown) { item in
            TopicRow(item: item, chatID: thread.sessionID, perform: cardAction)
        }
        if let pending = thread.pending {
            VStack(alignment: .trailing, spacing: 4) {
                TopicUserBubble(text: pending.text)
                Text("Sent to your computer")
                    .font(.caption)
                    .foregroundStyle(YansuTheme.secondary)
                    .padding(.trailing, 6)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .accessibilityIdentifier("topic.pending")
        }
        if thread.awaitingReply {
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Isoform is working on this.")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("topic.working")
        }
    }

    private var notAllowed: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 12) {
                Text("This phone is not allowed to read your main chat.")
                    .font(.headline)
                    .foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                Text("Pair this phone again in Settings to open topics here. Your topics stay on your computer.")
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Open Settings") { router.openSettings() }
                    .buttonStyle(YansuPrimaryButtonStyle())
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("topic.notAllowed")
    }

    private func retryState(title: String, detail: String, identifier: String) -> some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 12) {
                Text(verbatim: title)
                    .font(.headline)
                    .foregroundStyle(YansuTheme.ink)
                Text(verbatim: detail)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Try again") { Task { await thread.load() } }
                    .buttonStyle(YansuSecondaryButtonStyle())
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(identifier)
    }

    // MARK: Composer

    private var showsComposer: Bool {
        thread.canSend && ![.notAllowed, .unsupported, .gone].contains(thread.state)
    }

    private var composer: some View {
        // Offline, Send asks the computer again first; the note says the words wait until it answers.
        let sendable = !thread.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !thread.sending
        return VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .bottom, spacing: 8) {
                TextField(String(localized: "Ask about \(name)"),
                          text: Binding(get: { thread.draft }, set: { thread.edit($0) }), axis: .vertical)
                    .font(.body)
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(1...5)
                    .focused($composerFocused)
                    .composerKeyboardDone { composerFocused = false }
                    .padding(.vertical, 8)
                    .accessibilityLabel(Text("Ask about \(name)"))
                    .accessibilityIdentifier("topic.composer")
                Button {
                    Task {
                        if await thread.send() { composerFocused = false }
                    }
                } label: {
                    Group {
                        if thread.sending {
                            ProgressView().controlSize(.small).tint(YansuTheme.onSolid)
                        } else {
                            Image(systemName: "arrow.up").font(.subheadline.weight(.bold))
                        }
                    }
                    .foregroundStyle(YansuTheme.onSolid)
                    .frame(width: 36, height: 36)
                    .background(YansuTheme.solid, in: Circle())
                    .frame(width: 44, height: 44)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(!sendable)
                .opacity(sendable || thread.sending ? 1 : 0.45)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("topic.send")
            }
            .padding(.leading, 14)
            .padding(.trailing, 2)
            .background(YansuTheme.composer, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 22, style: .continuous)
                    .strokeBorder(YansuTheme.composerBorder, lineWidth: 1)
            }
            Text(verbatim: composerNote)
                .font(.caption)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 6)
                .accessibilityIdentifier("topic.composer.note")
        }
        .padding(.horizontal, 12)
        .padding(.top, 8)
        .padding(.bottom, 10 + tabChromeHeight)
        .background(YansuTheme.background)
        .overlay(alignment: .top) {
            Rectangle().fill(YansuTheme.borderSubtle).frame(height: 1)
        }
    }

    private var composerNote: String {
        if thread.computerOffline { return TopicCopy.waitsForComputer }
        return String(localized: "Sends to your main chat with this topic attached.")
    }

    /// Card actions use the chat's journaled route; nil until the phone knows the chat and may write to it.
    private var cardAction: ((ChatProactiveView, ChatProactiveAction) async -> ProactiveActionOutcome)? {
        guard thread.canSend, let chatID = thread.sessionID else { return nil }
        let (actions, thread, topics) = (actions, thread, topics)
        return { card, action in
            let outcome = await actions.performProactiveAction(chatID: chatID, momentID: card.momentID,
                                                               actionID: action.id,
                                                               permissionRequest: card.permission != nil)
            if case .applied = outcome {
                if action.id == "send" { await topics.refresh() }
                await thread.load()
            }
            return outcome
        }
    }
}

/// One row of a topic: an Activity event, the user's words, Isoform's reply, or a card with a prepared reply.
/// Tool, status and other system rows stay in the chat, as in the computer's topic panel.
struct TopicRow: View {
    let item: MobileTopicItem
    var chatID: String? = nil
    @EnvironmentObject private var chat: ChatStore
    /// Actions on a card with a prepared reply; nil when the phone cannot act on cards.
    let perform: ((ChatProactiveView, ChatProactiveAction) async -> ProactiveActionOutcome)?

    static func isShown(_ item: MobileTopicItem) -> Bool {
        if item.kind == "event" { return !item.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
        guard let message = item.message else { return false }
        if message.report != nil { return true }
        switch (message.role, message.kind) {
        case (.user, _), (.assistant, .text):
            return !message.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        case (.system, .proactive):
            return message.proactive.map { !$0.isWithdrawnThreadUpdate } ?? false
        default:
            return false
        }
    }

    var body: some View {
        if item.kind == "event" {
            event
        } else if let message = item.message {
            if let report = message.report {
                ChatMessageReportRow(report: report)
            } else {
                switch message.role {
                case .user:
                    TopicUserBubble(text: message.text)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                case .assistant:
                    VStack(alignment: .leading, spacing: 4) {
                        Text(verbatim: TopicLines.isoform(at: item.at ?? message.atDate))
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                        ChatMessageText(text: message.text, kind: .reply)
                            .textSelection(.enabled)
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("topic.reply")
                default:
                    if let proactive = message.proactive {
                        if proactive.draft == true {
                            TopicReplyCard(item: proactive, at: item.at ?? message.atDate, perform: perform)
                        } else {
                            ChatProactiveCard(item: proactive, showsActions: perform != nil, onOpen: {
                                if let chatID { chat.openProactiveItem(proactive, chatID: chatID) }
                            }) { action in
                                guard let perform else { return .cancelled }
                                return await perform(proactive, action)
                            }
                        }
                    }
                }
            }
        }
    }

    private var event: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Circle()
                .fill(item.actor == "isoform" ? YansuTheme.accent : YansuTheme.ink)
                .frame(width: 8, height: 8)
                .accessibilityHidden(true)
            (item.actor == "user"
                ? Text("You").fontWeight(.semibold).foregroundStyle(YansuTheme.ink) + Text(verbatim: " \(item.text)")
                : Text(verbatim: item.text))
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
            if let at = item.at {
                Text(verbatim: TopicLines.time(at))
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("topic.event")
    }
}

/// A message from the computer with a prepared reply. Reading is local; actions use the chat's journaled route.
struct TopicReplyCard: View {
    let item: ChatProactiveView
    let at: Date?
    let perform: ((ChatProactiveView, ChatProactiveAction) async -> ProactiveActionOutcome)?
    @Environment(\.locale) private var locale
    @State private var busy = false
    @State private var appliedState: String?
    @State private var result: String?
    @State private var showsDraft = false

    private var language: String { ChatPresentationLanguage(locale: locale).code }

    /// The card's Not now, when the computer offered it and the card still waits.
    static func notNowAction(for item: ChatProactiveView, language: String) -> ChatProactiveAction? {
        guard item.canAct else { return nil }
        return item.actions(language: language).first { $0.id == "not_now" }
    }

    static func sendAction(for item: ChatProactiveView, language: String) -> ChatProactiveAction? {
        guard item.canAct, item.readableDraft != nil else { return nil }
        return item.actions(language: language).first { $0.id == "send" }
    }

    var body: some View {
        let copy = item.copy(language: language)
        var current = item
        current.state = appliedState ?? item.state
        let choices = [
            showsDraft ? Self.sendAction(for: current, language: language) : nil,
            Self.notNowAction(for: current, language: language),
        ].compactMap { $0 }
        return VStack(alignment: .leading, spacing: 10) {
            Text(verbatim: TopicLines.isoform(at: at))
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
            if !copy.headline.isEmpty {
                Text(verbatim: copy.headline)
                    .font(.body.weight(.medium))
                    .foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !copy.body.isEmpty {
                ChatMessageText(text: copy.body, kind: .reply)
            }
            if let draft = item.readableDraft {
                Button(showsDraft ? String(localized: "Hide the draft") : String(localized: "Read the draft")) {
                    showsDraft.toggle()
                }
                // The draft's own action is the filled one, so Read the draft leads the card (P130 follow-up, #648).
                .buttonStyle(YansuPillButtonStyle(prominent: true))
                .disabled(busy)
                .accessibilityIdentifier("topic.readDraft")
                if showsDraft {
                    ChatProactiveDraftContent(draft: draft, identifier: "topic.replyCard.draft")
                }
            }
            if let perform {
                ForEach(choices) { action in
                    Button {
                        guard !busy, current.canAct, action.id != "send" || showsDraft else { return }
                        busy = true
                        result = nil
                        Task {
                            let outcome = await perform(item, action)
                            busy = false
                            switch outcome {
                            case .applied(let state):
                                appliedState = state
                            case .refused(let message):
                                result = action.id == "send" ? String(localized: "Not sent. Try again.") : message
                            case .pending(let message):
                                result = message
                            case .cancelled:
                                break
                            }
                        }
                    } label: {
                        HStack(spacing: 6) {
                            if busy { ProgressView().controlSize(.small) }
                            Text(verbatim: action.label)
                        }
                    }
                    .buttonStyle(YansuPillButtonStyle(prominent: action.id == "send"))
                    .disabled(busy || !current.canAct)
                    .accessibilityLabel(Text(verbatim: action.label))
                    .accessibilityHint("Asks your computer to apply this action.")
                    .accessibilityIdentifier(action.id == "send" ? "topic.sendReply" : "topic.notNow")
                }
            }
            if let line = result ?? appliedState.map({ _ in current.statusLabel(language: language) }) {
                Text(verbatim: line)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.tertiary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("topic.replyCard.result")
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                .strokeBorder(YansuTheme.border, lineWidth: 1)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("topic.replyCard")
        .onChange(of: item.state) { _, _ in appliedState = nil }
    }
}

/// The user's own words, on the right.
struct TopicUserBubble: View {
    let text: String

    var body: some View {
        HStack {
            Spacer(minLength: 48)
            ChatMessageText(text: text, kind: .message)
                .textSelection(.enabled)
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(YansuTheme.surfaceUser, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 16, style: .continuous)
                        .strokeBorder(YansuTheme.border, lineWidth: 1)
                }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("topic.message.user")
    }
}

/// A topic's state as a small chip: "Waiting on you", "Waiting on them", "Today", "Moving", "2 new".
struct TopicStatusChip: View {
    let topic: MobileTopic

    var body: some View {
        if let label = Self.label(for: topic) {
            let tone = Self.tone(for: topic.status)
            Text(verbatim: label)
                .font(.caption.weight(.medium))
                .foregroundStyle(tone.text)
                .padding(.horizontal, 8)
                .frame(minHeight: 22)
                .background(tone.wash, in: Capsule())
                .overlay { Capsule().strokeBorder(tone.border, lineWidth: 1) }
                .accessibilityIdentifier("topic.status")
        }
    }

    static func label(for topic: MobileTopic) -> String? {
        switch topic.status {
        case "waiting_on_you": return String(localized: "Waiting on you")
        case "waiting_on_them": return String(localized: "Waiting on them")
        case "today": return String(localized: "Today")
        case "moving": return String(localized: "Moving")
        case "new_stories":
            return topic.newStories > 0 ? String(localized: "\(topic.newStories) new") : String(localized: "New stories")
        default: return nil
        }
    }

    private static func tone(for status: String) -> (text: Color, wash: Color, border: Color) {
        switch status {
        case "waiting_on_you": return (YansuTheme.warning, YansuTheme.warningWash, YansuTheme.warningBorder)
        case "new_stories": return (YansuTheme.info, YansuTheme.infoWash, YansuTheme.infoBorder)
        default: return (YansuTheme.secondary, YansuTheme.surfaceWarm, YansuTheme.border)
        }
    }
}

/// One card of "Your topics" on the Feed: its state, its name, what happened last and what comes next.
struct TopicCard: View {
    let topic: MobileTopic

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            TopicStatusChip(topic: topic)
            Text(verbatim: topic.name)
                .font(.callout.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
                .lineLimit(2)
                .multilineTextAlignment(.leading)
            if let summary = TopicLines.summary(topic) {
                Text(verbatim: summary)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(1)
                    .multilineTextAlignment(.leading)
            }
            Spacer(minLength: 0)
        }
        .padding(14)
        .frame(width: 210, alignment: .topLeading)
        .frame(minHeight: 150, alignment: .topLeading)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                .strokeBorder(YansuTheme.border, lineWidth: 1)
        }
        .contentShape(RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
    }
}

/// The words a topic shows besides its name, from the computer's fields only.
enum TopicLines {
    /// What happened last, or why Isoform follows it.
    static func summary(_ topic: MobileTopic) -> String? {
        let latest = topic.latest?.text ?? ""
        if !latest.isEmpty { return latest }
        let why = topic.why?.interestWhy?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return why.isEmpty ? nil : why
    }

    /// What comes next; a followed topic says so instead.
    static func footer(_ topic: MobileTopic) -> String {
        if let next = nextLine(topic) { return next }
        return topic.isFollowing ? String(localized: "Following") : String(localized: "Project")
    }

    /// The people and what comes next, under the topic's name.
    static func subtitle(_ topic: MobileTopic) -> String? {
        let parts = [people(topic), nextLine(topic)].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    static func people(_ topic: MobileTopic) -> String? {
        guard let line = topic.peopleLine, !line.names.isEmpty else { return nil }
        let names = line.names.joined(separator: ", ")
        let others = line.count - line.names.count
        if others <= 0 { return names }
        return others == 1 ? String(localized: "\(names) and 1 other") : String(localized: "\(names) and \(others) others")
    }

    static func nextLine(_ topic: MobileTopic) -> String? {
        guard let next = topic.next, !next.text.isEmpty else { return nil }
        if let at = next.atDate {
            return String(localized: "Next: \(next.text), \(time(at))")
        }
        return String(localized: "Next: \(next.text)")
    }

    /// "Isoform · 7:42 AM" over the computer's words.
    static func isoform(at date: Date?) -> String {
        guard let date else { return "Isoform" }
        return String(localized: "Isoform · \(time(date))")
    }

    /// The time today, otherwise the day and time.
    static func time(_ date: Date, now: Date = Date()) -> String {
        Calendar.current.isDate(date, inSameDayAs: now)
            ? date.formatted(date: .omitted, time: .shortened)
            : date.formatted(.dateTime.month(.abbreviated).day().hour().minute())
    }
}
