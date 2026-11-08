#if DEBUG && targetEnvironment(simulator)
import Foundation

/// A cached chat for the seeded Chat UI test states. "chat" ends with a message sent from this iPhone that
/// the Mac has answered (Seen); "chat-sent" ends with one the Mac has stored but not started on (Sent). The seeded
/// pairing points at a relay that does not exist, so the chat stays on its cached rows and a new message stays
/// Sending.
enum ChatUITestSeed {
    static let mainChatID = "ui-main-chat"
    static let lastMessage = "Can you draft the reply to Sam about the Q3 deck?"
    static let lastReply = "Drafted. It thanks Sam for the notes and asks for the final numbers by Thursday."
    static let proactiveMomentID = "ui-proactive-moment"

    static func firstReadItem(now: Date) throws -> ChatItemView {
        let chinese = Locale.preferredLanguages.first?.hasPrefix("zh") == true
        let sentences = chinese
            ? ["你一边在发布 Isoform 的 iPhone 应用，一边在收尾 Halcyon 的 SOW。", "你上午在 Claude Code 里做开发，下午留给通话。"]
            : ["You're shipping Isoform's iPhone app while closing the Halcyon SOW.", "You build in Claude Code before noon and keep afternoons for calls."]
        let english = ["Help you prepare a reply to Priya.", "Brief you at 7:30 AM.", "Tell you when Claude Code work needs you."]
        let chineseHelp = ["帮你准备给 Priya 的回复。", "在上午 7:30 给你简报。", "Claude Code 的工作需要你时告诉你。"]
        let payload: [String: Any] = [
            "seq": 1, "id": "ui-first-read-row", "role": "system", "kind": "proactive",
            "text": sentences.joined(separator: " "), "toolName": "main_chat_proactive", "at": stamp(now),
            "proactive": ["momentId": "ui-first-read", "kind": "first_read", "headline": "What I see so far",
                          "body": sentences.joined(separator: " "), "sentences": sentences, "helps": chinese ? chineseHelp : english,
                          "offerCount": 3, "actionIds": ["confirm_portrait", "correct_portrait"],
                          "actionLabels": [:], "target": ["kind": "none"], "evidence": [], "state": "pending",
                          "localized": [
                              "en": ["headline": "What I see so far", "body": sentences.joined(separator: " "), "actionLabels": [:], "helps": english],
                              "zh-CN": ["headline": "What I see so far", "body": sentences.joined(separator: " "), "actionLabels": [:], "helps": chineseHelp],
                          ]],
        ]
        return try JSONDecoder().decode(ChatItemView.self, from: JSONSerialization.data(withJSONObject: payload))
    }

    /// Short, wire-shaped transcripts keep the whole card or status row visible on the smallest iPhone.
    /// The action response is supplied separately, so layout tests never depend on a relay.
    static func seedPresentation(_ cache: ChatCache, item: ChatItemView, now: Date) throws {
        try cache.rememberMainChat(mainSummary(now: now))
        try cache.save(ChatHeaderView(id: mainChatID, title: "Isoform", state: .idle), items: [item])
    }

    static func seedPresentation(_ cache: ChatCache, items: [ChatItemView], now: Date) throws {
        try cache.rememberMainChat(mainSummary(now: now))
        try cache.save(ChatHeaderView(id: mainChatID, title: "Isoform", state: .idle), items: items)
    }

