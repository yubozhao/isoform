import SwiftUI

/// The brief's clicks on the phone, as on the computer (backlog item 58, RC 1). On the Feed it shows the brief's
/// paragraphs too, each with its topic and up to two prepared questions; then what needs the user's attention with
/// its reasons; then the closing question with its answers as two full-width buttons, and "Talk about this brief".
/// Questions and answer cards open an Ask over Today, while "Talk about this brief" starts with Isoform's question.
/// With the computer offline, an Ask waits in the phone's journal. The phone writes none of the card words.
struct BriefCardsView: View {
    let brief: MobileBriefV1
    /// The brief's own page lists every item in its sections, so it shows only the buttons.
    var showsAttention = true
    /// The Feed shows the brief's paragraphs here; the brief's own page writes them out itself.
    var showsParagraphs = false
    /// Topic buttons on the paragraphs; nil where the screen has no topics.
    var topics: TopicStore? = nil
    var onPrepareInTopic: ((String, String) -> Bool)? = nil
    /// Today shows a short preview; the complete brief retains the other actions.
    var preview = false
    /// Today's week brief sits inside the brief's own card: see YourWeekCardView.embedded.
    var weekEmbedded = false
    var weekBelowTitle: AnyView? = nil
    var weekMakeNow: (() -> Void)? = nil
    var weekCanMakeNow = false
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var briefs: BriefStore
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var environment: AppEnvironment
    @State private var notice: String?
    /// The one line under "Talk about this brief" when the conversation cannot open (P151).
    @State private var talkLine: String?
    @State private var opening = false
    @State private var showingFindTime = false
    @State private var askRequest: BriefAskRequest?
    @State private var answeredCards: Set<String> = []

    private var canTalk: Bool {
        let scopes = pairing.paired?.scopes ?? []
        return scopes.contains("brief:read") && SettingsView.allowsChat(scopes)
    }

