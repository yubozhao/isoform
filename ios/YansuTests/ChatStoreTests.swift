import Foundation
import XCTest
@testable import Yansu

private actor HeldChatOperation {
    private var held: CheckedContinuation<Void, Error>?
    private var waiters: [CheckedContinuation<Void, Never>] = []

    func pause() async throws {
        try await withCheckedThrowingContinuation { continuation in
            held = continuation
            waiters.forEach { $0.resume() }
            waiters.removeAll()
        }
    }

    func waitUntilHeld() async {
        if held != nil { return }
        await withCheckedContinuation { waiters.append($0) }
    }

    func release(error: Error? = nil) {
        if let error {
            held?.resume(throwing: error)
        } else {
            held?.resume()
        }
        held = nil
    }
}

/// Answers the chat routes the way the Mac does (section 15.4): a newest page without `after`, rows after a
/// cursor with `after`, a cursor that moves past rows the Mac does not send, and a transient permission that
/// only the latest page carries.
private final class StubChatHub: ChatHubAPI, @unchecked Sendable {
    private struct Row {
        var seq: Int
        /// nil for a row the Mac consumes but does not send (thinking, compaction).
        var item: ChatItemView?
    }

    private struct Stored {
        var header: ChatHeaderView
        var rows: [Row]
        var pending: ChatPermissionView?
        var pendingText = ""
    }

    private let lock = NSLock()
    private var summaries: [ChatSummaryView] = []
    private var stored: [String: Stored] = [:]
    private var readAfters: [Int?] = []
    private var readFailures: [Error] = []
    private var sendFailures: [Error] = []
    private var answerFailures: [Error] = []
    private var sendRecords: [(text: String, key: String)] = []
    private var referenceRecords: [[ChatOfferReference]] = []
    private var briefReferenceRecords: [ChatBriefReference] = []
    private var liveAnswer: ChatLiveAnswer?
    private var permissionRecords: [(requestID: String, optionID: String, key: String)] = []
    private var cancelRecords: [String] = []
    private var stoppedBriefTurnIDs: [String] = []
    private var createRecords: [(title: String, key: String)] = []
    private var eventBatches: [[MobileChatTurnEvent]] = []
    private var pollFailures: [Error] = []
    private var pollCount = 0
    private var heldOperations: [String: HeldChatOperation] = [:]
    private var nextSeq = 1
    private var nextTurn = 1
    /// The hub's receipts (PROTOCOL.md 15.2): a repeat of an accepted key gets the stored answer and runs nothing.
    private var receipts: [String: ChatMessageAccepted] = [:]
    private var answersToLose = 0
    /// Answers 202 without the stored item's seq, as a Mac from before that field did.
    var omitsSeq = false

    private func locked<T>(_ body: () throws -> T) rethrows -> T {
        lock.lock()
        defer { lock.unlock() }
        return try body()
    }

    func seed(chatID: String, title: String, state: ChatState = .idle, items: [ChatItemView] = []) {
        locked {
            let rows = items.map { Row(seq: $0.seq, item: $0) }
            stored[chatID] = Stored(header: ChatHeaderView(id: chatID, title: title, state: state), rows: rows)
            nextSeq = max(nextSeq, (items.map(\.seq).max() ?? 0) + 1)
            summaries.append(ChatSummaryView(id: chatID, title: title, updatedAt: "", state: state, kind: .chat))
        }
    }

    func append(chatID: String, role: ChatRole, text: String) {
        locked {
            guard var entry = stored[chatID] else { return }
            let item = ChatItemView(seq: nextSeq, role: role, kind: .text, text: text, toolName: "", at: "",
                                    permission: nil)
            entry.rows.append(Row(seq: nextSeq, item: item))
            nextSeq += 1
            stored[chatID] = entry
        }
    }

    func replace(chatID: String, item: ChatItemView) {
        locked {
            guard var entry = stored[chatID], let index = entry.rows.firstIndex(where: { $0.seq == item.seq })
            else { return }
            entry.rows[index].item = item
            stored[chatID] = entry
        }
    }

    /// A row the Mac consumes without sending it, so the cursor moves past it.
    func appendHidden(chatID: String) {
        locked {
            guard var entry = stored[chatID] else { return }
            entry.rows.append(Row(seq: nextSeq, item: nil))
            nextSeq += 1
            stored[chatID] = entry
        }
    }

    func setPending(chatID: String, requestID: String, options: [String] = ["allow", "deny"]) {
        locked {
            guard var entry = stored[chatID] else { return }
            entry.pending = ChatPermissionView(requestID: requestID,
                                               options: options.map { ChatPermissionOption(id: $0, label: $0.capitalized) })
            entry.pendingText = "Read the design notes folder?"
            stored[chatID] = entry
        }
    }

    func clearPending(chatID: String) {
        locked {
            guard var entry = stored[chatID] else { return }
            entry.pending = nil
            stored[chatID] = entry
        }
    }

    func setState(chatID: String, _ state: ChatState) {
        locked {
            guard var entry = stored[chatID] else { return }
            entry.header.state = state
            stored[chatID] = entry
        }
    }

    func delete(chatID: String) {
        locked {
            stored.removeValue(forKey: chatID)
            summaries.removeAll { $0.id == chatID }
        }
    }

    func failReads(_ errors: [Error]) {
        locked { readFailures = errors }
    }

    func failSends(_ errors: [Error]) {
        locked { sendFailures = errors }
    }

    func setLiveAnswer(_ answer: ChatLiveAnswer?) { locked { liveAnswer = answer } }

    /// The next send is stored and its turn started, but its answer never reaches the iPhone, as when the relay
    /// delivers the request after the iPhone stopped waiting.
    func loseNextAnswer() {
        locked { answersToLose += 1 }
    }

    var storedUserTexts: [String] {
        locked {
            let items: [ChatItemView] = stored.values.flatMap { entry in entry.rows.compactMap { $0.item } }
            let users = items.filter { $0.role == .user }.sorted { $0.seq < $1.seq }
            return users.map { $0.text }
        }
    }

    func failAnswers(_ errors: [Error]) {
        locked { answerFailures = errors }
    }

    func queueEvents(_ batch: [MobileChatTurnEvent]) {
        locked { eventBatches.append(batch) }
    }

    func failPolls(_ errors: [Error]) {
        locked { pollFailures = errors }
    }

    func holdNext(_ operation: String, using gate: HeldChatOperation) {
        locked { heldOperations[operation] = gate }
    }

    private func pauseIfRequested(_ operation: String) async throws {
        let gate = locked { heldOperations.removeValue(forKey: operation) }
        try await gate?.pause()
    }

    var afterValues: [Int?] { locked { readAfters } }
    var sent: [(text: String, key: String)] { locked { sendRecords } }
    var permissions: [(requestID: String, optionID: String, key: String)] { locked { permissionRecords } }
    var cancels: [String] { locked { cancelRecords } }
    var stoppedBriefTurns: [String] { locked { stoppedBriefTurnIDs } }
    var creates: [(title: String, key: String)] { locked { createRecords } }
    var sentTexts: [String] { locked { sendRecords.map(\.text) } }
    var sentOfferReferences: [[ChatOfferReference]] { locked { referenceRecords } }
    var polls: Int { locked { pollCount } }

    func chats(limit: Int) async throws -> [ChatSummaryView] {
        let result = locked { summaries }
        try await pauseIfRequested("list")
        return result
    }

    func chat(id: String, after: Int?, limit: Int) async throws -> ChatDetail {
        let result = try locked {
            readAfters.append(after)
            if !readFailures.isEmpty {
                throw readFailures.removeFirst()
            }
            guard let entry = stored[id] else {
                throw HubRequestError.rejected(status: 404, message: "That chat is not on your Mac.")
            }
            let newest = entry.rows.last?.seq ?? 0
            let consumed: [Row]
            let hasMore: Bool
            if let after {
                let waiting = entry.rows.filter { $0.seq > after }
                consumed = Array(waiting.prefix(limit))
                hasMore = waiting.count > consumed.count
            } else {
                consumed = Array(entry.rows.suffix(limit))
                hasMore = entry.rows.count > consumed.count
            }
            let cursor = consumed.last?.seq ?? (after ?? 0)
            var pending: ChatItemView?
            if !hasMore, let permission = entry.pending {
                pending = ChatItemView(seq: newest + 1, role: .system, kind: .permissionRequest,
                                       text: entry.pendingText, toolName: "", at: "", permission: permission)
            }
            return ChatDetail(chat: entry.header, items: consumed.compactMap(\.item), pendingPermission: pending,
                              cursor: cursor, hasMore: hasMore, live: liveAnswer)
        }
        try await pauseIfRequested("read")
        return result
    }

    func createChat(title: String, idempotencyKey: String) async throws -> String {
        locked {
            createRecords.append((title, idempotencyKey))
            let id = "chat-created-\(createRecords.count)"
            stored[id] = Stored(header: ChatHeaderView(id: id, title: title, state: .idle), rows: [])
            summaries.append(ChatSummaryView(id: id, title: title, updatedAt: "", state: .idle, kind: .chat))
            return id
        }
    }

    func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                     idempotencyKey: String) async throws -> ChatMessageAccepted {
        locked { referenceRecords.append(offerReferences) }
        return try await sendMessage(chatID: chatID, text: text, idempotencyKey: idempotencyKey)
    }

    var briefReferences: [ChatBriefReference] { locked { briefReferenceRecords } }

    func sendBriefMessage(chatID: String, text: String, reference: ChatBriefReference,
                          idempotencyKey: String) async throws -> ChatMessageAccepted {
        locked { briefReferenceRecords.append(reference) }
        return try await sendMessage(chatID: chatID, text: text, idempotencyKey: idempotencyKey)
    }

    func sendMessage(chatID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted {
        let result: Result<ChatMessageAccepted, Error> = Result {
            try locked {
                sendRecords.append((text, idempotencyKey))
                if let turn = receipts[idempotencyKey] {
                    return turn
                }
                if !sendFailures.isEmpty {
                    throw sendFailures.removeFirst()
                }
                guard var entry = stored[chatID] else {
                    throw HubRequestError.rejected(status: 404, message: "That chat is not on your Mac.")
                }
                if entry.header.state == .running {
                    throw ChatHubError.busy
                }
                let item = ChatItemView(seq: nextSeq, role: .user, kind: .text, text: text, toolName: "", at: "",
                                        permission: nil)
                entry.rows.append(Row(seq: nextSeq, item: item))
                nextSeq += 1
                entry.header.state = .running
                stored[chatID] = entry
                nextTurn += 1
                let accepted = ChatMessageAccepted(turnId: "turn-\(nextTurn)", seq: omitsSeq ? nil : item.seq)
                receipts[idempotencyKey] = accepted
                if answersToLose > 0 {
                    answersToLose -= 1
                    throw HubRequestError.noAnswer
                }
                return accepted
            }
        }
        try await pauseIfRequested("send")
        return try result.get()
    }

    func answerPermission(chatID: String, requestID: String, optionID: String, idempotencyKey: String) async throws -> Bool {
        try locked {
            permissionRecords.append((requestID, optionID, idempotencyKey))
            if !answerFailures.isEmpty {
                throw answerFailures.removeFirst()
            }
            guard var entry = stored[chatID] else {
                throw HubRequestError.rejected(status: 404, message: "That chat is not on your Mac.")
            }
            guard let pending = entry.pending, pending.requestID == requestID else {
                throw HubRequestError.rejection(status: 404, message: "no such pending request")
            }
            guard pending.options.contains(where: { $0.id == optionID }) else {
                throw HubRequestError.rejection(status: 400, message: "the option is not offered for this request")
            }
            entry.pending = nil
            stored[chatID] = entry
            return true
        }
    }

    func cancel(chatID: String, idempotencyKey: String) async throws -> Bool {
        locked {
            cancelRecords.append(idempotencyKey)
            guard var entry = stored[chatID], entry.header.state == .running else { return false }
            entry.header.state = .idle
            stored[chatID] = entry
            return true
        }
    }

    func stopBriefAsk(chatID: String, turnID: String, idempotencyKey: String) async throws -> Bool {
        locked {
            stoppedBriefTurnIDs.append(turnID)
            return receipts.values.contains { $0.turnId == turnID }
        }
    }

    func pollChatTurns() async throws -> [MobileChatTurnEvent] {
        let failure: Error? = locked {
            pollCount += 1
            return pollFailures.isEmpty ? nil : pollFailures.removeFirst()
        }
        try await pauseIfRequested("poll")
        if let failure { throw failure }
        let batch: [MobileChatTurnEvent]? = locked {
            eventBatches.isEmpty ? nil : eventBatches.removeFirst()
        }
        guard let batch else {
            try await Task.sleep(nanoseconds: 2_000_000)
            return []
        }
        return batch
    }
}

private final class ScriptedGuard: DecisionGuarding, @unchecked Sendable {
    private let lock = NSLock()
    private var denial: Error?
    private var heldAuthorization: HeldChatOperation?
    private(set) var reasons: [String] = []

    func deny(with error: Error) {
        lock.lock()
        defer { lock.unlock() }
        denial = error
    }

    func authorize(reason: String) async throws {
        if let failure = record(reason) { throw failure }
        try await authorizationGate()?.pause()
    }

    func holdAuthorization(using gate: HeldChatOperation) {
        lock.lock()
        defer { lock.unlock() }
        heldAuthorization = gate
    }

    private func authorizationGate() -> HeldChatOperation? {
        lock.lock()
        defer { lock.unlock() }
        defer { heldAuthorization = nil }
        return heldAuthorization
    }

    private func record(_ reason: String) -> Error? {
        lock.lock()
        defer { lock.unlock() }
        reasons.append(reason)
        return denial
    }
}

private final class KeyCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0

    func next() -> String {
        lock.lock()
        defer { lock.unlock() }
        count += 1
        return "key-\(count)"
    }
}

/// Counts presence checks and can hold the next one, the way a slow relay would.
private final class ScriptedPresence: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0
    private var gate: HeldChatOperation?
    private var refusal: Error?
    private var online = true

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }

    var calls: Int { locked { count } }

    func holdNext(using gate: HeldChatOperation) {
        locked { self.gate = gate }
    }

    /// Answers every later check with this relay refusal, or online again with nil.
    func refuse(with error: Error?) {
        locked { refusal = error }
    }

    func setOnline(_ online: Bool) {
        locked { self.online = online }
    }

    func check() async throws -> Bool {
        let gate: HeldChatOperation? = locked {
            count += 1
            defer { self.gate = nil }
            return self.gate
        }
        try await gate?.pause()
        if let refusal = locked({ refusal }) { throw refusal }
        return locked { online }
    }
}

private final class ChatTestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var instant = Date(timeIntervalSince1970: 1_800_000_000)

    func read() -> Date {
        lock.lock()
        defer { lock.unlock() }
        return instant
    }

    func advance(_ seconds: TimeInterval) {
        lock.lock()
        defer { lock.unlock() }
        instant.addTimeInterval(seconds)
    }
}

private func turn(_ chatID: String, seq: Int) -> MobileChatTurnEvent {
    MobileChatTurnEvent(event: "chat_turn", chatId: chatID, turnId: "turn-\(seq)", state: "done", seq: seq, at: "")
}

private func rememberMainChat(in cache: ChatCache, state: ChatState, text: String) throws {
    try cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "", state: state,
                                               kind: .main))
    try cache.save(ChatHeaderView(id: "main-chat", title: "Chat", state: state), items: [item(1, text: text)])
}

private func item(_ seq: Int, _ role: ChatRole = .assistant, text: String = "Reply") -> ChatItemView {
    ChatItemView(seq: seq, role: role, kind: .text, text: text, toolName: "", at: "", permission: nil)
}

private func permissionItem(_ seq: Int, requestID: String = "req-1", options: [String] = ["allow", "deny"]) -> ChatItemView {
    ChatItemView(seq: seq, role: .system, kind: .permissionRequest, text: "Read the design notes folder?",
                 toolName: "", at: "",
                 permission: ChatPermissionView(requestID: requestID,
                                                options: options.map { ChatPermissionOption(id: $0, label: $0.capitalized) }))
}

private struct ChatHarness {
    let hub: StubChatHub
    let guardian: ScriptedGuard
    let journal: PendingWriteJournal
    let cache: ChatCache
    let store: ChatStore
    /// Where the cache keeps its files.
    let chatsDirectory: URL
}

@MainActor
private func makeHarness(isPaired: @escaping @MainActor () -> Bool = { true },
                         isRemote: @escaping @MainActor () -> Bool = { false },
                         presence: @escaping @Sendable () async throws -> Bool = { true },
                         readTimeout: Duration = .seconds(20),
                         now: @escaping @Sendable () -> Date = { Date(timeIntervalSince1970: 1_800_000_000) }) throws -> ChatHarness {
    let hub = StubChatHub()
    let guardian = ScriptedGuard()
    let directory = try makeTemporaryDirectory()
    let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("desk-writes.json"))
    let cache = ChatCache(directory: directory.appendingPathComponent("chats", isDirectory: true))
    let keys = KeyCounter()
    let store = ChatStore(hub: hub, journal: journal, guardian: guardian, presence: presence, isPaired: isPaired,
                          cache: cache, readTimeout: readTimeout, isRemote: isRemote, now: now,
                          makeKey: { keys.next() })
    return ChatHarness(hub: hub, guardian: guardian, journal: journal, cache: cache, store: store,
                       chatsDirectory: directory.appendingPathComponent("chats", isDirectory: true))
}