    static func presentationItem(status: Bool, now: Date) throws -> ChatItemView {
        let payload: [String: Any]
        if status {
            payload = [
                "seq": 1, "id": "ui-status-row", "role": "system", "kind": "status",
                "text": "Your morning brief is ready. Calendar capture is paused.",
                "toolName": "main_chat_status", "at": stamp(now),
                "status": [
                    "period": "2026-10-16-morning",
                    "entries": [
                        ["id": "ui-status-brief", "kind": "brief_ready", "text": "Your morning brief is ready.",
                         "target": ["kind": "today"]],
                        ["id": "ui-status-capture", "kind": "capture_paused", "text": "Calendar capture is paused.",
                         "target": ["kind": "settings", "id": "activity"]],
                    ],
                ],
            ]
        } else {
            payload = [
                "seq": 1, "id": "ui-proactive-row", "role": "system", "kind": "proactive",
                "text": "A quick review of the launch plan", "toolName": "main_chat_proactive", "at": stamp(now),
                "proactive": [
                    "momentId": proactiveMomentID, "kind": "suggestion",
                    "headline": "A quick review of the launch plan",
                    "body": "I can prepare a short summary of the launch plan for your next review.",
                    "draft": false, "actionIds": ["do_once", "not_now"],
                    "actionLabels": ["do_once": "Do once", "not_now": "Not now"],
                    "target": ["kind": "suggestion", "id": "ui-launch-plan"],
                    "evidence": [["kind": "work_pattern", "id": "ui-review-pattern", "at": stamp(now),
                                  "target": ["kind": "activity"]]],
                    "state": "pending",
                ],
            ]
        }
        return try JSONDecoder().decode(ChatItemView.self, from: JSONSerialization.data(withJSONObject: payload))
    }

    static func proactiveCards(now: Date) throws -> [ChatItemView] {
        let fixtures: [(String, String, [String: Any])] = [
            ("ui-waiting-on-reply", "waiting_on_reply", [
                "headline": "Sam is waiting on your reply", "body": "A reply about the Q3 deck is ready.",
                "actionIds": ["open", "not_now"], "target": ["kind": "mail"],
            ]),
            ("ui-learned-to-confirm", "learned_to_confirm", [
                "headline": "A detail about the launch", "body": "The launch date is October 23.",
                "actionIds": ["keep", "correct"],
            ]),
            ("ui-meeting-prep", "meeting_prep", [
                "headline": "Prep for the launch review", "body": "Review the launch plan before the meeting.",
                "actionIds": ["open"], "event": ["title": "Launch review",
                    "start": "2026-10-16T10:00:00Z", "place": "Room 2", "private": false],
            ]),
        ]
        return try fixtures.enumerated().map { index, fixture in
            let (momentID, kind, fields) = fixture
            var proactive: [String: Any] = [
                "momentId": momentID, "kind": kind, "draft": false,
                "actionLabels": ["open": "Open", "not_now": "Not now", "keep": "Keep"],
                "target": ["kind": "today"], "evidence": [], "state": "pending",
            ]
            proactive.merge(fields) { _, new in new }
            let headline = fields["headline"] as? String ?? ""
            let payload: [String: Any] = [
                "seq": index + 1, "id": "ui-\(kind)", "role": "system", "kind": "proactive",
                "text": headline, "toolName": "main_chat_proactive", "at": stamp(now),
                "proactive": proactive,
            ]
            return try JSONDecoder().decode(ChatItemView.self,
                from: JSONSerialization.data(withJSONObject: payload))
        }
    }

    static func draftItem(now: Date) throws -> ChatItemView {
        let payload: [String: Any] = [
            "seq": 1, "id": "ui-draft-row", "role": "system", "kind": "proactive",
            "text": "Sam is waiting on your reply", "toolName": "main_chat_proactive", "at": stamp(now),
            "proactive": [
                "momentId": proactiveMomentID, "kind": "waiting_on_reply",
                "headline": "Sam is waiting on your reply", "body": "A reply about the Q3 deck is ready.",
                "draft": true, "actionIds": ["send", "not_now"],
                "actionLabels": ["send": "Send", "not_now": "Not now"],
                "draftContent": [
                    "to": "sam@example.com", "subject": "Q3 deck",
                    "text": "Thanks for the notes. Please send the final numbers by Thursday.",
                    "channel": "mail", "recipient": "Sam", "principalId": "person-sam", "revision": "rev-1",
                ],
                "target": ["kind": "mail"], "evidence": [], "state": "pending",
            ],
        ]
        return try JSONDecoder().decode(ChatItemView.self, from: JSONSerialization.data(withJSONObject: payload))
    }

