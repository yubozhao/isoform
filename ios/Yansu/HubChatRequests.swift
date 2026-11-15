import Foundation

/// The chat routes on the Mac. Every write carries an idempotency key from `PendingWriteJournal`, so a retry
/// after a lost answer is applied once.
protocol ChatHubAPI: Sendable {
    /// Whether the computer takes a send to its main chat before the phone lists one (PROTOCOL.md 15.5a).
    func supportsMainChatSend() async -> Bool
    /// The newest "what Isoform is doing" line the computer published (PROTOCOL.md 20.7e), if any.
    func latestPresence() async -> MobilePresence?
    func chats(limit: Int) async throws -> [ChatSummaryView]
    /// Items after `after` (exclusive) when given, otherwise the newest page.
    func chat(id: String, after: Int?, limit: Int) async throws -> ChatDetail
    func createChat(title: String, idempotencyKey: String) async throws -> String
    func sendMessage(chatID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted
    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     idempotencyKey: String) async throws -> ChatMessageAccepted
    /// The same, saying when a message waited for the computer (`queuedAt`, `undone`).
    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     waiting: ChatWaitingFields?, idempotencyKey: String) async throws -> ChatMessageAccepted
    func sendBriefMessage(chatID: String, text: String, reference: ChatBriefReference,
                          idempotencyKey: String) async throws -> ChatMessageAccepted
    func savedBriefAsk(reference: ChatBriefReference) async throws -> SavedBriefAsk?
    /// The cards of one brief that already have a saved answer, in one pass: the computer's support is asked once,
    /// and the pass stops at the first "the brief changed" (409), since every later card names the same revision.
    func answeredBriefCards(reference: ChatBriefReference, cards: [(id: String, message: String)]) async -> Set<String>
    /// The brief's conversation on the computer, asked by the brief's id when the phone has no reference of its own
    /// (POST /briefs/{id}/conversation). The computer makes the main chat if needed and makes no brief.
    func briefConversation(briefID: String) async throws -> BriefConversation
    /// The computer says the brief's opener first in the chat (POST /chats/{id}/brief-opener).
    func openBrief(chatID: String, reference: ChatBriefReference, idempotencyKey: String) async throws
    /// The computer quotes a paragraph from its delivered brief (POST /chats/{id}/brief-paragraph).
    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int,
                            idempotencyKey: String) async throws
    /// The same, after a 409, with the paragraph's words as this phone showed them: the computer quotes them when
    /// its brief, whose revision moved, still says them.
    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int, shownText: String,
                            idempotencyKey: String) async throws
    func answerPermission(chatID: String, requestID: String, optionID: String, idempotencyKey: String) async throws -> Bool
    func cancel(chatID: String, idempotencyKey: String) async throws -> Bool
    func stopBriefAsk(chatID: String, turnID: String, idempotencyKey: String) async throws -> Bool
    func pollChatTurns() async throws -> [MobileChatTurnEvent]
}

extension ChatHubAPI {
    /// Older computers and test doubles do not take a send before a main chat is listed.
    func supportsMainChatSend() async -> Bool { false }

    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     waiting: ChatWaitingFields?, idempotencyKey: String) async throws -> ChatMessageAccepted {
        try await sendMessage(chatID: chatID, text: text, offerReferences: offerReferences, idempotencyKey: idempotencyKey)
    }
}

/// Proactive actions use the same encrypted chat writes and saved retry keys as permission answers.
protocol ProactiveActionHubAPI: Sendable {
    func performProactiveAction(chatID: String, momentID: String, actionID: String,
                                idempotencyKey: String) async throws -> String
    func recordProactiveOpened(chatID: String, momentID: String, idempotencyKey: String) async throws
    func muteProactiveKind(chatID: String, momentID: String, idempotencyKey: String) async throws -> String
}

protocol FirstReadHubAPI: Sendable {
    func answerFirstRead(momentID: String, action: String, text: String, idempotencyKey: String) async throws -> FirstReadAnswer
}

