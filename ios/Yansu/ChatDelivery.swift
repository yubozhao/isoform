import Foundation

/// How far a message sent from this iPhone has got. It only moves forward.
enum ChatDeliveryState: String, Codable, Equatable, Sendable {
    /// Saved on this iPhone and on its way to the Mac, including while the Mac is offline.
    case sending
    /// The Mac stored it: the hub accepted it with a 202.
    case sent
    /// The Mac took it as a turn and started answering: a `chat_turn` event for its turn, or a reply after it.
    case seen

    var label: String {
        switch self {
        case .sending: return String(localized: "Sending")
        case .sent: return String(localized: "Sent to your computer")
        case .seen: return String(localized: "Seen by your computer")
        }
    }

    fileprivate var rank: Int {
        switch self {
        case .sending: return 0
        case .sent: return 1
        case .seen: return 2
        }
    }

    func advanced(to other: ChatDeliveryState) -> ChatDeliveryState {
        other.rank > rank ? other : self
    }
}

/// Delivery evidence stays monotonic; a waiting or failed attempt is tracked separately from that evidence.
enum ChatDeliveryIssue: String, Codable, Equatable, Sendable {
    case waitingForComputer
    case waitingForTurn
    case failed
}

enum ChatDeliveryStatus: Equatable {
    case sending, waitingForComputer, waitingForTurn, sent, seen, failed

    var label: String {
        switch self {
        case .sending: return ChatDeliveryState.sending.label
        case .waitingForComputer: return String(localized: "Waits for your computer")
        case .waitingForTurn: return String(localized: "Waiting for your computer to finish")
        case .sent: return ChatDeliveryState.sent.label
        case .seen: return ChatDeliveryState.seen.label
        case .failed: return String(localized: "Not sent")
        }
    }

    var explanation: String? {
        switch self {
        case .waitingForComputer, .waitingForTurn:
            return String(localized: "This phone keeps trying automatically. You do not need to send this message again.")
        case .failed:
            return String(localized: "Your message could not be sent. Its text is saved on this phone so you can edit it and send it again.")
        default: return nil
        }
    }
}

/// A message sent from this iPhone, kept until the Mac has seen it. The newest one is kept after that too, so
/// its bubble can say Seen. A refused message stays here until its text is taken into the composer.
struct ChatOutgoingMessage: Codable, Equatable, Sendable, Identifiable {
    let id: String
    let text: String
    /// The newest seq on screen when it was sent; the Mac stores the message after it. Nil when the chat had
    /// not been read yet.
    let after: Int?
    let sentAt: Date
    var turnID: String?
    /// The seq of the item the Mac stored for it: from the 202, or, from a Mac that predates that field, from a
    /// read after the 202.
    var seq: Int?
    var state: ChatDeliveryState
    /// Optional so outgoing files from before refused drafts were persisted still decode.
    var returnedToDraft: Bool? = nil
    var offerReferences: [ChatOfferReference] = []
    var briefReference: ChatBriefReference? = nil
    /// Optional so previously saved messages keep decoding. A failure receipt remains visible after draft recovery.
    var deliveryIssue: ChatDeliveryIssue? = nil
    /// Keep returnedToDraft true on disk so an older app can never auto-send a retained failure receipt.
    var failureDraftRecovered: Bool? = nil
    /// A brief Ask queued while offline has not reached the transport and may still be undone.
    var askUndispatched: Bool? = nil
    /// The relay took it for a computer that did not answer (PROTOCOL.md section 23): the computer runs it when it
    /// is back, so it can no longer be taken back and shows as sent, without Undo. Optional so saved messages decode.
    var relayHolds: Bool? = nil

    var isReturnedDraft: Bool { returnedToDraft == true && failureDraftRecovered != true }
    var hasFailed: Bool { deliveryIssue == .failed }
    var isVisibleInTranscript: Bool { !isReturnedDraft || hasFailed }

    var deliveryFingerprint: String {
        let references = offerReferences.sorted { ($0.id, $0.version) < ($1.id, $1.version) }
        // A card's identity and shown words join only when there is a card, so older queued messages keep theirs.
        let brief = briefReference.map { reference in
            ["brief", reference.briefId, reference.briefVersion, reference.offerId ?? ""] +
                (reference.cardId.map { [$0, reference.cardMessage ?? ""] } ?? []) +
                (reference.ask == true ? ["ask", reference.paragraph.map(String.init) ?? "", reference.paragraphText ?? ""] : []) +
                (reference.weekTopicId.map { ["week-topic", $0, reference.weekMessage ?? ""] } ?? [])
        } ?? []
        return WriteFingerprint.make([text, id] + references.flatMap { [$0.id, $0.version] } + brief)
    }