    static let refusedMessage = "What should I prepare before the review?"

    /// `refused` adds a brief-card question the computer refused, already back in the box: "Not sent" and Try again.
    static func seed(_ cache: ChatCache, answered: Bool, refused: Bool = false, now: Date) throws {
        try cache.rememberMainChat(mainSummary(now: now))
        let items = transcript(answered: answered, now: now)
        let sentSeq = answered ? items.count - 1 : items.count
        try cache.save(ChatHeaderView(id: mainChatID, title: "Isoform", state: .idle), items: items)
        let message = ChatOutgoingMessage(id: "ui-seed-message", text: lastMessage, after: sentSeq - 1,
                                          sentAt: now.addingTimeInterval(-60), turnID: "ui-seed-turn",
                                          seq: sentSeq, state: answered ? .seen : .sent)
        var messages = [message]
        if refused {
            var question = ChatOutgoingMessage(id: "ui-seed-refused", text: refusedMessage, after: items.count,
                                               sentAt: now.addingTimeInterval(-30), turnID: nil, seq: nil,
                                               state: .sending)
            question.returnedToDraft = true
            question.failureDraftRecovered = true
            question.deliveryIssue = .failed
            messages.append(question)
            var composer = ChatComposerDraft()
            composer.text = refusedMessage
            composer.recoveredDraftIDs = ["\(mainChatID):ui-seed-refused"]
            try cache.saveComposer(composer, chatID: mainChatID)
        }
        try cache.saveOutgoing(messages, chatID: mainChatID)
    }

    static func mainSummary(now: Date) -> ChatSummaryView {
        ChatSummaryView(id: mainChatID, title: "Isoform", updatedAt: stamp(now), state: .idle, kind: .main)
    }

    static func transcript(answered: Bool, now: Date) -> [ChatItemView] {
        let exchanges = [
            ("What is on my calendar this morning?", "Two meetings: design review at 11 and planning with Priya at 2."),
            ("Move the planning one to tomorrow if Priya is free.", "Priya is free at 2 tomorrow. I moved it and told her."),
            ("Did the expense report go out?", "Yes, at 8:02 with three receipts attached."),
            ("Summarize the pricing call notes.", "Annual plans move to two tiers; the team plan keeps its price."),
            ("Who is waiting on me?", "Dana, since Tuesday, about the Q3 deck."),
            ("Remind me about Dana after lunch.", "I will remind you at 1:30."),
            ("What changed in the Wails release?", "Drag regions are read from the CSS property now, not the attribute."),
            ("Open the launch plan.", "It is open in Keynote on your computer."),
            ("Any new comments on the roadmap?", "Two, both from Sam, on the March milestones."),
            ("Reply that March still works.", "Sent to Sam in the roadmap thread."),
            ("How long was I in meetings yesterday?", "Three hours and twenty minutes."),
        ]
        var items: [ChatItemView] = []
        for (question, answer) in exchanges {
            items.append(item(items.count + 1, .user, question, now))
            items.append(item(items.count + 1, .assistant, answer, now))
        }
        items.append(item(items.count + 1, .user, lastMessage, now))
        if answered {
            items.append(item(items.count + 1, .assistant, lastReply, now))
        }
        return items
    }

    private static func item(_ seq: Int, _ role: ChatRole, _ text: String, _ now: Date) -> ChatItemView {
        ChatItemView(seq: seq, role: role, kind: .text, text: text, toolName: "",
                     at: stamp(now.addingTimeInterval(TimeInterval(seq * 60 - 3600))), permission: nil)
    }

    static let awayQuestions = ["Can you move my 3 PM with Dana to Friday?", "And tell Sam the deck is coming Monday."]
    static let awayAnswers = ["Moved it. Dana has Friday at 3 PM and accepted.", "Told Sam in the roadmap thread."]