struct FirstReadAnswer: Decodable, Equatable, Sendable {
    let state: String
    var confirmedAt: Int64? = nil
}

private struct FirstReadAnswerRequest: Encodable {
    let idempotencyKey: String
    let momentId: String
    let action: String
    let text: String
}

extension HubChannel: FirstReadHubAPI {
    func answerFirstRead(momentID: String, action: String, text: String, idempotencyKey: String) async throws -> FirstReadAnswer {
        let status = try await perform(.status).payload(HubStatus.self)
        guard status.firstRead == true else { throw HubRequestError.rejected(status: 404, message: "") }
        let request = FirstReadAnswerRequest(idempotencyKey: idempotencyKey, momentId: momentID, action: action, text: text)
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: "/first-read/answer", body: String(decoding: body, as: UTF8.self)))
            .payload(FirstReadAnswer.self)
    }
}

struct ProactiveActionRequest: Encodable, Equatable, Sendable {
    let idempotencyKey: String
    let momentId: String
    let actionId: String
}

struct ProactiveActionResult: Decodable, Equatable, Sendable {
    let state: String
}

private struct ProactiveReceiptRequest: Encodable {
    let idempotencyKey: String
    let state: String
}

private struct ProactiveReceiptResult: Decodable {
    let accepted: Bool
}

extension HubChannel: ProactiveActionHubAPI {
    func performProactiveAction(chatID: String, momentID: String, actionID: String,
                                idempotencyKey: String) async throws -> String {
        guard !momentID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              momentID.utf8.count <= 48 * 1024, Identifiers.isSafePathID(actionID) else {
            throw HubRequestError.invalidIdentifier
        }
        let path = try chatPath(chatID, "proactive/actions")
        let result = try await chatWrite(path, ProactiveActionRequest(idempotencyKey: idempotencyKey,
                                                                      momentId: momentID,
                                                                      actionId: actionID),
                                         as: ProactiveActionResult.self)
        guard ["opened", "acted", "dismissed", "deferred"].contains(result.state) else {
            throw HubRequestError.malformedResponse
        }
        return result.state
    }

    func recordProactiveOpened(chatID: String, momentID: String, idempotencyKey: String) async throws {
        guard Identifiers.isSafePathID(momentID) else {
            throw HubRequestError.invalidIdentifier
        }
        let path = try chatPath(chatID, "proactive/\(momentID)/receipts")
        let result = try await chatWrite(path, ProactiveReceiptRequest(idempotencyKey: idempotencyKey, state: "opened"),
                                         as: ProactiveReceiptResult.self)
        guard result.accepted else { throw HubRequestError.malformedResponse }
    }

    func muteProactiveKind(chatID: String, momentID: String, idempotencyKey: String) async throws -> String {
        guard Identifiers.isSafePathID(momentID) else {
            throw HubRequestError.invalidIdentifier
        }
        let path = try chatPath(chatID, "proactive/\(momentID)/actions")
        let result = try await chatWrite(path, ProactiveActionRequest(idempotencyKey: idempotencyKey,
                                                                      momentId: momentID,
                                                                      actionId: "mute_kind"),
                                         as: ProactiveActionResult.self)
        guard ["opened", "acted", "dismissed", "deferred"].contains(result.state) else {
            throw HubRequestError.malformedResponse
        }
        return result.state
    }
}

extension ProactiveActionHubAPI {
    func recordProactiveOpened(chatID: String, momentID: String, idempotencyKey: String) async throws {}

    func muteProactiveKind(chatID: String, momentID: String, idempotencyKey: String) async throws -> String {
        try await performProactiveAction(chatID: chatID, momentID: momentID, actionID: "mute_kind",
                                         idempotencyKey: idempotencyKey)
    }
}