    /// Still to be sent: the Mac has not answered it.
    var isQueued: Bool { !isReturnedDraft && !hasFailed && turnID == nil && state == .sending }

    /// "Will send when your computer is back" only while the computer is out of reach: the phone knows it is away,
    /// or the last try got no answer. A computer whose state is not known yet does not make a message wait for it,
    /// and a refusal is never a wait: it is "Not sent".
    func deliveryStatus(computerOnline: Bool?) -> ChatDeliveryStatus {
        if hasFailed { return .failed }
        switch state {
        case .seen: return .seen
        case .sent: return .sent
        case .sending:
            if relayHolds == true { return .sent }
            if computerOnline == false || deliveryIssue == .waitingForComputer { return .waitingForComputer }
            if deliveryIssue == .waitingForTurn { return .waitingForTurn }
            return .sending
        }
    }
}

extension ChatOutgoingMessage {
    /// The relay keeps a request for a sleeping computer for a day (PROTOCOL.md section 23).
    static let relayRetention: TimeInterval = 24 * 60 * 60

    /// Still waiting for the computer a day after it was sent: the relay has let it go.
    func expiredWhileWaiting(computerOnline: Bool?, now: Date) -> Bool {
        isQueued && deliveryStatus(computerOnline: computerOnline) == .waitingForComputer &&
            now.timeIntervalSince(sentAt) >= Self.relayRetention
    }
}

extension ChatOutgoingMessage {
    enum CodingKeys: String, CodingKey {
        case id, text, after, sentAt, turnID, seq, state, returnedToDraft, offerReferences, briefReference
        case deliveryIssue, failureDraftRecovered, askUndispatched
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        text = try values.decode(String.self, forKey: .text)
        after = try values.decodeIfPresent(Int.self, forKey: .after)
        sentAt = try values.decode(Date.self, forKey: .sentAt)
        turnID = try values.decodeIfPresent(String.self, forKey: .turnID)
        seq = try values.decodeIfPresent(Int.self, forKey: .seq)
        state = try values.decode(ChatDeliveryState.self, forKey: .state)
        returnedToDraft = try values.decodeIfPresent(Bool.self, forKey: .returnedToDraft)
        deliveryIssue = try values.decodeIfPresent(ChatDeliveryIssue.self, forKey: .deliveryIssue)
        failureDraftRecovered = try values.decodeIfPresent(Bool.self, forKey: .failureDraftRecovered)
        askUndispatched = try values.decodeIfPresent(Bool.self, forKey: .askUndispatched)
        briefReference = try values.decodeIfPresent(ChatBriefReference.self, forKey: .briefReference)
        if let references = try values.decodeIfPresent([ChatOfferReference].self, forKey: .offerReferences) {
            offerReferences = references
        } else if isReturnedDraft || (turnID == nil && state == .sending) {
            offerReferences = [.unverifiedLegacy]
            returnedToDraft = true
        }
    }
}

/// Ties messages sent from this iPhone to what the Mac reports (PROTOCOL.md section 15): the 202's turn id and
/// seq, `chat_turn` events, and the stored items of a read. Messages are kept in the order they were sent,
/// oldest first. Only the Mac's answer ties a message to a row: a row with the same text may have been typed on
/// the Mac.
enum ChatDelivery {
    /// The Mac stores the trimmed text and bounds the copy it sends at 4096 bytes, marking the cut.
    static func matches(_ item: ChatItemView, text: String) -> Bool {
        guard item.role == .user else { return false }
        if item.text == text { return true }
        let marker = " [truncated]"
        guard item.text.hasSuffix(marker) else { return false }
        let kept = item.text.dropLast(marker.count)
        return !kept.isEmpty && text.hasPrefix(kept)
    }

    /// True for a stored item that shows the Mac moved past a message: a reply, a tool step, an error or a later
    /// message. A system status line can land while the Mac has not started, so it does not count.
    static func showsAnswer(_ item: ChatItemView) -> Bool {
        item.role != .system || item.kind == .error
    }

