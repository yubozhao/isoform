import Foundation

enum ChatLoadState: Equatable {
    case idle
    case loading
    case loaded
    case failed(String)
}

struct OpenChat: Equatable {
    var header: ChatHeaderView
    /// Stored rows, oldest first. Each one is covered by the cursor.
    var items: [ChatItemView]
    /// The Mac's pending permission prompt for this chat. It is transient: every read replaces it.
    var pendingPermission: ChatItemView?
    /// The Mac's cursor after the last read. Nil for a cached transcript, so the next read starts at the newest page.
    var cursor: Int?
    var asOf: Date?
    var live: ChatLiveAnswer? = nil

    /// The newest seq on screen, used to keep the bottom of the transcript in view.
    var newestSeq: Int {
        max(items.last?.seq ?? 0, pendingPermission?.seq ?? 0)
    }
}

/// Text for a composer to take: a message the Mac refused or an unsent hand-off from Goals.
struct ChatReturnedDraft: Equatable, Identifiable {
    let id: String
    let chatID: String
    let text: String
    var offerReferences: [ChatOfferReference] = []
    var briefReference: ChatBriefReference? = nil
}

private struct TranscriptRead {
    var header: ChatHeaderView?
    var items: [ChatItemView] = []
    var pendingPermission: ChatItemView?
    var cursor: Int
    var live: ChatLiveAnswer? = nil
}

@MainActor
final class ChatStore: ObservableObject {
    @Published private(set) var chats: [ChatSummaryView] = []
    @Published private(set) var listState: ChatLoadState = .idle
    @Published private(set) var listRefreshFailed = false
    @Published private(set) var openChatID: String?
    @Published private(set) var transcript: OpenChat?
    @Published private(set) var selectedProactive: ChatProactiveView?
    @Published private(set) var selectedProactiveChatID: String?
    @Published private(set) var transcriptState: ChatLoadState = .idle
    /// The chat ID the phone sends to before the computer lists a main chat: the message route's main-chat alias (PROTOCOL.md 15.5a).
    static let mainChatAlias = "main"
    @Published private(set) var transcriptRefreshFailed = false
    @Published private(set) var hubOnline: Bool?
    /// The relay no longer accepts this iPhone's pairing, for example after it was removed on the Mac. Chat shows
    /// Pair again instead of the cached chat. It is kept across launches until an authenticated read succeeds
    /// or the iPhone pairs again; a presence answer never clears it.
    @Published private(set) var repairNeeded = false
    @Published private(set) var busyTargets: Set<String> = []
    @Published private(set) var answeredRequestIDs: Set<String> = []
    /// Messages sent from this iPhone, by chat: the ones still on their way to being seen, and the newest one.
    @Published private(set) var outgoing: [String: [ChatOutgoingMessage]] = [:]
    /// Refused messages and hand-off prefills, oldest first, kept on disk until the composer takes them.
    @Published private(set) var returnedDrafts: [ChatReturnedDraft] = []
    @Published private var composers: [String: ChatComposerDraft] = [:]
    @Published var notice: String?
    /// The stretches messages waited for the computer, by chat, oldest first.
    @Published private(set) var awayPeriods: [String: [ChatAwayPeriod]] = [:]
    /// Why the computer is away now, from the connection check and the relay goodbye.
    @Published private(set) var awayCause: ChatAwayCause?
    private var awaySince: Date?
    /// Waiting messages taken back since the last one went out, by chat. A count only.
    private var undoneSinceSent: [String: Int] = [:]

    static let pageLimit = 50
    static let awayPeriodsKept = 20
    static let listLimit = 20
    static let maxMessageBytes = 16 * 1024
    static let maxCatchUpPages = 10
    static var eventRetryBaseNanoseconds: UInt64 = 5_000_000_000
    static let eventPresenceInterval: TimeInterval = 15
    static let heardTurnLimit = 20
    /// At most one flush of waiting messages per this many seconds when the phone hears from the computer.
    static let heardFlushInterval: TimeInterval = 5

    private let hub: ChatHubAPI
    /// What Isoform is working on right now, from the computer; shown under Chat's title while it is fresh.
    @Published private(set) var activity: MobilePresence?
    private let journal: PendingWriteJournal
    private let writer: JournaledWriter
    private let guardian: DecisionGuarding
    private let presence: @Sendable () async throws -> Bool
    private let isPaired: @MainActor () -> Bool
    private let isRemote: @MainActor () -> Bool
    private let cache: ChatCache
    private let readTimeout: Duration
    private let now: @Sendable () -> Date
    private let makeMessageID: @Sendable () -> String
    /// Turn ids from recent `chat_turn` events, by chat, newest last. An event can arrive before the 202 that
    /// names its turn.
    @Published private(set) var lastTopicTurn: MobileChatTurnEvent?
    @Published private(set) var recentTurnEvents: [MobileChatTurnEvent] = []
    private var heardTurnIDs: [String: [String]] = [:]
    private var lastHeardFlush: Date?
    /// Rows a queued message turned out not to be (the Mac answered busy when asked under its key).
    private var ruledOutRows: [String: Set<Int>] = [:]
    /// Chats whose outgoing file exists but could not be read; it is not overwritten until it can be.
    private var outgoingUnreadable: Set<String> = []
    /// Counts reads of the open chat, so a 202 can be compared with the read that follows it.
    private var readsStarted = 0
    /// For each answered message, how many reads had started when its 202 arrived.
    private var answeredAtRead: [String: Int] = [:]
    /// Prefills received before this iPhone knew its chat, in hand-off order.
    private var pendingMainChatDrafts: [ChatOutgoingMessage] = []
    private var eventListeners: [UUID: CheckedContinuation<Void, Never>] = [:]
    private var eventPump: Task<Void, Never>?
    private var eventRetryWait: Task<Void, Never>?
    /// Set when a screen starts listening, so the pump polls at once instead of finishing a retry backoff.
    private var eventRetrySkipped = false
    private var openRefreshRunning = false
    private var openRefreshQueued = false
    private var presenceRead: (id: UUID, task: Task<Void, Never>)?
    private var lastPresenceCheckAt: Date?
    private var reachabilityRevision = UUID()
    private var openGeneration = UUID()
    // In-flight work from a cleared pairing must not publish into its replacement.
    private var pairingGeneration = UUID()

    init(hub: ChatHubAPI,
         journal: PendingWriteJournal,
         guardian: DecisionGuarding,
         presence: @escaping @Sendable () async throws -> Bool,
         isPaired: @escaping @MainActor () -> Bool,
         cache: ChatCache,
         readTimeout: Duration = .seconds(20),
         isRemote: @escaping @MainActor () -> Bool = { false },
         now: @escaping @Sendable () -> Date = { Date() },
         makeKey: @escaping @Sendable () -> String = { UUID().uuidString.lowercased() },
         makeMessageID: @escaping @Sendable () -> String = { UUID().uuidString.lowercased() }) {
        self.hub = hub
        self.journal = journal
        self.guardian = guardian
        self.presence = presence
        self.isPaired = isPaired
        self.isRemote = isRemote
        self.cache = cache
        self.readTimeout = readTimeout
        self.now = now
        self.makeMessageID = makeMessageID
        self.writer = JournaledWriter(journal: journal, makeKey: makeKey, now: now)
        pendingMainChatDrafts = cache.loadMainDrafts() ?? []
        repairNeeded = cache.refusalRemembered
        awayPeriods = cache.loadAwayPeriods() ?? [:]
        for chatID in cache.outgoingChatIDs(knownChatIDs: journal.initialChatMessageTargets) {
            _ = loadedOutgoing(chatID)
        }
    }

    func isBusy(_ target: String) -> Bool {
        busyTargets.contains(target)
    }

    func dismissNotice() {
        notice = nil
    }

    func openProactiveItem(_ item: ChatProactiveView, chatID: String) {
        guard Identifiers.isSafePathID(chatID) else { return }
        selectedProactive = item
        selectedProactiveChatID = chatID
    }

    func closeProactiveItem() {
        selectedProactive = nil
        selectedProactiveChatID = nil
    }

    func isAnswered(_ requestID: String) -> Bool {
        answeredRequestIDs.contains(requestID)
    }

    /// Whether any message from this phone is still waiting to be sent, read from the saved queue of every chat the
    /// phone knows, not only the chats already opened (#668).
    var hasUnsentMessages: Bool {
        let ids = Set(chats.map(\.id) + [openChatID].compactMap { $0 })
        return ids.contains { !loadedOutgoing($0).isEmpty }
    }

    func outgoingMessages(in chatID: String) -> [ChatOutgoingMessage] {
        (outgoing[chatID] ?? []).filter(\.isVisibleInTranscript)
    }

    func queuedMessageCount(in chatID: String) -> Int {
        (outgoing[chatID] ?? []).filter(\.isQueued).count
    }

    func draft(in chatID: String) -> String {
        composerDraft(in: chatID).text
    }

    /// Keep the target's string API while retaining the source envelope through every edit.
    func setDraft(_ text: String, in chatID: String) {
        _ = editComposerDraft(text, in: chatID)
    }

    /// A hand-off is durable even before this phone discovers its chat. It never sends a turn.
    @discardableResult
    func prefillMainChat(_ text: String, offerReferences: [ChatOfferReference] = [],
                         briefReference: ChatBriefReference? = nil) -> Bool {
        // An empty hand-off still carries a brief: the user's answer after Isoform spoke first.
        guard isPaired(), !text.isEmpty || briefReference != nil else { return false }
        guard let saved = cache.loadMainDrafts() else {
            notice = ChatCopy.draftSaveFailed
            return false
        }
        let draft = ChatOutgoingMessage(id: makeMessageID(), text: text, after: nil, sentAt: now(),
                                        state: .sending, returnedToDraft: true, offerReferences: offerReferences,
                                        briefReference: briefReference)
        do {
            try cache.saveMainDrafts(saved + [draft])
            pendingMainChatDrafts = saved + [draft]
        } catch {
            notice = ChatCopy.draftSaveFailed
            return false
        }
        deliverMainChatDrafts()
        return true
    }

    private func deliverMainChatDrafts() {
        guard isPaired(), let saved = cache.loadMainDrafts(),
              let chatID = chats.first(where: { $0.kind == .main })?.id ?? cache.loadMainChatSummary()?.id,
              Identifiers.isSafePathID(chatID) else { return }
        pendingMainChatDrafts = saved
        while let draft = pendingMainChatDrafts.first {
            guard let composer = cache.loadComposer(chatID: chatID) else { return }
            if !wasRecovered(draft.id, from: chatID, in: composer) {
                let current = loadedOutgoing(chatID)
                guard !outgoingUnreadable.contains(chatID) else { return }
                let updated = current.contains(where: { $0.id == draft.id }) ? current : current + [draft]
                do {
                    // Persist even when a prior attempt left this id in memory. Only disk can acknowledge
                    // the transfer before its durable pending handoff is removed.
                    try cache.saveOutgoing(updated, chatID: chatID)
                    outgoing[chatID] = updated
                    updateReturnedDrafts(chatID)
                } catch {
                    notice = ChatCopy.draftSaveFailed
                    return
                }
            }
            let remaining = Array(pendingMainChatDrafts.dropFirst())
            guard (try? cache.saveMainDrafts(remaining)) != nil else {
                notice = ChatCopy.draftSaveFailed
                return
            }
            pendingMainChatDrafts = remaining
        }
    }