@MainActor
final class ChatStoreTests: XCTestCase {
    func testChatDetailLiveAnswerIsOptionalAndDecoded() throws {
        let decoder = JSONDecoder()
        let old = try decoder.decode(ChatDetail.self, from: Data(#"{"items":[],"cursor":0,"hasMore":false}"#.utf8))
        XCTAssertNil(old.live)
        let current = try decoder.decode(ChatDetail.self, from: Data(#"{"items":[],"cursor":0,"hasMore":false,"live":{"turnId":"turn-1","text":"Writing"}}"#.utf8))
        XCTAssertEqual(current.live, ChatLiveAnswer(turnId: "turn-1", text: "Writing"))
        let oldStatus = try decoder.decode(HubStatus.self, from: Data(#"{}"#.utf8))
        XCTAssertNil(oldStatus.briefAsk)
        let status = try decoder.decode(HubStatus.self, from: Data(#"{"briefAsk":true}"#.utf8))
        XCTAssertEqual(status.briefAsk, true)
    }

    func testSavedBriefAskRouteEncodesTheDisplayedParagraph() throws {
        let route = HubRoute.briefAskAnswer(id: "brief-1", version: "version", paragraph: 2,
                                           text: "A & B + 中文")
        XCTAssertEqual(route.method, "GET")
        XCTAssertEqual(route.path, "/briefs/brief-1/ask-answer")
        var components = URLComponents()
        components.percentEncodedQuery = route.query
        let values = Dictionary(uniqueKeysWithValues: (components.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        XCTAssertEqual(values["text"], "A & B + 中文")
        XCTAssertEqual(values["paragraph"], "2")
        let payload = try JSONDecoder().decode(SavedBriefAskPayload.self,
            from: Data(#"{"answer":{"question":"Why?","answer":"Because."}}"#.utf8))
        XCTAssertEqual(payload.answer, SavedBriefAsk(question: "Why?", answer: "Because."))
        let card = HubRoute.briefCardAskAnswer(id: "brief-1", version: "version", cardID: "card-1", message: "A & B + 中文")
        components.percentEncodedQuery = card.query
        let cardValues = Dictionary(uniqueKeysWithValues: (components.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        XCTAssertEqual(card.path, route.path)
        XCTAssertEqual(cardValues["cardId"], "card-1")
        XCTAssertEqual(cardValues["cardMessage"], "A & B + 中文")
    }

    func testParagraphAskReceivesLiveAnswerFromChatTurnEvent() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Chat")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "", state: .idle, kind: .main))
        await h.store.refreshBriefAskPresence()
        let ref = ChatBriefReference(briefId: "brief", briefVersion: "revision", ask: true,
                                     paragraph: 0, paragraphText: "Selected paragraph")
        var queuedID: String?
        _ = await h.store.sendBriefAsk("What happened?", reference: ref) { _, id in queuedID = id }
        let message = try XCTUnwrap(h.store.outgoingMessages(in: "main-chat").first { $0.id == queuedID })
        let turnID = try XCTUnwrap(message.turnID)
        h.hub.setLiveAnswer(ChatLiveAnswer(turnId: turnID, text: "Partial answer"))
        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "main-chat",
                                                             turnId: turnID, state: "running", seq: 1, at: "")])
        XCTAssertEqual(h.store.transcript?.live, ChatLiveAnswer(turnId: turnID, text: "Partial answer"))
        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "other-chat",
                                                             turnId: "other-turn", state: "done", seq: 2, at: "")])
        XCTAssertEqual(h.store.recentTurnEvents.first { $0.chatId == "main-chat" && $0.turnId == turnID }?.state, "running")
        let stopped = await h.store.stopBriefAsk(chatID: "main-chat", turnID: turnID)
        XCTAssertTrue(stopped)
        XCTAssertEqual(h.hub.stoppedBriefTurns, [turnID])
        h.hub.setLiveAnswer(nil)
        h.hub.append(chatID: "main-chat", role: .assistant, text: "Finished answer")
        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "main-chat",
                                                             turnId: turnID, state: "done", seq: 2, at: "")])
        XCTAssertNil(h.store.transcript?.live)
        XCTAssertTrue(h.store.transcript?.items.contains { $0.text == "Finished answer" } == true)
    }

    func testParagraphAskQueuesWithItsReferenceWithoutChangingComposer() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Chat")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "", state: .idle, kind: .main))
        h.store.setDraft("Existing draft", in: "main-chat")
        h.hub.failSends([HubRequestError.noAnswer])
        let ref = ChatBriefReference(briefId: "brief", briefVersion: "revision", ask: true,
                                     paragraph: 0, paragraphText: "Selected paragraph")
        var queuedID: String?
        let accepted = await h.store.sendBriefAsk("What happened?", reference: ref) { _, id in queuedID = id }
        XCTAssertTrue(accepted)
        let id = try XCTUnwrap(queuedID)
        let pending = try XCTUnwrap(h.store.outgoingMessages(in: "main-chat").first { $0.id == id })
        XCTAssertEqual(pending.briefReference, ref)
        XCTAssertEqual(pending.deliveryIssue, .waitingForComputer)
        XCTAssertEqual(h.store.draft(in: "main-chat"), "Existing draft")
        let journaled = await h.journal.entries()
        let key = try XCTUnwrap(journaled.first?.key)
        let restored = relaunch(h)
        restored.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.sent.count < 2 || restored.isBusy("send:main-chat") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.hub.sent.map(\.key), [key, key])
        XCTAssertEqual(h.hub.briefReferences, [ref, ref])
        XCTAssertEqual(h.hub.storedUserTexts, ["What happened?"])
        XCTAssertEqual(restored.draft(in: "main-chat"), "Existing draft")
    }

    func testRefusedBriefAskRetriesWithItsSourceWithoutChangingTheComposer() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Chat")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "", state: .idle, kind: .main))
        h.store.setDraft("Existing draft", in: "main-chat")
        h.hub.failSends([HubRequestError.rejected(status: 400, message: "Not accepted")])
        let ref = ChatBriefReference(briefId: "brief", briefVersion: "revision", ask: true,
                                     paragraph: 0, paragraphText: "Selected paragraph")
        var failedID: String?
        _ = await h.store.sendBriefAsk("What happened?", reference: ref) { _, id in failedID = id }
        let id = try XCTUnwrap(failedID)
        XCTAssertTrue(h.store.outgoingMessages(in: "main-chat").first { $0.id == id }?.hasFailed == true)
        XCTAssertTrue(h.store.returnedDrafts.isEmpty)
        XCTAssertEqual(h.store.draft(in: "main-chat"), "Existing draft")

        let restored = relaunch(h)
        await restored.open(chatID: "main-chat")
        let retried = await restored.retryFailed(id, in: "main-chat")
        XCTAssertTrue(retried)
        XCTAssertEqual(h.hub.briefReferences, [ref, ref])
        XCTAssertEqual(h.hub.storedUserTexts, ["What happened?"])
        XCTAssertEqual(restored.draft(in: "main-chat"), "Existing draft")
        XCTAssertFalse(restored.outgoingMessages(in: "main-chat").contains { $0.id == id })
    }

    func testReopeningQueuedCardAskDoesNotCreateAnotherMessage() async throws {
        let presence = ScriptedPresence()
        presence.setOnline(false)
        let h = try makeHarness(presence: { try await presence.check() })
        h.hub.seed(chatID: "main-chat", title: "Chat")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "", state: .idle, kind: .main))
        await h.store.refreshBriefAskPresence()
        let ref = ChatBriefReference(briefId: "brief", briefVersion: "revision", cardId: "card",
                                     cardMessage: "What changed?", ask: true)
        var id: String?
        _ = await h.store.sendBriefAsk("What changed?", reference: ref) { _, value in id = value }
        let existing = h.store.existingBriefAsk(reference: ref, question: "What changed?")
        XCTAssertEqual(existing?.messageID, id)
        XCTAssertEqual(h.store.queuedMessageCount(in: "main-chat"), 1)
        XCTAssertTrue(h.hub.sent.isEmpty)
        XCTAssertTrue(h.store.draft(in: "main-chat").isEmpty)
    }

    func testOfflineBriefAskCanBeUndoneBeforeDispatchAndSurvivesRelaunch() async throws {
        let presence = ScriptedPresence()
        presence.setOnline(false)
        let h = try makeHarness(presence: { try await presence.check() })
        h.hub.seed(chatID: "main-chat", title: "Chat")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "", state: .idle, kind: .main))
        await h.store.refreshBriefAskPresence()
        let ref = ChatBriefReference(briefId: "brief", briefVersion: "revision", ask: true,
                                     paragraph: 0, paragraphText: "Selected paragraph")
        var firstID: String?
        let queued = await h.store.sendBriefAsk("Undo this", reference: ref) { _, id in firstID = id }
        XCTAssertTrue(queued)
        XCTAssertTrue(h.hub.sent.isEmpty)
        let firstWrites = await h.journal.entries()
        XCTAssertEqual(firstWrites.count, 1)
        let undone = await h.store.undoBriefAsk(chatID: "main-chat", messageID: try XCTUnwrap(firstID))
        XCTAssertTrue(undone)
        let remainingWrites = await h.journal.entries()
        XCTAssertTrue(remainingWrites.isEmpty)
        XCTAssertEqual(relaunch(h).queuedMessageCount(in: "main-chat"), 0)

        var secondID: String?
        _ = await h.store.sendBriefAsk("Send later", reference: ref) { _, id in secondID = id }
        let saved = relaunch(h)
        XCTAssertEqual(saved.outgoingMessages(in: "main-chat").first?.askUndispatched, true)
        presence.setOnline(true)
        saved.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.sent.isEmpty || saved.isBusy("send:main-chat") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.hub.storedUserTexts, ["Send later"])
        let lateUndo = await saved.undoBriefAsk(chatID: "main-chat", messageID: try XCTUnwrap(secondID))
        XCTAssertFalse(lateUndo)
    }

    func testStalledListShowsFailureAndCanRetry() async throws {
        let h = try makeHarness(readTimeout: .milliseconds(50))
        h.hub.seed(chatID: "chat-1", title: "Chat")
        let gate = HeldChatOperation()
        h.hub.holdNext("list", using: gate)
        await h.store.refreshList()
        guard case .failed = h.store.listState else { return XCTFail("stalled list did not fail") }
        await h.store.refreshList()
        XCTAssertEqual(h.store.listState, .loaded)
        XCTAssertEqual(h.store.chats.first?.id, "chat-1")
        await gate.release()
    }

    func testStalledTranscriptShowsFailureAndCanRetry() async throws {
        let h = try makeHarness(readTimeout: .milliseconds(50))
        h.hub.seed(chatID: "chat-1", title: "Chat", items: [item(1)])
        let gate = HeldChatOperation()
        h.hub.holdNext("read", using: gate)
        await h.store.open(chatID: "chat-1")
        guard case .failed = h.store.transcriptState else { return XCTFail("stalled transcript did not fail") }
        await h.store.open(chatID: "chat-1")
        XCTAssertEqual(h.store.transcriptState, .loaded)
        XCTAssertEqual(h.store.transcript?.items.count, 1)
        await gate.release()
    }
    func testOfflineDeliveryShowsWaitingImmediatelyAndPersistsItsReasonAcrossRelaunch() async throws {
        let h = try makeHarness(presence: { false })
        h.hub.seed(chatID: "chat-1", title: "Chat")
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)
        let sending = Task { await h.store.send("Keep this message") }
        await gate.waitUntilHeld()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first?.deliveryStatus(computerOnline: h.store.hubOnline),
                       .waitingForComputer, "the label is honest before the request timeout")
        await gate.release(error: HubRequestError.noAnswer)
        _ = await sending.value

        let restored = relaunch(h)
        XCTAssertEqual(restored.queuedMessageCount(in: "chat-1"), 1)
        XCTAssertEqual(restored.outgoingMessages(in: "chat-1").first?.deliveryIssue, .waitingForComputer)
        XCTAssertEqual(restored.outgoingMessages(in: "chat-1").first?.deliveryStatus(computerOnline: true),
                       .waitingForComputer, "a presence result cannot replace a delivery receipt")
        XCTAssertEqual(h.store.notice, ChatDeliveryStatus.waitingForComputer.explanation)
    }

    func testRemotePresenceRecoversFromOfflineToOnline() async throws {
        let presence = ScriptedPresence()
        let clock = ChatTestClock()
        presence.refuse(with: RelayClientError.offline)
        let h = try makeHarness(isRemote: { true }, presence: { try await presence.check() },
                                now: { clock.read() })

        await h.store.refreshList()
        XCTAssertEqual(h.store.hubOnline, false)

        presence.refuse(with: nil)
        clock.advance(ChatStore.eventPresenceInterval)
        await h.store.refreshList()

        XCTAssertEqual(h.store.hubOnline, true)
    }

    func testRemoteQueuedWriteResumesWithItsJournalKeyAfterRecovery() async throws {
        let h = try makeHarness(isRemote: { true })
        h.hub.seed(chatID: "chat-1", title: "Chat")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([RelayClientError.offline])

        let enqueued = await h.store.send("Send this after waking")
        let journaled = await h.journal.entries()
        let key = try XCTUnwrap(journaled.first?.key)
        XCTAssertTrue(enqueued)
        XCTAssertEqual(h.store.queuedMessageCount(in: "chat-1"), 1)

        h.hub.failSends([])
        h.store.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.sent.count < 2 || h.store.isBusy("send:chat-1") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.hub.sent.map(\.key), [key, key])
        XCTAssertEqual(h.store.queuedMessageCount(in: "chat-1"), 0)
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testWaitingCountIncludesEveryQueuedMessageAndClearsAfterOrderedDelivery() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer, HubRequestError.noAnswer])
        await h.store.send("First")
        await h.store.send("Second")
        XCTAssertEqual(h.store.queuedMessageCount(in: "chat-1"), 2)
        let queued = h.store.outgoingMessages(in: "chat-1")
        XCTAssertEqual(queued.first { $0.text == "First" }?.deliveryStatus(computerOnline: h.store.hubOnline), .sent,
                       "the relay holds the unanswered head: it shows as sent, without Undo")
        XCTAssertEqual(queued.first { $0.text == "Second" }?.deliveryStatus(computerOnline: true), .waitingForComputer,
                       "later queued messages wait behind the unanswered head even when presence returns")

        try await drainQueue(h)
        XCTAssertEqual(h.store.queuedMessageCount(in: "chat-1"), 0)
        XCTAssertEqual(h.hub.storedUserTexts, ["First", "Second"])
        XCTAssertEqual(Set(h.hub.sent.filter { $0.text == "First" }.map(\.key)).count, 1)
    }

    func testFailureReceiptStaysVisibleAfterDraftRecoveryWithoutRetryingIt() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.rejected(status: 400, message: "Not accepted")])
        await h.store.send("Edit this message")
        let failure = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(failure.deliveryStatus(computerOnline: true), .failed)
        XCTAssertEqual(h.store.queuedMessageCount(in: "chat-1"), 0)
        XCTAssertEqual(h.store.takeReturnedDrafts(in: "chat-1", appendingTo: ""), "Edit this message")
        let restored = relaunch(h)
        XCTAssertEqual(restored.outgoingMessages(in: "chat-1").first?.deliveryStatus(computerOnline: nil), .failed)
        XCTAssertEqual(restored.draft(in: "chat-1"), "Edit this message")
        XCTAssertTrue(restored.returnedDrafts.isEmpty)
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.first?.returnedToDraft, true,
                       "an older build must never auto-send a retained failure receipt after reverting")
        restored.resumeQueuedMessages()
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(h.hub.sent.count, 1)
    }

    override func setUp() {
        super.setUp()
        ChatStore.eventRetryBaseNanoseconds = 1_000_000
    }

    override func tearDown() {
        ChatStore.eventRetryBaseNanoseconds = 5_000_000_000
        super.tearDown()
    }

    func testTheListShowsChatsWithTheirStateAndKind() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .running)
        h.hub.seed(chatID: "chat-2", title: "Expense export", state: .waiting)

        await h.store.refreshList()

        XCTAssertEqual(h.store.listState, .loaded)
        XCTAssertEqual(h.store.chats.map(\.id), ["chat-1", "chat-2"])
        XCTAssertEqual(h.store.chats.map(\.state), [.running, .waiting])
        XCTAssertEqual(h.store.chats.first?.state.label, "Working")
    }

    func testAFailedListRefreshKeepsTheListAndSetsTheQuietFlagUntilTheNextSuccess() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.refreshList()
        XCTAssertEqual(h.store.listState, .loaded)
        XCTAssertFalse(h.store.listRefreshFailed)

        let gate = HeldChatOperation()
        h.hub.holdNext("list", using: gate)
        let failed = Task { await h.store.refreshList() }
        await gate.waitUntilHeld()
        await gate.release(error: HubRequestError.noAnswer)
        await failed.value

        XCTAssertEqual(h.store.chats.map(\.id), ["chat-1"], "a failed refresh keeps the list on screen")
        XCTAssertEqual(h.store.listState, .loaded)
        XCTAssertTrue(h.store.listRefreshFailed, "the chat screen shows its quiet refresh-failure line")

        await h.store.refreshList()
        XCTAssertFalse(h.store.listRefreshFailed, "the next success clears the flag")
    }

    func testOpeningAChatReadsTheNewestPageAndKeepsItOnDisk() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])

        await h.store.open(chatID: "chat-1")

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Hello"])
        XCTAssertEqual(h.store.transcript?.header.title, "Pricing sync")
        XCTAssertEqual(h.store.transcript?.cursor, 2)
        XCTAssertEqual(h.hub.afterValues, [nil])
        XCTAssertEqual(h.cache.load(chatID: "chat-1")?.items.map(\.seq), [1, 2])
    }

    func testOpeningALongChatShowsOnlyTheNewestPageAndDoesNotReadOlderRows() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Long", items: (1...120).map { item($0, text: "Message \($0)") })

        await h.store.open(chatID: "chat-1")

        XCTAssertEqual(h.store.transcript?.items.count, 50)
        XCTAssertEqual(h.store.transcript?.items.first?.seq, 71)
        XCTAssertEqual(h.store.transcript?.cursor, 120)
        XCTAssertEqual(h.hub.afterValues, [nil], "there is no backwards cursor, so the first read never follows hasMore")
    }

    func testAnActionRefreshReadsARevisionAtTheExistingSequence() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat", items: [item(1, text: "Waiting on you")])
        await h.store.open(chatID: "chat-1")
        h.hub.replace(chatID: "chat-1", item: item(1, text: "Done"))

        await h.store.refreshAfterProactiveAction(chatID: "chat-1")

        XCTAssertEqual(h.hub.afterValues, [nil, nil])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Done"])
        XCTAssertEqual(h.cache.load(chatID: "chat-1")?.items.map(\.text), ["Done"])
    }

    func testAnActionRefreshSupersedesAnOlderReadAndKeepsRowsOnFailure() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat", items: [item(1, text: "Waiting on you")])
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("read", using: gate)
        let oldRead = Task { await h.store.refreshOpen() }
        await gate.waitUntilHeld()
        h.hub.replace(chatID: "chat-1", item: item(1, text: "Done"))

        await h.store.refreshAfterProactiveAction(chatID: "chat-1")
        await gate.release()
        await oldRead.value
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Done"])

        h.hub.failReads([HubRequestError.noAnswer])
        await h.store.refreshAfterProactiveAction(chatID: "chat-1")
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Done"])
        XCTAssertTrue(h.store.transcriptRefreshFailed)
    }

    func testAnActionFinishingInAnotherChatDoesNotChangeTheSelection() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat")
        h.hub.seed(chatID: "chat-2", title: "Another topic")
        await h.store.open(chatID: "chat-2")

        await h.store.refreshAfterProactiveAction(chatID: "chat-1")

        XCTAssertEqual(h.store.openChatID, "chat-2")
        XCTAssertEqual(h.hub.afterValues, [nil])
    }

    func testIncrementalReadsAskOnlyForItemsAfterTheCursor() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])
        await h.store.open(chatID: "chat-1")

        h.hub.append(chatID: "chat-1", role: .assistant, text: "New reply")
        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues, [nil, 2])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Hello", "New reply"])
    }

    func testAnIncrementalStatusUpdateMovesTheExistingRowInsteadOfDuplicatingIt() async throws {
        let h = try makeHarness()
        let original = try JSONDecoder().decode(ChatItemView.self, from: Data(
            #"{"id":"status-morning","seq":1,"role":"system","kind":"status","text":"One update"}"#.utf8))
        let moved = try JSONDecoder().decode(ChatItemView.self, from: Data(
            #"{"id":"status-morning","seq":3,"role":"system","kind":"status","text":"Two updates"}"#.utf8))
        let reply = item(2, .user, text: "Conversation after the first update")
        let outgoing = ChatOutgoingMessage(id: "outgoing", text: reply.text, after: 1, sentAt: Date(),
                                           seq: reply.seq, state: .sent)
        try h.cache.saveOutgoing([outgoing], chatID: "main-chat")
        h.hub.seed(chatID: "main-chat", title: "Isoform", items: [original, reply])
        await h.store.open(chatID: "main-chat")
        let originalIdentity = try XCTUnwrap(h.store.transcript?.items.first?.id)

        h.hub.seed(chatID: "main-chat", title: "Isoform", items: [reply, moved])
        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues, [nil, 2])
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [2, 3])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), [reply.text, "Two updates"])
        XCTAssertEqual(h.store.transcript?.items.last?.id, originalIdentity)
        XCTAssertEqual(h.store.transcript?.cursor, 3)
        XCTAssertEqual(h.cache.load(chatID: "main-chat")?.items.map(\.seq), [2, 3])
        XCTAssertEqual(h.store.outgoingMessages(in: "main-chat").first?.state, .sent,
                       "moving a background status below a sent message must not mark the message Seen")

        await h.store.refreshOpen()
        XCTAssertEqual(h.store.transcript?.items.count, 2, "repeated refreshes must keep one current status row")
    }

    func testAStableIdentityReplacesALegacySnapshotAtTheSameSequence() async throws {
        let h = try makeHarness()
        let legacy = item(1, text: "Legacy snapshot")
        let current = try JSONDecoder().decode(ChatItemView.self, from: Data(
            #"{"id":"stored-row","seq":1,"role":"assistant","kind":"text","text":"Current snapshot"}"#.utf8))
        h.hub.seed(chatID: "main-chat", title: "Isoform", items: [legacy, current])

        await h.store.open(chatID: "main-chat")

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Current snapshot"])
        XCTAssertEqual(h.store.transcript?.items.first?.id, current.id)
        XCTAssertNotEqual(h.store.transcript?.items.first?.id, legacy.id)
    }

    func testAnOldCacheWithoutRowIDsIsReplacedBeforeIncrementalStatusUpdates() async throws {
        let h = try makeHarness()
        try rememberMainChat(in: h.cache, state: .idle, text: "Legacy status without an id")
        XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Legacy status without an id"])
        XCTAssertNil(h.store.transcript?.cursor, "cached seq values must never become an incremental cursor")
        let reply = item(2, text: "Conversation")
        let current = try JSONDecoder().decode(ChatItemView.self, from: Data(
            #"{"id":"status-morning","seq":3,"role":"assistant","kind":"status","text":"Two updates"}"#.utf8))
        h.hub.seed(chatID: "main-chat", title: "Isoform", items: [reply, current])

        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues, [nil], "the first live page replaces old cache rows rather than merging them")
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [2, 3])
        let moved = try JSONDecoder().decode(ChatItemView.self, from: Data(
            #"{"id":"status-morning","seq":4,"role":"assistant","kind":"status","text":"Three updates"}"#.utf8))
        h.hub.seed(chatID: "main-chat", title: "Isoform", items: [reply, moved])

        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues, [nil, 3])
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [2, 4])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Conversation", "Three updates"])
    }

    func testTheNextReadStartsFromTheMacsCursorNotFromTheLastItemSeq() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])
        h.hub.appendHidden(chatID: "chat-1")
        await h.store.open(chatID: "chat-1")
        XCTAssertEqual(h.store.transcript?.cursor, 3, "the Mac's cursor covers the row it did not send")

        h.hub.append(chatID: "chat-1", role: .assistant, text: "After the hidden row")
        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues, [nil, 3])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Hello", "After the hidden row"])
    }

    func testAChatThatWasEmptyReadsFromTheStartOnceItHasRows() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "New")
        await h.store.open(chatID: "chat-1")
        XCTAssertEqual(h.store.transcript?.cursor, 0)

        h.hub.append(chatID: "chat-1", role: .user, text: "First")
        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues, [nil, 0])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["First"])
    }

    func testLongCatchUpsFollowThePagesUntilTheMacHasNoMore() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Long", items: [item(1, .user, text: "Start")])
        await h.store.open(chatID: "chat-1")
        for index in 0..<120 {
            h.hub.append(chatID: "chat-1", role: .assistant, text: "Message \(index)")
        }

        await h.store.refreshOpen()

        XCTAssertEqual(h.store.transcript?.items.count, 121)
        XCTAssertEqual(h.hub.afterValues, [nil, 1, 51, 101])
    }

    func testACatchUpStopsAtTenPagesAndTheNextReadResumesFromTheCursor() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Long", items: [item(1, .user, text: "Start")])
        await h.store.open(chatID: "chat-1")
        for index in 0..<600 {
            h.hub.append(chatID: "chat-1", role: .assistant, text: "Message \(index)")
        }

        await h.store.refreshOpen()
        XCTAssertEqual(h.store.transcript?.items.count, 501, "ten pages of 50 after the first item")
        XCTAssertEqual(h.store.transcript?.cursor, 501)

        await h.store.refreshOpen()
        XCTAssertEqual(h.store.transcript?.items.count, 601)
        XCTAssertEqual(Array(h.hub.afterValues.suffix(2)), [501, 551], "the second read resumes at the stored cursor")
    }

    func testACachedChatShowsWhileTheMacIsSilentAndSaysSo() async throws {
        let offline = try makeHarness()
        try offline.cache.save(ChatHeaderView(id: "chat-1", title: "Pricing sync", state: .idle),
                               items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])
        offline.hub.failReads([HubRequestError.noAnswer])
        let cachedStore = ChatStore(hub: offline.hub, journal: offline.journal, guardian: offline.guardian,
                                    presence: { false }, isPaired: { true }, cache: offline.cache)
        await cachedStore.open(chatID: "chat-1")

        XCTAssertEqual(cachedStore.transcript?.items.map(\.text), ["Hi", "Hello"])
        XCTAssertNotNil(cachedStore.transcript?.asOf)
        XCTAssertNil(cachedStore.transcript?.cursor, "a cached transcript has no cursor, so the next read is the newest page")
        XCTAssertTrue(cachedStore.transcriptRefreshFailed)
        XCTAssertEqual(cachedStore.hubOnline, false)
        if case .failed = cachedStore.transcriptState {
            XCTFail("a cached chat must not show the failure screen")
        }
    }

    func testAFirstReadAfterACachedTranscriptReplacesItWithTheNewestPage() async throws {
        let h = try makeHarness()
        try h.cache.save(ChatHeaderView(id: "chat-1", title: "Pricing sync", state: .idle),
                         items: [item(1, .user, text: "Old"), item(2, text: "Old reply")])
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Old"),
                                                                    item(2, text: "Old reply"),
                                                                    item(3, .user, text: "New")])

        await h.store.open(chatID: "chat-1")

        XCTAssertEqual(h.hub.afterValues, [nil])
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [1, 2, 3])
        XCTAssertEqual(h.store.transcript?.cursor, 3)
    }

    func testASendUnderAKeyThatASecondTryReuses() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])

        let first = await h.store.send("Draft the reply to Sam")
        let kept = await h.journal.entries()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending],
                       "a message the Mac did not answer stays on screen as Sending")

        try await drainQueue(h)

        XCTAssertTrue(first)
        XCTAssertEqual(kept.map(\.operation), [.chatMessage])
        XCTAssertEqual(h.hub.sent.count, 2)
        XCTAssertEqual(Set(h.hub.sent.map(\.key)).count, 1, "the retry must reuse the key of the first try")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent])
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAMessageForARunningChatWaitsForTheTurnAndThenGoes() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .running)
        await h.store.open(chatID: "chat-1")

        let accepted = await h.store.send("One more thing")

        XCTAssertTrue(accepted, "the message waits on screen; it does not bounce back to the composer")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending])
        XCTAssertTrue(h.hub.sent.isEmpty, "nothing goes while the Mac is answering")
        XCTAssertNil(h.store.notice)

        try await drainQueue(h)

        XCTAssertEqual(h.hub.storedUserTexts, ["One more thing"])
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent])
    }

    func testABusyAnswerKeepsTheMessageQueuedAndKeepsNoKey() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        // The phone's copy of the chat says idle, but the Mac has just started a turn.
        h.hub.setState(chatID: "chat-1", .running)

        let accepted = await h.store.send("One more thing")

        XCTAssertTrue(accepted)
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending])
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty, "a busy answer means the Mac did not take the message, so no key is kept")
        try await drainQueue(h)
        XCTAssertEqual(h.hub.storedUserTexts, ["One more thing"])
    }

    func testABusyAnswerTellsTheSenderIsoformIsStillAnswering() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        // The phone's copy of the chat says idle, but the Mac has just started a turn.
        h.hub.setState(chatID: "chat-1", .running)

        let accepted = await h.store.send("One more thing")

        XCTAssertTrue(accepted)
        XCTAssertEqual(h.store.notice,
                       ChatDeliveryStatus.waitingForTurn.explanation)
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending],
                       "the message still waits for the turn to end")

        try await drainQueue(h)
        XCTAssertEqual(h.hub.storedUserTexts, ["One more thing"])
        XCTAssertNil(h.store.notice, "the notice clears once the message goes")
    }

    func testAnAcceptedMessageRereadsTheChatAndClearsItsKey() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")

        let accepted = await h.store.send("Summarize the pricing call")

        XCTAssertTrue(accepted)
        XCTAssertEqual(h.hub.sentTexts, ["Summarize the pricing call"])
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Summarize the pricing call"])
        XCTAssertEqual(h.store.transcript?.header.state, .running)
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAMessageOverSixteenKibIsRefusedBeforeItIsSent() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")

        let accepted = await h.store.send(String(repeating: "a", count: 16 * 1024 + 1))

        XCTAssertFalse(accepted)
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
        XCTAssertEqual(h.store.notice, "That message is too long for Isoform. Shorten it and try again.")
    }

    func testEachUnsentDraftSurvivesANewStoreAndCacheWithoutANetworkRead() throws {
        let h = try makeHarness()
        h.store.setDraft("  Finish the reply\nTomorrow ", in: "chat-1")
        h.store.setDraft("Keep the other topic", in: "chat-2")

        let next = relaunch(h)

        XCTAssertEqual(next.draft(in: "chat-1"), "  Finish the reply\nTomorrow ")
        XCTAssertEqual(next.draft(in: "chat-2"), "Keep the other topic")
        XCTAssertEqual(next.draft(in: "chat-3"), "")
        XCTAssertTrue(h.hub.afterValues.isEmpty, "restoring local text must not wait for the Mac")
        XCTAssertTrue(h.hub.sentTexts.isEmpty, "restoring a draft never sends it")
    }

    func testDeletingADraftKeepsItDeletedAfterRelaunch() throws {
        let h = try makeHarness()
        h.store.setDraft("Remove this", in: "chat-1")
        h.store.setDraft("Keep this", in: "chat-2")
        h.store.setDraft("", in: "chat-1")

        let next = relaunch(h)

        XCTAssertEqual(next.draft(in: "chat-1"), "")
        XCTAssertEqual(next.draft(in: "chat-2"), "Keep this")
    }

    func testSendingOrQueuingClearsOnlyThatChatsDraftAcrossRelaunch() async throws {
        let outcomes: [Error?] = [nil, HubRequestError.noAnswer]
        for error in outcomes {
            let h = try makeHarness()
            h.hub.seed(chatID: "chat-1", title: "Pricing sync")
            await h.store.open(chatID: "chat-1")
            h.store.setDraft("Send this", in: "chat-1")
            h.store.setDraft("Keep this", in: "chat-2")
            if let error { h.hub.failSends([error]) }

            let accepted = await h.store.send(h.store.draft(in: "chat-1"))
            let next = relaunch(h)

            XCTAssertTrue(accepted)
            XCTAssertEqual(next.draft(in: "chat-1"), "")
            XCTAssertEqual(next.draft(in: "chat-2"), "Keep this")
            XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.text), ["Send this"],
                           "the outgoing record owns the text before the composer lets it go")
        }
    }

    func testAnEditDuringASendSurvivesTheAcknowledgementAndRelaunch() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.store.setDraft("First message", in: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)
        let sending = Task { await h.store.send("First message") }
        await gate.waitUntilHeld()
        XCTAssertEqual(h.store.draft(in: "chat-1"), "")
        h.store.setDraft("The next thought", in: "chat-1")

        await gate.release()
        let accepted = await sending.value

        XCTAssertTrue(accepted)
        XCTAssertEqual(relaunch(h).draft(in: "chat-1"), "The next thought")
    }

    func testOversizedDraftStaysOnDiskWhenValidationRefusesIt() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        let text = String(repeating: "a", count: ChatStore.maxMessageBytes + 1)
        h.store.setDraft(text, in: "chat-1")

        let accepted = await h.store.send(text)

        XCTAssertFalse(accepted)
        XCTAssertEqual(relaunch(h).draft(in: "chat-1"), text)
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testAnOutgoingSaveFailureKeepsTheDraftAndDoesNotSend() async throws {
        let h = try makeHarness()
        // A file where the cache directory belongs makes its writes fail without changing permissions.
        try Data("not a directory".utf8).write(to: h.chatsDirectory)
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.store.setDraft("Keep this here", in: "chat-1")

        let accepted = await h.store.send("Keep this here")

        XCTAssertFalse(accepted)
        XCTAssertEqual(h.store.draft(in: "chat-1"), "Keep this here")
        XCTAssertTrue(h.store.outgoingMessages(in: "chat-1").isEmpty)
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testSameMacRepairKeepsDraftsAndPairingRemovalWipesThem() throws {
        let h = try makeHarness()
        h.store.setDraft("Private thought", in: "chat-1")

        h.store.pairingCommitted()
        XCTAssertEqual(relaunch(h).draft(in: "chat-1"), "Private thought")
        XCTAssertTrue(h.store.clearAfterUnpair())

        XCTAssertEqual(h.store.draft(in: "chat-1"), "")
        XCTAssertEqual(relaunch(h).draft(in: "chat-1"), "")
    }

    func testAnUnreadableComposerIsNotOverwrittenByANewEdit() throws {
        let h = try makeHarness()
        h.store.setDraft("Keep the saved thought", in: "chat-1")
        let file = try XCTUnwrap(outgoingFile(h))
        let before = try Data(contentsOf: file)
        try FileManager.default.setAttributes([.posixPermissions: 0o000], ofItemAtPath: file.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: file.path) }

        let next = relaunch(h)
        next.setDraft("An edit while storage cannot be read", in: "chat-1")

        XCTAssertEqual(next.notice, ChatCopy.draftSaveFailed)
        XCTAssertEqual(next.draft(in: "chat-1"), "An edit while storage cannot be read")
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: file.path)
        XCTAssertEqual(try Data(contentsOf: file), before)
    }

    func testTakingReturnedDraftsPersistsTheComposerInTheSameEnvelope() throws {
        let h = try makeHarness()
        try rememberMainChat(in: h.cache, state: .idle, text: "Hello")
        h.store.setDraft("Already typed", in: "main-chat")
        h.store.prefillMainChat("I want to create a goal about health")

        let text = h.store.takeReturnedDrafts(in: "main-chat", appendingTo: h.store.draft(in: "main-chat"))
        let next = relaunch(h)

        XCTAssertEqual(text, "Already typed\nI want to create a goal about health")
        XCTAssertEqual(next.draft(in: "main-chat"), text)
        XCTAssertTrue(next.returnedDrafts.isEmpty, "the durable transfer consumes the source only once")
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testARelaunchAfterCopyingAnotherChatsDraftDoesNotAppendItTwice() throws {
        let h = try makeHarness()
        try rememberMainChat(in: h.cache, state: .idle, text: "Hello")
        let returned = ChatOutgoingMessage(id: "returned-1", text: "The refused text", after: nil,
                                           sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                           state: .sending, returnedToDraft: true)
        try h.cache.saveOutgoing([returned], chatID: "other-chat")
        let recovered = "Already typed\n" + ChatCopy.recoveredFromChat("other-chat") + "\nThe refused text"
        // The destination reached disk, but the app stopped before cleaning the source file.
        try h.cache.saveComposerDraft(recovered, chatID: "main-chat", recoveredDraftIDs: ["other-chat:returned-1"])
        let next = relaunch(h)

        let text = next.takeReturnedDrafts(in: "main-chat", appendingTo: next.draft(in: "main-chat"))

        XCTAssertEqual(text, recovered)
        XCTAssertEqual(relaunch(h).draft(in: "main-chat"), recovered)
        XCTAssertTrue(next.returnedDrafts.isEmpty)
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "other-chat"), [])
    }

    func testAPermissionPromptIsTransientAndIsNeverStoredOrCached() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Read the notes")])
        h.hub.setPending(chatID: "chat-1", requestID: "req-1")

        await h.store.open(chatID: "chat-1")

        XCTAssertEqual(h.store.transcript?.pendingPermission?.permission?.requestID, "req-1")
        XCTAssertEqual(h.store.transcript?.pendingPermission?.seq, 2, "the prompt sits one past the newest stored row")
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [1])
        XCTAssertEqual(h.store.transcript?.cursor, 1, "a transient prompt never moves the cursor")
        XCTAssertEqual(h.cache.load(chatID: "chat-1")?.items.map(\.seq), [1])
    }

    func testAPermissionPromptDisappearsOnceTheLatestReadNoLongerHasIt() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Read the notes")])
        h.hub.setPending(chatID: "chat-1", requestID: "req-1")
        await h.store.open(chatID: "chat-1")
        XCTAssertNotNil(h.store.transcript?.pendingPermission)

        h.hub.clearPending(chatID: "chat-1")
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Done")
        await h.store.refreshOpen()

        XCTAssertNil(h.store.transcript?.pendingPermission)
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Read the notes", "Done"])
    }

    func testAPermissionAnswerNeedsTheOwnerCheckAndSendsNothingWithoutIt() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        h.hub.setPending(chatID: "chat-1", requestID: "req-1")
        await h.store.open(chatID: "chat-1")
        h.guardian.deny(with: DecisionGuardError.notConfirmed)

        let answered = await h.store.answerPermission(permissionItem(2), optionID: "allow")

        XCTAssertFalse(answered)
        XCTAssertTrue(h.hub.permissions.isEmpty)
        XCTAssertEqual(h.guardian.reasons.count, 1)
        XCTAssertFalse(h.store.isAnswered("req-1"))
    }

    func testAPermissionAnswerSendsTheOptionUnderAKeyAndClosesTheCard() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        h.hub.setPending(chatID: "chat-1", requestID: "req-1")
        await h.store.open(chatID: "chat-1")

        let answered = await h.store.answerPermission(permissionItem(2), optionID: "allow")

        XCTAssertTrue(answered)
        XCTAssertEqual(h.hub.permissions.map(\.requestID), ["req-1"])
        XCTAssertEqual(h.hub.permissions.map(\.optionID), ["allow"])
        XCTAssertTrue(h.store.isAnswered("req-1"))
        XCTAssertNil(h.store.transcript?.pendingPermission)
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAnOptionTheMacNoLongerOffersGetsACalmNoticeAndARead() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        h.hub.setPending(chatID: "chat-1", requestID: "req-1")
        await h.store.open(chatID: "chat-1")
        let readsBefore = h.hub.afterValues.count

        let answered = await h.store.answerPermission(permissionItem(2, options: ["allow", "deny", "always"]),
                                                     optionID: "always")

        XCTAssertFalse(answered)
        XCTAssertEqual(h.store.notice, "That answer is no longer offered for this request on your computer. Check the chat again.")
        XCTAssertGreaterThan(h.hub.afterValues.count, readsBefore, "the chat is read again")
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty, "a refused answer is not kept for a retry")
    }

    func testARequestThatIsNoLongerPendingGetsACalmNoticeAndARead() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        h.hub.setPending(chatID: "chat-1", requestID: "req-2")
        await h.store.open(chatID: "chat-1")
        let readsBefore = h.hub.afterValues.count

        let answered = await h.store.answerPermission(permissionItem(2, requestID: "req-1"), optionID: "allow")

        XCTAssertFalse(answered)
        XCTAssertEqual(h.store.notice, "This request is no longer waiting on your computer. Check the chat again.")
        XCTAssertGreaterThan(h.hub.afterValues.count, readsBefore)
    }

    func testARequestAnsweredElsewhereGetsTheAlreadyAnsweredNotice() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        h.hub.setPending(chatID: "chat-1", requestID: "req-1")
        await h.store.open(chatID: "chat-1")
        h.hub.failAnswers([HubRequestError.rejection(status: 409, message: "this request is no longer pending")])

        let answered = await h.store.answerPermission(permissionItem(2), optionID: "allow")

        XCTAssertFalse(answered)
        XCTAssertEqual(h.store.notice, "This request was already answered on your computer.")
        XCTAssertFalse(h.store.isAnswered("req-1"), "a refused answer does not close the card as if it succeeded")
    }

    func testStopSendsCancelUnderAKey() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .running)
        await h.store.open(chatID: "chat-1")

        let cancelled = await h.store.cancelTurn()

        XCTAssertTrue(cancelled)
        XCTAssertEqual(h.hub.cancels.count, 1)
        XCTAssertEqual(h.store.notice, "Stopping. Isoform will stop after its current step.")
    }

    func testStopWhenNothingIsRunningShowsNothing() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .idle)
        await h.store.open(chatID: "chat-1")

        let cancelled = await h.store.cancelTurn()

        XCTAssertFalse(cancelled)
        XCTAssertEqual(h.hub.cancels.count, 1)
        XCTAssertNil(h.store.notice)
    }

    func testATurnEventForTheOpenChatRereadsIt() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Done")

        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "t-1",
                                                             state: "done", seq: 2, at: "")])

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Done"])
        XCTAssertEqual(h.hub.afterValues.last, 1)
    }

    func testEmptyRelayPollsRefreshComputerPresenceWithoutRepeatingEveryPoll() async throws {
        let presence = ScriptedPresence()
        let clock = ChatTestClock()
        let h = try makeHarness(presence: { try await presence.check() }, now: { clock.read() })
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .running)
        await h.store.open(chatID: "chat-1")
        XCTAssertTrue(ChatView.showsRunningState(h.store.transcript?.header.state, computerOnline: h.store.hubOnline))

        let gate = HeldChatOperation()
        h.hub.holdNext("poll", using: gate)
        let listener = Task { await h.store.watchForEvents() }
        defer { listener.cancel() }
        await gate.waitUntilHeld()
        presence.setOnline(false)
        clock.advance(ChatStore.eventPresenceInterval)
        await gate.release()
        for _ in 0..<200 where h.store.hubOnline != false || h.hub.polls < 4 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.hubOnline, false, "An available relay must not hide a computer that went offline")
        XCTAssertFalse(ChatView.showsRunningState(h.store.transcript?.header.state, computerOnline: h.store.hubOnline))
        XCTAssertEqual(presence.calls, 2, "Rapid empty polls share the bounded presence refresh")
        XCTAssertEqual(h.hub.afterValues.count, 1, "Checking presence does not repeatedly reload the transcript")
        listener.cancel()
        await listener.value
    }

    func testPollFailureInvalidatesTheLastOnlineRunningState() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .running)
        await h.store.open(chatID: "chat-1")
        let failedPoll = HeldChatOperation()
        h.hub.holdNext("poll", using: failedPoll)
        let listener = Task { await h.store.watchForEvents() }
        defer { listener.cancel() }
        await failedPoll.waitUntilHeld()
        let nextPoll = HeldChatOperation()
        h.hub.holdNext("poll", using: nextPoll)
        await failedPoll.release(error: HubRequestError.noAnswer)
        await nextPoll.waitUntilHeld()

        XCTAssertNil(h.store.hubOnline)
        XCTAssertFalse(ChatView.showsRunningState(h.store.transcript?.header.state, computerOnline: h.store.hubOnline))
        listener.cancel()
        await listener.value
        await nextPoll.release()
    }

    func testFailedTranscriptRefreshCannotKeepOrRestoreAnOnlineAnsweringClaim() async throws {
        let presence = ScriptedPresence()
        let h = try makeHarness(presence: { try await presence.check() })
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", state: .running)
        await h.store.open(chatID: "chat-1")
        let latePresence = HeldChatOperation()
        presence.holdNext(using: latePresence)
        // Start presence through the list so no overlapping transcript read can make refreshOpen
        // return after merely queuing another pass. This refresh below must consume the failure itself.
        let listing = Task { await h.store.refreshList() }
        await latePresence.waitUntilHeld()
        h.hub.failReads([HubRequestError.noAnswer])
        await h.store.refreshOpen()

        XCTAssertEqual(h.hub.afterValues.count, 2)
        XCTAssertTrue(h.store.transcriptRefreshFailed, "The failed read must finish before checking presence")
        XCTAssertNil(h.store.hubOnline)
        await latePresence.release()
        await listing.value
        XCTAssertNil(h.store.hubOnline, "A presence read started before the failed refresh cannot restore stale certainty")
        XCTAssertFalse(ChatView.showsRunningState(h.store.transcript?.header.state, computerOnline: h.store.hubOnline))
    }

    func testAfterAPollFailureTheListenerRereadsTheChatOnReconnect() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Missed while offline")
        h.hub.failPolls([HubRequestError.noAnswer])
        h.hub.queueEvents([])

        let listener = Task { @MainActor in
            await h.store.watchForEvents()
        }
        for _ in 0..<200 where h.store.transcript?.items.count != 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        listener.cancel()

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Missed while offline"])
    }

    func testPairingTransitionCancellationResumesTheEventListener() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Arrived during pairing")
        h.hub.failPolls([CancellationError(), CancellationError()])
        h.hub.queueEvents([])

        let listener = Task { await h.store.watchForEvents() }
        defer { listener.cancel() }
        for _ in 0..<200 where h.store.transcript?.items.count != 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Arrived during pairing"])

        h.hub.append(chatID: "chat-1", role: .assistant, text: "A later event")
        h.hub.queueEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "t-2",
                                               state: "done", seq: 3, at: "")])
        for _ in 0..<200 where h.store.transcript?.items.count != 3 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        listener.cancel()
        await listener.value

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Arrived during pairing", "A later event"])
        XCTAssertGreaterThanOrEqual(h.hub.polls, 4)
    }

    func testCancellingTheListenerTaskDoesNotRetryThePoll() async throws {
        let h = try makeHarness()
        let gate = HeldChatOperation()
        h.hub.holdNext("poll", using: gate)
        let listener = Task { await h.store.watchForEvents() }
        await gate.waitUntilHeld()

        listener.cancel()
        await gate.release(error: CancellationError())
        await listener.value

        XCTAssertEqual(h.hub.polls, 1)
    }

    func testReturningToChatStartsAReplacementWatcherAfterTheCancelledPollDrains() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat", items: [item(1, text: "Before leaving Chat")])
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("poll", using: gate)
        let oldWatcher = Task { await h.store.watchForEvents() }
        await gate.waitUntilHeld()

        oldWatcher.cancel()
        let replacementStarted = expectation(description: "The returning Chat tab starts its watcher")
        let replacement = Task {
            replacementStarted.fulfill()
            await h.store.watchForEvents()
        }
        await fulfillment(of: [replacementStarted], timeout: 1)
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Arrived while away")
        h.hub.queueEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "turn-2",
                                               state: "done", seq: 2, at: "")])
        await gate.release(error: CancellationError())
        await oldWatcher.value
        for _ in 0..<200 where h.store.transcript?.items.count != 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Before leaving Chat", "Arrived while away"])
        XCTAssertGreaterThanOrEqual(h.hub.polls, 2, "Returning to Chat must resume polling without a third watcher")
        replacement.cancel()
        await replacement.value
    }

    func testUnpairingDuringAPollCancellationStopsTheListener() async throws {
        var paired = true
        let h = try makeHarness(isPaired: { paired })
        let gate = HeldChatOperation()
        h.hub.holdNext("poll", using: gate)
        let listener = Task { await h.store.watchForEvents() }
        await gate.waitUntilHeld()

        paired = false
        await gate.release(error: CancellationError())
        await listener.value

        XCTAssertEqual(h.hub.polls, 1)
    }

    func testClearingThePairingDiscardsALateListOrFailure() async throws {
        let outcomes: [Error?] = [nil, HubRequestError.noAnswer]
        for error in outcomes {
            let h = try makeHarness()
            h.hub.seed(chatID: "old-chat", title: "Old Mac")
            let gate = HeldChatOperation()
            h.hub.holdNext("list", using: gate)
            let refresh = Task { await h.store.refreshList() }
            await gate.waitUntilHeld()

            h.store.clearAfterUnpair()
            await gate.release(error: error)
            await refresh.value

            XCTAssertTrue(h.store.chats.isEmpty)
            XCTAssertEqual(h.store.listState, .idle)
            XCTAssertFalse(h.store.listRefreshFailed)
            XCTAssertNil(h.store.hubOnline)
        }
    }

    func testClearingThePairingDiscardsALateTranscriptWithoutBlockingTheNewChat() async throws {
        let outcomes: [Error?] = [nil, HubRequestError.noAnswer]
        for error in outcomes {
            let h = try makeHarness()
            h.hub.seed(chatID: "old-chat", title: "Old Mac", items: [item(1, text: "Old work")])
            let gate = HeldChatOperation()
            h.hub.holdNext("read", using: gate)
            let oldRead = Task { await h.store.open(chatID: "old-chat") }
            await gate.waitUntilHeld()

            h.store.clearAfterUnpair()
            h.hub.seed(chatID: "new-chat", title: "New Mac", items: [item(1, text: "New work")])
            await h.store.open(chatID: "new-chat")
            XCTAssertEqual(h.store.transcript?.header.id, "new-chat")
            await gate.release(error: error)
            await oldRead.value

            XCTAssertEqual(h.store.transcript?.header.id, "new-chat")
            XCTAssertEqual(h.store.transcript?.items.map(\.text), ["New work"])
            XCTAssertEqual(h.store.transcriptState, .loaded)
            XCTAssertFalse(h.store.transcriptRefreshFailed)
            XCTAssertNil(h.cache.load(chatID: "old-chat"))
            XCTAssertEqual(h.cache.load(chatID: "new-chat")?.items.map(\.text), ["New work"])
        }
    }

    func testClearingThePairingDiscardsALateSendResultAndNotice() async throws {
        let outcomes: [Error?] = [nil, HubRequestError.forbidden]
        for error in outcomes {
            let h = try makeHarness()
            h.hub.seed(chatID: "old-chat", title: "Old Mac", items: [item(1, text: "Old work")])
            await h.store.open(chatID: "old-chat")
            let gate = HeldChatOperation()
            h.hub.holdNext("send", using: gate)
            let send = Task { await h.store.send("Continue") }
            await gate.waitUntilHeld()

            h.store.clearAfterUnpair()
            h.store.notice = "The new Mac is ready."
            await gate.release(error: error)
            let accepted = await send.value

            XCTAssertTrue(accepted, "the composer already handed this message to the durable queue")
            XCTAssertEqual(h.store.notice, "The new Mac is ready.")
            XCTAssertTrue(h.store.busyTargets.isEmpty)
            XCTAssertNil(h.store.transcript)
            XCTAssertNil(h.cache.load(chatID: "old-chat"))
        }
    }

    func testAnOldSendCannotReleaseTheNewPairingsBusyTarget() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat")
        await h.store.open(chatID: "chat-1")
        let oldGate = HeldChatOperation()
        h.hub.holdNext("send", using: oldGate)
        let oldSend = Task { await h.store.send("Old work") }
        await oldGate.waitUntilHeld()

        h.store.clearAfterUnpair()
        h.hub.setState(chatID: "chat-1", .idle)
        await h.store.open(chatID: "chat-1")
        let newGate = HeldChatOperation()
        h.hub.holdNext("send", using: newGate)
        let newSend = Task { await h.store.send("New work") }
        await newGate.waitUntilHeld()
        await oldGate.release(error: HubRequestError.noAnswer)
        let oldAccepted = await oldSend.value

        XCTAssertTrue(oldAccepted, "the old pairing's queued text must not be restored in the new pairing")
        XCTAssertTrue(h.store.isBusy("send:chat-1"))
        XCTAssertNil(h.store.notice)
        await newGate.release()
        let newAccepted = await newSend.value
        XCTAssertTrue(newAccepted)
        XCTAssertFalse(h.store.isBusy("send:chat-1"))
    }

    func testClearingThePairingWhileAuthenticatingDoesNotAnswerTheOldPermission() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "old-chat", title: "Old Mac", state: .waiting)
        h.hub.setPending(chatID: "old-chat", requestID: "req-1")
        await h.store.open(chatID: "old-chat")
        let permission = try XCTUnwrap(h.store.transcript?.pendingPermission)
        let gate = HeldChatOperation()
        h.guardian.holdAuthorization(using: gate)
        let answer = Task { await h.store.answerPermission(permission, optionID: "allow") }
        await gate.waitUntilHeld()

        h.store.clearAfterUnpair()
        await gate.release()
        let answered = await answer.value

        XCTAssertFalse(answered)
        XCTAssertTrue(h.hub.permissions.isEmpty)
        XCTAssertTrue(h.store.answeredRequestIDs.isEmpty)
        XCTAssertTrue(h.store.busyTargets.isEmpty)
        XCTAssertNil(h.store.notice)
        let writes = await h.journal.entries()
        XCTAssertTrue(writes.isEmpty)
    }

    func testCreatingAChatUsesAKeyAndShowsItInTheList() async throws {
        let h = try makeHarness()

        let id = await h.store.createChat(title: "  Quarterly review  ")

        XCTAssertEqual(id, "chat-created-1")
        XCTAssertEqual(h.hub.creates.map(\.title), ["Quarterly review"])
        XCTAssertEqual(h.store.chats.map(\.id), ["chat-created-1"])
    }

    func testAnEmptyTitleBecomesNewChat() async throws {
        let h = try makeHarness()

        _ = await h.store.createChat(title: "   ")

        XCTAssertEqual(h.hub.creates.map(\.title), ["New chat"])
    }

    func testClearingAfterUnpairRemovesEveryCachedChat() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        XCTAssertNotNil(h.cache.load(chatID: "chat-1"))

        h.store.clearAfterUnpair()

        XCTAssertNil(h.cache.load(chatID: "chat-1"))
        XCTAssertNil(h.store.transcript)
        XCTAssertTrue(h.store.chats.isEmpty)
    }

    func testFirstPollFailureGetsOneFastRetryBeforeTheNormalBackoff() async throws {
        ChatStore.eventRetryBaseNanoseconds = 5_000_000_000
        let h = try makeHarness()
        h.hub.failPolls([RelayClientError.offline, RelayClientError.offline])
        let started = ProcessInfo.processInfo.systemUptime
        let listener = Task { await h.store.watchForEvents() }
        for _ in 0..<100 where h.hub.polls < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.hub.polls, 2, "The first connection failure must get one immediate retry")
        XCTAssertLessThan(ProcessInfo.processInfo.systemUptime - started, 1)
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(h.hub.polls, 2, "Repeated failures must retain the normal backoff")
        listener.cancel()
        await listener.value
    }

    func testEncryptedRelayMeasuresSerialBaselineAndCachedMainChatStartup() async throws {
        let baseline = try startupHarness()
        let baselineStart = ProcessInfo.processInfo.systemUptime
        // This is the pre-fix view/store sequence, retained explicitly so the measurement does not
        // silently become the optimized implementation: presence, list and ACK, presence, detail and ACK.
        _ = try await baseline.relay.presence()
        let summaries = try await baseline.channel.chats(limit: ChatStore.listLimit)
        let firstAck = await baseline.relay.waitForAcknowledgements(1)
        XCTAssertTrue(firstAck)
        let baselineNavigationElapsed = ProcessInfo.processInfo.systemUptime - baselineStart
        _ = try await baseline.relay.presence()
        let main = try XCTUnwrap(summaries.first { $0.kind == .main })
        let detail = try await baseline.channel.chat(id: main.id, after: nil, limit: ChatStore.pageLimit)
        let secondAck = await baseline.relay.waitForAcknowledgements(2)
        XCTAssertTrue(secondAck)
        let baselineElapsed = ProcessInfo.processInfo.systemUptime - baselineStart
        let baselineCounts = await baseline.relay.counts()
        XCTAssertEqual(detail.items.map(\.text), ["Fresh chat reply"])
        XCTAssertEqual(baselineCounts.paths, ["/chats", "/chats/main-chat"])
        XCTAssertEqual(baselineCounts.presence, 2)
        XCTAssertEqual(baselineCounts.polls, 2)
        XCTAssertEqual(baselineCounts.acknowledgements, 2)

        let optimized = try startupHarness(cached: true)
        let start = ProcessInfo.processInfo.systemUptime
        let mainID = try XCTUnwrap(optimized.store.prepareMainChat())
        let cachedElapsed = ProcessInfo.processInfo.systemUptime - start
        let beforeRender = await optimized.relay.counts()
        XCTAssertEqual(optimized.store.transcript?.items.map(\.text), ["Cached chat reply"])
        XCTAssertNil(optimized.store.transcript?.cursor)
        XCTAssertEqual(optimized.store.transcriptState, .loaded)
        XCTAssertEqual(beforeRender.total, 0, "Cached first render must not require any relay round trip")
        XCTAssertLessThan(cachedElapsed, 1)

        let opening = Task { await optimized.store.open(chatID: mainID) }
        await waitForFreshTranscript(optimized.store)
        let freshElapsed = ProcessInfo.processInfo.systemUptime - start
        let freshCounts = await optimized.relay.counts()
        XCTAssertEqual(optimized.store.transcript?.items.map(\.text), ["Fresh chat reply"])
        XCTAssertLessThan(freshElapsed, 3)
        XCTAssertLessThan(freshElapsed, baselineElapsed)
        XCTAssertEqual(freshCounts.paths, ["/chats/main-chat"], "Only one hub read is needed for a known chat")
        XCTAssertEqual(freshCounts.polls, 1)
        await opening.value
        await baseline.channel.prepareForPairingChange()
        await optimized.channel.prepareForPairingChange()
        recordStartupMeasurement(String(format:
            "FIRST_LOAD_BENCHMARK serial_navigation=%.3fs serial_fresh=%.3fs cached=%.3fs fresh=%.3fs serial_http_round_trips=8 cached_http_round_trips=0 fresh_hub_reads=1 fixture_presence=0.400s fixture_post=0.100s fixture_reply=0.250s fixture_ack=0.100s",
            baselineNavigationElapsed, baselineElapsed, cachedElapsed, freshElapsed))
    }

    func testCachedAndFreshMainChatDoNotWaitForSlowPresenceListBriefOrAcknowledgement() async throws {
        let h = try startupHarness(cached: true, presenceDelay: 4, slowRoutes: ["/chats", "/brief/latest"],
                                   acknowledgementDelay: 4)
        let start = ProcessInfo.processInfo.systemUptime
        let mainID = try XCTUnwrap(h.store.prepareMainChat())
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Cached chat reply"])
        let cachedCounts = await h.relay.counts()
        XCTAssertEqual(cachedCounts.total, 0)
        XCTAssertLessThan(ProcessInfo.processInfo.systemUptime - start, 1)

        let listing = Task { await h.store.refreshList() }
        let briefing = Task { try? await h.channel.latestBrief() }
        let opening = Task { await h.store.open(chatID: mainID) }
        await waitForFreshTranscript(h.store)
        let freshElapsed = ProcessInfo.processInfo.systemUptime - start
        let counts = await h.relay.counts()
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Fresh chat reply"])
        XCTAssertLessThan(freshElapsed, 3)
        XCTAssertEqual(counts.paths.filter { $0 == "/chats/main-chat" }.count, 1)
        XCTAssertTrue(counts.paths.contains("/chats"))
        XCTAssertTrue(counts.paths.contains("/brief/latest"))
        XCTAssertEqual(counts.presenceCompleted, 0)
        XCTAssertFalse(counts.completedPaths.contains("/chats"))
        XCTAssertFalse(counts.completedPaths.contains("/brief/latest"))
        XCTAssertEqual(counts.acknowledgements, 0, "A slow relay ACK must not hold the transcript")
        listing.cancel()
        briefing.cancel()
        opening.cancel()
        await h.channel.prepareForPairingChange()
        await listing.value
        _ = await briefing.value
        await opening.value
        recordStartupMeasurement(String(format:
            "FIRST_LOAD_SLOW_DEPENDENCIES fresh=%.3fs presence_list_brief_ack_delay=4.000s cached_http_round_trips=0 fresh_hub_reads=1",
            freshElapsed))
    }

    func testColdMainChatDiscoveryPublishesBeforeSlowPresenceReturns() async throws {
        let h = try startupHarness(presenceDelay: 4)
        XCTAssertNil(h.store.prepareMainChat())
        let start = ProcessInfo.processInfo.systemUptime
        let listing = Task { await h.store.refreshList() }
        for _ in 0..<250 where h.store.chats.isEmpty {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        let mainID = try XCTUnwrap(h.store.chats.first { $0.kind == .main }?.id)
        let opening = Task { await h.store.open(chatID: mainID) }
        await waitForFreshTranscript(h.store)
        let freshElapsed = ProcessInfo.processInfo.systemUptime - start
        let counts = await h.relay.counts()
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Fresh chat reply"])
        XCTAssertLessThan(freshElapsed, 3)
        XCTAssertEqual(counts.paths, ["/chats", "/chats/main-chat"])
        XCTAssertEqual(counts.presenceCompleted, 0)
        listing.cancel()
        opening.cancel()
        await h.channel.prepareForPairingChange()
        await listing.value
        await opening.value
        recordStartupMeasurement(String(format:
            "FIRST_LOAD_COLD fresh=%.3fs presence_delay=4.000s hub_reads=2", freshElapsed))
    }

    func testKnownMainChatWithoutCachedRowsStartsLoadingWithNoRelayRequests() async throws {
        let h = try startupHarness(knownMain: true)

        XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
        XCTAssertEqual(h.store.openChatID, "main-chat")
        XCTAssertNil(h.store.transcript)
        XCTAssertEqual(h.store.transcriptState, .loading, "A missing snapshot must render a skeleton, not an empty chat")
        let counts = await h.relay.counts()
        XCTAssertEqual(counts.total, 0)
    }

    func testPreparingMainChatDoesNotGuessWhenThereIsNoCachedMainIdentity() throws {
        let h = try makeHarness()
        try h.cache.save(ChatHeaderView(id: "side-chat", title: "A side chat", state: .idle), items: [item(1)])

        XCTAssertNil(h.store.prepareMainChat())
        XCTAssertNil(h.store.openChatID)
        XCTAssertNil(h.store.transcript)
        XCTAssertEqual(h.store.transcriptState, .idle)
    }

    func testSwitchingChatsDoesNotWaitForACancelledOldReadOrPublishItsResult() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "old-chat", title: "Old chat", items: [item(1, text: "Old reply")])
        h.hub.seed(chatID: "new-chat", title: "New chat", items: [item(1, text: "New reply")])
        let gate = HeldChatOperation()
        h.hub.holdNext("read", using: gate)
        let oldRead = Task { await h.store.open(chatID: "old-chat") }
        await gate.waitUntilHeld()

        oldRead.cancel()
        await h.store.open(chatID: "new-chat")

        XCTAssertEqual(h.store.transcript?.header.id, "new-chat")
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["New reply"])
        XCTAssertEqual(h.store.transcriptState, .loaded)
        await gate.release()
        await oldRead.value
        XCTAssertEqual(h.store.transcript?.header.id, "new-chat")
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["New reply"])
        XCTAssertFalse(h.store.transcriptRefreshFailed)
        XCTAssertNil(h.cache.load(chatID: "old-chat"))
    }

    func testARefreshQueuedBehindACancelledReadStillRuns() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("read", using: gate)
        let viewRead = Task { await h.store.refreshOpen() }
        await gate.waitUntilHeld()

        h.hub.append(chatID: "chat-1", role: .assistant, text: "Arrived during the read")
        await h.store.refreshOpen()
        viewRead.cancel()
        await gate.release()
        await viewRead.value
        for _ in 0..<200 where h.store.transcript?.items.count != 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Arrived during the read"],
                       "A refresh asked for during a read must run after that read, even a cancelled one")
        XCTAssertEqual(h.hub.afterValues, [nil, 1, 1])
    }

    func testARevokedPairingAsksToPairAgainInsteadOfOpeningTheCachedMainChat() async throws {
        for refusal in [RelayClientError.unauthorized, RelayClientError.forbidden] {
            let presence = ScriptedPresence()
            presence.refuse(with: refusal)
            let h = try makeHarness(presence: { try await presence.check() })
            try rememberMainChat(in: h.cache, state: .idle, text: "Cached reply")
            h.hub.failReads([refusal])
            XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
            await h.store.open(chatID: "main-chat")

            XCTAssertTrue(h.store.repairNeeded)
            XCTAssertNil(h.store.prepareMainChat(), "A refused pairing asks to pair again, not for the cached chat")

            presence.refuse(with: nil)
            h.hub.seed(chatID: "main-chat", title: "Chat", items: [item(1, text: "Cached reply")])
            await h.store.refreshList()
            XCTAssertFalse(h.store.repairNeeded, "A request that succeeds again, after Pair again, clears it")
        }
    }

    func testARefusalIsRememberedAcrossLaunchesUntilAReadSucceedsOrTheIPhonePairsAgain() async throws {
        let h = try makeHarness(presence: { throw RelayClientError.unauthorized })
        try rememberMainChat(in: h.cache, state: .idle, text: "Cached reply")
        h.hub.failReads([RelayClientError.unauthorized])
        await h.store.open(chatID: "main-chat")
        XCTAssertTrue(h.store.repairNeeded)

        let relaunched = relaunch(h)
        XCTAssertTrue(relaunched.repairNeeded, "A cold start asks to pair again before any request")
        XCTAssertNil(relaunched.prepareMainChat(), "A cold start does not open the cached chat")
        XCTAssertNil(relaunched.transcript)

        h.hub.seed(chatID: "main-chat", title: "Chat", items: [item(1, text: "Cached reply")])
        await relaunched.refreshList()
        XCTAssertFalse(relaunched.repairNeeded)
        XCTAssertFalse(relaunch(h).repairNeeded, "A successful list read forgets the refusal")

        h.hub.failReads([RelayClientError.unauthorized])
        await relaunched.open(chatID: "main-chat")
        XCTAssertTrue(relaunch(h).repairNeeded)
        relaunched.pairingCommitted()
        XCTAssertFalse(relaunched.repairNeeded)
        let paired = relaunch(h)
        XCTAssertFalse(paired.repairNeeded, "A new pairing forgets the refusal of the old one")
        XCTAssertEqual(paired.prepareMainChat(), "main-chat")
    }

    func testALatePresenceAnswerCannotClearARefusalFromARead() async throws {
        let presence = ScriptedPresence()
        let h = try makeHarness(presence: { try await presence.check() })
        try rememberMainChat(in: h.cache, state: .idle, text: "Cached reply")
        let gate = HeldChatOperation()
        presence.holdNext(using: gate)
        h.hub.failReads([RelayClientError.unauthorized])
        let opening = Task { await h.store.open(chatID: "main-chat") }
        await gate.waitUntilHeld()
        for _ in 0..<200 where !h.store.repairNeeded {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertTrue(h.store.repairNeeded)

        await gate.release()
        await opening.value

        XCTAssertEqual(h.store.hubOnline, true)
        XCTAssertTrue(h.store.repairNeeded, "Presence does not read the chat routes, so it cannot clear a refusal")
    }

    func testAReturningChatScreenPollsAtOnceInsteadOfFinishingTheRetryBackoff() async throws {
        ChatStore.eventRetryBaseNanoseconds = 5_000_000_000
        let h = try makeHarness()
        h.hub.failPolls([RelayClientError.offline, RelayClientError.offline])
        let away = Task { await h.store.watchForEvents() }
        for _ in 0..<100 where h.hub.polls < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.hub.polls, 2, "The second failure starts the backoff")
        away.cancel()
        await away.value

        let returning = Task { await h.store.watchForEvents() }
        for _ in 0..<100 where h.hub.polls < 3 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertGreaterThanOrEqual(h.hub.polls, 3,
                                    "A screen that starts listening polls now, not after a backoff of up to 30 s")
        returning.cancel()
        await returning.value
    }

    func testACancelledPresenceCheckDoesNotLeaveItsAnswerForTheNextRefresh() async throws {
        let presence = ScriptedPresence()
        let h = try makeHarness(presence: { try await presence.check() })
        let gate = HeldChatOperation()
        presence.holdNext(using: gate)
        let cancelled = Task { await h.store.refreshList() }
        await gate.waitUntilHeld()

        cancelled.cancel()
        await gate.release()
        await cancelled.value
        await h.store.refreshList()

        XCTAssertEqual(presence.calls, 2, "The next refresh checks again instead of reusing a cancelled answer")
    }

    func testACachedWorkingOrWaitingStateIsNotShownAsCurrentUntilAReadConfirmsIt() async throws {
        for state in [ChatState.running, .waiting] {
            let h = try makeHarness()
            try rememberMainChat(in: h.cache, state: state, text: "Earlier work")

            XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
            XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Earlier work"])
            XCTAssertNotEqual(h.store.transcript?.header.state, state, "No Stop, no still answering, Send enabled")
            XCTAssertNotEqual(h.store.chats.first?.state, state, "The list does not repeat the cached state")

            h.hub.failReads([HubRequestError.noAnswer])
            await h.store.open(chatID: "main-chat")
            XCTAssertTrue(h.store.transcriptRefreshFailed)
            XCTAssertNotEqual(h.store.transcript?.header.state, state, "An offline Mac does not confirm it")

            h.hub.seed(chatID: "main-chat", title: "Chat", state: state, items: [item(1, text: "Earlier work")])
            await h.store.open(chatID: "main-chat")
            XCTAssertEqual(h.store.transcript?.header.state, state, "A read from the Mac confirms it")
        }
    }

    func testTheEventWatcherPollsAgainWithoutWaitingForAPresenceCheck() async throws {
        let presence = ScriptedPresence()
        let h = try makeHarness(presence: { try await presence.check() })
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        presence.holdNext(using: gate)
        h.hub.append(chatID: "chat-1", role: .assistant, text: "First")
        h.hub.queueEvents([turn("chat-1", seq: 2)])
        let watcher = Task { await h.store.watchForEvents() }
        await gate.waitUntilHeld()

        h.hub.append(chatID: "chat-1", role: .assistant, text: "Second")
        h.hub.queueEvents([turn("chat-1", seq: 3)])
        for _ in 0..<200 where h.store.transcript?.items.count != 3 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "First", "Second"],
                       "The next poll must not wait for a held presence check")
        watcher.cancel()
        await gate.release()
        await watcher.value
    }

    func testLeavingChatStopsListeningAtOnceAndTheNextVisitGetsTheEventsInFlight() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Chat", items: [item(1, text: "Before leaving Chat")])
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("poll", using: gate)
        let oldVisit = Task { await h.store.watchForEvents() }
        await gate.waitUntilHeld()

        oldVisit.cancel()
        let stopped = expectation(description: "The old visit stops listening while its long-poll is still open")
        Task {
            await oldVisit.value
            stopped.fulfill()
        }
        await fulfillment(of: [stopped], timeout: 1)
        let returning = Task { await h.store.watchForEvents() }
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Arrived while away")
        h.hub.queueEvents([turn("chat-1", seq: 2)])
        await gate.release()
        for _ in 0..<200 where h.store.transcript?.items.count != 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Before leaving Chat", "Arrived while away"],
                       "The poll the old visit started delivers its events to the returning one")
        returning.cancel()
        await returning.value
    }

    func testAMessageIsSendingUntilTheMacAcceptsItThenSentAndTiedToItsStoredItem() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)

        let sending = Task { await h.store.send("Summarize the pricing call") }
        await gate.waitUntilHeld()
        let inFlight = h.store.outgoingMessages(in: "chat-1")
        XCTAssertEqual(inFlight.map(\.state), [.sending])
        XCTAssertEqual(inFlight.first?.text, "Summarize the pricing call")
        XCTAssertNil(inFlight.first?.seq, "not stored on the Mac yet, so it shows under the stored rows")
        await gate.release()
        let accepted = await sending.value

        XCTAssertTrue(accepted)
        let sent = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(sent.state, .sent)
        XCTAssertEqual(sent.turnID, "turn-2")
        XCTAssertEqual(sent.seq, 3, "the read ties it to the item the Mac stored")
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1"), h.store.outgoingMessages(in: "chat-1"))
    }

    func testATurnEventForTheMessagesTurnMarksItSeen() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        await h.store.send("Summarize the pricing call")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent])

        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "turn-other",
                                                             state: "done", seq: 1, at: "")])
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent], "another turn says nothing about it")

        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "turn-2",
                                                             state: "running", seq: 1, at: "")])
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.seen])
    }

    func testATurnEventThatArrivesBeforeTheMacsAnswerStillMarksTheMessageSeen() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)

        let sending = Task { await h.store.send("Summarize the pricing call") }
        await gate.waitUntilHeld()
        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "turn-2",
                                                             state: "running", seq: 1, at: "")])
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending],
                       "the read the event started shows a row with its text, but only the Mac's answer ties it")
        await gate.release()
        _ = await sending.value

        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.seen])
    }

    func testAReplyStoredAfterTheMessageMarksItSeen() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        await h.store.send("Summarize the pricing call")

        h.hub.append(chatID: "chat-1", role: .system, text: "Background work finished.")
        await h.store.refreshOpen()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent],
                       "a status line can land before the Mac starts on the message")

        h.hub.append(chatID: "chat-1", role: .assistant, text: "Here is the summary.")
        await h.store.refreshOpen()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.seen])
    }

    func testAMessageTheMacIsAskingPermissionAboutIsSeen() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        await h.store.send("Read the design notes")

        h.hub.setPending(chatID: "chat-1", requestID: "req-1")
        await h.store.refreshOpen()

        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.seen])
    }

    func testOfflineTheMessageStaysSendingWithTheOfflineNoteThenGoesOnceTheMacAnswers() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])

        let kept = await h.store.send("Draft the reply to Sam")

        XCTAssertTrue(kept, "the composer lets it go; the bubble keeps it")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending])
        XCTAssertEqual(h.store.notice, ChatDeliveryStatus.waitingForComputer.explanation)
        XCTAssertEqual(h.hub.sent.count, 1)

        // The Mac answers a read again: the waiting message goes under its first key.
        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.outgoingMessages(in: "chat-1").first?.state != .sent
            || h.store.transcript?.items.count != 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent])
        XCTAssertEqual(h.hub.sentTexts, ["Draft the reply to Sam", "Draft the reply to Sam"])
        XCTAssertEqual(Set(h.hub.sent.map(\.key)).count, 1, "the second try reuses the first key")
        XCTAssertEqual(h.store.transcript?.items.map(\.text), ["Hi", "Draft the reply to Sam"])
    }

    func testAMessageTheMacStoredWithoutItsAnswerArrivingGetsThatAnswerBackAndRunsOnce() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi")])
        await h.store.open(chatID: "chat-1")
        // The relay delivered the request after the iPhone stopped waiting; the Mac stored it and started the turn.
        h.hub.loseNextAnswer()
        await h.store.send("Hi")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending])

        // The read shows a stored Hi while the turn runs: the message asks again under its key and gets the
        // stored answer back, so it is tied to its own row and nothing runs twice.
        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.outgoingMessages(in: "chat-1").first?.turnID == nil {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        let message = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(message.state, .sent)
        XCTAssertEqual(message.turnID, "turn-2")
        XCTAssertEqual(message.seq, 2, "the earlier message with the same text was already on screen when it was sent")
        XCTAssertEqual(h.hub.storedUserTexts, ["Hi", "Hi"], "the Mac stored it once")
        XCTAssertEqual(Set(h.hub.sent.map(\.key)).count, 1)
    }

    func testAWaitingMessageWaitsForTheRunningTurnBeforeItGoesAgain() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])
        await h.store.send("One more thing")
        h.hub.setState(chatID: "chat-1", .running)

        await h.store.refreshOpen()
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(h.hub.sent.count, 1, "a running turn would answer busy")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending])

        h.hub.setState(chatID: "chat-1", .idle)
        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.outgoingMessages(in: "chat-1").first?.state != .sent {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent])
    }

    func testAWaitingMessageTheMacRefusesGoesBackToTheComposer() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer, HubRequestError.rejected(status: 400, message: "Not valid.")])
        await h.store.send("Draft the reply to Sam")

        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.returnedDrafts.isEmpty {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.returnedDrafts.map(\.text), ["Draft the reply to Sam"])
        XCTAssertEqual(h.store.returnedDrafts.first?.chatID, "chat-1")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.deliveryIssue), [.failed])
        XCTAssertEqual(h.store.notice, "Not valid.")
        let returned = try XCTUnwrap(h.store.returnedDrafts.first)
        h.store.takeReturnedDraft(returned)
        XCTAssertTrue(h.store.returnedDrafts.isEmpty)
    }

    func testIdeasHandoffOpensMainChatUnsentAndPreservesExistingDraftsAcrossRelaunch() throws {
        let h = try makeHarness()
        let router = NotificationRouter()
        router.selectedTab = .tasks
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "",
                                                     state: .idle, kind: .main))
        h.store.prefillMainChat("Keep my earlier draft")
        let idea = IdeaOffer(id: "suggestion.notes", kind: "suggestion", title: "I can tidy the project notes",
                             why: "You worked on these notes three times this week.",
                             prompt: "Help me tidy the project notes. Show me a plan before making changes.",
                             version: String(repeating: "a", count: 64))

        IdeaHandoff.open(idea, chat: h.store, router: router)

        XCTAssertEqual(router.selectedTab, .chat)
        XCTAssertEqual(h.store.returnedDrafts.map(\.text), ["Keep my earlier draft", idea.prompt])
        XCTAssertEqual(h.store.returnedDrafts.map(\.chatID), ["main-chat", "main-chat"])
        let reference = try XCTUnwrap(idea.chatReference)
        XCTAssertEqual(h.store.returnedDrafts.map(\.offerReferences), [[], [reference]])
        XCTAssertTrue(h.hub.sentTexts.isEmpty, "Do it only prepares text for the owner to send")
        XCTAssertTrue(h.hub.creates.isEmpty, "Ideas cannot start a side chat")
        let restored = relaunch(h)
        XCTAssertEqual(restored.returnedDrafts.map(\.text), ["Keep my earlier draft", idea.prompt])
        XCTAssertEqual(restored.returnedDrafts.map(\.offerReferences), [[], [reference]])
        XCTAssertEqual(restored.takeReturnedDrafts(in: "main-chat", appendingTo: "Already typing"),
                       "Already typing\nKeep my earlier draft\n\(idea.prompt)")
        XCTAssertEqual(restored.composerDraft(in: "main-chat").offerReferences, [reference])
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").offerReferences, [reference])
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testIdeasHandoffWaitsForMainIdentityAndDoesNotCrossPairings() throws {
        let h = try makeHarness()
        let router = NotificationRouter()
        let idea = IdeaOffer(id: "find.local", kind: "topic_find", title: "Explore local AI tools",
                             why: "You have been reading about local AI tools.", prompt: "Explore local AI tools with me.",
                             version: String(repeating: "b", count: 64))
        IdeaHandoff.open(idea, chat: h.store, router: router)
        XCTAssertNil(h.store.prepareMainChat())
        XCTAssertTrue(h.store.returnedDrafts.isEmpty)
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "",
                                                     state: .idle, kind: .main))
        XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
        XCTAssertEqual(h.store.returnedDrafts.map(\.text), [idea.prompt])
        XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
        XCTAssertEqual(h.store.returnedDrafts.count, 1)
        h.store.clearAfterUnpair()
        XCTAssertTrue(relaunch(h).returnedDrafts.isEmpty)
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testIdeaProvenanceSurvivesHandoffBeforeIdentityAndComposerRelaunch() throws {
        let h = try makeHarness()
        let reference = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
        XCTAssertTrue(h.store.prefillMainChat("Source-derived idea", offerReferences: [reference]))
        let beforeIdentity = relaunch(h)
        XCTAssertNil(beforeIdentity.prepareMainChat())
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "",
                                                     state: .idle, kind: .main))
        XCTAssertEqual(beforeIdentity.prepareMainChat(), "main-chat")
        XCTAssertEqual(beforeIdentity.returnedDrafts.first?.offerReferences, [reference])
        let text = beforeIdentity.takeReturnedDrafts(in: "main-chat", appendingTo: "My own words")
        XCTAssertEqual(text, "My own words\nSource-derived idea")
        XCTAssertTrue(beforeIdentity.returnedDrafts.isEmpty)
        let composer = relaunch(h).composerDraft(in: "main-chat")
        XCTAssertEqual(composer.text, text)
        XCTAssertEqual(composer.offerReferences, [reference])
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testIdeaComposerRetainsEveryDependencyThroughEditsClearUndoAndRelaunch() throws {
        let h = try makeHarness()
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        let first = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
        let second = ChatOfferReference(id: "find.research", version: String(repeating: "b", count: 64))
        h.store.prefillMainChat("Original idea", offerReferences: [first])
        _ = h.store.takeReturnedDrafts(in: "main-chat", appendingTo: "")
        XCTAssertTrue(h.store.editComposerDraft("Edited in my own words", in: "main-chat"))
        h.store.prefillMainChat("Another idea", offerReferences: [second])
        _ = h.store.takeReturnedDrafts(in: "main-chat", appendingTo: h.store.composerDraft(in: "main-chat").text)
        XCTAssertTrue(h.store.editComposerDraft("", in: "main-chat"))
        let restored = relaunch(h)
        XCTAssertEqual(restored.composerDraft(in: "main-chat").offerReferences, [first, second])
        XCTAssertTrue(restored.editComposerDraft("Original idea", in: "main-chat"), "Undo is an edit, not new authorship")
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").offerReferences, [first, second])
        XCTAssertTrue(restored.discardComposer(in: "main-chat"))
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").text, "")
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").offerReferences, [])
    }

    func testTextOnlyComposerHelpersCannotDetachIdeaProvenance() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Isoform")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        await h.store.open(chatID: "main-chat")
        let reference = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
        h.store.prefillMainChat("Original idea", offerReferences: [reference])
        let returned = try XCTUnwrap(h.store.returnedDrafts.first)
        XCTAssertTrue(h.store.takeReturnedDraft(returned))
        let sent = await h.store.send("I edited the idea")
        XCTAssertTrue(sent)
        XCTAssertEqual(h.hub.sentOfferReferences, [[reference]])
    }

    func testOfflineIdeaQueueRetainsReferencesAndKeyAcrossRelaunchAndWithdrawalRefusal() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Isoform")
        await h.store.open(chatID: "main-chat")
        let reference = ChatOfferReference(id: "find.research", version: String(repeating: "b", count: 64))
        h.hub.failSends([HubRequestError.noAnswer, HubRequestError.conflict(message: "Source withdrawn")])
        let kept = await h.store.send("Source-derived idea", offerReferences: [reference])
        XCTAssertTrue(kept)
        let firstKey = try XCTUnwrap(h.hub.sent.first?.key)
        XCTAssertEqual(try XCTUnwrap(h.cache.loadOutgoing(chatID: "main-chat")?.first).offerReferences, [reference])
        let restored = relaunch(h)
        await restored.open(chatID: "main-chat")
        // Refused text is published before the delivery outcome sets its notice and releases the send target.
        for _ in 0..<200 where restored.returnedDrafts.isEmpty || restored.isBusy("send:main-chat") {
            try await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTAssertFalse(restored.isBusy("send:main-chat"))
        XCTAssertEqual(h.hub.sent.map(\.key), [firstKey, firstKey])
        XCTAssertEqual(h.hub.sentOfferReferences, [[reference], [reference]])
        XCTAssertEqual(restored.returnedDrafts.first?.offerReferences, [reference])
        XCTAssertEqual(restored.notice, ChatProvenanceCopy.sourceUnavailable())
        XCTAssertEqual(restored.outgoingMessages(in: "main-chat").map(\.deliveryIssue), [.failed])
        _ = restored.takeReturnedDrafts(in: "main-chat", appendingTo: "")
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").offerReferences, [reference])
        XCTAssertEqual(h.hub.sentTexts.count, 2, "source refusal must not trigger a text-only fallback")
    }

    func testComposerSendMovesReferencesToQueueAndStartsAFreshComposer() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Isoform")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        await h.store.open(chatID: "main-chat")
        let reference = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
        h.store.prefillMainChat("Idea to send", offerReferences: [reference])
        _ = h.store.takeReturnedDrafts(in: "main-chat", appendingTo: "")
        h.hub.failSends([HubRequestError.noAnswer])
        var resetBeforeDelivery = false
        let sent = await h.store.sendComposer(in: "main-chat", onEnqueued: {
            XCTAssertTrue(h.hub.sentTexts.isEmpty, "Reset Undo before the first delivery can suspend")
            XCTAssertEqual(h.store.composerDraft(in: "main-chat").text, "")
            XCTAssertEqual(h.store.composerDraft(in: "main-chat").offerReferences, [])
            XCTAssertEqual(h.store.outgoingMessages(in: "main-chat").first?.offerReferences, [reference])
            resetBeforeDelivery = true
        })
        XCTAssertTrue(resetBeforeDelivery)
        XCTAssertTrue(sent)
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").text, "")
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").offerReferences, [])
        XCTAssertEqual(h.hub.sentOfferReferences, [[reference]])
        XCTAssertEqual(h.store.outgoingMessages(in: "main-chat").first?.offerReferences, [reference])
    }

    func testInterruptedPendingHandoffCleanupCannotReappearAfterSendOrDiscard() async throws {
        for shouldSend in [false, true] {
            let h = try makeHarness()
            h.hub.seed(chatID: "main-chat", title: "Isoform")
            try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
            var pendingWrites = 0
            let cache = ChatCache(directory: h.chatsDirectory, writeData: { data, url in
                if url.lastPathComponent == "main-drafts.json" {
                    pendingWrites += 1
                    if pendingWrites == 2 { throw CocoaError(.fileWriteNoPermission) }
                }
                try data.write(to: url, options: [.atomic, .completeFileProtection])
            })
            let store = ChatStore(hub: h.hub, journal: h.journal, guardian: h.guardian,
                                  presence: { true }, isPaired: { true }, cache: cache)
            await store.open(chatID: "main-chat")
            let reference = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
            XCTAssertTrue(store.prefillMainChat("Idea", offerReferences: [reference]))
            XCTAssertEqual(try XCTUnwrap(cache.loadMainDrafts()).count, 1, "The cleanup write was interrupted")
            let stale = try XCTUnwrap(store.returnedDrafts.first)
            _ = store.takeReturnedDrafts(in: "main-chat", appendingTo: "")
            if shouldSend {
                h.hub.failSends([HubRequestError.noAnswer])
                let sent = await store.sendComposer(in: "main-chat")
                XCTAssertTrue(sent)
            } else {
                XCTAssertTrue(store.discardComposer(in: "main-chat"))
            }
            XCTAssertFalse(store.takeReturnedDraft(stale), "A previously consumed draft cannot be revived by a stale caller")
            let restored = relaunch(h)
            XCTAssertEqual(restored.prepareMainChat(), "main-chat")
            XCTAssertTrue(restored.returnedDrafts.isEmpty)
            XCTAssertEqual(restored.composerDraft(in: "main-chat").text, "")
            XCTAssertEqual(restored.composerDraft(in: "main-chat").offerReferences, [])
            XCTAssertTrue(try XCTUnwrap(cache.loadMainDrafts()).isEmpty)
            XCTAssertEqual(restored.outgoingMessages(in: "main-chat").count, shouldSend ? 1 : 0)
        }
    }

    func testFailedOutgoingHandoffSaveRetriesDurablyBeforeDroppingPendingDraft() throws {
        let h = try makeHarness()
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        var failedFirstOutgoingWrite = false
        let cache = ChatCache(directory: h.chatsDirectory, writeData: { data, url in
            if url.lastPathComponent.hasSuffix(".outgoing.json"), !failedFirstOutgoingWrite {
                failedFirstOutgoingWrite = true
                throw CocoaError(.fileWriteNoPermission)
            }
            try data.write(to: url, options: [.atomic, .completeFileProtection])
        })
        let store = ChatStore(hub: h.hub, journal: h.journal, guardian: h.guardian,
                              presence: { true }, isPaired: { true }, cache: cache)
        let reference = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
        XCTAssertTrue(store.prefillMainChat("Keep this idea", offerReferences: [reference]), "Pending handoff was durably saved")
        XCTAssertTrue(failedFirstOutgoingWrite)
        XCTAssertEqual(try XCTUnwrap(cache.loadMainDrafts()).count, 1)
        XCTAssertEqual(store.prepareMainChat(), "main-chat")
        XCTAssertTrue(try XCTUnwrap(cache.loadMainDrafts()).isEmpty)
        let restored = relaunch(h)
        XCTAssertEqual(restored.returnedDrafts.map(\.text), ["Keep this idea"])
        XCTAssertEqual(restored.returnedDrafts.first?.offerReferences, [reference])
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testFailedRefusalRecoveryNeverPersistsBothAComposerAndQueuedCopy() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Isoform")
        var failedRefusal = false
        var interruptRecovery = true
        let cache = ChatCache(directory: h.chatsDirectory, writeData: { data, url in
            if url.lastPathComponent.hasSuffix(".outgoing.json"),
               let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
               let messages = object["messages"] as? [[String: Any]] {
                if messages.contains(where: { $0["returnedToDraft"] as? Bool == true }), !failedRefusal {
                    failedRefusal = true
                    throw CocoaError(.fileWriteNoPermission)
                }
                let composer = object["composer"] as? [String: Any]
                if messages.allSatisfy({ $0["failureDraftRecovered"] as? Bool == true }),
                   composer?["text"] as? String == "Source-derived idea", interruptRecovery {
                    throw CocoaError(.fileWriteNoPermission)
                }
            }
            try data.write(to: url, options: [.atomic, .completeFileProtection])
        })
        let store = ChatStore(hub: h.hub, journal: h.journal, guardian: h.guardian,
                              presence: { true }, isPaired: { true }, cache: cache)
        await store.open(chatID: "main-chat")
        let reference = ChatOfferReference(id: "find.research", version: String(repeating: "b", count: 64))
        h.hub.failSends([HubRequestError.conflict(message: "Source withdrawn")])
        let sent = await store.send("Source-derived idea", offerReferences: [reference])
        XCTAssertTrue(sent)
        XCTAssertTrue(failedRefusal)
        XCTAssertEqual(cache.loadOutgoing(chatID: "main-chat")?.first?.isQueued, true, "The refused-state write was interrupted")
        XCTAssertEqual(store.returnedDrafts.first?.offerReferences, [reference])

        _ = store.takeReturnedDrafts(in: "main-chat", appendingTo: "")
        let interrupted = ChatCache(directory: h.chatsDirectory)
        XCTAssertEqual(interrupted.loadComposer(chatID: "main-chat")?.text, "")
        XCTAssertEqual(interrupted.loadOutgoing(chatID: "main-chat")?.first?.isQueued, true)
        XCTAssertFalse(store.returnedDrafts.isEmpty, "Failure cannot consume the in-memory refused message")

        interruptRecovery = false
        XCTAssertEqual(store.takeReturnedDrafts(in: "main-chat", appendingTo: ""), "Source-derived idea")
        let recovered = ChatCache(directory: h.chatsDirectory)
        XCTAssertEqual(recovered.loadComposer(chatID: "main-chat")?.offerReferences, [reference])
        XCTAssertEqual(recovered.loadOutgoing(chatID: "main-chat")?.map(\.deliveryIssue), [.failed])
        XCTAssertEqual(recovered.loadOutgoing(chatID: "main-chat")?.first?.isQueued, false)
        XCTAssertEqual(relaunch(h).composerDraft(in: "main-chat").text, "Source-derived idea")
        XCTAssertEqual(h.hub.sentTexts.count, 1)
    }

    func testTargetRecoveryTokensPreventDuplicatingAnInterruptedCrossChatTransfer() throws {
        let h = try makeHarness()
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        try h.cache.saveComposer(ChatComposerDraft(), chatID: "main-chat")
        let mainFile = try XCTUnwrap(outgoingFile(h))
        let originalText = "Already recovered source text"
        let targetFixture = #"{"chatId":"main-chat","messages":[],"composerText":"Already recovered source text","recoveredDraftIDs":["side-chat:refused-1"]}"#
        try Data(targetFixture.utf8).write(to: mainFile, options: [.atomic, .completeFileProtection])
        let source = ChatOutgoingMessage(id: "refused-1", text: "Source text already copied", after: nil,
                                         sentAt: Date(), state: .sending, returnedToDraft: true)
        try h.cache.saveOutgoing([source], chatID: "side-chat")

        let upgraded = relaunch(h)
        XCTAssertEqual(upgraded.returnedDrafts.map(\.id), [source.id])
        let recovered = upgraded.takeReturnedDrafts(in: "main-chat", appendingTo: upgraded.composerDraft(in: "main-chat").text)
        XCTAssertEqual(recovered, originalText, "A target-format recovery token already covers the lingering source")
        XCTAssertTrue(upgraded.returnedDrafts.isEmpty)
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "side-chat"), [])
        let restored = relaunch(h)
        XCTAssertEqual(restored.composerDraft(in: "main-chat").text, originalText)
        XCTAssertEqual(restored.composerDraft(in: "main-chat").offerReferences, [.unverifiedLegacy])
        XCTAssertTrue(restored.composerDraft(in: "main-chat").recoveredDraftIDs.contains("side-chat:refused-1"))
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testLegacyUnattributedDraftCannotBeResentAsUserAuthored() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Isoform")
        let raw = #"{"id":"legacy","text":"Possibly derived from a source","sentAt":"2026-10-17T12:00:00Z","state":"sending"}"#
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let legacy = try decoder.decode(ChatOutgoingMessage.self, from: Data(raw.utf8))
        try h.cache.saveOutgoing([legacy], chatID: "main-chat")
        let restored = relaunch(h)
        await restored.open(chatID: "main-chat")
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
        XCTAssertTrue(legacy.isReturnedDraft)
        _ = restored.takeReturnedDrafts(in: "main-chat", appendingTo: "")
        let sent = await restored.sendComposer(in: "main-chat")
        XCTAssertFalse(sent)
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
        XCTAssertTrue(restored.discardComposer(in: "main-chat"))
        XCTAssertTrue(restored.editComposerDraft("A new message I wrote", in: "main-chat"))
        let fresh = await restored.sendComposer(in: "main-chat")
        XCTAssertTrue(fresh)
        XCTAssertEqual(h.hub.sentOfferReferences, [[]])
    }

    func testUnversionedOrMalformedIdeasCannotBecomeMainChatDrafts() throws {
        let h = try makeHarness()
        let router = NotificationRouter()
        router.selectedTab = .tasks
        for version in [nil, "", "old", String(repeating: "A", count: 64)] as [String?] {
            let idea = IdeaOffer(id: "suggestion.notes", kind: "suggestion", title: "Draft", why: "Source", prompt: "Source text", version: version)
            XCTAssertFalse(IdeaHandoff.open(idea, chat: h.store, router: router))
            XCTAssertEqual(router.selectedTab, .tasks)
            XCTAssertTrue(h.store.returnedDrafts.isEmpty)
            XCTAssertTrue(try XCTUnwrap(h.cache.loadMainDrafts()).isEmpty)
        }
    }

    func testIdeaDeliveryFingerprintBindsReferenceVersionAndOrderIsCanonical() {
        let first = ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64))
        let second = ChatOfferReference(id: "find.research", version: String(repeating: "b", count: 64))
        let plain = ChatOutgoingMessage(id: "message", text: "Same text", after: nil, sentAt: Date(), state: .sending)
        var linked = plain
        linked.offerReferences = [first, second]
        XCTAssertNotEqual(linked.deliveryFingerprint, plain.deliveryFingerprint)
        var reordered = linked
        reordered.offerReferences = [second, first]
        XCTAssertEqual(linked.deliveryFingerprint, reordered.deliveryFingerprint)
        reordered.offerReferences = [ChatOfferReference(id: first.id, version: second.version), second]
        XCTAssertNotEqual(linked.deliveryFingerprint, reordered.deliveryFingerprint)
    }

    func testGoalsHandoffKeepsRefusedDraftsUntilTheirOwnComposerTakesThem() async throws {
        for refusedChatID in ["main-chat", "side-chat"] {
            for mainAlreadyKnown in [true, false] {
                let h = try makeHarness()
                h.hub.seed(chatID: refusedChatID, title: "Isoform")
                await h.store.open(chatID: refusedChatID)
                h.hub.failSends([HubRequestError.noAnswer, HubRequestError.rejected(status: 400, message: "Not valid.")])
                await h.store.send("Keep this refused message")
                await h.store.refreshOpen()
                for _ in 0..<200 where h.store.returnedDrafts.isEmpty {
                    try await Task.sleep(nanoseconds: 10_000_000)
                }
                let refused = try XCTUnwrap(h.store.returnedDrafts.first)
                let main = ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main)
                if mainAlreadyKnown { try h.cache.rememberMainChat(main) }

                h.store.prefillMainChat(GoalCategory.health.chatPrefill)
                if !mainAlreadyKnown {
                    XCTAssertEqual(h.store.returnedDrafts, [refused], "the hand-off waits for the chat identity")
                    try h.cache.rememberMainChat(main)
                }
                XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
                XCTAssertEqual(h.store.returnedDrafts.map(\.text), ["Keep this refused message", GoalCategory.health.chatPrefill])
                XCTAssertEqual(h.store.returnedDrafts.map(\.chatID), [refusedChatID, "main-chat"])
                let handoff = try XCTUnwrap(h.store.returnedDrafts.last)
                h.store.takeReturnedDraft(handoff)
                h.store.takeReturnedDraft(handoff)
                XCTAssertEqual(h.store.returnedDrafts, [refused], "taking the hand-off cannot consume the refused draft")
                h.store.takeReturnedDraft(refused)
                XCTAssertTrue(h.store.returnedDrafts.isEmpty)
                XCTAssertEqual(h.hub.sentTexts, ["Keep this refused message", "Keep this refused message"], "the Goals hand-off stays unsent")
            }
        }
    }

    func testARefusalAfterTheGoalsHandoffKeepsBothDrafts() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Isoform")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        await h.store.open(chatID: "main-chat")
        h.hub.failSends([HubRequestError.noAnswer, HubRequestError.rejected(status: 400, message: "Not valid.")])
        await h.store.send("Keep this refused message")
        h.store.prefillMainChat(GoalCategory.health.chatPrefill)

        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.returnedDrafts.count < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(h.store.returnedDrafts.map(\.text), [GoalCategory.health.chatPrefill, "Keep this refused message"])
    }

    func testGoalsHandoffsBeforeMainChatDiscoveryArriveOnceAndClearWithPairing() throws {
        let h = try makeHarness()
        h.store.prefillMainChat(GoalCategory.health.chatPrefill)
        h.store.prefillMainChat(GoalCategory.career.chatPrefill)
        XCTAssertNil(h.store.prepareMainChat())
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Isoform", updatedAt: "", state: .idle, kind: .main))

        XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
        XCTAssertEqual(h.store.prepareMainChat(), "main-chat")
        XCTAssertEqual(h.store.returnedDrafts.map(\.text), [GoalCategory.health.chatPrefill, GoalCategory.career.chatPrefill])
        h.store.clearAfterUnpair()
        XCTAssertTrue(h.store.returnedDrafts.isEmpty)

        h.store.prefillMainChat(GoalCategory.finance.chatPrefill)
        h.store.clearAfterUnpair()
        try h.cache.rememberMainChat(ChatSummaryView(id: "replacement-main", title: "Isoform", updatedAt: "", state: .idle, kind: .main))
        XCTAssertEqual(h.store.prepareMainChat(), "replacement-main")
        XCTAssertTrue(h.store.returnedDrafts.isEmpty, "a pending hand-off must not follow a different pairing")
        XCTAssertTrue(h.hub.sentTexts.isEmpty)
    }

    func testMainChatEmptyStateCopySaysComputer() {
        for text in [ChatCopy.pairingDetail, ChatCopy.pair, ChatCopy.pairingRefused,
                     ChatCopy.unavailableDetail, ChatCopy.noMainChatTitle, ChatCopy.noMainChatDetail] {
            XCTAssertTrue(text.contains("computer"))
            XCTAssertFalse(text.contains("Mac"))
            XCTAssertFalse(text.contains("iPhone"))
        }
    }

    func testWaitingAndSentMessagesSurviveARelaunch() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])
        await h.store.send("Draft the reply to Sam")
        h.hub.failReads([HubRequestError.noAnswer])

        let next = relaunch(h)
        await next.open(chatID: "chat-1")
        XCTAssertEqual(next.outgoingMessages(in: "chat-1").map(\.state), [.sending],
                       "the Mac is still silent, so the message is still on its way")
        XCTAssertEqual(h.hub.sent.count, 1)

        await next.refreshOpen()
        for _ in 0..<200 where next.outgoingMessages(in: "chat-1").first?.state != .sent {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(next.outgoingMessages(in: "chat-1").map(\.state), [.sent])
        XCTAssertEqual(Set(h.hub.sent.map(\.key)).count, 1)
    }

    func testOnlyTheNewestSeenMessageKeepsItsLabel() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        await h.store.send("First")
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Answer one")
        h.hub.setState(chatID: "chat-1", .idle)
        await h.store.refreshOpen()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.text), ["First"])

        await h.store.send("Second")

        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.text), ["Second"],
                       "the earlier seen message no longer needs a label")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent])
    }

    func testUnpairingForgetsTheMessagesSentFromThisIPhone() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        await h.store.send("Summarize the pricing call")

        XCTAssertTrue(h.store.clearAfterUnpair())

        XCTAssertTrue(h.store.outgoingMessages(in: "chat-1").isEmpty)
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1"), [])
    }

    // MARK: - Review fixes: provenance, order and persistence of phone messages

    func testAQueuedMessageIsNotTakenForTheSameTextTypedOnTheMac() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, text: "Anything else?")])
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])
        await h.store.send("yes")

        // The phone's request never reached the Mac; the user typed the same word on the Mac.
        h.hub.append(chatID: "chat-1", role: .user, text: "yes")
        try await drainQueue(h)

        XCTAssertEqual(h.hub.storedUserTexts, ["yes", "yes"], "the phone's message still reaches the Mac")
        let message = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(message.seq, 3, "tied to its own row, not to the one typed on the Mac")
    }

    func testTheSameTextSentTwiceIsTwoMessagesWithTheirOwnKeys() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])

        await h.store.send("continue")
        await h.store.send("continue")

        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.text), ["continue", "continue"])
        try await drainQueue(h)
        XCTAssertEqual(h.hub.storedUserTexts, ["continue", "continue"], "both reach the Mac")
        XCTAssertEqual(Set(h.hub.sent.map(\.key)).count, 2, "one key per message; only a retry reuses one")
    }

    func testQueuedMessagesReachTheMacInTheOrderTheyWereWritten() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer])

        await h.store.send("first")
        await h.store.send("second")
        try await drainQueue(h)

        XCTAssertEqual(h.hub.storedUserTexts, ["first", "second"])
    }

    func testASendWhileAnotherIsInFlightWaitsBehindItInsteadOfBouncing() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)
        let first = Task { await h.store.send("first") }
        await gate.waitUntilHeld()

        let second = await h.store.send("second")

        XCTAssertTrue(second, "the composer lets it go")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.text), ["first", "second"])
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending, .sending])
        await gate.release()
        _ = await first.value
        try await drainQueue(h)
        XCTAssertEqual(h.hub.storedUserTexts, ["first", "second"])
    }

    func testAnAnswerThatCannotBeSavedKeepsTheKeySoTheNextLaunchGetsItBack() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)
        let send = Task { await h.store.send("Summarize the pricing call") }
        await gate.waitUntilHeld()
        // The phone locks before the answer arrives: the chat cache cannot be written.
        let chats = h.chatsDirectory
        try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: chats.path)
        await gate.release()
        _ = await send.value
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: chats.path)
        let kept = await h.journal.entries()
        XCTAssertEqual(kept.count, 1, "the key stays until the answer is saved")

        let next = relaunch(h)
        await next.open(chatID: "chat-1")
        h.hub.setState(chatID: "chat-1", .idle)
        await next.refreshOpen()
        for _ in 0..<200 where next.outgoingMessages(in: "chat-1").first?.turnID == nil {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(next.outgoingMessages(in: "chat-1").first?.turnID, "turn-2", "the stored answer, not a new turn")
        XCTAssertEqual(h.hub.storedUserTexts, ["Summarize the pricing call"])
    }

    func testAMessageOlderThanTheNewestPageIsTiedAndSeenInsteadOfShownAsNew() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Long", items: [item(1, text: "Start"), item(2, .user, text: "Run the checks")])
        for index in 0..<60 {
            h.hub.append(chatID: "chat-1", role: .assistant, text: "Step \(index)")
        }
        // The Mac accepted the message, but the iPhone closed before any read tied it to its row.
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-1", text: "Run the checks", after: 1,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      turnID: "turn-old", seq: nil, state: .sent)],
                                 chatID: "chat-1")

        let next = relaunch(h)
        await next.open(chatID: "chat-1")

        let message = try XCTUnwrap(next.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(message.state, .seen, "sixty rows follow it")
        let firstOnPage = try XCTUnwrap(next.transcript?.items.first?.seq)
        XCTAssertLessThan(try XCTUnwrap(message.seq), firstOnPage, "it sits before the page, not under it as new")
    }

    func testAnUnreadableOutgoingFileIsNeitherTakenAsEmptyNorOverwritten() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-1", text: "Earlier", after: 0,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      turnID: nil, seq: nil, state: .sending)],
                                 chatID: "chat-1")
        let file = try XCTUnwrap(outgoingFile(h))
        let before = try Data(contentsOf: file)
        // Locked phone: the file is there but cannot be read.
        try FileManager.default.setAttributes([.posixPermissions: 0o000], ofItemAtPath: file.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: file.path) }

        await h.store.open(chatID: "chat-1")
        let accepted = await h.store.send("Later")

        XCTAssertFalse(accepted, "it could not be kept, so it stays in the composer")
        XCTAssertTrue(h.hub.sent.isEmpty)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: file.path)
        XCTAssertEqual(try Data(contentsOf: file), before, "what could not be read is not replaced")
    }

    func testADamagedOutgoingFileIsSetAsideAndTheChatKeepsWorking() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-1", text: "Earlier", after: 0,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      turnID: nil, seq: nil, state: .sending)],
                                 chatID: "chat-1")
        let file = try XCTUnwrap(outgoingFile(h))
        let damaged = Data("not json".utf8)
        try damaged.write(to: file)

        await h.store.open(chatID: "chat-1")
        let accepted = await h.store.send("Later")

        XCTAssertTrue(accepted)
        XCTAssertEqual(h.hub.storedUserTexts, ["Later"])
        let names = try FileManager.default.contentsOfDirectory(atPath: h.chatsDirectory.path)
        let aside = try XCTUnwrap(names.first { $0.contains(".outgoing.damaged-") })
        XCTAssertEqual(try Data(contentsOf: h.chatsDirectory.appendingPathComponent(aside)), damaged,
                       "kept, not overwritten")
    }

    // MARK: - Review round 3

    func testASendStillCountsAsSentWhenItWasSeenAndPrunedBeforeItsCallReturned() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        let gate = HeldChatOperation()
        h.hub.holdNext("send", using: gate)
        let first = Task { await h.store.send("first") }
        await gate.waitUntilHeld()
        _ = await h.store.send("second")
        // The Mac answers the first before its 202 arrives, so the read after the 202 shows it seen.
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Done with first")
        await gate.release()

        let accepted = await first.value

        XCTAssertTrue(accepted, "a message the Mac took must not come back to the composer")
        XCTAssertFalse(h.store.outgoingMessages(in: "chat-1").contains { $0.text == "first" },
                       "seen and older than the queued one, so its label is gone")
    }

    func testASendThatCannotBeSavedFailsVisiblyAndSendsNothing() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        try FileManager.default.createDirectory(at: h.chatsDirectory, withIntermediateDirectories: true)
        try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: h.chatsDirectory.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: h.chatsDirectory.path) }

        let accepted = await h.store.send("Summarize the pricing call")

        XCTAssertFalse(accepted, "the composer keeps the text")
        XCTAssertEqual(h.store.notice, "Isoform could not save this on this phone, so nothing was sent to your computer. Try again.")
        XCTAssertTrue(h.hub.sent.isEmpty, "a message that would not survive a kill is not sent")
        XCTAssertTrue(h.store.outgoingMessages(in: "chat-1").isEmpty)
    }

    func testAMessageFromTheFirstBuildKeepsItsTextKeyedJournalEntry() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        // The first build keyed a message by its text alone and lost the 202.
        _ = try await h.journal.reserve(operation: .chatMessage, target: "chat-1", choice: "message",
                                        fingerprint: WriteFingerprint.make(["Draft the reply to Sam"]),
                                        makeKey: { "first-build-key" }, createdAt: Date())
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-1", text: "Draft the reply to Sam", after: 0,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      turnID: nil, seq: nil, state: .sending)],
                                 chatID: "chat-1")

        await h.store.open(chatID: "chat-1")
        for _ in 0..<200 where h.hub.sent.isEmpty {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.hub.sent.map(\.key), ["first-build-key"], "a retry under the old key, so the Mac runs it once")
    }

    func testAQueuedMessageInAChatThatIsNotOpenGoesAfterARelaunch() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Open chat")
        h.hub.seed(chatID: "chat-2", title: "Other chat")
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-2", text: "From the other chat", after: 0,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      turnID: nil, seq: nil, state: .sending)],
                                 chatID: "chat-2")

        let next = relaunch(h)
        await next.open(chatID: "chat-1")
        await next.refreshList()
        for _ in 0..<200 where h.hub.storedUserTexts.isEmpty {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.hub.storedUserTexts, ["From the other chat"])
    }

    func testAMacWithoutTheSeqIsTiedByTextInTheReadAfterItsAnswerAndThenSeen() async throws {
        let h = try makeHarness()
        h.hub.omitsSeq = true
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "yes"), item(2, text: "Ok")])
        await h.store.open(chatID: "chat-1")

        await h.store.send("yes")

        let sent = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(sent.state, .sent)
        XCTAssertEqual(sent.seq, 3, "the read after the 202 holds its row; the older yes was already on screen")
        h.hub.append(chatID: "chat-1", role: .assistant, text: "Noted")
        await h.store.refreshOpen()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.seen])
    }

    func testAMacWithoutTheSeqCannotTieAMessageUsingAReadStartedBeforeItsAnswer() async throws {
        let h = try makeHarness()
        h.hub.omitsSeq = true
        h.hub.seed(chatID: "chat-1", title: "Long chat", items: (1...60).map { item($0, text: "Earlier \($0)") })
        let staleRead = HeldChatOperation()
        h.hub.holdNext("read", using: staleRead)
        let opening = Task { await h.store.open(chatID: "chat-1") }
        await staleRead.waitUntilHeld()

        // The held newest-page read has rows 11...60, and cannot contain the message sent after it started.
        let accepted = await h.store.send("Written after that read started")
        XCTAssertTrue(accepted)
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first?.state, .sent)
        XCTAssertNil(h.store.outgoingMessages(in: "chat-1").first?.seq)
        let freshRead = HeldChatOperation()
        h.hub.holdNext("read", using: freshRead)
        await staleRead.release()
        await freshRead.waitUntilHeld()

        // Pause the follow-up read before it applies, so a mistaken tie cannot be hidden by its completion.
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), Array(11...60))
        XCTAssertNil(h.store.outgoingMessages(in: "chat-1").first?.seq,
                     "the stale page must not place this newer message before its first row")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first?.state, .sent,
                       "rows read before the 202 cannot prove the Mac answered this message")
        XCTAssertEqual(ChatDelivery.drawnBelowRows(h.store.outgoingMessages(in: "chat-1"),
                                                   rows: h.store.transcript?.items).map(\.text),
                       ["Written after that read started"])

        await freshRead.release()
        await opening.value

        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first?.seq, 61,
                       "the read started after the 202 is the first one allowed to tie the message")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first?.state, .sent)
    }

    func testAnAcceptedSendStaysAcceptedWhenThePairingChangesDuringItsRefresh() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Old Mac")
        await h.store.open(chatID: "chat-1")
        let read = HeldChatOperation()
        h.hub.holdNext("read", using: read)
        let sending = Task { await h.store.send("Already accepted by the old Mac") }
        await read.waitUntilHeld()
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first?.state, .sent,
                       "the 202 was received and saved before this read started")

        h.store.clearAfterUnpair()
        h.store.notice = "The new Mac is ready."
        await read.release()
        let accepted = await sending.value

        XCTAssertTrue(accepted, "changing the pairing after acceptance must not return sent text to the composer")
        XCTAssertEqual(h.hub.storedUserTexts, ["Already accepted by the old Mac"])
        XCTAssertEqual(h.store.notice, "The new Mac is ready.")
        XCTAssertNil(h.store.transcript, "the old Mac's refresh cannot publish into the replacement pairing")
        XCTAssertTrue(h.store.outgoingMessages(in: "chat-1").isEmpty)
    }

    func testASentMessageIsDrawnUntilAReadBringsItsRowAndAcrossARelaunchWhileReadsFail() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])
        await h.store.open(chatID: "chat-1")
        // The read after the 202 fails, and so does the first read after the relaunch.
        h.hub.failReads([HubRequestError.noAnswer, HubRequestError.noAnswer])

        await h.store.send("Summarize the pricing call")

        let message = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first)
        XCTAssertEqual(message.seq, 3, "the 202 names its row")
        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [1, 2], "the read after the 202 failed")
        XCTAssertEqual(ChatDelivery.drawnBelowRows(h.store.outgoingMessages(in: "chat-1"),
                                                   rows: h.store.transcript?.items).map(\.id), [message.id],
                       "drawn as its own bubble until a read brings the row")

        let next = relaunch(h)
        await next.open(chatID: "chat-1")
        XCTAssertEqual(next.transcript?.items.map(\.seq), [1, 2], "still only the cached rows")
        XCTAssertEqual(ChatDelivery.drawnBelowRows(next.outgoingMessages(in: "chat-1"),
                                                   rows: next.transcript?.items).map(\.id), [message.id])

        await next.refreshOpen()
        XCTAssertEqual(next.transcript?.items.map(\.seq), [1, 2, 3])
        XCTAssertTrue(ChatDelivery.drawnBelowRows(next.outgoingMessages(in: "chat-1"),
                                                  rows: next.transcript?.items).isEmpty, "now drawn as its row")
    }

    func testASentMessageIsStillDrawnAfterSwitchingChatsAndBack() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, .user, text: "Hi"), item(2, text: "Hello")])
        h.hub.seed(chatID: "chat-2", title: "Other")
        await h.store.open(chatID: "chat-1")
        h.hub.failReads([HubRequestError.noAnswer])
        await h.store.send("Summarize the pricing call")

        await h.store.open(chatID: "chat-2")
        h.hub.failReads([HubRequestError.noAnswer])
        await h.store.open(chatID: "chat-1")

        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [1, 2], "back on the cached rows from before the send")
        XCTAssertEqual(ChatDelivery.drawnBelowRows(h.store.outgoingMessages(in: "chat-1"),
                                                   rows: h.store.transcript?.items).map(\.text),
                       ["Summarize the pricing call"], "the user must not think it is gone and send it again")
    }

    func testATurnEventDoesNotPruneASeenMessageWhoseRowIsStillOffScreen() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync", items: [item(1, text: "Earlier reply")])
        await h.store.open(chatID: "chat-1")
        h.hub.failReads([HubRequestError.noAnswer])
        await h.store.send("Accepted but its row has not arrived")
        await h.store.send("Waiting behind it")
        let turnID = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first?.turnID)
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sent, .sending])
        h.hub.failReads([HubRequestError.noAnswer])

        await h.store.receiveTurnEvents([MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: turnID,
                                                             state: "running", seq: 2, at: "")])

        XCTAssertEqual(h.store.transcript?.items.map(\.seq), [1], "the event's read also failed")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.seen, .sending])
        XCTAssertEqual(ChatDelivery.drawnBelowRows(h.store.outgoingMessages(in: "chat-1"),
                                                   rows: h.store.transcript?.items).map(\.text),
                       ["Accepted but its row has not arrived", "Waiting behind it"],
                       "the earlier seen message is still the only visible copy of its text")
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.text),
                       ["Accepted but its row has not arrived", "Waiting behind it"],
                       "the event must not remove that visible copy from disk either")
    }

    func testEveryRefusedMessageComesBackToTheComposer() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.noAnswer, HubRequestError.noAnswer])
        await h.store.send("first")
        await h.store.send("second")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.state), [.sending, .sending])
        h.hub.failSends([HubRequestError.rejected(status: 400, message: "Not valid."),
                         HubRequestError.rejected(status: 400, message: "Not valid.")])

        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.returnedDrafts.count < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(h.store.returnedDrafts.map(\.text), ["first", "second"], "neither is lost")
        XCTAssertEqual(h.store.takeReturnedDrafts(in: "chat-1", appendingTo: "Already typed"),
                       "Already typed\nfirst\nsecond", "the composer preserves the order the messages were sent")
        XCTAssertTrue(h.store.returnedDrafts.isEmpty)
        XCTAssertTrue(relaunch(h).returnedDrafts.isEmpty, "taken drafts stay consumed after relaunch")
    }

    func testAQueuedPermanentRefusalSurvivesRelaunchUntilTheComposerTakesIt() async throws {
        for status in [400, 409, 422] {
            let h = try makeHarness()
            h.hub.seed(chatID: "chat-1", title: "Pricing sync")
            await h.store.open(chatID: "chat-1")
            h.hub.failSends([HubRequestError.noAnswer])
            await h.store.send("Keep this refused text for status \(status)")
            h.hub.failSends([HubRequestError.rejected(status: status, message: "This chat cannot take the message.")])
            await h.store.refreshOpen()
            for _ in 0..<200 where h.store.returnedDrafts.isEmpty || h.store.isBusy("send:chat-1") {
                try await Task.sleep(nanoseconds: 10_000_000)
            }

            let refused = try XCTUnwrap(h.store.returnedDrafts.first)
            XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.text), [refused.text],
                           "a refusal keeps the text on disk, including changed-chat 409 answers")
            let sends = h.hub.sent.count
            let next = relaunch(h)
            XCTAssertNil(next.openChatID, "draft recovery does not require opening the chat")
            XCTAssertEqual(next.returnedDrafts, [refused])
            XCTAssertEqual(next.outgoingMessages(in: "chat-1").map(\.deliveryIssue), [.failed],
                           "a refused message stays visibly failed, never queued")
            next.resumeQueuedMessages()
            try await Task.sleep(nanoseconds: 50_000_000)
            XCTAssertEqual(h.hub.sent.count, sends, "a refused draft is not sent automatically again")

            XCTAssertEqual(next.takeReturnedDrafts(in: "chat-1", appendingTo: "Already typed"),
                           "Already typed\n\(refused.text)")
            XCTAssertTrue(next.returnedDrafts.isEmpty)
            XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.deliveryIssue), [.failed])
            XCTAssertTrue(relaunch(h).returnedDrafts.isEmpty)
        }
    }

    func testADeletedChatsRefusedTextSurvivesRelaunchWithoutOpeningTheDeletedChat() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "deleted-chat", title: "Deleted on the Mac", state: .running)
        await h.store.open(chatID: "deleted-chat")
        await h.store.send("Recover this even though its chat is gone")
        XCTAssertTrue(h.hub.sent.isEmpty, "it waited behind the running turn")
        h.hub.delete(chatID: "deleted-chat")

        h.store.resumeQueuedMessages()
        for _ in 0..<200 where h.store.returnedDrafts.isEmpty || h.store.isBusy("send:deleted-chat") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        let refused = try XCTUnwrap(h.store.returnedDrafts.first)
        XCTAssertEqual(refused.chatID, "deleted-chat")
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "deleted-chat")?.map(\.text), [refused.text])

        let next = relaunch(h)
        await next.refreshList()
        XCTAssertTrue(next.chats.isEmpty, "the Mac no longer lists the deleted chat")
        XCTAssertNil(next.openChatID)
        XCTAssertEqual(next.returnedDrafts, [refused], "the list can recover text without a navigable chat row")
        XCTAssertTrue(next.takeReturnedDraft(refused), "a list recovery action can take the durable draft")
        XCTAssertTrue(relaunch(h).returnedDrafts.isEmpty)
    }

    func testATakenRefusedDraftStaysOnDiskAndInTheListUntilItsRemovalCanBeSaved() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Pricing sync")
        await h.store.open(chatID: "chat-1")
        h.hub.failSends([HubRequestError.rejected(status: 400, message: "Not valid.")])
        let queued = await h.store.send("Keep this until the composer can take it")
        XCTAssertTrue(queued, "once saved, even an immediate refusal returns through the durable draft path")
        let refused = try XCTUnwrap(h.store.returnedDrafts.first)
        try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: h.chatsDirectory.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: h.chatsDirectory.path) }

        XCTAssertFalse(h.store.takeReturnedDraft(refused))
        XCTAssertEqual(h.store.returnedDrafts, [refused])
        XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.text), [refused.text])
        XCTAssertEqual(h.store.takeReturnedDrafts(in: "chat-1", appendingTo: "Already typed"), "Already typed",
                       "a failed durable take must not append another copy to the composer")
        XCTAssertEqual(relaunch(h).returnedDrafts, [refused])

        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: h.chatsDirectory.path)
        XCTAssertEqual(h.store.takeReturnedDrafts(in: "chat-1", appendingTo: "Already typed"),
                       "Already typed\n\(refused.text)")
        XCTAssertTrue(relaunch(h).returnedDrafts.isEmpty)
    }

    func testAPermanentRefusalRecoversAfterSaveFailureWithoutRequeuing() async throws {
        for recoveringAfterRelaunch in [false, true] {
            let h = try makeHarness()
            h.hub.seed(chatID: "chat-1", title: "Pricing sync")
            await h.store.open(chatID: "chat-1")
            let refusal = HubRequestError.rejected(status: 404, message: "That chat is no longer on your Mac.")
            h.hub.failSends([refusal])
            let answer = HeldChatOperation()
            h.hub.holdNext("send", using: answer)
            let sending = Task { await h.store.send("Keep this text and its unanswered key") }
            await answer.waitUntilHeld()
            let originalKey = try XCTUnwrap(h.hub.sent.first?.key)
            let originalID = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first?.id)
            if !recoveringAfterRelaunch {
                let queuedOther = await h.store.send("A different message still waiting to go")
                XCTAssertTrue(queuedOther)
            }
            let originalFile = try XCTUnwrap(h.cache.loadOutgoing(chatID: "chat-1"))
            XCTAssertTrue(originalFile.allSatisfy(\.isQueued))
            try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: h.chatsDirectory.path)
            defer { try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: h.chatsDirectory.path) }

            await answer.release(error: refusal)
            let queued = await sending.value
            let keptKeys = await h.journal.entries()

            XCTAssertTrue(queued)
            XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1"), originalFile,
                           "a locked cache retains the original text instead of deleting it on refusal")
            XCTAssertEqual(keptKeys.map(\.key), [originalKey],
                           "the journal must not forget the key before the refused draft is durable")
            try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: h.chatsDirectory.path)

            if !recoveringAfterRelaunch {
                let expectedComposer = "Already typed\nKeep this text and its unanswered key"
                let composer = h.store.takeReturnedDrafts(in: "chat-1", appendingTo: "Already typed")
                XCTAssertEqual(composer, expectedComposer)
                XCTAssertTrue(h.store.returnedDrafts.isEmpty)
                let recovered = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first { $0.id == originalID })
                let savedRecovery = try XCTUnwrap(h.cache.loadOutgoing(chatID: "chat-1")?.first { $0.id == originalID })
                for receipt in [recovered, savedRecovery] {
                    XCTAssertTrue(receipt.hasFailed, "the failed delivery remains visible after composer recovery")
                    XCTAssertFalse(receipt.isQueued, "the authoritative refusal replaces the stale queued state")
                    XCTAssertFalse(receipt.isReturnedDraft, "recovered text must not be offered to the composer twice")
                    XCTAssertEqual(receipt.returnedToDraft, true, "older app versions must not resend the retained receipt")
                    XCTAssertEqual(receipt.failureDraftRecovered, true)
                }
                XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").filter(\.isQueued).map(\.text),
                               ["A different message still waiting to go"], "unrelated queued messages survive the transfer")
                XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.filter(\.isQueued).map(\.text),
                               ["A different message still waiting to go"])

                try await drainQueue(h)
                XCTAssertEqual(h.hub.sentTexts.filter { $0 == "Keep this text and its unanswered key" }.count, 1,
                               "refresh must never resend text already recovered into the composer")
                XCTAssertEqual(h.hub.storedUserTexts, ["A different message still waiting to go"])
                XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").first { $0.id == originalID }, recovered,
                               "refresh keeps the failed receipt without putting it back in the queue")
                XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.first { $0.id == originalID }, savedRecovery)
                XCTAssertEqual(h.store.takeReturnedDrafts(in: "chat-1", appendingTo: h.store.draft(in: "chat-1")),
                               expectedComposer, "a second recovery must not duplicate the composer text")
                XCTAssertEqual(h.cache.loadComposerDraft(chatID: "chat-1")?.text, expectedComposer)
                continue
            }

            h.hub.failSends([refusal])
            let next = relaunch(h)
            XCTAssertTrue(next.returnedDrafts.isEmpty, "the previous launch could not save the refusal")
            next.resumeQueuedMessages()
            for _ in 0..<200 where next.returnedDrafts.isEmpty || next.isBusy("send:chat-1") {
                try await Task.sleep(nanoseconds: 10_000_000)
            }

            XCTAssertEqual(h.hub.sent.map(\.key), [originalKey, originalKey])
            XCTAssertEqual(next.returnedDrafts.map(\.text), ["Keep this text and its unanswered key"])
            XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.isReturnedDraft), [true])
            let persistedJournal = PendingWriteJournal(fileURL: h.chatsDirectory.deletingLastPathComponent()
                .appendingPathComponent("desk-writes.json"))
            let remaining = await persistedJournal.entries()
            XCTAssertTrue(remaining.isEmpty, "the original key can be forgotten once the refusal is saved")
            XCTAssertEqual(relaunch(h).returnedDrafts, next.returnedDrafts)
        }
    }

    func testGoalsPrefillAppendsAfterRefusedDraftsWithoutReplacingThem() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Chat")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "",
                                                     state: .idle, kind: .main))
        await h.store.open(chatID: "main-chat")
        h.hub.failSends([HubRequestError.noAnswer])
        await h.store.send("My refused message")
        h.hub.failSends([HubRequestError.rejected(status: 400, message: "Not valid.")])
        await h.store.refreshOpen()
        for _ in 0..<200 where h.store.returnedDrafts.isEmpty || h.store.isBusy("send:main-chat") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        h.store.prefillMainChat("Let's do it: prepare the launch plan")

        XCTAssertEqual(h.store.returnedDrafts.map(\.text),
                       ["My refused message", "Let's do it: prepare the launch plan"])
        let next = relaunch(h)
        XCTAssertEqual(next.returnedDrafts.map(\.text), h.store.returnedDrafts.map(\.text),
                       "both the refused message and Goals prefill survive relaunch")
        XCTAssertEqual(next.takeReturnedDrafts(in: "main-chat", appendingTo: "Already typed"),
                       "Already typed\nMy refused message\nLet's do it: prepare the launch plan")
    }

    func testDeferredGoalsPrefillJoinsARefusedDraftWhenTheMainChatIsDiscovered() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "main-chat", title: "Chat")
        await h.store.open(chatID: "main-chat")
        h.hub.failSends([HubRequestError.rejected(status: 400, message: "Not valid.")])
        await h.store.send("My refused message")
        let refused = try XCTUnwrap(h.store.returnedDrafts.first)
        let next = relaunch(h)
        XCTAssertNil(h.cache.loadMainChatSummary(), "the refused chat's main identity is not known yet")

        next.prefillMainChat("I want to create a goal about health")

        XCTAssertNil(next.prepareMainChat())
        XCTAssertEqual(next.returnedDrafts, [refused], "a pending Goals hand-off must leave refused text intact")
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "",
                                                     state: .idle, kind: .main))

        XCTAssertEqual(next.prepareMainChat(), "main-chat")
        XCTAssertEqual(next.returnedDrafts.map(\.text),
                       ["My refused message", "I want to create a goal about health"])
        XCTAssertEqual(next.returnedDrafts.first, refused, "discovery must preserve the refused draft's identity")
        XCTAssertEqual(next.prepareMainChat(), "main-chat")
        XCTAssertEqual(next.returnedDrafts.count, 2, "preparing the chat again must not repeat the hand-off")
        XCTAssertEqual(relaunch(h).returnedDrafts, next.returnedDrafts,
                       "the deferred hand-off becomes durable alongside the refused draft")
    }

    func testSeveralDeferredGoalsPrefillsKeepTheirOrderAndTakingOneLeavesTheOthers() throws {
        let h = try makeHarness()
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "refused-1", text: "My refused message", after: nil,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      state: .sending, returnedToDraft: true)], chatID: "main-chat")
        let next = relaunch(h)
        next.prefillMainChat("I want to create a goal about health")
        next.prefillMainChat("I want to create a goal about career")
        XCTAssertEqual(next.returnedDrafts.map(\.text), ["My refused message"])
        try h.cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "",
                                                     state: .idle, kind: .main))

        XCTAssertEqual(next.prepareMainChat(), "main-chat")
        let drafts = next.returnedDrafts
        XCTAssertEqual(drafts.map(\.text), ["My refused message", "I want to create a goal about health",
                                           "I want to create a goal about career"])
        let health = try XCTUnwrap(drafts.first { $0.text == "I want to create a goal about health" })
        XCTAssertTrue(next.takeReturnedDraft(health))
        let remaining = drafts.filter { $0.id != health.id }
        XCTAssertEqual(next.returnedDrafts, remaining, "taking one prefill must leave every other draft in order")
        XCTAssertFalse(next.takeReturnedDraft(health), "a composer cannot take the same draft twice")
        XCTAssertEqual(relaunch(h).returnedDrafts, remaining, "only the selected draft is consumed on disk")
        XCTAssertEqual(next.takeReturnedDrafts(in: "main-chat", appendingTo: "Already typed"),
                       "Already typed\nMy refused message\nI want to create a goal about career")
        XCTAssertTrue(relaunch(h).returnedDrafts.isEmpty)
    }

    func testLaunchResumesQueuedMessagesInEveryChat() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "One")
        h.hub.seed(chatID: "chat-2", title: "Two")
        for (chat, text) in [("chat-1", "To one"), ("chat-2", "To two")] {
            try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-\(chat)", text: text, after: 0,
                                                          sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                          turnID: nil, seq: nil, state: .sending)],
                                     chatID: chat)
        }
        XCTAssertEqual(Set(h.cache.outgoingChatIDs()), ["chat-1", "chat-2"])

        let next = relaunch(h)
        next.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.storedUserTexts.count < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertEqual(Set(h.hub.storedUserTexts), ["To one", "To two"], "no chat had to be opened")
    }

    func testAnOutgoingFileFromTheFirstBuildStillLoads() throws {
        let h = try makeHarness()
        try h.cache.saveOutgoing([ChatOutgoingMessage(id: "m-1", text: "Earlier", after: 0,
                                                      sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                      turnID: nil, seq: nil, state: .sending)],
                                 chatID: "chat-1")
        let file = try XCTUnwrap(outgoingFile(h))
        // The first build wrote a bare list without the chat's id.
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        try encoder.encode([ChatOutgoingMessage(id: "m-1", text: "Earlier", after: 0,
                                                sentAt: Date(timeIntervalSince1970: 1_800_000_000),
                                                turnID: nil, seq: nil, state: .sending)]).write(to: file)

        XCTAssertEqual(h.cache.loadOutgoing(chatID: "chat-1")?.map(\.text), ["Earlier"])
        XCTAssertEqual(h.cache.outgoingChatIDs(), [], "found when its chat is listed or opened instead")
    }

    func testLaunchResumesAnOldBuildOutgoingFileUsingItsCachedChatHeader() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "chat-1", title: "Cached but never opened")
        try h.cache.save(ChatHeaderView(id: "chat-1", title: "Cached but never opened", state: .idle), items: [])
        try writeLegacyOutgoing(ChatOutgoingMessage(id: "m-1", text: "Waiting since the old build", after: 0,
                                                     sentAt: Date(timeIntervalSince1970: 1_800_000_000), state: .sending),
                                chatID: "chat-1", in: h)

        let next = relaunch(h)
        next.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.storedUserTexts.isEmpty || next.isBusy("send:chat-1") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertNil(next.openChatID)
        XCTAssertEqual(h.hub.storedUserTexts, ["Waiting since the old build"])
        XCTAssertEqual(h.cache.outgoingChatIDs(), ["chat-1"], "the recovered file names its chat on later launches")
    }

    func testLaunchResumesAnOldBuildOutgoingFileUsingOnlyItsJournalTarget() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "uncached-chat", title: "Not listed on this iPhone")
        _ = try await h.journal.reserve(operation: .chatMessage, target: "uncached-chat", choice: "message",
                                        fingerprint: WriteFingerprint.make(["Waiting with its original key"]),
                                        makeKey: { "old-build-key" }, createdAt: Date())
        try writeLegacyOutgoing(ChatOutgoingMessage(id: "m-1", text: "Waiting with its original key", after: 0,
                                                     sentAt: Date(timeIntervalSince1970: 1_800_000_000), state: .sending),
                                chatID: "uncached-chat", in: h)
        XCTAssertNil(h.cache.load(chatID: "uncached-chat"))
        XCTAssertTrue(h.cache.outgoingChatIDs().isEmpty, "the outgoing hash alone does not reveal its chat")

        let next = relaunch(h)
        next.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.storedUserTexts.isEmpty || next.isBusy("send:uncached-chat") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertNil(next.openChatID)
        XCTAssertTrue(next.chats.isEmpty, "neither the chat nor the list needed a read")
        XCTAssertEqual(h.hub.storedUserTexts, ["Waiting with its original key"])
        XCTAssertEqual(h.hub.sent.map(\.key), ["old-build-key"], "legacy recovery preserves idempotency")
        XCTAssertEqual(h.cache.outgoingChatIDs(), ["uncached-chat"])
    }

    func testLegacyChatIdentitySurvivesJournalPruningBeforeTheChatStoreIsCreated() async throws {
        let h = try makeHarness()
        h.hub.seed(chatID: "uncached-chat", title: "Waiting across an upgrade")
        let oldWrite = try await h.journal.reserve(operation: .chatMessage, target: "uncached-chat", choice: "message",
                                                  fingerprint: WriteFingerprint.make(["An older queued message"]),
                                                  makeKey: { "expired-key" },
                                                  createdAt: Date().addingTimeInterval(-2 * ActionStore.journalRetention))
        try writeLegacyOutgoing(ChatOutgoingMessage(id: "m-1", text: "An older queued message", after: 0,
                                                     sentAt: oldWrite.createdAt, state: .sending),
                                chatID: "uncached-chat", in: h)
        let journal = PendingWriteJournal(fileURL: h.chatsDirectory.deletingLastPathComponent()
            .appendingPathComponent("desk-writes.json"))
        XCTAssertEqual(journal.initialChatMessageTargets, ["uncached-chat"])
        // Launch maintenance removes stale keys before ChatStore gets a chance to recover the old file's identity.
        await journal.remove(keys: [oldWrite.key])
        let pruned = await journal.entries()
        XCTAssertTrue(pruned.isEmpty)
        XCTAssertTrue(h.cache.outgoingChatIDs().isEmpty)

        let next = ChatStore(hub: h.hub, journal: journal, guardian: h.guardian, presence: { true }, isPaired: { true },
                             cache: ChatCache(directory: h.chatsDirectory))
        XCTAssertEqual(h.cache.outgoingChatIDs(), ["uncached-chat"],
                       "the launch identity snapshot migrates the bare file even after its key was pruned")
        next.resumeQueuedMessages()
        for _ in 0..<200 where h.hub.storedUserTexts.isEmpty || next.isBusy("send:uncached-chat") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        XCTAssertNil(next.openChatID)
        XCTAssertEqual(h.hub.storedUserTexts, ["An older queued message"])
        XCTAssertNotEqual(h.hub.sent.first?.key, oldWrite.key, "the expired key is not restored by identity migration")
    }

    /// Lets the Mac finish each turn and answer a read until no message waits to be sent.
    private func drainQueue(_ h: ChatHarness, chatID: String = "chat-1") async throws {
        for _ in 0..<12 {
            guard h.store.outgoingMessages(in: chatID).contains(where: \.isQueued) else { return }
            h.hub.setState(chatID: chatID, .idle)
            await h.store.refreshOpen()
            try await Task.sleep(nanoseconds: 20_000_000)
            for _ in 0..<200 where h.store.isBusy("send:\(chatID)") {
                try await Task.sleep(nanoseconds: 5_000_000)
            }
        }
    }

    private func outgoingFile(_ h: ChatHarness) -> URL? {
        let names = (try? FileManager.default.contentsOfDirectory(atPath: h.chatsDirectory.path)) ?? []
        return names.first { $0.hasSuffix(".outgoing.json") }.map { h.chatsDirectory.appendingPathComponent($0) }
    }

    private func writeLegacyOutgoing(_ message: ChatOutgoingMessage, chatID: String, in h: ChatHarness) throws {
        try h.cache.saveOutgoing([message], chatID: chatID)
        let file = try XCTUnwrap(outgoingFile(h))
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        try encoder.encode([message]).write(to: file)
    }

    /// A new store over the same cache, as on the next launch.
    private func relaunch(_ h: ChatHarness) -> ChatStore {
        let journal = PendingWriteJournal(fileURL: h.chatsDirectory.deletingLastPathComponent()
            .appendingPathComponent("desk-writes.json"))
        let cache = ChatCache(directory: h.chatsDirectory)
        return ChatStore(hub: h.hub, journal: journal, guardian: h.guardian, presence: { true }, isPaired: { true },
                         cache: cache)
    }

    private func startupHarness(cached: Bool = false, knownMain: Bool = false, presenceDelay: TimeInterval = 0.4,
                                slowRoutes: Set<String> = [], acknowledgementDelay: TimeInterval = 0.1) throws
        -> MainChatStartupHarness {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Test iPhone", scopes: ["chat:read"], pairedAt: Date(),
                                  counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
        let relay = EncryptedStartupRelay(context: try vault.session(), presenceDelay: presenceDelay,
                                           slowRoutes: slowRoutes, acknowledgementDelay: acknowledgementDelay)
        let channel = HubChannel(vault: vault, relay: relay)
        let cache = ChatCache(directory: directory.appendingPathComponent("chats", isDirectory: true))
        if cached {
            try cache.save(ChatHeaderView(id: "main-chat", title: "Chat", state: .idle),
                           items: [item(1, text: "Cached chat reply")])
        }
        if cached || knownMain {
            try cache.rememberMainChat(ChatSummaryView(id: "main-chat", title: "Chat", updatedAt: "",
                                                       state: .idle, kind: .main))
        }
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("writes.json"))
        let store = ChatStore(hub: channel, journal: journal, guardian: ScriptedGuard(),
                              presence: { try await relay.presence() }, isPaired: { vault.current() != nil }, cache: cache)
        return MainChatStartupHarness(store: store, channel: channel, relay: relay)
    }

    private func waitForFreshTranscript(_ store: ChatStore) async {
        for _ in 0..<250 where store.transcript?.cursor == nil {
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func recordStartupMeasurement(_ text: String) {
        print(text)
        let attachment = XCTAttachment(string: text)
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}

private struct MainChatStartupHarness {
    let store: ChatStore
    let channel: HubChannel
    let relay: EncryptedStartupRelay
}

/// Exercises the real encrypted request, response, cursor and ACK path with controlled local-relay delays.
/// Delays are fixture inputs, not measurements of the user's Mac or Simulator.
private actor EncryptedStartupRelay: HubTransport {
    struct Counts {
        var paths: [String]
        var completedPaths: [String]
        var presence: Int
        var presenceCompleted: Int
        var polls: Int
        var acknowledgements: Int
        var total: Int { paths.count + presence + polls + acknowledgements }
    }

    private struct Request: Decodable {
        var id: String
        var path: String
    }

    private struct WaitingResponse {
        var request: Request
        var readyAt: TimeInterval
    }

    private let context: SessionContext
    private let presenceDelay: TimeInterval
    private let slowRoutes: Set<String>
    private let acknowledgementDelay: TimeInterval
    private var waiting: [WaitingResponse] = []
    private var paths: [String] = []
    private var completedPaths: [String] = []
    private var presenceCalls = 0
    private var presenceCompleted = 0
    private var polls = 0
    private var acknowledgements = 0
    private var sequence: UInt64 = 0

    init(context: SessionContext, presenceDelay: TimeInterval, slowRoutes: Set<String>,
         acknowledgementDelay: TimeInterval) {
        self.context = context
        self.presenceDelay = presenceDelay
        self.slowRoutes = slowRoutes
        self.acknowledgementDelay = acknowledgementDelay
    }

    func counts() -> Counts {
        Counts(paths: paths, completedPaths: completedPaths, presence: presenceCalls,
               presenceCompleted: presenceCompleted, polls: polls, acknowledgements: acknowledgements)
    }

    func presence() async throws -> Bool {
        presenceCalls += 1
        try await pause(presenceDelay)
        presenceCompleted += 1
        return true
    }

    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        let ciphertext = try RelayCrypto.base64URLDecode(envelope.ct)
        let plaintext = try RelayCrypto.openEnvelope(ciphertext, key: context.keys.d2h, hub: context.hubID,
                                                     dev: context.deviceID, dir: "d2h", seq: envelope.seq, kind: "req")
        let request = try JSONDecoder().decode(Request.self, from: plaintext)
        paths.append(request.path)
        try await pause(0.1)
        let delay: TimeInterval = slowRoutes.contains(request.path) ? 4 : 0.25
        waiting.append(WaitingResponse(request: request, readyAt: ProcessInfo.processInfo.systemUptime + delay))
    }

    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
        polls += 1
        let deadline = ProcessInfo.processInfo.systemUptime + TimeInterval(max(wait, 0))
        repeat {
            try Task.checkCancellation()
            let now = ProcessInfo.processInfo.systemUptime
            let ready = waiting.filter { $0.readyAt <= now }.sorted { $0.readyAt < $1.readyAt }
            if !ready.isEmpty {
                waiting.removeAll { $0.readyAt <= now }
                return try ready.map { pending in
                    completedPaths.append(pending.request.path)
                    sequence += 1
                    return try response(pending.request, seq: sequence)
                }
            }
            if now >= deadline { return [] }
            try await pause(0.01)
        } while true
    }

    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {
        try await pause(acknowledgementDelay)
        acknowledgements += 1
    }

    func waitForAcknowledgements(_ count: Int) async -> Bool {
        for _ in 0..<300 where acknowledgements < count {
            try? await pause(0.01)
        }
        return acknowledgements >= count
    }

    private func pause(_ seconds: TimeInterval) async throws {
        try await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
    }

    private func response(_ request: Request, seq: UInt64) throws -> RelayEnvelope {
        let status: Int
        let body: String
        switch request.path {
        case "/chats":
            status = 200
            body = #"{"ok":true,"data":{"chats":[{"id":"main-chat","title":"Chat","state":"idle","kind":"main"}]}}"#
        case "/chats/main-chat":
            status = 200
            body = #"{"ok":true,"data":{"chat":{"id":"main-chat","title":"Chat","state":"idle"},"items":[{"seq":2,"role":"assistant","kind":"text","text":"Fresh chat reply"}],"cursor":2,"hasMore":false}}"#
        default:
            status = 404
            body = #"{"ok":false,"error":"No brief yet"}"#
        }
        let plaintext = try JSONSerialization.data(withJSONObject: ["id": request.id, "status": status, "body": body])
        let ciphertext = try RelayCrypto.sealEnvelope(plaintext, key: context.keys.h2d, hub: context.hubID,
                                                      dev: context.deviceID, dir: "h2d", seq: seq, kind: "res")
        return RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                             dir: "h2d", seq: seq, kind: "res", ct: RelayCrypto.base64URLEncode(ciphertext))
    }
}