    /// Chat while the computer is away (YANSU_UITEST_AWAY_CAUSE and YANSU_UITEST_AWAY_PHASE): two messages
    /// waiting under one thread line, the same a day later, or back with send times and the answers.
    static func seedAway(_ cache: ChatCache, cause: ChatAwayCause, phase: String, startedAt: Date? = nil, now: Date) throws {
        try cache.rememberMainChat(mainSummary(now: now))
        // "untimed" is a day-long stretch whose goodbye time is unknown: Chat must not say "since" for it.
        let untimed = phase.hasPrefix("untimed")
        let day = phase == "long" || untimed
        var items = transcript(answered: true, now: now.addingTimeInterval(day ? -27 * 3600 : -9 * 3600))
        let start = startedAt ?? (day ? now.addingTimeInterval(-26 * 3600) : now.addingTimeInterval(-8 * 3600))
        let ids = ["ui-away-1", "ui-away-2"]
        // "untimed-waiting" sent its messages minutes ago, so they still wait; "untimed" sent them a day ago, so they
        // were not sent (v1.0.4: the line drops its promise then).
        let typed = phase == "untimed-waiting"
            ? [now.addingTimeInterval(-600), now.addingTimeInterval(-540)]
            : [start.addingTimeInterval(120), start.addingTimeInterval(180)]
        var period = ChatAwayPeriod(id: "ui-away-period", cause: cause, startedAt: start, sinceKnown: !untimed,
                                    messageIDs: ids)
        var messages: [ChatOutgoingMessage] = []
        if phase == "back" {
            let wentOut = now.addingTimeInterval(-90)
            for (index, question) in awayQuestions.enumerated() {
                let seq = items.count + 1
                items.append(ChatItemView(seq: seq, role: .user, kind: .text, text: question, toolName: "",
                                          at: stamp(wentOut), permission: nil))
                items.append(ChatItemView(seq: seq + 1, role: .assistant, kind: .text, text: awayAnswers[index],
                                          toolName: "", at: stamp(wentOut.addingTimeInterval(30)), permission: nil,
                                          askedAt: stamp(wentOut)))
                period.sentAt[ids[index]] = wentOut
                period.seqs[ids[index]] = seq
                period.typedAt[ids[index]] = typed[index]
            }
            period.endedAt = wentOut
        } else {
            for (index, question) in awayQuestions.enumerated() {
                var message = ChatOutgoingMessage(id: ids[index], text: question, after: items.count, sentAt: typed[index],
                                                  turnID: nil, seq: nil, state: .sending)
                message.askUndispatched = true
                message.deliveryIssue = .waitingForComputer
                messages.append(message)
            }
        }
        try cache.save(ChatHeaderView(id: mainChatID, title: "Isoform", state: .idle), items: items)
        try cache.saveOutgoing(messages, chatID: mainChatID)
        try cache.saveAwayPeriods([mainChatID: [period]])
    }

    fileprivate static func stamp(_ date: Date) -> String {
        ISO8601DateFormatter().string(from: date)
    }
}