extension ChatHubAPI {
    func savedBriefAsk(reference: ChatBriefReference) async throws -> SavedBriefAsk? { nil }
    /// A computer without the route answers 404, which the phone shows as it shows an unreachable computer.
    func briefConversation(briefID: String) async throws -> BriefConversation {
        throw HubRequestError.rejected(status: 404, message: "")
    }
    func stopBriefAsk(chatID: String, turnID: String, idempotencyKey: String) async throws -> Bool {
        throw HubRequestError.rejected(status: 404, message: "")
    }
    func sendBriefMessage(chatID: String, text: String, reference: ChatBriefReference,
                          idempotencyKey: String) async throws -> ChatMessageAccepted {
        // An older transport must never silently send a brief's text without its source.
        throw HubRequestError.conflict(message: BriefConversationCopy.changed)
    }

    func openBrief(chatID: String, reference: ChatBriefReference, idempotencyKey: String) async throws {
        throw HubRequestError.conflict(message: BriefConversationCopy.changed)
    }

    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int,
                            idempotencyKey: String) async throws {
        throw HubRequestError.conflict(message: BriefConversationCopy.changed)
    }

    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int, shownText: String,
                            idempotencyKey: String) async throws {
        throw HubRequestError.conflict(message: BriefConversationCopy.changed)
    }

    /// Older transports may send typed text, but must never drop an idea's source association.
    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     idempotencyKey: String) async throws -> ChatMessageAccepted {
        guard offerReferences.isEmpty else { throw HubRequestError.rejected(status: 409, message: "Source unavailable") }
        return try await sendMessage(chatID: chatID, text: text, idempotencyKey: idempotencyKey)
    }
}

/// The Mac answered a message with 409 "busy": a turn is already running in that chat. The message was not
/// accepted, so the phone keeps the draft and does not retry on its own.
enum ChatHubError: LocalizedError, Equatable {
    case busy

    var errorDescription: String? {
        switch self {
        case .busy:
            return "Isoform is still answering in this chat. Your message is still here. Send it when the reply finishes."
        }
    }
}

/// One pass over a brief's cards for saved answers. A 409 means the computer's brief changed since the phone read
/// it: every later card names the same revision, so the pass stops instead of asking again for each.
enum BriefAnsweredCards {
    static func collect(_ cards: [(id: String, message: String)],
                        ask: (_ id: String, _ message: String) async throws -> Bool) async -> Set<String> {
        var answered = Set<String>()
        for card in cards {
            do {
                if try await ask(card.id, card.message) { answered.insert(card.id) }
            } catch HubRequestError.conflict {
                break
            } catch {}
        }
        return answered
    }
}

extension ChatHubAPI {
    func answeredBriefCards(reference: ChatBriefReference, cards: [(id: String, message: String)]) async -> Set<String> {
        await BriefAnsweredCards.collect(cards) { id, message in
            var cardReference = reference
            cardReference.cardId = id
            cardReference.cardMessage = message
            return try await savedBriefAsk(reference: cardReference) != nil
        }
    }
}

extension HubChannel: ChatHubAPI {
    /// One /status for the whole pass, then one ask-answer per card (Nov 14: two requests per card, five cards at
    /// a time, every one answered 409).
    func answeredBriefCards(reference: ChatBriefReference, cards: [(id: String, message: String)]) async -> Set<String> {
        guard !cards.isEmpty, (try? await status().briefAsk) == true else { return [] }
        return await BriefAnsweredCards.collect(cards) { id, message in
            let route = HubRoute.briefCardAskAnswer(id: reference.briefId, version: reference.briefVersion,
                                                    cardID: id, message: message)
            return try await perform(route).payload(SavedBriefAskPayload.self).answer != nil
        }
    }

    func supportsMainChatSend() async -> Bool {
        (try? await status().chatMainSend) == true
    }