private final class AwayClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value = Date(timeIntervalSince1970: 1_800_000_000)
    var now: Date { lock.withLock { value } }
    func advance(_ seconds: TimeInterval) { lock.withLock { value += seconds } }
}

/// Chat while the computer is away: messages wait in order under one away period, undo on their own, never
/// expire, and send in order with their journaled keys when the computer is back.
@MainActor
final class ChatAwayStoreTests: XCTestCase {
    private func awayHarness(_ presence: ScriptedPresence, clock: AwayClock = AwayClock()) async throws -> ChatHarness {
        presence.setOnline(false)
        let h = try makeHarness(presence: { try await presence.check() }, now: { clock.now })
        h.hub.seed(chatID: "chat-1", title: "Chat")
        await h.store.open(chatID: "chat-1")
        await h.store.refreshBriefAskPresence()
        XCTAssertEqual(h.store.hubOnline, false)
        return h
    }

    private func relaunch(_ h: ChatHarness) -> ChatStore {
        let journal = PendingWriteJournal(fileURL: h.chatsDirectory.deletingLastPathComponent()
            .appendingPathComponent("desk-writes.json"))
        return ChatStore(hub: h.hub, journal: journal, guardian: h.guardian, presence: { true }, isPaired: { true },
                         cache: ChatCache(directory: h.chatsDirectory))
    }