/// Answers the chat routes the way a reachable Mac does, for the "chat-live" states: the list with the chat
/// and one other, each chat's newest page, and an event long-poll that waits like the relay's. Each answer takes a
/// few hundred milliseconds, like a relay round trip. Presentation states supply their one cached row as `main`.
actor ChatUITestHub: ChatHubAPI, FirstReadHubAPI {
    /// A test or seed computer takes a send to its main chat (PROTOCOL.md 15.5a).
    func supportsMainChatSend() async -> Bool { true }
    static let otherChatID = "ui-other-chat"

    private let now: Date
    private var main: [ChatItemView]
    private let other: [ChatItemView]
    private var ideaReceipts: [String: ChatMessageAccepted] = [:]
    private var briefLive: ChatLiveAnswer?
    private var briefEvents: [MobileChatTurnEvent] = []
    private var cardAskAnswers: [String: SavedBriefAsk] = [:]
    private var cardByTurn: [String: String] = [:]
    private var refusedBriefAsk = false
    private var stoppedBriefTurns: Set<String> = []
    private var savedParagraphAsk: SavedBriefAsk?
    private var firstReadReceipts: [String: FirstReadAnswer] = [:]

    init(now: Date, main: [ChatItemView]? = nil) {
        self.now = now
        self.main = main ?? ChatUITestSeed.transcript(answered: true, now: now)
        other = [ChatItemView(seq: 1, role: .user, kind: .text, text: "Export last month's expenses.", toolName: "",
                              at: ChatUITestSeed.stamp(now), permission: nil),
                 ChatItemView(seq: 2, role: .assistant, kind: .text, text: "Exported to Expenses/September.csv.",
                              toolName: "", at: ChatUITestSeed.stamp(now), permission: nil)]
    }

    func chats(limit: Int) async throws -> [ChatSummaryView] {
        try await Task.sleep(nanoseconds: 300_000_000)
        return [ChatUITestSeed.mainSummary(now: now),
                ChatSummaryView(id: Self.otherChatID, title: "Expense export", updatedAt: ChatUITestSeed.stamp(now),
                                state: .idle, kind: .chat)]
    }

    func chat(id: String, after: Int?, limit: Int) async throws -> ChatDetail {
        try await Task.sleep(nanoseconds: 300_000_000)
        let isMain = id == ChatUITestSeed.mainChatID
        guard isMain || id == Self.otherChatID else {
            throw HubRequestError.rejected(status: 404, message: "That chat is not on your computer.")
        }
        let rows = isMain ? main : other
        let page = after.map { cursor in rows.filter { $0.seq > cursor } } ?? Array(rows.suffix(limit))
        return ChatDetail(chat: ChatHeaderView(id: id, title: isMain ? "Isoform" : "Expense export", state: .idle),
                          items: page, pendingPermission: nil, cursor: rows.last?.seq ?? 0, hasMore: false,
                          live: isMain ? briefLive : nil)
    }

    func answerFirstRead(momentID: String, action: String, text: String, idempotencyKey: String) async throws -> FirstReadAnswer {
        if let receipt = firstReadReceipts[idempotencyKey] { return receipt }
        guard momentID == "ui-first-read", let index = main.firstIndex(where: { $0.proactive?.momentID == momentID }) else {
            throw HubRequestError.invalidIdentifier
        }
        let result: FirstReadAnswer
        switch action {
        case "confirm_portrait":
            let at = Int64(Date().timeIntervalSince1970 * 1000)
            main[index].proactive?.state = "acted"
            main[index].proactive?.firstReadConfirmedAt = at
            result = FirstReadAnswer(state: "acted", confirmedAt: at)
        case "undo_portrait":
            main[index].proactive?.state = "pending"
            main[index].proactive?.firstReadConfirmedAt = nil
            result = FirstReadAnswer(state: "pending")
        case "correct_portrait":
            // A blank Not quite is accepted and saves nothing (P163 v1.0.1). The computer keeps the card and marks it corrected, as first_read.go does.
            switch ProcessInfo.processInfo.environment["YANSU_UITEST_FIRST_READ_ANSWER"] {
            case "held": throw HubRequestError.noAnswer
            case "failed": throw HubRequestError.rejected(status: 400, message: "Couldn't save your answer.")
            default: break
            }
            main[index].proactive?.state = "corrected"
            result = FirstReadAnswer(state: "corrected")
        default: throw HubRequestError.invalidIdentifier
        }
        firstReadReceipts[idempotencyKey] = result
        return result
    }

    func createChat(title: String, idempotencyKey: String) async throws -> String {
        throw HubRequestError.noAnswer
    }

    func sendMessage(chatID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted {
        throw HubRequestError.noAnswer
    }

    /// The Ideas fixture accepts the same source bindings its cards provide. Ordinary seeded chat sends
    /// keep their offline behavior; a source-bound send must exercise acceptance instead of the fallback refusal.
    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     idempotencyKey: String) async throws -> ChatMessageAccepted {
        guard !offerReferences.isEmpty else {
            return try await sendMessage(chatID: chatID, text: text, idempotencyKey: idempotencyKey)
        }
        guard chatID == ChatUITestSeed.mainChatID else {
            throw HubRequestError.rejected(status: 404, message: "That chat is not on your computer.")
        }
        let allowed = Set(IdeasUITestSeed.offers.compactMap(\.chatReference))
        guard offerReferences.count <= 16, Set(offerReferences).count == offerReferences.count,
              offerReferences.allSatisfy(allowed.contains) else {
            throw HubRequestError.conflict(message: "This seeded idea changed or is no longer available.")
        }
        if let accepted = ideaReceipts[idempotencyKey] { return accepted }
        let seq = (main.last?.seq ?? 0) + 1
        let accepted = ChatMessageAccepted(turnId: "ui-idea-turn-\(seq)", seq: seq)
        main.append(ChatItemView(seq: seq, role: .user, kind: .text, text: text, toolName: "",
                                 at: ChatUITestSeed.stamp(now), permission: nil))
        ideaReceipts[idempotencyKey] = accepted
        return accepted
    }

    func sendBriefMessage(chatID: String, text: String, reference: ChatBriefReference,
                          idempotencyKey: String) async throws -> ChatMessageAccepted {
        let brief = ProcessInfo.processInfo.environment["YANSU_UITEST_SETTINGS_STATE"] == "feed-your-week"
            ? FeedUITestSeed.yourWeekBrief(now: now) : FeedUITestSeed.conversationBrief(now: now)
        let card = reference.cardId.flatMap { id in brief.sendCards.first { $0.id == id } }
        guard chatID == ChatUITestSeed.mainChatID, reference.briefId == brief.briefId,
              reference.briefVersion == brief.conversationKey, reference.offerId == nil,
              reference.cardId == nil || card != nil && reference.cardMessage != nil else {
            throw HubRequestError.conflict(message: BriefConversationCopy.changed)
        }
        if reference.ask == true {
            if let index = reference.paragraph {
                guard brief.narrative.indices.contains(index), reference.paragraphText == brief.narrative[index].text else {
                    throw HubRequestError.conflict(message: BriefConversationCopy.changed)
                }
            } else if let topicID = reference.weekTopicId {
                guard brief.yourWeek?.ask?.topicId == topicID, brief.yourWeek?.ask?.message == reference.weekMessage else {
                    throw HubRequestError.conflict(message: BriefConversationCopy.changed)
                }
            } else if let card {
                guard reference.cardMessage == card.message else { throw HubRequestError.conflict(message: BriefConversationCopy.changed) }
            } else {
                throw HubRequestError.conflict(message: BriefConversationCopy.changed)
            }
            if ProcessInfo.processInfo.environment["YANSU_UITEST_ASK_OFFLINE"] == "1" { throw HubRequestError.noAnswer }
            if ProcessInfo.processInfo.environment["YANSU_UITEST_ASK_FAIL_ONCE"] == "1", !refusedBriefAsk {
                refusedBriefAsk = true
                throw HubRequestError.rejected(status: 400, message: "Not accepted")
            }
        }
        if let accepted = ideaReceipts[idempotencyKey] { return accepted }
        let seq = (main.last?.seq ?? 0) + 1
        let accepted = ChatMessageAccepted(turnId: "ui-brief-turn-\(seq)", seq: seq)
        main.append(ChatItemView(seq: seq, role: .user, kind: .text, text: text, toolName: "",
                                 at: ChatUITestSeed.stamp(now), permission: nil))
        ideaReceipts[idempotencyKey] = accepted
        if reference.ask == true {
            if let cardID = reference.cardId { cardByTurn[accepted.turnId] = cardID }
            else if let topicID = reference.weekTopicId { cardByTurn[accepted.turnId] = "week-topic:\(topicID)" }
            Task { await self.finishBriefAsk(accepted) }
        }
        return accepted
    }

    private func finishBriefAsk(_ accepted: ChatMessageAccepted) async {
        try? await Task.sleep(for: .milliseconds(600))
        briefLive = ChatLiveAnswer(turnId: accepted.turnId, text: "The project notes are ready")
        briefEvents.append(MobileChatTurnEvent(event: "chat_turn", chatId: ChatUITestSeed.mainChatID,
                                               turnId: accepted.turnId, state: "running", seq: 1, at: ""))
        if ProcessInfo.processInfo.environment["YANSU_UITEST_ASK_STREAM_FAIL"] == "1" {
            try? await Task.sleep(for: .seconds(1))
            let seq = (main.last?.seq ?? 0) + 1
            main.append(ChatItemView(seq: seq, role: .assistant, kind: .text, text: briefLive?.text ?? "",
                                     toolName: "", at: ChatUITestSeed.stamp(now), permission: nil))
            briefLive = nil
            briefEvents.append(MobileChatTurnEvent(event: "chat_turn", chatId: ChatUITestSeed.mainChatID,
                                                   turnId: accepted.turnId, state: "error", seq: 2, at: ""))
            return
        }
        try? await Task.sleep(for: .seconds(6))
        guard !stoppedBriefTurns.contains(accepted.turnId) else { return }
        briefLive = nil
        let seq = (main.last?.seq ?? 0) + 1
        main.append(ChatItemView(seq: seq, role: .assistant, kind: .text,
                                 text: "The project notes are ready for your review.", toolName: "",
                                 at: ChatUITestSeed.stamp(now), permission: nil))
        if let user = main.first(where: { $0.seq == accepted.seq }) {
            let saved = SavedBriefAsk(question: user.text, answer: "The project notes are ready for your review.")
            if let cardID = cardByTurn[accepted.turnId] { cardAskAnswers[cardID] = saved }
            else { savedParagraphAsk = saved }
        }
        briefEvents.append(MobileChatTurnEvent(event: "chat_turn", chatId: ChatUITestSeed.mainChatID,
                                               turnId: accepted.turnId, state: "done", seq: 2, at: ""))
    }

    /// "Talk about this brief" frames (YANSU_UITEST_BRIEF_TALK): a refusal (409) or a computer without the route
    /// (404, as an older Mac answers); otherwise the computer says the conversation is in the main chat.
    func briefConversation(briefID: String) async throws -> BriefConversation {
        switch ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_TALK"] {
        case "refused":
            throw HubRequestError.conflict(message: BriefConversationCopy.changed)
        case "too-old":
            throw HubRequestError.rejected(status: 404, message: "mobile route not found")
        default:
            return BriefConversation(briefId: briefID, briefVersion: "ui-conversation-version", chatId: ChatUITestSeed.mainChatID)
        }
    }

    func savedBriefAsk(reference: ChatBriefReference) async throws -> SavedBriefAsk? {
        guard reference.ask == true else { return nil }
        if let cardID = reference.cardId { return cardAskAnswers[cardID] }
        if let topicID = reference.weekTopicId { return cardAskAnswers["week-topic:\(topicID)"] }
        guard reference.paragraph == 0 else { return nil }
        return savedParagraphAsk
    }

    /// "Talk about this brief": the seeded computer says the brief's opener once per key, as the real route does.
    func openBrief(chatID: String, reference: ChatBriefReference, idempotencyKey: String) async throws {
        let brief = FeedUITestSeed.conversationBrief(now: now)
        guard chatID == ChatUITestSeed.mainChatID, reference.briefId == brief.briefId,
              reference.briefVersion == brief.conversationKey,
              let opener = brief.cards.first(where: { $0.kind == "opener" }) else {
            throw HubRequestError.conflict(message: BriefConversationCopy.changed)
        }
        guard ideaReceipts[idempotencyKey] == nil else { return }
        let seq = (main.last?.seq ?? 0) + 1
        main.append(ChatItemView(seq: seq, role: .assistant, kind: .text, text: opener.message, toolName: "",
                                 at: ChatUITestSeed.stamp(now), permission: nil))
        ideaReceipts[idempotencyKey] = ChatMessageAccepted(turnId: "ui-brief-opener-\(seq)", seq: seq)
    }

    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int,
                            idempotencyKey: String) async throws {
        let brief = FeedUITestSeed.conversationBrief(now: now)
        guard chatID == ChatUITestSeed.mainChatID, reference == brief.chatReference,
              brief.narrative.indices.contains(paragraph), paragraph != brief.closingParagraphIndex,
              let text = brief.narrative[paragraph].text?.trimmingCharacters(in: .whitespacesAndNewlines),
              !text.isEmpty else {
            throw HubRequestError.conflict(message: BriefConversationCopy.changed)
        }
        guard ideaReceipts[idempotencyKey] == nil else { return }
        let quoted = text.split(separator: "\n", omittingEmptySubsequences: false).map { "> \($0)" }.joined(separator: "\n")
        let seq = (main.last?.seq ?? 0) + 1
        main.append(ChatItemView(seq: seq, role: .assistant, kind: .text,
                                 text: "About this part of your brief:\n\n\(quoted)", toolName: "",
                                 at: ChatUITestSeed.stamp(now), permission: nil))
        ideaReceipts[idempotencyKey] = ChatMessageAccepted(turnId: "ui-brief-paragraph-\(seq)", seq: seq)
    }

    func answerPermission(chatID: String, requestID: String, optionID: String, idempotencyKey: String) async throws -> Bool {
        throw HubRequestError.noAnswer
    }

    func cancel(chatID: String, idempotencyKey: String) async throws -> Bool {
        false
    }

    func stopBriefAsk(chatID: String, turnID: String, idempotencyKey: String) async throws -> Bool {
        guard chatID == ChatUITestSeed.mainChatID, briefLive?.turnId == turnID else { return false }
        let partial = briefLive?.text ?? ""
        stoppedBriefTurns.insert(turnID)
        briefLive = nil
        let seq = (main.last?.seq ?? 0) + 1
        main.append(ChatItemView(seq: seq, role: .assistant, kind: .text, text: partial, toolName: "",
                                 at: ChatUITestSeed.stamp(now), permission: nil))
        return true
    }

    func pollChatTurns() async throws -> [MobileChatTurnEvent] {
        if ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_ASK"] == "1" {
            try await Task.sleep(for: .milliseconds(200))
            let events = briefEvents
            briefEvents = []
            return events
        }
        let wait = Self.showsPresence ? 1 : UInt64(HubChannel.pollWait)
        try await Task.sleep(nanoseconds: wait * 1_000_000_000)
        return []
    }

    /// With YANSU_UITEST_PRESENCE=1 the computer is writing a reply, said again on every poll.
    func latestPresence() async -> MobilePresence? {
        guard Self.showsPresence else { return nil }
        return MobilePresence(text: ["en": "Writing the reply to Priya…", "zh-CN": "正在写给 Priya 的回复…"], at: Date())
    }

    private static var showsPresence: Bool { ProcessInfo.processInfo.environment["YANSU_UITEST_PRESENCE"] == "1" }
}

/// Canned UI responses exercise the card's real owner check and write journal without a live Mac.
/// Transport and server authorization are covered separately; this is a presentation fixture.
struct ChatUITestProactiveHub: ProactiveActionHubAPI {
    var refusesAction: Bool

    func performProactiveAction(chatID: String, momentID: String, actionID: String,
                                idempotencyKey: String) async throws -> String {
        guard chatID == ChatUITestSeed.mainChatID,
              [ChatUITestSeed.proactiveMomentID, "ui-meeting-prep", "ui-waiting-on-reply", "ui-learned-to-confirm"].contains(momentID),
              ["do_once", "not_now", "send", "keep"].contains(actionID), !idempotencyKey.isEmpty else {
            throw HubRequestError.rejected(status: 400, message: "This action is no longer available.")
        }
        if refusesAction {
            throw HubRequestError.rejected(status: 400, message: "This action is no longer available.")
        }
        return actionID == "not_now" ? "deferred" : "acted"
    }
}
#endif