    func composerDraft(in chatID: String) -> ChatComposerDraft {
        guard isPaired(), Identifiers.isSafePathID(chatID) else { return ChatComposerDraft() }
        return composers[chatID] ?? cache.loadComposer(chatID: chatID) ?? ChatComposerDraft()
    }

    /// Editing, including deleting every character and then Undo, never detaches source provenance.
    @discardableResult
    func editComposerDraft(_ text: String, in chatID: String) -> Bool {
        guard isPaired(), Identifiers.isSafePathID(chatID) else { return false }
        guard let saved = cache.loadComposer(chatID: chatID) else {
            // Keep the visible edit, but unreadable storage cannot prove that it has no source.
            var draft = composers[chatID] ?? ChatComposerDraft(offerReferences: [.unverifiedLegacy])
            draft.text = text
            composers[chatID] = draft
            notice = ChatCopy.draftSaveFailed
            return false
        }
        var draft = composers[chatID] ?? saved
        draft.text = text
        composers[chatID] = draft
        return saveComposer(draft, chatID: chatID)
    }

    /// The UI clears its undo stack and recreates the text field when this explicit discard succeeds.
    @discardableResult
    func discardComposer(in chatID: String) -> Bool {
        guard let draft = readableComposer(chatID) else { return false }
        return saveComposer(ChatComposerDraft(recoveredDraftIDs: draft.recoveredDraftIDs), chatID: chatID)
    }

    private func readableComposer(_ chatID: String) -> ChatComposerDraft? {
        guard isPaired(), Identifiers.isSafePathID(chatID) else { return nil }
        guard let saved = cache.loadComposer(chatID: chatID) else {
            notice = ChatCopy.draftSaveFailed
            return nil
        }
        return composers[chatID] ?? saved
    }

    @discardableResult
    private func saveComposer(_ draft: ChatComposerDraft, chatID: String) -> Bool {
        guard isPaired(), Identifiers.isSafePathID(chatID) else { return false }
        do {
            try cache.saveComposer(draft, chatID: chatID)
            composers[chatID] = draft
            return true
        } catch {
            notice = ChatCopy.draftSaveFailed
            return false
        }
    }

    /// Compatibility callers can take text, but its dependencies are first transferred into the durable
    /// composer. A later String-only send still receives those dependencies from this store.
    @discardableResult
    func takeReturnedDraft(_ draft: ChatReturnedDraft) -> Bool {
        guard isPaired(), Identifiers.isSafePathID(draft.chatID) else { return false }
        guard loadedOutgoing(draft.chatID).contains(where: { $0.id == draft.id && $0.isReturnedDraft }),
              var composer = readableComposer(draft.chatID) else { return false }
        guard composer.accepts(brief: draft.briefReference, offers: draft.offerReferences) else {
            notice = BriefConversationCopy.finishDraft
            return false
        }
        if !wasRecovered(draft.id, from: draft.chatID, in: composer) {
            composer.text = [composer.text, draft.text].filter { !$0.isEmpty }.joined(separator: "\n")
            composer.retain(draft.offerReferences)
            composer.briefReference = composer.briefReference ?? draft.briefReference
            composer.recoveredDraftIDs.insert("\(draft.chatID):\(draft.id)")
        }
        return transferReturnedDrafts([draft], from: draft.chatID, to: draft.chatID, composer: composer)
    }

    /// Saves text, references and the authoritative same-chat queue atomically. A retired side chat first
    /// persists its refused state, so interruption of cross-chat cleanup cannot retry the copied message.
    func takeReturnedDrafts(in chatID: String, appendingTo text: String) -> String {
        guard var composer = readableComposer(chatID) else { return text }
        composer.text = text
        // An empty box holds nothing of the user's, such as the answer box after Isoform spoke first: the brief
        // it names gives way to the next tap instead of asking the user to discard nothing.
        if text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, composer.offerReferences.isEmpty {
            composer.briefReference = nil
        }
        for source in [chatID] + chatsWithDraftsForMainChat(composer: chatID) {
            var drafts: [ChatReturnedDraft] = []
            var candidate = composer
            for draft in returnedDrafts where draft.chatID == source {
                guard candidate.accepts(brief: draft.briefReference, offers: draft.offerReferences) else {
                    notice = BriefConversationCopy.finishDraft
                    continue
                }
                candidate.retain(draft.offerReferences)
                candidate.briefReference = candidate.briefReference ?? draft.briefReference
                drafts.append(draft)
            }
            guard !drafts.isEmpty else { continue }
            var updated = composer
            for draft in drafts where !wasRecovered(draft.id, from: source, in: updated) {
                let recovered = source == chatID ? draft.text : ChatCopy.recoveredFromChat(chatTitle(source)) + "\n" + draft.text
                updated.text = [updated.text, recovered].filter { !$0.isEmpty }.joined(separator: "\n")
                updated.retain(draft.offerReferences)
                updated.briefReference = updated.briefReference ?? draft.briefReference
                updated.recoveredDraftIDs.insert("\(source):\(draft.id)")
            }
            guard transferReturnedDrafts(drafts, from: source, to: chatID, composer: updated) else { return composer.text }
            composer = updated
        }
        return composer.text
    }

    /// The companion target scoped interrupted-transfer markers by source chat; the first Ideas build used
    /// globally generated message ids alone. Both persisted formats acknowledge the same recovered draft.
    private func wasRecovered(_ id: String, from source: String, in composer: ChatComposerDraft) -> Bool {
        composer.recoveredDraftIDs.contains(id) || composer.recoveredDraftIDs.contains("\(source):\(id)")
    }

    /// Other chats holding returned text, in the order it came back, when `composer` is the chat.
    private func chatsWithDraftsForMainChat(composer chatID: String) -> [String] {
        guard chatID == chats.first(where: { $0.kind == .main })?.id ?? cache.loadMainChatSummary()?.id else { return [] }
        var sources: [String] = []
        for draft in returnedDrafts where draft.chatID != chatID && !sources.contains(draft.chatID) {
            sources.append(draft.chatID)
        }
        return sources
    }

    private func chatTitle(_ chatID: String) -> String {
        let title = chats.first(where: { $0.id == chatID })?.title ?? cache.load(chatID: chatID)?.title ?? ""
        let name = title.split(whereSeparator: \.isWhitespace).joined(separator: " ")
        return name.isEmpty ? chatID : name
    }

    /// Keys of messages still waiting to go. Pruning keeps them past the journal's usual day: the Mac may already
    /// have taken a message whose answer was lost, and only its first key gets that answer back instead of a second
    /// turn. A chat whose waiting messages cannot be read now keeps its keys too.
    func queuedMessageKeys() async -> Set<String> {
        let entries = await journal.entries()
        return Set(entries.filter { write in
            guard write.operation == .chatMessage else { return false }
            guard let messages = cache.loadOutgoing(chatID: write.target) else { return true }
            return messages.contains { message in
                message.isQueued && (write.fingerprint == message.deliveryFingerprint
                    || (message.offerReferences.isEmpty && message.briefReference == nil && write.fingerprint == WriteFingerprint.make([message.text])))
            }
        }.map(\.key))
    }

    private func transferReturnedDrafts(_ drafts: [ChatReturnedDraft], from source: String, to destination: String,
                                        composer: ChatComposerDraft) -> Bool {
        let current = loadedOutgoing(source)
        let ids = Set(drafts.map(\.id))
        guard !outgoingUnreadable.contains(source),
              ids.isSubset(of: Set(current.filter(\.isReturnedDraft).map(\.id))) else { return false }
        let remaining = current.compactMap { message -> ChatOutgoingMessage? in
            guard ids.contains(message.id) else { return message }
            guard message.hasFailed else { return nil }
            var receipt = message
            receipt.failureDraftRecovered = true
            return receipt
        }
        do {
            if source == destination {
                // Disk may still call these messages queued when persisting their refusal failed.
                // The store's authoritative queue and composer must replace that state in one write.
                try cache.saveSubmission(remaining, chatID: destination, composer: composer)
                outgoing[source] = remaining
                composers[destination] = composer
                updateReturnedDrafts(source)
                return true
            }
            // Cross-file transfer first makes the source non-sendable. If later cleanup is interrupted,
            // recovery ids prevent copying it again, and its durable refused state prevents an auto-retry.
            try cache.saveOutgoing(current, chatID: source)
            let destinationMessages = loadedOutgoing(destination)
            guard !outgoingUnreadable.contains(destination) else { return false }
            try cache.saveSubmission(destinationMessages, chatID: destination, composer: composer)
            composers[destination] = composer
            do {
                try cache.saveOutgoing(remaining, chatID: source)
                outgoing[source] = remaining
                updateReturnedDrafts(source)
            } catch {
                notice = ChatCopy.draftSaveFailed
            }
            return true
        } catch {
            notice = ChatCopy.draftSaveFailed
            return false
        }
    }

    private func updateReturnedDrafts(_ chatID: String) {
        // An Ask keeps its own retry surface and must not populate the Chat composer.
        let drafts = (outgoing[chatID] ?? []).filter { $0.isReturnedDraft && $0.briefReference?.ask != true }
            .map { ChatReturnedDraft(id: $0.id, chatID: chatID, text: $0.text, offerReferences: $0.offerReferences,
                                     briefReference: $0.briefReference) }
        let updated = returnedDrafts.filter { $0.chatID != chatID } + drafts
        if returnedDrafts != updated { returnedDrafts = updated }
    }

    func refreshList() async {
        await refreshList(waitingForPresence: true)
    }

    /// The event watcher passes false: it starts the presence check but polls again without waiting for it.
    private func refreshList(waitingForPresence: Bool) async {
        guard isPaired() else { return }
        let presenceUpdate = checkPresence()
        await readList()
        if waitingForPresence { await presenceUpdate.value }
    }

    private func readList() async {
        let generation = pairingGeneration
        let started = FirstLoadTrace.now
        if chats.isEmpty { listState = .loading }
        do {
            let hub = self.hub
            let refreshed = try await boundedRead { try await hub.chats(limit: Self.listLimit) }
            guard isCurrent(generation) else { return }
            pairingAccepted()
            chats = refreshed
            if let main = refreshed.first(where: { $0.kind == .main }) {
                try? cache.rememberMainChat(main)
            }
            deliverMainChatDrafts()
            // A chat that is not open has no reads of its own; the list says when its turn is over.
            for summary in refreshed where summary.state == .idle && summary.id != openChatID {
                resumeOutgoing(chatID: summary.id, chatFree: true)
            }
            listState = .loaded
            listRefreshFailed = false
            FirstLoadTrace.record("chat.list.ready", since: started)
        } catch is CancellationError {
            return
        } catch {
            guard isCurrent(generation) else { return }
            noteRepairNeeded(by: error)
            if JournaledWriter.disposition(for: error) == .keep { invalidatePresence() }
            if chats.isEmpty && listState != .loaded {
                listState = .failed(Self.message(for: error))
            } else {
                listRefreshFailed = true
            }
        }
    }