    /// Each answer finishes its turn so the next waiting message can go out.
    private func waitForSends(_ h: ChatHarness, count: Int) async throws {
        for _ in 0..<40 where h.hub.storedUserTexts.count < count || h.store.isBusy("send:chat-1") {
            h.hub.setState(chatID: "chat-1", .idle)
            await h.store.refreshOpen()
            try await Task.sleep(nanoseconds: 20_000_000)
            for _ in 0..<200 where h.store.isBusy("send:chat-1") {
                try await Task.sleep(nanoseconds: 5_000_000)
            }
        }
    }

    /// While the computer is away every try fails the way an unreachable computer does.
    private func unreachable(_ h: ChatHarness) {
        h.hub.failSends(Array(repeating: RelayClientError.offline, count: 20))
    }

    private func keys(_ h: ChatHarness, for text: String) -> Set<String> {
        Set(h.hub.sent.filter { $0.text == text }.map(\.key))
    }

    func testWaitingMessagesKeepTheirOrderInOneAwayPeriodAndSendInOrderOnReconnect() async throws {
        let presence = ScriptedPresence()
        let h = try await awayHarness(presence)
        unreachable(h)
        h.store.noteConnection(cause: .sleeping, reason: "lid_closed", since: nil)
        _ = await h.store.send("First question")
        _ = await h.store.send("Second question")
        XCTAssertTrue(h.hub.storedUserTexts.isEmpty, "nothing reaches the computer while it is away")
        let waiting = h.store.outgoingMessages(in: "chat-1")
        XCTAssertEqual(waiting.map(\.text), ["First question", "Second question"])
        XCTAssertTrue(waiting.allSatisfy { $0.deliveryStatus(computerOnline: false) == .waitingForComputer })
        XCTAssertTrue(waiting.allSatisfy(h.store.canUndoWaiting))
        let periods = h.store.awayPeriods(in: "chat-1")
        XCTAssertEqual(periods.count, 1)
        XCTAssertEqual(periods.first?.messageIDs, waiting.map(\.id))
        XCTAssertEqual(periods.first?.cause, .lidClosed)
        let firstKey = try XCTUnwrap(keys(h, for: "First question").first)

        h.hub.failSends([])
        presence.setOnline(true)
        h.store.resumeQueuedMessages()
        try await waitForSends(h, count: 2)
        XCTAssertEqual(h.hub.storedUserTexts, ["First question", "Second question"])
        XCTAssertEqual(keys(h, for: "First question"), [firstKey], "every try of a message uses one journaled key")
        XCTAssertEqual(keys(h, for: "Second question").count, 1)
        let back = try XCTUnwrap(h.store.awayPeriods(in: "chat-1").first)
        XCTAssertTrue(back.isOver)
        XCTAssertEqual(Set(back.sentAt.keys), Set(waiting.map(\.id)))
        XCTAssertEqual(back.seqs.count, 2)
        XCTAssertNotNil(back.backLine())
        let left = await h.journal.entries()
        XCTAssertTrue(left.isEmpty)
    }