    func savedBriefAsk(reference: ChatBriefReference) async throws -> SavedBriefAsk? {
        guard try await status().briefAsk == true else { return nil }
        let route: HubRoute
        if let paragraph = reference.paragraph, let text = reference.paragraphText {
            route = .briefAskAnswer(id: reference.briefId, version: reference.briefVersion,
                                    paragraph: paragraph, text: text)
        } else if let cardID = reference.cardId, let message = reference.cardMessage {
            route = .briefCardAskAnswer(id: reference.briefId, version: reference.briefVersion,
                                        cardID: cardID, message: message)
        } else if let topicID = reference.weekTopicId, let message = reference.weekMessage {
            route = .briefWeekAskAnswer(id: reference.briefId, version: reference.briefVersion,
                                        topicID: topicID, message: message)
        } else { return nil }
        return try await perform(route).payload(SavedBriefAskPayload.self).answer
    }
    func briefConversation(briefID: String) async throws -> BriefConversation {
        guard Identifiers.isSafeBriefID(briefID) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.write(path: "/briefs/\(briefID)/conversation", body: "{}")).payload(BriefConversation.self)
    }
    func chats(limit: Int) async throws -> [ChatSummaryView] {
        try await perform(.chats(limit: limit)).payload(ChatListPayload.self).chats
    }

    func chat(id: String, after: Int?, limit: Int) async throws -> ChatDetail {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.chat(id: id, after: after, limit: limit)).payload(ChatDetail.self)
    }

    func createChat(title: String, idempotencyKey: String) async throws -> String {
        try await chatWrite("/chats", CreateChatRequest(idempotencyKey: idempotencyKey, title: title),
                            as: CreatedRecordPayload.self).id
    }

    func sendMessage(chatID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted {
        try await sendMessage(chatID: chatID, text: text, offerReferences: [], idempotencyKey: idempotencyKey)
    }

    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     idempotencyKey: String) async throws -> ChatMessageAccepted {
        try await sendMessage(chatID: chatID, text: text, offerReferences: offerReferences, waiting: nil,
                              idempotencyKey: idempotencyKey)
    }

    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     waiting: ChatWaitingFields?, idempotencyKey: String) async throws -> ChatMessageAccepted {
        let path = try chatPath(chatID, offerReferences.isEmpty ? "messages" : "offer-messages")
        let undone = waiting.map(\.undone).flatMap { $0 > 0 ? $0 : nil }
        return try await chatWrite(path, SendChatMessageRequest(idempotencyKey: idempotencyKey, text: text,
                                                               offerReferences: offerReferences,
                                                               queuedAt: waiting?.queuedAt.map(RelayTime.format),
                                                               undone: undone),
                                   as: ChatMessageAccepted.self)
    }

    func sendBriefMessage(chatID: String, text: String, reference: ChatBriefReference,
                          idempotencyKey: String) async throws -> ChatMessageAccepted {
        guard Identifiers.isSafePathID(reference.briefId), !reference.briefVersion.isEmpty else {
            throw HubRequestError.invalidIdentifier
        }
        if reference.ask == true, try await status().briefAsk != true {
            throw HubRequestError.rejected(status: 404, message: "")
        }
        return try await chatWrite(chatPath(chatID, "brief-messages"),
                                   SendBriefMessageRequest(idempotencyKey: idempotencyKey, text: text,
                                                           briefId: reference.briefId,
                                                           briefVersion: reference.briefVersion,
                                                           offerId: reference.offerId,
                                                           cardId: reference.cardId,
                                                           cardMessage: reference.cardMessage,
                                                           ask: reference.ask, paragraph: reference.paragraph,
                                                           paragraphText: reference.paragraphText,
                                                           weekTopicId: reference.weekTopicId, weekMessage: reference.weekMessage), as: ChatMessageAccepted.self)
    }

    func openBrief(chatID: String, reference: ChatBriefReference, idempotencyKey: String) async throws {
        guard Identifiers.isSafePathID(reference.briefId), !reference.briefVersion.isEmpty else {
            throw HubRequestError.invalidIdentifier
        }
        _ = try await chatWrite(chatPath(chatID, "brief-opener"),
                                BriefOpenerRequest(idempotencyKey: idempotencyKey, briefId: reference.briefId,
                                                   briefVersion: reference.briefVersion), as: BriefOpened.self)
    }

    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int,
                            idempotencyKey: String) async throws {
        guard Identifiers.isSafePathID(reference.briefId), !reference.briefVersion.isEmpty, paragraph >= 0 else {
            throw HubRequestError.invalidIdentifier
        }
        _ = try await chatWrite(chatPath(chatID, "brief-paragraph"),
                                BriefParagraphRequest(idempotencyKey: idempotencyKey, briefId: reference.briefId,
                                                      briefVersion: reference.briefVersion, paragraph: paragraph),
                                as: BriefOpened.self)
    }

    func openBriefParagraph(chatID: String, reference: ChatBriefReference, paragraph: Int, shownText: String,
                            idempotencyKey: String) async throws {
        guard Identifiers.isSafePathID(reference.briefId), !reference.briefVersion.isEmpty, paragraph >= 0,
              !shownText.isEmpty else {
            throw HubRequestError.invalidIdentifier
        }
        _ = try await chatWrite(chatPath(chatID, "brief-paragraph"),
                                BriefParagraphRequest(idempotencyKey: idempotencyKey, briefId: reference.briefId,
                                                      briefVersion: reference.briefVersion, paragraph: paragraph,
                                                      text: shownText),
                                as: BriefOpened.self)
    }

    func answerPermission(chatID: String, requestID: String, optionID: String, idempotencyKey: String) async throws -> Bool {
        guard Identifiers.isSafePathID(requestID) else { throw HubRequestError.invalidIdentifier }
        let path = try chatPath(chatID, "permissions/\(requestID)")
        let answer = try await chatWrite(path, AnswerChatPermissionRequest(idempotencyKey: idempotencyKey,
                                                                           optionId: optionID),
                                         as: ChatPermissionAnswered.self)
        return answer.answered
    }

    func cancel(chatID: String, idempotencyKey: String) async throws -> Bool {
        let path = try chatPath(chatID, "cancel")
        let result = try await chatWrite(path, CancelChatRequest(idempotencyKey: idempotencyKey),
                                         as: ChatCancelled.self)
        return result.cancelled
    }

    func stopBriefAsk(chatID: String, turnID: String, idempotencyKey: String) async throws -> Bool {
        guard Identifiers.isSafePathID(turnID), try await status().briefAsk == true else {
            throw HubRequestError.rejected(status: 404, message: "")
        }
        let result = try await chatWrite(chatPath(chatID, "cancel"),
                                         CancelChatRequest(idempotencyKey: idempotencyKey, turnId: turnID),
                                         as: ChatCancelled.self)
        return result.cancelled
    }

    private func chatPath(_ id: String, _ suffix: String) throws -> String {
        guard Identifiers.isSafePathID(id) else { throw HubRequestError.invalidIdentifier }
        return "/chats/\(id)/\(suffix)"
    }

    /// Sends a chat write. A 409 whose body says "busy" becomes `ChatHubError.busy`; any other answer goes
    /// through the usual envelope rules (409 becomes a conflict, which the caller treats as changed).
    private func chatWrite<Body: Encodable, Payload: Decodable>(_ path: String, _ body: Body,
                                                                as type: Payload.Type) async throws -> Payload {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        guard let text = String(data: try encoder.encode(body), encoding: .utf8) else {
            throw HubRequestError.malformedResponse
        }
        let response = try await perform(.write(path: path, body: text))
        if response.status == 409,
           let error = try? JSONDecoder().decode(ChatErrorBody.self, from: Data(response.body.utf8)),
           error.error == "busy" {
            throw ChatHubError.busy
        }
        return try response.payload(type)
    }
}

/// The computer's answer to "Talk about this brief" when the phone asked by the brief's id: the revision the
/// conversation belongs to and the main chat it lives in. It carries no brief words.
struct BriefConversation: Decodable, Equatable, Sendable {
    let briefId: String
    let briefVersion: String
    let chatId: String
}

/// The computer's answer to "Talk about this brief": its opener is in the chat.
struct BriefOpened: Decodable, Equatable, Sendable {
    let posted: Bool
}

extension ChatHubAPI {
    func latestPresence() async -> MobilePresence? { nil }
}