    func createChat(title: String) async -> String? {
        guard isPaired() else { return nil }
        let generation = pairingGeneration
        let name = Self.boundedTitle(title)
        let target = "new-chat"
        guard claim(target) else { return nil }
        defer { release(target, generation: generation) }
        let fingerprint = WriteFingerprint.make([name])
        let id: String? = await submit(.chatCreate, target: target, choice: "create", fingerprint: fingerprint,
                                       conflictNotice: ChatCopy.changed) { key in
            try await self.hub.createChat(title: name, idempotencyKey: key)
        }
        guard isCurrent(generation), let id else { return nil }
        await refreshList()
        return isCurrent(generation) ? id : nil
    }

    /// Selects the last known chat before any network work, including its cached rows.
    /// A missing transcript leaves the loading state visible rather than implying an empty chat. A pairing the
    /// relay refused, in this launch or an earlier one, gets Pair again instead of the cached chat.
    @discardableResult
    func prepareMainChat() -> String? {
        guard isPaired(), !repairNeeded else { return nil }
        let started = FirstLoadTrace.now
        let main: ChatSummaryView
        if let listed = chats.first(where: { $0.kind == .main }) {
            main = listed
        } else if var cached = cache.loadMainChatSummary() {
            // Only a read from the Mac can say the chat is working or waiting.
            cached.state = .unknown
            main = cached
        } else {
            return nil
        }
        if chats.isEmpty { chats = [main] }
        deliverMainChatDrafts()
        select(chatID: main.id)
        FirstLoadTrace.record("chat.main.selected", since: started)
        return main.id
    }

    private func select(chatID: String) {
        guard isPaired(), Identifiers.isSafePathID(chatID) else { return }
        if openChatID != chatID {
            openGeneration = UUID()
            openRefreshRunning = false
            openRefreshQueued = false
            openChatID = chatID
            transcript = nil
            transcriptState = .loading
            transcriptRefreshFailed = false
            _ = loadedOutgoing(chatID)
            showCachedTranscript(chatID: chatID)
        }
    }

    func open(chatID: String) async {
        guard isPaired(), Identifiers.isSafePathID(chatID) else { return }
        if chatID == Self.mainChatAlias {
            // The main chat is not listed yet: there is nothing to read, and the composer is ready (PROTOCOL.md 15.5a).
            select(chatID: chatID)
            transcript = nil
            transcriptState = .loaded
            return
        }
        select(chatID: chatID)
        let presenceUpdate = checkPresence()
        await refreshOpen()
        await presenceUpdate.value
    }

    /// Card decisions revise existing rows without advancing their seq. Start from the newest page while
    /// keeping the visible transcript if the Mac cannot answer. An older in-flight read cannot undo the revision.
    func refreshAfterProactiveAction(chatID: String) async {
        guard isPaired(), openChatID == chatID else { return }
        openGeneration = UUID()
        openRefreshRunning = false
        openRefreshQueued = false
        transcript?.cursor = nil
        await refreshOpen()
    }

    /// Re-reads the open chat. Reads that overlap are coalesced: a call during a read sets one more pass, so
    /// two reads never move the cursor backwards. The caller that queued a pass has already returned, so a pass
    /// still queued when the reading task is cancelled runs in a task of its own instead of being dropped.
    func refreshOpen() async {
        if openRefreshRunning {
            openRefreshQueued = true
            return
        }
        let generation = pairingGeneration
        let selection = openGeneration
        openRefreshRunning = true
        repeat {
            openRefreshQueued = false
            await readOpenOnce(selection: selection)
        } while openRefreshQueued && selection == openGeneration && isCurrent(generation)
        // A new pairing or another chat reset the flags already, and their own reads replace this one.
        guard generation == pairingGeneration, selection == openGeneration else { return }
        openRefreshRunning = false
        if openRefreshQueued, isPaired() {
            Task { await self.refreshOpen() }
        }
    }