    func testARetryAfterRelaunchReusesTheKeyAndSendsOnce() async throws {
        let presence = ScriptedPresence()
        let h = try await awayHarness(presence)
        unreachable(h)
        _ = await h.store.send("Only once")
        let journaled = await h.journal.entries()
        let key = try XCTUnwrap(journaled.first?.key)
        h.hub.failSends([])
        let saved = relaunch(h)
        XCTAssertEqual(saved.outgoingMessages(in: "chat-1").first?.deliveryIssue, .waitingForComputer)
        XCTAssertEqual(saved.awayPeriods(in: "chat-1").first?.messageIDs.count, 1, "the away period survives relaunch")
        saved.resumeQueuedMessages()
        saved.resumeQueuedMessages()
        for _ in 0..<300 where h.hub.storedUserTexts.isEmpty || saved.isBusy("send:chat-1") {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(keys(h, for: "Only once"), [key])
        XCTAssertEqual(h.hub.storedUserTexts, ["Only once"])
    }

    func testUndoPutsTheWordsAfterTheBoxTextAndTheOthersKeepTheirOrder() async throws {
        let presence = ScriptedPresence()
        let h = try await awayHarness(presence)
        unreachable(h)
        _ = await h.store.send("First question")
        _ = await h.store.send("Second question")
        _ = await h.store.send("Third question")
        h.store.setDraft("Also", in: "chat-1")
        let first = try XCTUnwrap(h.store.outgoingMessages(in: "chat-1").first { $0.text == "First question" })
        let undone = await h.store.undoWaiting(first.id, in: "chat-1")
        XCTAssertTrue(undone)
        XCTAssertEqual(h.store.draft(in: "chat-1"), "Also\nFirst question")
        XCTAssertEqual(h.store.outgoingMessages(in: "chat-1").map(\.text), ["Second question", "Third question"])
        XCTAssertEqual(h.store.awayPeriods(in: "chat-1").first?.messageIDs.count, 2)
        let journaled = await h.journal.entries()
        XCTAssertFalse(journaled.contains { keys(h, for: "First question").contains($0.key) },
                       "the undone message's key is dropped")

        h.hub.failSends([])
        presence.setOnline(true)
        h.store.resumeQueuedMessages()
        try await waitForSends(h, count: 2)
        XCTAssertEqual(h.hub.storedUserTexts, ["Second question", "Third question"])
        let late = await h.store.undoWaiting(first.id, in: "chat-1")
        XCTAssertFalse(late)
    }

    /// The relay took the message but the asleep computer did not answer: it will run when the computer wakes, so
    /// the bubble says sent and offers no Undo (an undone message used to go out anyway). A message the relay never
    /// got (offline) can still be undone, and a later try keeps the key.
    func testAMessageTheRelayHoldsShowsSentWithoutUndo() async throws {
        let presence = ScriptedPresence()
        let h = try await awayHarness(presence)
        h.hub.failSends([HubRequestError.noAnswer] + Array(repeating: RelayClientError.offline, count: 20))
        _ = await h.store.send("Held at the relay")
        _ = await h.store.send("Never left the phone")
        let messages = h.store.outgoingMessages(in: "chat-1")
        let held = try XCTUnwrap(messages.first { $0.text == "Held at the relay" })
        let local = try XCTUnwrap(messages.first { $0.text == "Never left the phone" })
        XCTAssertEqual(held.relayHolds, true)
        XCTAssertFalse(h.store.canUndoWaiting(held))
        XCTAssertEqual(held.deliveryStatus(computerOnline: false), .sent)
        let undone = await h.store.undoWaiting(held.id, in: "chat-1")
        XCTAssertFalse(undone, "the computer will run it, so it cannot be taken back")
        XCTAssertNotEqual(local.relayHolds, true)
        XCTAssertTrue(h.store.canUndoWaiting(local))
        let firstKey = try XCTUnwrap(keys(h, for: "Held at the relay").first)

        h.hub.failSends([])
        presence.setOnline(true)
        h.store.resumeQueuedMessages()
        try await waitForSends(h, count: 2)
        XCTAssertEqual(keys(h, for: "Held at the relay"), [firstKey], "the retry reuses the key the relay holds")
    }

    func testAfterADayAwayTheMessagesStillWaitAndTheLineSaysSinceWhen() async throws {
        let presence = ScriptedPresence()
        let clock = AwayClock()
        let h = try await awayHarness(presence, clock: clock)
        unreachable(h)
        h.store.noteConnection(cause: .sleeping, reason: nil, since: nil)
        _ = await h.store.send("Still here tomorrow?")
        clock.advance(25 * 60 * 60)
        let saved = relaunch(h)
        XCTAssertEqual(saved.queuedMessageCount(in: "chat-1"), 1, "chat messages do not expire")
        let keys = await saved.queuedMessageKeys()
        let entries = await h.journal.entries()
        XCTAssertEqual(keys, Set(entries.map(\.key)), "pruning keeps the waiting message's key past a day")
        let period = try XCTUnwrap(saved.awayPeriods(in: "chat-1").first)
        XCTAssertFalse(period.isOver)
        let line = period.cause.line(since: period.startedAt, now: clock.now, locale: Locale(identifier: "en_US"))
        XCTAssertTrue(line.hasPrefix("Your computer has been asleep since "), line)
        XCTAssertEqual(period.cause.line(since: period.startedAt, now: period.startedAt.addingTimeInterval(60)),
                       "Your computer is asleep. Isoform answers when it wakes.")
    }
}

@MainActor
final class ChatMainAliasTests: XCTestCase {
    /// P128: with no main chat listed, a send goes to the computer's main-chat alias only when it says it can take
    /// one. An older computer refuses plainly, and the text stays in the composer (PROTOCOL.md 15.5a).
    func testMainAliasOnAnOlderComputerKeepsTheTextAndAsksForAnUpdate() async throws {
        let h = try makeHarness()
        await h.store.open(chatID: ChatStore.mainChatAlias)
        XCTAssertEqual(h.store.transcriptState, .loaded, "the composer opens without reading a chat that is not listed")
        h.store.setDraft("Hello there", in: ChatStore.mainChatAlias)

        let accepted = await h.store.send(h.store.draft(in: ChatStore.mainChatAlias))

        XCTAssertFalse(accepted)
        XCTAssertEqual(h.store.notice, ChatCopy.updateToSendFromPhone)
        XCTAssertEqual(h.store.draft(in: ChatStore.mainChatAlias), "Hello there")
        XCTAssertEqual(h.hub.sentTexts, [], "nothing is sent to an older computer")
    }
}