    /// Moves states forward from what a read shows. A Mac that predates the 202's seq leaves a message to be
    /// tied by its text; `answeredBefore` names the messages whose 202 arrived before this read started, the only
    /// ones whose row the read must hold. Such a message whose row is older than the page is placed just before
    /// it, so it is not drawn under the page as new.
    static func reconcile(_ messages: [ChatOutgoingMessage], items: [ChatItemView], pendingPermission: ChatItemView?,
                          answeredBefore: Set<String> = []) -> [ChatOutgoingMessage] {
        var result = messages
        var claimed = Set(result.filter { !$0.isReturnedDraft && !$0.hasFailed }.compactMap(\.seq))
        let users = items.filter { $0.role == .user }.sorted { $0.seq < $1.seq }
        let firstOnPage = items.map(\.seq).min()
        for index in result.indices where !result[index].isReturnedDraft && !result[index].hasFailed
            && result[index].seq == nil && result[index].turnID != nil
            && answeredBefore.contains(result[index].id) {
            let message = result[index]
            let candidates = users.filter { !claimed.contains($0.seq) && matches($0, text: message.text) }
            let match = message.after.map { after in candidates.first { $0.seq > after } } ?? candidates.last
            if let match {
                result[index].seq = match.seq
                claimed.insert(match.seq)
            } else if let firstOnPage, firstOnPage > 1, firstOnPage - 1 > (message.after ?? 0) {
                result[index].seq = firstOnPage - 1
            }
        }
        let newestAnswer = items.filter(showsAnswer).map(\.seq).max() ?? 0
        let waitingSeq = pendingPermission?.seq ?? 0
        for index in result.indices {
            guard !result[index].isReturnedDraft, !result[index].hasFailed,
                  let seq = result[index].seq, max(newestAnswer, waitingSeq) > seq else { continue }
            result[index].state = .seen
        }
        return settled(result)
    }

    /// A row the oldest queued message may already be, if the Mac stored it but its answer was lost: same text,
    /// after what was on screen when it was sent, not tied to another message, and not ruled out already. Asking
    /// again under the message's key tells: the Mac answers with the stored 202, or busy when it is not this one.
    static func possibleRow(for message: ChatOutgoingMessage, in messages: [ChatOutgoingMessage],
                            items: [ChatItemView], ruledOut: Set<Int>) -> Int? {
        guard message.isQueued, let after = message.after else { return nil }
        let claimed = Set(messages.filter { !$0.isReturnedDraft && !$0.hasFailed }.compactMap(\.seq))
        return items.first { item in
            item.seq > after && !claimed.contains(item.seq) && !ruledOut.contains(item.seq)
                && matches(item, text: message.text)
        }?.seq
    }

    /// The messages drawn as their own bubbles under the rows on screen: those without a row yet, those still
    /// waiting to be seen, and those stored after the newest row on screen. A page of system statuses can pass
    /// a Sent message without showing the Mac has seen it. Keep that message visible even when its row is older
    /// than the page. Once Seen, a message older than the page is history; one on screen is drawn as that row.
    static func drawnBelowRows(_ messages: [ChatOutgoingMessage], rows: [ChatItemView]?) -> [ChatOutgoingMessage] {
        let outgoing = messages.filter(\.isVisibleInTranscript)
        guard let rows else { return outgoing }
        let onScreen = Set(rows.map(\.seq))
        let newest = rows.map(\.seq).max() ?? 0
        return outgoing.filter { message in
            guard let seq = message.seq else { return true }
            return !onScreen.contains(seq) && (message.state == .sent || seq > newest)
        }
    }

    /// A `chat_turn` event names the turn the Mac is running; the message that started it has been seen.
    static func markSeen(_ messages: [ChatOutgoingMessage], turnID: String) -> [ChatOutgoingMessage] {
        guard !turnID.isEmpty else { return messages }
        let updated = messages.map { message -> ChatOutgoingMessage in
            guard !message.isReturnedDraft, !message.hasFailed, message.turnID == turnID else { return message }
            var seen = message
            seen.state = .seen
            return seen
        }
        return settled(updated)
    }

    /// The Mac runs one turn at a time, so once it has seen a message it has seen every one stored before it.
    private static func settled(_ messages: [ChatOutgoingMessage]) -> [ChatOutgoingMessage] {
        guard let newestSeen = messages.lastIndex(where: { !$0.isReturnedDraft && !$0.hasFailed && $0.state == .seen }) else {
            return messages
        }
        var result = messages
        for index in result.indices where index < newestSeen && !result[index].isReturnedDraft && !result[index].hasFailed
            && result[index].state == .sent {
            result[index].state = .seen
        }
        return result
    }

    /// Keeps messages still on their way, the newest one, and any whose missing rows need their own bubble.
    /// A turn event can mark a message Seen before its row is read, so Seen alone must not remove that bubble.
    static func pruned(_ messages: [ChatOutgoingMessage], rows: [ChatItemView]?) -> [ChatOutgoingMessage] {
        let newest = messages.last { !$0.isReturnedDraft }?.id
        let drawn = Set(drawnBelowRows(messages, rows: rows).map(\.id))
        let failures = Set(messages.filter(\.hasFailed).suffix(50).map(\.id))
        return messages.filter { message in
            if message.hasFailed { return message.isReturnedDraft || failures.contains(message.id) }
            return message.isReturnedDraft || message.state != .seen || message.id == newest || drawn.contains(message.id)
        }
    }
}