    /// Listens for chat turn events while a Chat screen is shown. One poll serves every screen: a screen that
    /// goes away stops listening at once, and the poll it was waiting on keeps running for the next screen, so
    /// returning to Chat listens immediately instead of waiting for an abandoned long-poll to end.
    func watchForEvents() async {
        guard isPaired() else { return }
        let listener = UUID()
        await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                guard !Task.isCancelled else { return continuation.resume() }
                eventListeners[listener] = continuation
                if eventPump == nil {
                    eventPump = Task { await self.pumpEvents() }
                } else {
                    // A screen that just appeared polls now rather than after a backoff of up to 30 s.
                    eventRetrySkipped = true
                    eventRetryWait?.cancel()
                }
            }
        } onCancel: {
            Task { @MainActor in self.stopListening(listener) }
        }
    }

    private func stopListening(_ listener: UUID) {
        eventListeners.removeValue(forKey: listener)?.resume()
    }

    /// Polls while any screen listens. Events from a poll that ends after the last screen left are still read
    /// into the open chat: the channel has already handed them over, and no other poll will see them.
    private func pumpEvents() async {
        defer {
            eventPump = nil
            let listeners = eventListeners.values
            eventListeners.removeAll()
            listeners.forEach { $0.resume() }
        }
        var failures = 0
        while !eventListeners.isEmpty && isPaired() {
            let generation = pairingGeneration
            eventRetrySkipped = false
            do {
                let events = try await hub.pollChatTurns()
                guard isCurrent(generation) else { continue }
                pairingAccepted()
                activity = await hub.latestPresence()
                // An empty relay poll says nothing about the computer. Check its presence periodically
                // while a screen listens, without adding a timer or blocking the next event poll.
                if !eventListeners.isEmpty,
                   now().timeIntervalSince(lastPresenceCheckAt ?? .distantPast) >= Self.eventPresenceInterval {
                    _ = checkPresence()
                }
                if failures > 0 {
                    failures = 0
                    async let refresh: Void = refreshOpen()
                    await refreshList(waitingForPresence: false)
                    await refresh
                }
                guard isCurrent(generation) else { continue }
                if !events.isEmpty {
                    await receiveTurnEvents(events)
                }
            } catch is CancellationError {
                // A pairing transition cancels the channel's poll; the next pairing is listened to again.
                guard isPaired() else { return }
                failures += 1
                await waitBeforeEventRetry(failures: failures)
            } catch HubRequestError.notPaired {
                return
            } catch {
                if isCurrent(generation) {
                    noteRepairNeeded(by: error)
                    invalidatePresence()
                }
                failures += 1
                await waitBeforeEventRetry(failures: failures)
            }
        }
    }

    private func waitBeforeEventRetry(failures: Int) async {
        // One immediate recovery attempt on first connect; repeated failures keep the bounded backoff.
        guard failures > 1 else {
            FirstLoadTrace.record("chat.events.retry.immediate")
            return
        }
        guard !eventRetrySkipped else { return }
        FirstLoadTrace.record("chat.events.retry.backoff")
        let delay = Self.eventRetryBaseNanoseconds * UInt64(min(failures - 1, 6))
        let wait = Task<Void, Never> { try? await Task.sleep(nanoseconds: delay) }
        eventRetryWait = wait
        await wait.value
        eventRetryWait = nil
    }

    func receiveTurnEvents(_ events: [MobileChatTurnEvent]) async {
        let generation = pairingGeneration
        for event in events {
            lastTopicTurn = event
            if !event.chatId.isEmpty && !event.turnId.isEmpty {
                recentTurnEvents.removeAll { $0.chatId == event.chatId && $0.turnId == event.turnId }
                recentTurnEvents.append(event)
                if recentTurnEvents.count > Self.heardTurnLimit {
                    recentTurnEvents.removeFirst(recentTurnEvents.count - Self.heardTurnLimit)
                }
            }
            noteTurn(event)
        }
        let openChanged = events.contains { $0.chatId == openChatID }
        async let list: Void = refreshList(waitingForPresence: false)
        if isCurrent(generation), openChanged {
            await refreshOpen()
        }
        await list
    }

    /// Puts the message on screen as Sending and queues it behind any earlier message of the chat that is still
    /// waiting. True once it is saved; false before that, so the composer keeps it. Every refusal after it is
    /// saved comes back through the durable `returnedDrafts`, including a refusal of the first attempt.
    @discardableResult
    func send(_ text: String, offerReferences: [ChatOfferReference] = []) async -> Bool {
        let clearsComposer = openChatID.map { draft(in: $0) == text } ?? false
        return await send(text, offerReferences: offerReferences, clearingComposer: clearsComposer)
    }

    @discardableResult
    func sendComposer(in chatID: String, onEnqueued: () -> Void = {}) async -> Bool {
        guard chatID == openChatID, let draft = readableComposer(chatID) else { return false }
        return await send(draft.text, offerReferences: draft.offerReferences, clearingComposer: true, onEnqueued: onEnqueued)
    }

    private func send(_ text: String, offerReferences: [ChatOfferReference], clearingComposer: Bool,
                      directBriefReference: ChatBriefReference? = nil,
                      messageID: String? = nil,
                      queueOffline: Bool = false,
                      onEnqueued: () -> Void = {}) async -> Bool {
        guard isPaired(), let chatID = openChatID else { return false }
        if chatID == Self.mainChatAlias, !(await hub.supportsMainChatSend()) {
            // An older computer cannot take the message; the text stays in the composer (PROTOCOL.md 15.5a).
            notice = ChatCopy.updateToSendFromPhone
            return false
        }
        let offlineAsk = queueOffline && directBriefReference?.ask == true
        // A message typed while the phone knows the computer is away joins the away period at once. It is still
        // tried, since presence can be stale, and waits in the queue if the computer does not answer.
        let waitsAway = !offlineAsk && directBriefReference == nil && hubOnline == false
        let undispatched = offlineAsk
        let generation = pairingGeneration
        let target = "send:\(chatID)"
        if offlineAsk && !claim(target) { return false }
        defer { if offlineAsk { release(target, generation: generation) } }
        let message = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !message.isEmpty else { return false }
        guard message.utf8.count <= Self.maxMessageBytes else {
            notice = ChatCopy.tooLong
            return false
        }
        guard var composer = readableComposer(chatID) else { return false }
        let savedComposer = composer
        if let directBriefReference { composer = ChatComposerDraft(briefReference: directBriefReference) }
        guard composer.accepts(brief: nil, offers: offerReferences) else {
            notice = BriefConversationCopy.finishDraft
            return false
        }
        composer.retain(offerReferences)
        guard !composer.offerReferences.contains(.unverifiedLegacy) else {
            notice = ChatProvenanceCopy.legacyDraft
            return false
        }
        guard composer.offerReferences.count <= 16 else {
            notice = ChatProvenanceCopy.tooManyIdeas
            return false
        }
        let id = messageID ?? makeMessageID()
        let baseline = transcript.flatMap { $0.header.id == chatID ? ($0.items.last?.seq ?? 0) : nil }
        var queued = ChatOutgoingMessage(id: id, text: message, after: baseline, sentAt: now(), state: .sending,
                                         offerReferences: composer.offerReferences, briefReference: composer.briefReference)
        if undispatched {
            queued.askUndispatched = true
            queued.deliveryIssue = .waitingForComputer
        }
        var reservation: PendingWrite?
        if undispatched {
            do {
                reservation = try await journal.reserve(operation: .chatMessage, target: chatID, choice: "message",
                                                        fingerprint: queued.deliveryFingerprint,
                                                        makeKey: writer.makeKey, createdAt: now())
            } catch {
                notice = ChatCopy.saveFailed
                return false
            }
            guard isCurrent(generation), openChatID == chatID else {
                if let reservation { await journal.remove(keys: [reservation.key]) }
                return false
            }
        }
        if clearingComposer {
            let messages = loadedOutgoing(chatID)
            guard !outgoingUnreadable.contains(chatID) else { return false }
            let fresh = ChatComposerDraft(recoveredDraftIDs: savedComposer.recoveredDraftIDs)
            do {
                try cache.saveSubmission(messages + [queued], chatID: chatID, composer: fresh)
                outgoing[chatID] = messages + [queued]
                composers[chatID] = fresh
                // The UI resets Undo and its field identity before any delivery suspension.
                onEnqueued()
                updateReturnedDrafts(chatID)
            } catch {
                if let reservation { await journal.remove(keys: [reservation.key]) }
                notice = ChatCopy.saveFailed
                return false
            }
        } else if !updateOutgoing(chatID, { $0 + [queued] }) {
            // Not on disk, so a kill now would lose it: nothing goes, and the composer keeps the text.
            if let memory = outgoing[chatID] { outgoing[chatID] = memory.filter { $0.id != id } }
            if let reservation { await journal.remove(keys: [reservation.key]) }
            notice = ChatCopy.saveFailed
            return false
        }
        if !clearingComposer { onEnqueued() }
        if waitsAway { joinAwayPeriod(chatID: chatID, messageIDs: [id]) }
        if undispatched {
            if hubOnline == true {
                Task { await self.pumpOutgoing(chatID: chatID, chatFree: self.openChatIsFree(chatID)) }
            }
            return true
        }
        await pumpOutgoing(chatID: chatID, chatFree: openChatIsFree(chatID), caller: id)
        return true
    }

    /// Enqueue a brief Ask independently of the main-chat composer. The existing journal,
    /// receipt and event pump own delivery and updates after the sheet closes.
    func sendBriefAsk(_ text: String, reference: ChatBriefReference, queueOffline: Bool = false,
                      onEnqueued: @escaping (String, String) -> Void) async -> Bool {
        if prepareMainChat() == nil { await refreshList() }
        guard let chatID = prepareMainChat() else { notice = ChatCopy.noMainChatDetail; return false }
        let id = makeMessageID()
        return await send(text, offerReferences: [], clearingComposer: false, directBriefReference: reference,
                          messageID: id, queueOffline: queueOffline || hubOnline == false,
                          onEnqueued: { onEnqueued(chatID, id) })
    }

    func refreshBriefAskPresence() async {
        let presence = checkPresence()
        await presence.value
    }

    func savedBriefAsk(reference: ChatBriefReference) async -> SavedBriefAsk? {
        guard isPaired() else { return nil }
        let generation = pairingGeneration
        let answer = try? await hub.savedBriefAsk(reference: reference)
        return isCurrent(generation) ? answer : nil
    }

    /// Reopening a card while its Ask is queued or answering must not send it twice.
    func existingBriefAsk(reference: ChatBriefReference, question: String) -> (chatID: String, messageID: String)? {
        guard let chatID = prepareMainChat(),
              let message = loadedOutgoing(chatID).last(where: {
                  $0.briefReference == reference && $0.text == question && !$0.hasFailed
              }) else { return nil }
        return (chatID, message.id)
    }

    /// Post the computer's opener without changing a draft. Reopening retries an unanswered receipt with its key.
    func postBriefOpener(reference: ChatBriefReference) async {
        if prepareMainChat() == nil { await refreshList() }
        guard let chatID = prepareMainChat() else { return }
        let generation = pairingGeneration
        let fingerprint = WriteFingerprint.make([reference.briefId, reference.briefVersion])
        let result = await writer.send(.briefOpener, target: chatID, choice: "open", fingerprint: fingerprint) { key in
            try await self.hub.openBrief(chatID: chatID, reference: reference, idempotencyKey: key)
        }
        guard isCurrent(generation) else { return }
        switch result {
        case .sent:
            notice = nil
            await refreshOpen()
        case .failed(let error, _): notice = Self.message(for: error)
        case .notSaved: notice = ChatCopy.saveFailed
        case .cancelled: break
        }
    }

    /// An offline Ask is undoable only before the first dispatch was durably marked.
    func undoBriefAsk(chatID: String, messageID: String) async -> Bool {
        let generation = pairingGeneration
        let target = "send:\(chatID)"
        guard isPaired(), claim(target) else { return false }
        defer { release(target, generation: generation) }
        guard !outgoingUnreadable.contains(chatID),
              let message = loadedOutgoing(chatID).first(where: { $0.id == messageID }),
              message.askUndispatched == true, message.isQueued,
              let write = await journal.existing(operation: .chatMessage, target: chatID,
                                                 fingerprint: message.deliveryFingerprint),
              isCurrent(generation), !outgoingUnreadable.contains(chatID) else { return false }
        let remaining = loadedOutgoing(chatID).filter { $0.id != messageID }
        do { try cache.saveOutgoing(remaining, chatID: chatID) } catch { notice = ChatCopy.saveFailed; return false }
        outgoing[chatID] = remaining
        updateReturnedDrafts(chatID)
        await journal.remove(keys: [write.key])
        return isCurrent(generation)
    }

    /// A brief card's tap, as on the computer (backlog item 58): the card's words go in the chat's box and
    /// nothing is sent. The user edits and sends; the computer reads the card's sources again then.
    @discardableResult
    func prefillBriefCard(_ card: MobileBriefCard, from brief: MobileBriefV1) -> Bool {
        guard var reference = brief.chatReference, brief.sendCards.contains(card) else {
            notice = BriefConversationCopy.unavailable
            return false
        }
        reference.cardId = card.id
        reference.cardMessage = card.message
        return prefillMainChat(card.message, briefReference: reference)
    }

    /// The brief's conversation reference for "Talk about this brief": the phone's own when it drew one. A brief
    /// without one is asked of the computer by its id (POST /briefs/{id}/conversation); the computer makes the main
    /// chat if needed and no brief. The notice is what to show when there is no reference; nil with no reference
    /// means the answer is no longer current.
    func briefConversationReference(for brief: MobileBriefV1) async -> (reference: ChatBriefReference?, notice: String?) {
        if let reference = brief.chatReference { return (reference, nil) }
        guard isPaired() else { return (nil, BriefConversationCopy.unavailable) }
        let generation = pairingGeneration
        do {
            let answer = try await hub.briefConversation(briefID: brief.identifier)
            guard isCurrent(generation) else { return (nil, nil) }
            guard Identifiers.isSafePathID(answer.briefId), answer.briefId == brief.identifier,
                  !answer.briefVersion.isEmpty else { return (nil, BriefConversationCopy.unavailable) }
            return (ChatBriefReference(briefId: answer.briefId, briefVersion: answer.briefVersion), nil)
        } catch {
            guard isCurrent(generation) else { return (nil, nil) }
            // The computer answered when it refused: a gate refusal (409), a brief it does not hold (404 "brief not
            // found"), or a route an older computer lacks (any other 404). No answer at all says it is away.
            switch error as? HubRequestError {
            case .conflict?:
                return (nil, BriefConversationCopy.gateRefused)
            case .rejected(let status, let message)? where status == 404:
                return (nil, message == "brief not found" ? BriefConversationCopy.gateRefused : BriefConversationCopy.computerTooOld)
            default:
                return (nil, BriefConversationCopy.unavailable)
            }
        }
    }

    /// Whether the relay answered the presence check for "Talk about this brief", bounded. An answer that says the
    /// computer is asleep counts: the relay keeps the question for it (PROTOCOL.md section 23). No answer within the
    /// bound, a failed check, or a status still unknown counts as unreachable, so Talk never hangs.
    func talkAnswered(timeout: Duration = .seconds(3)) async -> Bool {
        let presence = checkPresence()
        let answered = await withTaskGroup(of: Bool.self) { group -> Bool in
            group.addTask { await presence.value; return true }
            group.addTask { try? await Task.sleep(for: timeout); return false }
            let first = await group.next() ?? false
            group.cancelAll()
            return first
        }
        return answered && hubOnline != nil
    }

    /// The reference "Talk about this brief" opens with. A brief that already has a reference keeps it and makes
    /// no route call, but the computer must be reachable first; without one, the computer is asked by the brief's id.
    func resolveTalkReference(for brief: MobileBriefV1,
                              timeout: Duration = .seconds(3)) async -> (reference: ChatBriefReference?, notice: String?) {
        guard let reference = brief.chatReference else { return await briefConversationReference(for: brief) }
        // A computer known to be asleep takes the question later (PROTOCOL.md section 23): no wait here.
        if hubOnline == false { return (reference, nil) }
        guard await talkAnswered(timeout: timeout) else { return (nil, BriefConversationCopy.unavailable) }
        return (reference, nil)
    }

    /// "Talk about this brief", as on the computer: Isoform says the brief's opener first in the chat, and
    /// the box waits for the user's answer with the brief attached. Nothing the phone wrote is sent.
    @discardableResult
    func openBriefConversation(_ brief: MobileBriefV1) async -> Bool {
        let generation = pairingGeneration
        let resolved = await briefConversationReference(for: brief)
        guard isCurrent(generation) else { return false }
        guard let reference = resolved.reference else {
            notice = resolved.notice
            return false
        }
        if prepareMainChat() == nil { await refreshList() }
        guard isCurrent(generation) else { return false }
        guard let chatID = prepareMainChat() else {
            notice = ChatCopy.noMainChatDetail
            return false
        }
        do {
            try await hub.openBrief(chatID: chatID, reference: reference, idempotencyKey: UUID().uuidString)
        } catch {
            guard isCurrent(generation) else { return false }
            // Only an unanswered request means the computer is away; a refusal names what to do.
            switch error as? HubRequestError {
            case .conflict(let message): notice = message
            case .noAnswer, .queueFull, nil: notice = BriefConversationCopy.offline
            default: notice = BriefConversationCopy.unavailable
            }
            return false
        }
        guard isCurrent(generation), prefillMainChat("", briefReference: reference) else { return false }
        await refreshOpen()
        return true
    }

    /// Opens Chat with an empty answer box after the computer quotes a paragraph from its delivered brief.
    @discardableResult
    func askAboutBriefParagraph(_ brief: MobileBriefV1, paragraph: Int) async -> Bool {
        let generation = pairingGeneration
        guard let reference = brief.chatReference else {
            notice = BriefConversationCopy.unavailable
            return false
        }
        if prepareMainChat() == nil { await refreshList() }
        guard isCurrent(generation) else { return false }
        guard let chatID = prepareMainChat() else {
            notice = ChatCopy.noMainChatDetail
            return false
        }
        do {
            do {
                try await hub.openBriefParagraph(chatID: chatID, reference: reference, paragraph: paragraph,
                                                 idempotencyKey: UUID().uuidString)
            } catch HubRequestError.conflict(let message) {
                // The brief moved on since this phone drew it. Ask once more with the words the phone showed;
                // the computer quotes them only if its brief still says them.
                let refused = HubRequestError.conflict(message: message)
                guard isCurrent(generation),
                      let shown = brief.narrative.indices.contains(paragraph)
                        ? brief.narrative[paragraph].text?.trimmingCharacters(in: .whitespacesAndNewlines) : nil,
                      !shown.isEmpty else { throw refused }
                do {
                    try await hub.openBriefParagraph(chatID: chatID, reference: reference, paragraph: paragraph,
                                                     shownText: shown, idempotencyKey: UUID().uuidString)
                } catch HubRequestError.noAnswer {
                    throw HubRequestError.noAnswer
                } catch {
                    // An older computer does not know `text` and refuses the request itself; the first answer
                    // still says why.
                    throw refused
                }
            }
        } catch {
            guard isCurrent(generation) else { return false }
            switch error as? HubRequestError {
            case .conflict(let message): notice = message
            case .noAnswer, .queueFull, nil: notice = BriefConversationCopy.offline
            default: notice = BriefConversationCopy.unavailable
            }
            return false
        }
        guard isCurrent(generation), prefillMainChat("", briefReference: reference) else { return false }
        await refreshOpen()
        return true
    }

    /// The phone just heard from the computer (any answer or event through the relay). Messages that were waiting
    /// for it go now instead of at the next read. Messages held behind a running turn wait for the reads that say
    /// the turn ended, so an answer to a busy retry never starts another retry.
    func heardFromComputer() {
        guard isPaired() else { return }
        let at = now()
        if let last = lastHeardFlush, at.timeIntervalSince(last) < Self.heardFlushInterval { return }
        let waiting = outgoing.filter { _, messages in
            messages.contains { $0.isQueued && $0.deliveryIssue != .waitingForTurn }
        }.map(\.key)
        guard !waiting.isEmpty else { return }
        lastHeardFlush = at
        for chatID in waiting { resumeOutgoing(chatID: chatID, chatFree: true) }
    }

    /// "Try again" under a message the computer refused. Its words are back in the box, so this sends the box as an
    /// ordinary message, without the brief or ideas the computer refused, and drops the "Not sent" line.
    @discardableResult
    func retryFailed(_ id: String, in chatID: String) async -> Bool {
        guard chatID == openChatID else { return false }
        if let failed = loadedOutgoing(chatID).first(where: { $0.id == id && $0.hasFailed }),
           let reference = failed.briefReference, reference.ask == true {
            return await sendBriefAsk(failed.text, reference: reference) { _, _ in
                self.discardFailedBriefAsk(chatID: chatID, messageID: id)
            }
        }
        // The open chat moves refused words into the box as they come back; take any still on their way.
        _ = takeReturnedDrafts(in: chatID, appendingTo: draft(in: chatID))
        guard var composer = readableComposer(chatID),
              !composer.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return false }
        composer.briefReference = nil
        composer.offerReferences = []
        guard saveComposer(composer, chatID: chatID) else { return false }
        updateOutgoing(chatID) { $0.filter { $0.id != id || !$0.hasFailed } }
        return await sendComposer(in: chatID)
    }

    /// Remove a refused Ask only after its replacement has been saved locally.
    func discardFailedBriefAsk(chatID: String, messageID: String) {
        updateOutgoing(chatID) { messages in
            messages.filter { !($0.id == messageID && $0.hasFailed && $0.briefReference?.ask == true) }
        }
    }

    /// Sends what was waiting in every chat when the app last stopped, not only in the chat that opens first.
    func resumeQueuedMessages() {
        guard isPaired() else { return }
        let generation = pairingGeneration
        Task {
            let entries = await journal.entries()
            guard isCurrent(generation) else { return }
            let known = chats.map(\.id) + entries.filter { $0.operation == .chatMessage }.map(\.target)
            for chatID in cache.outgoingChatIDs(knownChatIDs: known) {
                resumeOutgoing(chatID: chatID, chatFree: true)
            }
        }
    }

    private enum DeliveryResult {
        case accepted
        /// A turn is running in the chat, so the Mac did not take it.
        case busy
        /// No answer yet: the Mac may be asleep or offline, or the request was cancelled.
        case waiting
        case refused
    }

    /// The answer reached the iPhone but could not be saved with the message. The journal keeps the key, so the
    /// next try gets the same answer back instead of starting a second turn.
    private struct OutgoingAnswerNotSaved: Error {}

    /// The refusal must be durable before the journal can forget its key, just like an acceptance.
    private struct OutgoingRefusalNotSaved: Error {}

    /// Sends a chat's waiting messages one at a time, oldest first, so the Mac gets them in the order they were
    /// written. One without an answer, or one waiting for a running turn, holds the ones after it. While a turn
    /// runs, only a message the chat may already show is tried: if the Mac stored it, asking again under its key
    /// returns the stored answer; if not, the Mac answers busy and the message waits for the turn to end.
    private func pumpOutgoing(chatID: String, chatFree: Bool, caller: String? = nil) async {
        guard isPaired() else { return }
        let generation = pairingGeneration
        let target = "send:\(chatID)"
        guard claim(target) else { return }
        defer { release(target, generation: generation) }
        var free = chatFree
        while isCurrent(generation), let next = loadedOutgoing(chatID).first(where: \.isQueued) {
            if next.askUndispatched == true {
                let presence = checkPresence()
                await presence.value
                guard isCurrent(generation), hubOnline == true else { return }
                var messages = loadedOutgoing(chatID)
                guard let index = messages.firstIndex(where: { $0.id == next.id && $0.askUndispatched == true }) else { continue }
                messages[index].askUndispatched = false
                do { try cache.saveOutgoing(messages, chatID: chatID) } catch { notice = ChatCopy.saveFailed; return }
                outgoing[chatID] = messages
            }
            var possibleRow: Int?
            if !free {
                guard let row = possibleStoredRow(for: next, chatID: chatID) else {
                    markWaiting(next.id, chatID: chatID, issue: .waitingForTurn)
                    return
                }
                possibleRow = row
            }
            switch await deliver(next, chatID: chatID, generation: generation, announce: next.id == caller) {
            case .accepted:
                // Its turn runs now, unless the answer was a stored one for a turn that is over.
                free = openChatIsFree(chatID) && transcript?.header.id == chatID
            case .busy:
                if let possibleRow { ruledOutRows[next.id, default: []].insert(possibleRow) }
                return
            case .waiting:
                return
            case .refused:
                break
            }
        }
    }

    /// Sends one message under its own journaled key and records how far it got. The answer is saved with the
    /// message before the journal forgets the key.
    private func deliver(_ message: ChatOutgoingMessage, chatID: String, generation: UUID,
                         announce: Bool) async -> DeliveryResult {
        let id = message.id
        let text = message.text
        let fingerprint = message.deliveryFingerprint
        // The first build keyed a message by its text alone. Its retry keeps that key, so a request the Mac took
        // while the answer was lost is answered from the receipt instead of running twice.
        if message.offerReferences.isEmpty && message.briefReference == nil {
            _ = try? await journal.refingerprint(operation: .chatMessage, target: chatID,
                                                 from: WriteFingerprint.make([text]), to: fingerprint)
        }
        let outcome = await writer.send(.chatMessage, target: chatID, choice: "message", fingerprint: fingerprint) { key in
            guard self.isCurrent(generation) else { throw CancellationError() }
            let accepted: ChatMessageAccepted
            do {
                if let reference = message.briefReference {
                    accepted = try await self.hub.sendBriefMessage(chatID: chatID, text: text,
                                                                  reference: reference, idempotencyKey: key)
                } else {
                    accepted = try await self.hub.sendMessage(chatID: chatID, text: text,
                                                              offerReferences: message.offerReferences,
                                                              waiting: self.waitingFields(for: message, chatID: chatID),
                                                              idempotencyKey: key)
                }
            } catch {
                guard self.isCurrent(generation) else { throw CancellationError() }
                if error as? ChatHubError != .busy, JournaledWriter.disposition(for: error) != .keep,
                   !self.returnOutgoingToDraft(id, chatID: chatID) {
                    throw OutgoingRefusalNotSaved()
                }
                throw error
            }
            guard self.isCurrent(generation) else { throw CancellationError() }
            guard self.accept(id, chatID: chatID, answer: accepted) else { throw OutgoingAnswerNotSaved() }
            return accepted
        }
        guard isCurrent(generation) else { return .waiting }
        switch outcome {
        case .sent:
            notice = nil
            await refreshOpen()
            return .accepted
        case .failed(let error, _) where error is OutgoingAnswerNotSaved:
            notice = nil
            await refreshOpen()
            return .accepted
        case .failed(let error, _) where error is OutgoingRefusalNotSaved:
            notice = ChatCopy.draftSaveFailed
            return .waiting
        case .notSaved:
            returnOutgoingToDraft(id, chatID: chatID)
            notice = ChatCopy.saveFailed
            return .refused
        case .cancelled:
            // The key stays in the journal, so the message stays on its way. A cancelled try says nothing about the
            // computer, so it does not mark the message as waiting for it.
            return .waiting
        case .failed(let error, let disposition):
            noteRepairNeeded(by: error)
            if error as? ChatHubError == .busy {
                markWaiting(id, chatID: chatID, issue: .waitingForTurn)
                if announce { notice = ChatDeliveryStatus.waitingForTurn.explanation }
                return .busy
            }
            switch disposition {
            case .keep:
                // An unanswered request cannot support a live "working" claim from a cached chat header.
                invalidatePresence()
                markWaiting(id, chatID: chatID, issue: .waitingForComputer)
                if Self.relayMayHold(error) { markRelayHolds(id, chatID: chatID) }
                if announce { notice = ChatDeliveryStatus.waitingForComputer.explanation }
                return .waiting
            case .changed:
                notice = message.briefReference != nil && !repairNeeded ? BriefConversationCopy.changed
                    : message.offerReferences.isEmpty || repairNeeded ? ChatCopy.changed : ChatProvenanceCopy.sourceUnavailable()
                await refreshOpen()
                return .refused
            case .drop:
                notice = message.offerReferences.isEmpty || repairNeeded
                    ? Self.message(for: error) : ChatProvenanceCopy.sourceUnavailable()
                return .refused
            }
        }
    }

    // MARK: - Waiting while the computer is away

    /// The connection check says why the computer is away; nil once it answers again.
    func noteConnection(cause: TodayStore.ConnectionCause?, reason: String?, since: Date?) {
        awayCause = cause.map { ChatAwayCause(connection: $0, reason: reason) }
        awaySince = cause == nil ? nil : since
        guard let awayCause else { return }
        var changed = false
        var periods = awayPeriods
        for (chatID, list) in periods {
            guard let index = list.lastIndex(where: { !$0.isOver }), list[index].cause != awayCause else { continue }
            periods[chatID]?[index].cause = awayCause
            changed = true
        }
        if changed { saveAwayPeriods(periods) }
    }

    #if DEBUG && targetEnvironment(simulator)
    /// The seeded away states are written after the store starts.
    func reloadAwayPeriodsForUITest() { awayPeriods = cache.loadAwayPeriods() ?? [:] }
    #endif

    func awayPeriods(in chatID: String) -> [ChatAwayPeriod] { awayPeriods[chatID] ?? [] }

    func awayPeriod(containing messageID: String, in chatID: String) -> ChatAwayPeriod? {
        awayPeriods[chatID]?.last { $0.messageIDs.contains(messageID) }
    }

    /// True when every message under this stretch is one the computer did not take in a day, so none of them still
    /// waits and the away line may drop its promise (PROTOCOL.md section 23).
    func awayPeriodAllNotSent(_ period: ChatAwayPeriod, in chatID: String) -> Bool {
        let outgoing = loadedOutgoing(chatID)
        return !period.messageIDs.isEmpty && period.messageIDs.allSatisfy { id in
            outgoing.first { $0.id == id }?.expiredWhileWaiting(computerOnline: hubOnline, now: now()) ?? false
        }
    }

    func awayPeriod(containingSeq seq: Int, in chatID: String) -> ChatAwayPeriod? {
        awayPeriods[chatID]?.last { $0.seqs.values.contains(seq) }
    }

    /// Messages join the open stretch, or start one that begins when the computer went away.
    private func joinAwayPeriod(chatID: String, messageIDs: [String]) {
        guard !messageIDs.isEmpty else { return }
        var list = awayPeriods[chatID] ?? []
        let known = Set(list.flatMap(\.messageIDs))
        let fresh = messageIDs.filter { !known.contains($0) }
        guard !fresh.isEmpty else { return }
        if let index = list.lastIndex(where: { !$0.isOver }) {
            list[index].messageIDs += fresh
        } else {
            let started = min(awaySince ?? now(), now())
            list.append(ChatAwayPeriod(id: makeMessageID(), cause: awayCause ?? .notAnswering, startedAt: started,
                                       sinceKnown: awaySince != nil,
                                       messageIDs: fresh))
        }
        var periods = awayPeriods
        periods[chatID] = Array(list.suffix(Self.awayPeriodsKept))
        saveAwayPeriods(periods)
    }

    private func saveAwayPeriods(_ periods: [String: [ChatAwayPeriod]]) {
        awayPeriods = periods
        try? cache.saveAwayPeriods(periods)
    }

    /// The label under a message that waited becomes the time it went out; the stretch ends when all went.
    private func recordWentOut(_ id: String, chatID: String, seq: Int?) -> Bool {
        undoneSinceSent[chatID] = nil
        guard var list = awayPeriods[chatID], let index = list.lastIndex(where: { $0.messageIDs.contains(id) }),
              list[index].sentAt[id] == nil else { return true }
        list[index].sentAt[id] = now()
        if let typed = loadedOutgoing(chatID).first(where: { $0.id == id })?.sentAt { list[index].typedAt[id] = typed }
        if let seq, seq > 0 { list[index].seqs[id] = seq }
        if list[index].messageIDs.allSatisfy({ list[index].sentAt[$0] != nil }) { list[index].endedAt = now() }
        var periods = awayPeriods
        periods[chatID] = list
        saveAwayPeriods(periods)
        return true
    }

    /// What the computer is told about a message that waited: when it was typed, and how many were undone.
    private func waitingFields(for message: ChatOutgoingMessage, chatID: String) -> ChatWaitingFields? {
        let undone = undoneSinceSent[chatID] ?? 0
        let waited = awayPeriod(containing: message.id, in: chatID) != nil
        guard waited || undone > 0 else { return nil }
        return ChatWaitingFields(queuedAt: waited ? message.sentAt : nil, undone: undone)
    }

    /// Whether Undo applies: the message is still waiting for the computer. A brief's question is queued the same way.
    func canUndoWaiting(_ message: ChatOutgoingMessage) -> Bool {
        message.isQueued && message.relayHolds != true && (message.askUndispatched == true || message.deliveryIssue == .waitingForComputer || hubOnline == false)
    }

    /// Messages the computer has not taken within a day, across every chat on this phone (PROTOCOL.md section 23).
    /// Read without changing the store, so Today can call it while it draws.
    func expiredWaitingMessages(now: Date) -> [ChatOutgoingMessage] {
        var chatIDs = Set(chats.map(\.id)).union(outgoing.keys)
        if let main = cache.loadMainChatSummary() { chatIDs.insert(main.id) }
        return chatIDs.flatMap { outgoing[$0] ?? cache.loadOutgoing(chatID: $0) ?? [] }
            .filter { $0.expiredWhileWaiting(computerOnline: hubOnline, now: now) }
    }

    /// Send again for a waiting message the computer never answered within a day: it leaves the outbox and goes out
    /// as a new send with its words.
    func sendExpiredAgain(_ messageID: String, in chatID: String) async -> Bool {
        guard let message = loadedOutgoing(chatID).first(where: { $0.id == messageID }),
              message.expiredWhileWaiting(computerOnline: hubOnline, now: now()) else { return false }
        guard await discardWaiting(messageID, in: chatID) != nil else { return false }
        return await send(message.text, offerReferences: message.offerReferences)
    }

    /// Undo takes a waiting message back: its words go in the box, after any text already there on a new line.
    /// The others keep their order.
    /// Undo takes a waiting message back: its words go in the box, after any text already there on a new line.
    /// The others keep their order.
    @discardableResult
    func undoWaiting(_ messageID: String, in chatID: String) async -> Bool {
        guard let message = await discardWaiting(messageID, in: chatID) else { return false }
        let current = draft(in: chatID)
        _ = editComposerDraft(current.isEmpty ? message.text : current + "\n" + message.text, in: chatID)
        undoneSinceSent[chatID, default: 0] += 1
        return true
    }

    /// Takes a waiting message out of the outbox, its journal write and its away-period line. Returns the message, or nil
    /// when nothing was taken. The box is left to the caller.
    private func discardWaiting(_ messageID: String, in chatID: String) async -> ChatOutgoingMessage? {
        let generation = pairingGeneration
        let target = "send:\(chatID)"
        guard isPaired(), claim(target) else { return nil }
        defer { release(target, generation: generation) }
        guard !outgoingUnreadable.contains(chatID),
              let message = loadedOutgoing(chatID).first(where: { $0.id == messageID }),
              canUndoWaiting(message) || message.expiredWhileWaiting(computerOnline: hubOnline, now: now()) else { return nil }
        let write = await journal.existing(operation: .chatMessage, target: chatID,
                                           fingerprint: message.deliveryFingerprint)
        guard isCurrent(generation), !outgoingUnreadable.contains(chatID) else { return nil }
        let remaining = loadedOutgoing(chatID).filter { $0.id != messageID }
        do { try cache.saveOutgoing(remaining, chatID: chatID) } catch { notice = ChatCopy.saveFailed; return nil }
        outgoing[chatID] = remaining
        updateReturnedDrafts(chatID)
        if let write { await journal.remove(keys: [write.key]) }
        guard isCurrent(generation) else { return nil }
        if var list = awayPeriods[chatID], let index = list.lastIndex(where: { $0.messageIDs.contains(messageID) }) {
            list[index].messageIDs.removeAll { $0 == messageID }
            if list[index].messageIDs.isEmpty {
                list.remove(at: index)
            } else if list[index].messageIDs.allSatisfy({ list[index].sentAt[$0] != nil }) {
                list[index].endedAt = now()
            }
            var periods = awayPeriods
            periods[chatID] = list
            saveAwayPeriods(periods)
        }
        return message
    }

    /// Records the Mac's answer with the message. False when it could not be saved.
    private func accept(_ id: String, chatID: String, answer: ChatMessageAccepted) -> Bool {
        let heard = heardTurnIDs[chatID]?.contains(answer.turnId) == true
        answeredAtRead[id] = readsStarted
        return updateOutgoing(chatID) { messages in
            let accepted = messages.map { message -> ChatOutgoingMessage in
                guard message.id == id else { return message }
                var accepted = message
                accepted.turnID = answer.turnId
                if let seq = answer.seq, seq > 0 { accepted.seq = seq }
                accepted.state = message.state.advanced(to: .sent)
                accepted.deliveryIssue = nil
                return accepted
            }
            return heard ? ChatDelivery.markSeen(accepted, turnID: answer.turnId) : accepted
        } && recordWentOut(id, chatID: chatID, seq: answer.seq)
    }

    /// Whether the relay may have taken the request though no answer came: it accepted the envelope and the computer
    /// did not answer in time, or the send timed out after the relay may have queued it. Either way the computer
    /// may run it when it is back, so Undo would promise something the phone cannot keep.
    static func relayMayHold(_ error: Error) -> Bool {
        if case .noAnswer? = error as? HubRequestError { return true }
        if case .timedOut? = error as? RelayClientError { return true }
        return false
    }

    private func markRelayHolds(_ id: String, chatID: String) {
        updateOutgoing(chatID) { messages in
            messages.map { message in
                guard message.id == id, message.isQueued else { return message }
                var held = message
                held.relayHolds = true
                return held
            }
        }
    }

    private func markWaiting(_ id: String, chatID: String, issue: ChatDeliveryIssue) {
        updateOutgoing(chatID) { messages in
            guard let blocked = messages.firstIndex(where: { $0.id == id }) else { return messages }
            return messages.enumerated().map { index, message in
                guard index >= blocked, message.isQueued else { return message }
                var waiting = message
                waiting.deliveryIssue = issue
                return waiting
            }
        }
        if issue == .waitingForComputer {
            joinAwayPeriod(chatID: chatID, messageIDs: loadedOutgoing(chatID).filter {
                $0.isQueued && $0.deliveryIssue == .waitingForComputer && $0.briefReference == nil
            }.map(\.id))
        }
    }

    /// The open chat's last read shows no running turn. A chat waiting on a permission answer still takes a message:
    /// the computer refuses it as busy if a turn is running, and the message then waits for that turn.
    private func openChatIsFree(_ chatID: String) -> Bool {
        guard let header = transcript?.header, header.id == chatID else { return true }
        return !header.state.isBusy
    }

    private func possibleStoredRow(for message: ChatOutgoingMessage, chatID: String) -> Int? {
        guard let transcript, transcript.header.id == chatID else { return nil }
        return ChatDelivery.possibleRow(for: message, in: loadedOutgoing(chatID), items: transcript.items,
                                        ruledOut: ruledOutRows[message.id] ?? [])
    }

    /// Starts sending a chat's waiting messages after a read, unless they are already going.
    private func resumeOutgoing(chatID: String, chatFree: Bool) {
        guard isPaired(), !busyTargets.contains("send:\(chatID)"), Identifiers.isSafePathID(chatID),
              loadedOutgoing(chatID).contains(where: \.isQueued) else {
            return
        }
        Task { await self.pumpOutgoing(chatID: chatID, chatFree: chatFree) }
    }

    /// A `chat_turn` event says the Mac took that turn: the message that started it has been seen.
    private func noteTurn(_ event: MobileChatTurnEvent) {
        guard !event.chatId.isEmpty, !event.turnId.isEmpty else { return }
        var heard = heardTurnIDs[event.chatId] ?? []
        if !heard.contains(event.turnId) {
            heard.append(event.turnId)
            heardTurnIDs[event.chatId] = Array(heard.suffix(Self.heardTurnLimit))
        }
        guard outgoing[event.chatId]?.contains(where: { $0.turnID == event.turnId }) == true else { return }
        updateOutgoing(event.chatId) { ChatDelivery.markSeen($0, turnID: event.turnId) }
    }

    /// Drafts reach the composer in the order they came back, so a refused message waits behind a hand-off or
    /// another draft that was already waiting.
    @discardableResult
    private func returnOutgoingToDraft(_ id: String, chatID: String) -> Bool {
        updateOutgoing(chatID) { messages in
            guard var draft = messages.first(where: { $0.id == id }) else { return messages }
            draft.returnedToDraft = true
            draft.deliveryIssue = .failed
            return messages.filter { $0.id != id } + [draft]
        }
    }

    /// The chat's messages, read from disk the first time. A file that exists but cannot be read (the phone is
    /// locked, or it is damaged) is not taken as empty: the chat keeps what it holds in memory, nothing overwrites
    /// the file, and the next access reads it again and merges.
    private func loadedOutgoing(_ chatID: String) -> [ChatOutgoingMessage] {
        if let current = outgoing[chatID], !outgoingUnreadable.contains(chatID) { return current }
        guard let stored = cache.loadOutgoing(chatID: chatID) else {
            outgoingUnreadable.insert(chatID)
            return outgoing[chatID] ?? []
        }
        let wasUnreadable = outgoingUnreadable.remove(chatID) != nil
        let memory = outgoing[chatID] ?? []
        let merged = stored + memory.filter { message in !stored.contains { $0.id == message.id } }
        if outgoing[chatID] != merged { outgoing[chatID] = merged }
        updateReturnedDrafts(chatID)
        if wasUnreadable, merged != stored { try? cache.saveOutgoing(merged, chatID: chatID) }
        return merged
    }

    /// Applies a change to a chat's messages, keeps only the ones a bubble labels, and saves them when they
    /// changed. False when the change could not be saved.
    @discardableResult
    private func updateOutgoing(_ chatID: String, clearingComposer: Bool = false,
                                _ change: ([ChatOutgoingMessage]) -> [ChatOutgoingMessage]) -> Bool {
        let current = loadedOutgoing(chatID)
        let rows = transcript.flatMap { $0.header.id == chatID ? $0.items : nil }
        let updated = ChatDelivery.pruned(change(current), rows: rows)
        if outgoing[chatID] != updated { outgoing[chatID] = updated }
        updateReturnedDrafts(chatID)
        guard !outgoingUnreadable.contains(chatID) else { return false }
        guard updated != current else { return true }
        return (try? cache.saveOutgoing(updated, chatID: chatID, clearingComposer: clearingComposer)) != nil
    }

    @discardableResult
    func answerPermission(_ item: ChatItemView, optionID: String) async -> Bool {
        guard isPaired(), let chatID = openChatID, let permission = item.permission,
              Identifiers.isSafePathID(permission.requestID),
              permission.options.contains(where: { $0.id == optionID }) else { return false }
        let generation = pairingGeneration
        let requestID = permission.requestID
        let target = "permission:\(requestID)"
        guard claim(target) else { return false }
        defer { release(target, generation: generation) }
        guard await authorize(ChatCopy.permissionReason, generation: generation) else { return false }
        let fingerprint = WriteFingerprint.make([requestID, optionID])
        let answered: Bool? = await submit(.chatPermission, target: requestID, choice: optionID,
                                           fingerprint: fingerprint, conflictNotice: ChatCopy.permissionGone,
                                           failureNotice: Self.permissionFailureNotice) { key in
            try await self.hub.answerPermission(chatID: chatID, requestID: requestID, optionID: optionID,
                                                idempotencyKey: key)
        }
        guard isCurrent(generation), let answered else { return false }
        answeredRequestIDs.insert(requestID)
        notice = answered ? ChatCopy.permissionAnswered : ChatCopy.permissionGone
        await refreshOpen()
        return isCurrent(generation) && answered
    }

    /// Stops the open chat's turn. The Mac answers false when nothing was running; the chat then shows nothing.
    @discardableResult
    func cancelTurn() async -> Bool {
        guard isPaired(), let chatID = openChatID else { return false }
        let generation = pairingGeneration
        let target = "cancel:\(chatID)"
        guard claim(target) else { return false }
        defer { release(target, generation: generation) }
        let fingerprint = WriteFingerprint.make(["cancel", chatID])
        let cancelled: Bool? = await submit(.chatCancel, target: chatID, choice: "cancel", fingerprint: fingerprint,
                                            conflictNotice: ChatCopy.changed) { key in
            try await self.hub.cancel(chatID: chatID, idempotencyKey: key)
        }
        guard isCurrent(generation) else { return false }
        await refreshOpen()
        guard isCurrent(generation), cancelled == true else { return false }
        notice = ChatCopy.stopping
        return true
    }

    /// Stop only the Ask named by its accepted turn id, never a later chat turn.
    func stopBriefAsk(chatID: String, turnID: String) async -> Bool {
        guard isPaired(), openChatID == chatID else { return false }
        let generation = pairingGeneration
        let target = "cancel:\(chatID)"
        guard claim(target) else { return false }
        defer { release(target, generation: generation) }
        let cancelled: Bool? = await submit(.chatCancel, target: chatID, choice: "cancel",
                                            fingerprint: WriteFingerprint.make(["brief-ask", chatID, turnID]),
                                            conflictNotice: ChatCopy.changed) { key in
            try await self.hub.stopBriefAsk(chatID: chatID, turnID: turnID, idempotencyKey: key)
        }
        guard isCurrent(generation), cancelled == true else { return false }
        await refreshOpen()
        return isCurrent(generation)
    }

    @discardableResult
    func clearAfterUnpair() -> Bool {
        pairingGeneration = UUID()
        presenceRead?.task.cancel()
        presenceRead = nil
        lastPresenceCheckAt = nil
        lastHeardFlush = nil
        reachabilityRevision = UUID()
        repairNeeded = false
        openGeneration = UUID()
        openRefreshRunning = false
        openRefreshQueued = false
        busyTargets = []
        let cleared = (try? cache.removeAll()) != nil
        chats = []
        listState = .idle
        listRefreshFailed = false
        openChatID = nil
        transcript = nil
        transcriptState = .idle
        transcriptRefreshFailed = false
        hubOnline = nil
        answeredRequestIDs = []
        outgoing = [:]
        heardTurnIDs = [:]
        recentTurnEvents = []
        ruledOutRows = [:]
        outgoingUnreadable = []
        answeredAtRead = [:]
        returnedDrafts = []
        pendingMainChatDrafts = []
        composers = [:]
        notice = nil
        return cleared
    }

    private func readOpenOnce(selection: UUID) async {
        guard isPaired(), let chatID = openChatID else { return }
        let generation = pairingGeneration
        let started = FirstLoadTrace.now
        readsStarted += 1
        let readNumber = readsStarted
        // A transcript read from the Mac keeps its cursor and reads forward from it. A cached transcript, or a
        // chat opened for the first time, has no cursor, so its first read is the Mac's newest page.
        let continuing = transcript.flatMap { $0.header.id == chatID ? $0.cursor : nil }
        if transcript == nil { transcriptState = .loading }
        do {
            let read = try await readTranscript(chatID: chatID, after: continuing,
                                                generation: generation, selection: selection)
            guard isCurrent(generation), openGeneration == selection, openChatID == chatID else { return }
            pairingAccepted()
            apply(chatID: chatID, read: read, replacing: continuing == nil, readNumber: readNumber)
            FirstLoadTrace.record("chat.transcript.fresh", since: started)
        } catch is CancellationError {
            return
        } catch {
            guard isCurrent(generation), openGeneration == selection, openChatID == chatID else { return }
            noteRepairNeeded(by: error)
            if JournaledWriter.disposition(for: error) == .keep { invalidatePresence() }
            if transcript == nil {
                transcriptState = .failed(Self.message(for: error))
            } else {
                transcriptRefreshFailed = true
            }
        }
    }

    /// Starts a presence check, or joins the one running, and returns it for callers that wait. The check
    /// publishes its own answer and clears itself, so a caller that is cancelled, or does not wait, never
    /// leaves a finished answer behind for the next refresh to reuse.
    private func checkPresence() -> Task<Void, Never> {
        if let running = presenceRead { return running.task }
        lastPresenceCheckAt = now()
        let id = UUID()
        let generation = pairingGeneration
        let revision = reachabilityRevision
        let presence = self.presence
        let task = Task {
            let answer: Result<Bool, Error>
            do {
                answer = .success(try await presence())
            } catch {
                answer = .failure(error)
            }
            guard presenceRead?.id == id else { return }
            presenceRead = nil
            guard generation == pairingGeneration, isPaired() else { return }
            switch answer {
            case .success(let online):
                // The relay answers presence without reading the chat routes, so a refusal stays until an
                // authenticated read succeeds or the iPhone pairs again.
                if !online || revision == reachabilityRevision { hubOnline = online }
            case .failure(let error):
                noteRepairNeeded(by: error)
                hubOnline = RemoteStatus.offline(for: error, remote: isRemote())
            }
        }
        presenceRead = (id, task)
        return task
    }

    private func invalidatePresence() {
        // A presence request started before a failed chat read cannot restore a live answering claim.
        // Its offline answer remains useful, and an already known offline state stays visible.
        reachabilityRevision = UUID()
        if hubOnline == true { hubOnline = nil }
    }

    /// The relay answers 401 for a revoked or unknown bearer and 403 for a device it no longer serves. Only
    /// pairing again fixes either, so Chat asks for that instead of offering the cached chat.
    private func noteRepairNeeded(by error: Error) {
        switch error as? RelayClientError {
        case .unauthorized?, .forbidden?:
            repairNeeded = true
            try? cache.rememberRefusal()
        default:
            break
        }
    }

    /// A list, transcript or event read the relay authenticated: the pairing works again.
    private func pairingAccepted() {
        guard repairNeeded else { return }
        repairNeeded = false
        try? cache.forgetRefusal()
    }

    /// A new pairing was saved. Pairing again with the same Mac keeps the caches, but not a refusal of the old one.
    func pairingCommitted() {
        repairNeeded = false
        try? cache.forgetRefusal()
    }

    /// A fresh read asks for the newest page only. A continuing read follows the Mac's cursor while `hasMore`
    /// is true, up to `maxCatchUpPages` pages; anything left is read on the next event.
    private func readTranscript(chatID: String, after start: Int?, generation: UUID,
                                selection: UUID) async throws -> TranscriptRead {
        var read = TranscriptRead(header: nil, cursor: start ?? 0)
        var after = start
        for _ in 0..<Self.maxCatchUpPages {
            let hub = self.hub
            let cursor = after
            let page = try await boundedRead { try await hub.chat(id: chatID, after: cursor, limit: Self.pageLimit) }
            guard isCurrent(generation), openGeneration == selection,
                  openChatID == chatID else { throw CancellationError() }
            if let header = page.chat { read.header = header }
            read.items.append(contentsOf: page.items)
            read.pendingPermission = page.pendingPermission
            read.live = page.live
            read.cursor = page.cursor
            guard start != nil, page.hasMore else { break }
            after = page.cursor
        }
        return read
    }

    // The relay's shared long poll and request send can outlive its response deadline.
    // Return control to Chat even if the underlying transport does not promptly cancel.
    private func boundedRead<T: Sendable>(_ operation: @escaping @Sendable () async throws -> T) async throws -> T {
        let race = TimeoutRace<T>()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                race.start(continuation: continuation, timeout: readTimeout, operation: operation)
            }
        } onCancel: {
            race.cancel()
        }
    }

    private func showCachedTranscript(chatID: String) {
        let started = FirstLoadTrace.now
        guard let cached = cache.load(chatID: chatID) else { return }
        // A stored running or waiting state may be long over. Until a read confirms it, the chat offers Send,
        // not Stop, and does not say Isoform is still answering.
        let items = cached.items.map(\.view)
        transcript = OpenChat(header: ChatHeaderView(id: cached.id, title: cached.title, state: .unknown),
                              items: items, asOf: cached.storedAt)
        transcriptState = .loaded
        if outgoing[chatID]?.isEmpty == false {
            updateOutgoing(chatID) { ChatDelivery.reconcile($0, items: items, pendingPermission: nil) }
        }
        FirstLoadTrace.record("chat.transcript.cached", since: started)
    }

    private func apply(chatID: String, read: TranscriptRead, replacing: Bool, readNumber: Int) {
        let previous = replacing ? [] : (transcript?.items ?? [])
        var byID: [String: ChatItemView] = [:]
        var identitiesBySequence: [Int: String] = [:]
        for item in previous + read.items {
            if let replaced = byID[item.id] {
                identitiesBySequence.removeValue(forKey: replaced.seq)
            }
            // A fresh row with an id also replaces a legacy snapshot at the same sequence.
            if let previousID = identitiesBySequence[item.seq] {
                byID.removeValue(forKey: previousID)
            }
            byID[item.id] = item
            identitiesBySequence[item.seq] = item.id
        }
        let merged = byID.values.sorted { $0.seq < $1.seq }
        let resolved = read.header ?? transcript?.header ?? ChatHeaderView(id: chatID, title: "", state: .unknown)
        transcript = OpenChat(header: resolved, items: merged, pendingPermission: read.pendingPermission,
                              cursor: read.cursor, asOf: now(), live: read.live)
        transcriptState = .loaded
        transcriptRefreshFailed = false
        chats = chats.map { summary in
            guard summary.id == chatID else { return summary }
            var updated = summary
            updated.state = resolved.state
            if !resolved.title.isEmpty { updated.title = resolved.title }
            return updated
        }
        try? cache.save(resolved, items: merged)
        if outgoing[chatID]?.isEmpty == false {
            // Only a read that started after a message's 202 must hold its row (a Mac without the 202's seq).
            let answeredBefore = Set(outgoing[chatID, default: []].filter { message in
                message.turnID != nil && (answeredAtRead[message.id] ?? 0) < readNumber
            }.map(\.id))
            updateOutgoing(chatID) {
                ChatDelivery.reconcile($0, items: merged, pendingPermission: read.pendingPermission,
                                       answeredBefore: answeredBefore)
            }
            resumeOutgoing(chatID: chatID, chatFree: !resolved.state.isBusy)
        }
    }

    private func submit<Value>(_ operation: PendingWrite.Operation, target: String, choice: String,
                               fingerprint: String, conflictNotice: String,
                               failureNotice: ((Error) -> String?)? = nil,
                               send: (String) async throws -> Value) async -> Value? {
        let generation = pairingGeneration
        let outcome = await writer.send(operation, target: target, choice: choice, fingerprint: fingerprint) { key in
            guard self.isCurrent(generation) else { throw CancellationError() }
            return try await send(key)
        }
        guard isCurrent(generation) else { return nil }
        switch outcome {
        case .sent(let value):
            return value
        case .notSaved:
            notice = ChatCopy.saveFailed
            return nil
        case .cancelled:
            return nil
        case .failed(let error, let disposition):
            noteRepairNeeded(by: error)
            if let calm = failureNotice?(error) {
                notice = calm
                await refreshOpen()
                return nil
            }
            if error as? ChatHubError == .busy {
                notice = Self.message(for: error)
                await refreshOpen()
                return nil
            }
            switch disposition {
            case .changed:
                notice = conflictNotice
                await refreshOpen()
            case .keep, .drop:
                notice = Self.message(for: error)
            }
            return nil
        }
    }

    /// The calm notices for the answers a permission can get once the Mac has moved on.
    private static func permissionFailureNotice(_ error: Error) -> String? {
        guard let error = error as? HubRequestError else { return nil }
        switch error {
        case .rejected(let status, _) where status == 400:
            return ChatCopy.optionNotOffered
        case .rejected(let status, _) where status == 404:
            return ChatCopy.requestNotPending
        case .conflict:
            return ChatCopy.permissionGone
        default:
            return nil
        }
    }

    private func authorize(_ reason: String, generation: UUID) async -> Bool {
        do {
            try await guardian.authorize(reason: reason)
            return isCurrent(generation)
        } catch {
            guard isCurrent(generation) else { return false }
            notice = Self.message(for: error)
            return false
        }
    }

    private func claim(_ target: String) -> Bool {
        guard !busyTargets.contains(target) else { return false }
        busyTargets.insert(target)
        return true
    }

    private func release(_ target: String, generation: UUID) {
        if generation == pairingGeneration { busyTargets.remove(target) }
    }

    private func isCurrent(_ generation: UUID) -> Bool {
        generation == pairingGeneration && isPaired() && !Task.isCancelled
    }

    private static func boundedTitle(_ title: String) -> String {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return ChatCopy.defaultTitle }
        return String(trimmed.prefix(120))
    }

    private static func message(for error: Error) -> String {
        // A paired phone whose computer does not answer is never told to pair (P132 rule, on Chat too).
        if case HubRequestError.notPaired = error { return String(localized: "Couldn't reach your computer.") }
        return (error as? LocalizedError)?.errorDescription ?? ChatCopy.generic
    }
}