    var body: some View {
        let attention = showsAttention ? brief.attentionItems : []
        VStack(alignment: .leading, spacing: 20) {
            if let week = brief.yourWeek {
                // The written brief always shows; on a week day Your week follows it.
                if showsParagraphs {
                    paragraphs
                }
                YourWeekCardView(brief: brief, week: week, topics: topics, canTalk: canTalk,
                                 onPut: put, onPrepareInTopic: onPrepareInTopic, onFindTime: { showingFindTime = true },
                                 onAskTopic: askWeekTopic, embedded: weekEmbedded, belowTitle: weekBelowTitle,
                                 makeNow: weekMakeNow, canMakeNow: weekCanMakeNow)
            } else {
                if showsParagraphs {
                    paragraphs
                    if let question = brief.weekQuestion, let briefID = brief.briefId, canTalk {
                        MorningWeekQuestionView(briefID: briefID, question: question)
                            .environmentObject(briefs)
                    }
                }
                if !attention.isEmpty {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("What needs your attention")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(YansuTheme.secondary)
                            .accessibilityAddTraits(.isHeader)
                        ForEach(Array(attention.enumerated()), id: \.offset) { index, item in
                            attentionRow(item, index: index)
                        }
                    }
                }
                if showsParagraphs && !preview {
                    closing
                } else if !showsParagraphs {
                    pageActions
                }
            }
            if let notice {
                NoticeBanner(message: notice, dismiss: { self.notice = nil })
                    .accessibilityIdentifier("brief.notice")
            }
            if !preview, canTalk, let notice = chat.notice {
                NoticeBanner(message: notice, dismiss: chat.dismissNotice)
            }
        }
        .accessibilityElement(children: .contain)
        .sheet(isPresented: $showingFindTime) { PhoneFindTimeSheet(hub: environment.hub) }
        .sheet(item: $askRequest, onDismiss: { Task { await refreshAnsweredCards() } }) { BriefAskSheet(request: $0) }
        .task(id: brief.conversationKey) { await refreshAnsweredCards() }
        .onChange(of: chat.recentTurnEvents) { _, events in
            if events.last?.state == "done" { Task { await refreshAnsweredCards() } }
        }
    }

    // MARK: Paragraphs

    /// Every paragraph but the closing question, by its index in the delivered narrative: question cards name it.
    /// Today's preview shows the opening paragraph (up to six lines) and the chips of up to three paragraphs
    /// (TodayBriefCard.opening, TodayBriefCard.chipLimit).
    private var paragraphs: some View {
        let closingIndex = closingQuestion == nil ? nil : brief.closingParagraphIndex
        let shown = brief.narrative.enumerated().compactMap { index, paragraph -> ShownParagraph? in
            guard index != closingIndex,
                  let text = paragraph.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else { return nil }
            return ShownParagraph(index: index, paragraph: paragraph, text: text)
        }
        return VStack(alignment: .leading, spacing: 14) {
            if preview {
                if let opening = TodayBriefCard.opening(brief) {
                    paragraphText(opening.text, index: opening.index, lineLimit: 6)
                }
                let chipped = TodayBriefCard.chipParagraphs(brief)
                if !chipped.isEmpty {
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: 8) {
                            ForEach(chipped, id: \.self) { index in paragraphChip(forParagraph: index) }
                        }
                        VStack(alignment: .leading, spacing: 8) {
                            ForEach(chipped, id: \.self) { index in paragraphChip(forParagraph: index) }
                        }
                    }
                }
            } else {
                ForEach(shown) { paragraph in
                    VStack(alignment: .leading, spacing: 10) {
                        topicLabel(paragraph.paragraph, index: paragraph.index)
                        paragraphText(paragraph.text, index: paragraph.index)
                        paragraphChip(forParagraph: paragraph.index)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func paragraphText(_ text: String, index: Int, lineLimit: Int? = nil) -> some View {
        if canTalk, brief.chatReference != nil, index != brief.closingParagraphIndex {
            Button { askAboutParagraph(index) } label: {
                Text(verbatim: text)
                    .font(.body)
                    .lineSpacing(4)
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(lineLimit)
                    .fixedSize(horizontal: false, vertical: lineLimit == nil)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(opening)
            .accessibilityHint("Ask about this")
            .accessibilityIdentifier("brief.paragraph.\(index)")
        } else {
            Text(verbatim: text)
                .font(.body)
                .lineSpacing(4)
                .foregroundStyle(YansuTheme.ink)
                .lineLimit(lineLimit)
                .fixedSize(horizontal: false, vertical: lineLimit == nil)
                .accessibilityIdentifier("brief.paragraph.\(index)")
        }
    }

    private struct ShownParagraph: Identifiable {
        let index: Int
        let paragraph: BriefParagraph
        let text: String
        var id: Int { index }
    }

    /// The paragraph's topic: a text button that opens the Topic screen, or its name alone on a computer without
    /// topic routes.
    @ViewBuilder
    private func topicLabel(_ paragraph: BriefParagraph, index: Int) -> some View {
        if let topic = paragraph.topic {
            if let topics, topics.support != .unsupported {
                NavigationLink {
                    TopicView(route: TopicRoute(id: topic.id, name: topic.name), topics: topics)
                } label: {
                    HStack(spacing: 4) {
                        Text(verbatim: topic.name)
                        Image(systemName: "chevron.right")
                            .font(.caption2.weight(.semibold))
                            .accessibilityHidden(true)
                    }
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .frame(minHeight: 32)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(verbatim: topic.name))
                .accessibilityHint("Opens this topic")
                .accessibilityIdentifier("brief.topic.\(index)")
            } else {
                Text(verbatim: topic.name)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
    }

    @ViewBuilder
    private func paragraphChip(forParagraph index: Int) -> some View {
        if canTalk, brief.chatReference != nil, let card = brief.paragraphChip(forParagraph: index) {
            Button { put(card) } label: {
                HStack(spacing: 5) {
                    Text(verbatim: card.label ?? "")
                    if answeredCards.contains(card.id) { Image(systemName: "checkmark").accessibilityHidden(true) }
                }
            }
            .buttonStyle(YansuChipButtonStyle())
            .accessibilityValue(answeredCards.contains(card.id) ? String(localized: "Answered") : "")
            .accessibilityHint("Asks this in a sheet")
            .accessibilityIdentifier("brief.chip.\(index)")
        }
    }

    // MARK: Closing

    /// The closing question when the Feed shows the paragraphs; the brief's own page already printed it.
    private var closingQuestion: String? {
        guard showsParagraphs, let index = brief.closingParagraphIndex else { return nil }
        return brief.narrative[index].text?.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// The Feed's closing card: the last paragraph's topic, its question with its own prepared questions, the two
    /// answers as full-width buttons, and Talk.
    @ViewBuilder
    private var closing: some View {
        let question = closingQuestion
        let answers = canTalk && brief.chatReference != nil ? brief.actionCards : []
        if question != nil || canTalk {
            VStack(alignment: .leading, spacing: 12) {
                if let question, let index = brief.closingParagraphIndex {
                    topicLabel(brief.narrative[index], index: index)
                    Text(verbatim: question)
                        .font(.body.weight(.medium))
                        .lineSpacing(3)
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("brief.closing")
                }
                if !answers.isEmpty {
                    VStack(spacing: 8) {
                        ForEach(Array(answers.enumerated()), id: \.element.id) { index, card in
                            let label = card.label
                            Button { put(card) } label: {
                                HStack(spacing: 5) {
                                    actionTitle(for: label)
                                    if answeredCards.contains(card.id) { Image(systemName: "checkmark").accessibilityHidden(true) }
                                }
                            }
                                .buttonStyle(YansuPillButtonStyle(prominent: index == 0))
                                .accessibilityLabel(actionTitle(for: label))
                                .accessibilityHint("Asks this in a sheet")
                                .accessibilityIdentifier("brief.card.\(card.id)")
                        }
                    }
                }
                if canTalk, Self.offersTalk(brief), talkReplaced {
                    talkLineText(BriefConversationCopy.unavailable)
                } else if canTalk, Self.offersTalk(brief) {
                    Button { talk() } label: {
                        HStack(spacing: 6) {
                            if opening {
                                ProgressView().controlSize(.small)
                            } else {
                                Image(systemName: "bubble.left")
                                    .accessibilityHidden(true)
                            }
                            Text("Talk about this brief")
                        }
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(YansuTheme.accent)
                        .frame(minHeight: 44)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(opening)
                    .accessibilityLabel("Talk about this brief")
                    .accessibilityHint("Asks this in a sheet")
                    .accessibilityIdentifier("brief.talk")
                    if let talkLine {
                        talkLineText(talkLine)
                    }
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                    .strokeBorder(YansuTheme.border, lineWidth: 1)
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("brief.closingCard")
        }
    }

    /// The brief's own page keeps its clicks as they were: the buttons, then Talk.
    @ViewBuilder
    private var pageActions: some View {
        ForEach(Array(brief.narrative.enumerated()), id: \.offset) { index, paragraph in
            topicLabel(paragraph, index: index)
        }
        if canTalk {
            if brief.chatReference != nil, !brief.actionCards.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(brief.actionCards) { card in
                        let label = card.label
                        Button { put(card) } label: {
                            HStack(spacing: 5) {
                                actionTitle(for: label)
                                if answeredCards.contains(card.id) { Image(systemName: "checkmark").accessibilityHidden(true) }
                            }
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(YansuTheme.accent)
                                .padding(.horizontal, 14)
                                .padding(.vertical, 10)
                                .frame(minHeight: 44, alignment: .leading)
                                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
                                .overlay { RoundedRectangle(cornerRadius: 18).strokeBorder(YansuTheme.border, lineWidth: 1) }
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(actionTitle(for: label))
                        .accessibilityHint("Asks this in a sheet")
                        .accessibilityIdentifier("brief.card.\(card.id)")
                    }
                }
            }
            if Self.offersTalk(brief), talkReplaced {
                talkLineText(BriefConversationCopy.unavailable)
            } else if Self.offersTalk(brief) {
                Button { talk() } label: {
                    Label("Talk about this brief", systemImage: "bubble.left.and.bubble.right")
                }
                .buttonStyle(YansuSecondaryButtonStyle())
                .disabled(opening)
                .accessibilityLabel("Talk about this brief")
                .accessibilityHint("Asks this in a sheet")
                .accessibilityIdentifier("brief.talk")
                if let talkLine {
                    talkLineText(talkLine)
                }
            }
        }
    }

    // MARK: Attention

    private func attentionRow(_ item: BriefItem, index: Int) -> some View {
        HStack(alignment: .top, spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text(verbatim: item.title ?? "")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                Text(verbatim: item.attentionReason ?? "")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("brief.attention.reason.\(index)")
            }
            Spacer(minLength: 8)
            if canTalk, brief.chatReference != nil, let card = brief.itemCard(for: item) {
                Button { put(card) } label: {
                    Image(systemName: "bubble.left")
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.accent)
                        .frame(width: 44, height: 44)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Chat about this")
                .accessibilityHint("Asks this in a sheet")
                .accessibilityIdentifier("brief.attention.chat.\(index)")
            }
        }
        .padding(14)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
        .overlay { RoundedRectangle(cornerRadius: 16).strokeBorder(YansuTheme.border, lineWidth: 1) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.attention.\(index)")
    }

    private func actionTitle(for label: String?) -> Text {
        Self.usesNotNowLabel(label) ? Text("Not now") : Text(verbatim: label ?? "")
    }

    static func usesNotNowLabel(_ label: String?) -> Bool {
        let original = label ?? ""
        let normalized = original.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return ["skip", "later", "not now", "跳过", "稍后", "暂时不用"].contains(normalized)
    }

    // MARK: Taps

    /// What a question, an answer or Talk says instead of acting: only that it waits, while the computer is offline.
    static func offlineNotice(computerOnline: Bool?) -> String? {
        computerOnline == false ? BriefConversationCopy.waitsForComputer : nil
    }

    /// A card posts its prepared question as a source-checked Ask over Today.
    private func put(_ card: MobileBriefCard) {
        if Self.isFindTimeCard(card) {
            showingFindTime = true
            return
        }
        guard var reference = brief.chatReference, brief.sendCards.contains(card) else { return }
        reference.cardId = card.id
        reference.cardMessage = card.message
        reference.ask = true
        let quote = card.paragraph.flatMap { brief.narrative.indices.contains($0) ? brief.narrative[$0].text : nil }
        askRequest = BriefAskRequest(reference: reference, quote: quote, initialQuestion: card.message)
    }

    private func refreshAnsweredCards() async {
        guard var reference = brief.chatReference else { return }
        reference.ask = true
        var answered = Set<String>()
        for card in brief.sendCards {
            reference.cardId = card.id
            reference.cardMessage = card.message
            if await chat.savedBriefAsk(reference: reference) != nil { answered.insert(card.id) }
        }
        answeredCards = answered
    }

    private func askWeekTopic(_ topicID: String, _ message: String) {
        guard var reference = brief.chatReference, Identifiers.isSafePathID(topicID), !message.isEmpty else { return }
        reference.ask = true
        reference.weekTopicId = topicID
        reference.weekMessage = message
        askRequest = BriefAskRequest(reference: reference, quote: nil, initialQuestion: message)
    }

    static func isFindTimeCard(_ card: MobileBriefCard) -> Bool {
        let label = (card.label ?? "").lowercased()
        return card.itemKey.map(isCommitmentBriefItemKey) == true &&
            (label.hasPrefix("find time") || label.range(of: #"^为.*留出时间"#, options: .regularExpression) != nil)
    }

    /// Talk asks the computer for this brief's conversation when the phone has no reference for it, then opens the
    /// sheet on Isoform's question. With the computer away, the notice says so and no sheet opens.
    /// The unreachable line stands where Talk would be when the phone already knows the computer is away, or when a
    /// tap found it unreachable. A refusal or an older computer keeps the button, with its line under it.
    private var talkReplaced: Bool {
        // The same away state Today's line reads: the connection cause, mirrored into the chat store.
        Self.talkReplacedByLine(knownAway: chat.awayCause != nil, tapLine: talkLine)
    }

    // A sleeping computer does not replace Talk: the question waits for it (PROTOCOL.md section 23). Only a
    // computer that cannot be reached at all keeps the line.
    static func talkReplacedByLine(knownAway: Bool, tapLine: String?) -> Bool {
        tapLine == BriefConversationCopy.unavailable
    }

    /// Talk needs the computer's opener (the sheet opens on it). Until the computer can start a conversation from any
    /// brief, a brief the phone knows without an opener card offers no Talk button, rather than one it would refuse.
    static func offersTalk(_ brief: MobileBriefV1) -> Bool {
        brief.cards.contains { $0.kind == "opener" && !$0.message.isEmpty }
    }

    private func talkLineText(_ line: String) -> some View {
        Text(line)
            .font(.footnote)
            .foregroundStyle(YansuTheme.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("brief.talk.line")
    }

    /// Every tap resolves first, so a press always ends in the sheet or in a line under Talk. Nothing returns early.
    private func talk() {
        Task {
            opening = true
            let resolved = await chat.resolveTalkReference(for: brief)
            opening = false
            guard var reference = resolved.reference else {
                talkLine = resolved.notice
                return
            }
            // The sheet opens on Isoform's question and two answers; a brief without them cannot open one.
            let choices = brief.sendCards.filter { $0.kind == "opener_answer" && !($0.label ?? "").isEmpty }
            guard choices.count >= 2, let opener = brief.cards.first(where: { $0.kind == "opener" && !$0.message.isEmpty }) else {
                talkLine = BriefConversationCopy.gateRefused
                return
            }
            let first = choices[0]
            talkLine = nil
            reference.ask = true
            reference.cardId = first.id
            reference.cardMessage = first.message
            askRequest = BriefAskRequest(reference: reference, quote: nil, openingQuestion: opener.message,
                                         answerChoices: Array(choices.prefix(3)))
        }
    }

    private func askAboutParagraph(_ index: Int) {
        guard var reference = brief.chatReference, brief.narrative.indices.contains(index),
              let quote = brief.narrative[index].text, !quote.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        reference.ask = true
        reference.paragraph = index
        reference.paragraphText = quote
        askRequest = BriefAskRequest(reference: reference, quote: quote)
    }
}

private struct MorningWeekQuestionView: View {
    let briefID: String
    let question: MorningBriefWeekQuestion
    @EnvironmentObject private var briefs: BriefStore
    @State private var answer: String
    @State private var editing: Bool
    @State private var busy = false
    @State private var saved = false
    @State private var failed = false

    init(briefID: String, question: MorningBriefWeekQuestion) {
        self.briefID = briefID
        self.question = question
        _answer = State(initialValue: question.recommendation ?? "")
        _editing = State(initialValue: question.recommendation?.isEmpty != false)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("What is this week about?").font(.subheadline.weight(.semibold))
            if saved {
                Text("Saved for this week.").font(.footnote).foregroundStyle(YansuTheme.secondary)
            } else {
                if editing {
                    TextField("This week's focus", text: $answer).textFieldStyle(.roundedBorder)
                } else {
                    let recommendation = question.recommendation ?? ""
                    Text("I'd start with \(recommendation).").font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                HStack {
                    Button { save() } label: {
                        if editing { Text("Save") } else { Text("Still that") }
                    }
                        .disabled(busy || answer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    if !editing { Button("Something else") { editing = true }.disabled(busy) }
                }
                if failed { Text("Could not save your answer. Try again.").font(.footnote).foregroundStyle(YansuTheme.secondary) }
            }
        }
    }

    private func save() {
        guard !busy else { return }
        busy = true
        failed = false
        Task {
            do {
                try await briefs.saveMorningWeekIntent(briefID: briefID, answer: answer.trimmingCharacters(in: .whitespacesAndNewlines))
                saved = true
            } catch {
                failed = true
            }
            busy = false
        }
    }
}