enum ChatCopy {
    static func recoveredFromChat(_ title: String) -> String { "Unsent message for chat \"\(title)\":" }
    static let introduction = "Talk to Isoform from here."
    static let pairingDetail = "Pair this phone with the Isoform app on your computer. Chats stay on your computer, and this phone keeps only the last few messages of each chat."
    static let pair = "Pair with your computer"
    static let legacyPairing = "This phone was paired before Chat existed. Open Settings and choose Pair again to talk to Isoform from here."
    static let pairingRefused = "Your computer no longer accepts this phone. Open Settings and choose Pair again to talk to Isoform from here."
    static let pairAgain = "Pair this phone again"
    static let openSettings = "Open Settings"
    static let unavailableTitle = "Chat is not available yet."
    static let unavailableDetail = "Pull down to check with your computer again."
    static let noMainChatTitle = "Your computer has no chat yet."
    static var askAnything: String { String(localized: "Ask Isoform anything. It answers from your computer.") }
    static var updateToSendFromPhone: String { String(localized: "Update Isoform on your computer to send from here.") }
    static let noMainChatDetail = "Open Isoform on your computer, then pull down to check again."
    static let generic = "Isoform could not finish that. Try again."
    static let saveFailed = "Isoform could not save this on this phone, so nothing was sent to your computer. Try again."
    static let draftSaveFailed = "Isoform could not update the saved draft. Try again."
    static let changed = "This changed on your computer. Open the chat again to see it."
    static let tooLong = "That message is too long for Isoform. Shorten it and try again."
    static let defaultTitle = "New chat"
    static let permissionReason = "Confirm that you want Isoform to answer this request on your computer."
    static let permissionAnswered = "Answered on your computer."
    static let permissionGone = "This request was already answered on your computer."
    static let optionNotOffered = "That answer is no longer offered for this request on your computer. Check the chat again."
    static let requestNotPending = "This request is no longer waiting on your computer. Check the chat again."
    static let stopping = "Stopping. Isoform will stop after its current step."
}

/// Copy introduced for source-bound drafts follows the phone's English and Simplified Chinese selection.
enum ChatProvenanceCopy {
    static func text(_ english: String, _ chinese: String, locale: Locale = .current) -> String {
        ChatPresentationLanguage(locale: locale).text(en: english, zhCN: chinese)
    }

    static func sourceUnavailable(locale: Locale = .current) -> String {
        text("This idea changed or its source is no longer available. Discard this draft and refresh Ideas.",
             "这个想法已更改，或其来源已不可用。请放弃这份草稿，然后刷新想法。", locale: locale)
    }

    static var legacyDraft: String {
        text("This saved draft has no source reference. Discard it and choose the idea again.",
             "这份已保存的草稿没有来源关联。请放弃草稿，然后重新选择想法。")
    }

    static var tooManyIdeas: String {
        text("This draft contains too many ideas. Discard it and choose fewer ideas.",
             "这份草稿包含太多想法。请放弃草稿，然后选择较少的想法。")
    }
}
