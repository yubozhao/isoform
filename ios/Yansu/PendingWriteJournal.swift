import CryptoKit
import Foundation

/// One write the phone has sent or is about to send and has not seen answered. It keeps the idempotency
/// key and enough identity to recognise a retry (the fingerprint). Today actions also retain their
/// protected routing data and a card digest until they can be sent or undone.
struct PendingWrite: Codable, Equatable, Sendable {
    struct TodayReplay: Codable, Equatable, Sendable {
        let chatID: String
        let momentID: String
        let cardDigest: String
        let actionID: String
        var dispatched: Bool
        var dispatchedAt: Date? = nil
    }
    struct TodayFeedback: Codable, Equatable, Sendable {
        let briefID: String
        let itemID: String
        let cardKey: String
        let title: String
        let action: FeedFeedbackAction
        let reason: String?
        let bodyVersion: String?
        var dispatchedAt: Date? = nil
    }
    struct TodayCorrection: Codable, Equatable, Sendable {
        let briefID: String
        let bodyVersion: String?
        let memoryID: String
        let reason: String
        var dispatchedAt: Date? = nil
    }
    /// What a calendar hold needs to be sent again when the computer is back (handoff 7.3).
    struct CalendarHoldReplay: Codable, Equatable, Sendable {
        let proposalID: String
        let token: String
        let title: String
        let sourceKind: String
        let sourceID: String
        let start: String
        let end: String
        let calendarID: String
        /// The relay may already hold it for the computer (no answer came): it can no longer be undone.
        var relayHolds: Bool? = nil
    }
    struct TodayDraftSend: Codable, Equatable, Sendable {
        let taskID: String
        let revision: String
        let text: String?
        var dispatchedAt: Date? = nil
        var failed: Bool? = nil
        /// The person the reply is to, when the phone knows it. Optional so older journals still decode.
        var person: String? = nil
    }
    /// Persisted separately from the legacy dispatch markers so older journals remain readable.
    struct TodayLifecycle: Codable, Equatable, Sendable {
        let version: Int
        let state: TodayState
    }

    enum TodayState: String, Codable, CaseIterable, Sendable {
        case reserved, dispatched, outcomeUnknown, resolved, dismissed

        enum Event: CaseIterable {
            case dispatch, expire, answer, dismiss, undo
        }

        func transition(_ event: Event) -> TodayState? {
            switch (self, event) {
            case (.reserved, .dispatch): return .dispatched
            case (.dispatched, .dispatch): return .dispatched
            case (.dispatched, .expire): return .outcomeUnknown
            case (.dispatched, .answer), (.outcomeUnknown, .answer): return .resolved
            case (.outcomeUnknown, .dismiss), (.reserved, .undo): return .dismissed
            default: return nil
            }
        }
    }

    enum Operation: String, Codable, Sendable {
        // Retired with the Desk. Kept so a journal written by an older build still decodes;
        // ActionStore.pruneJournal drops these entries, because nothing can send them again.
        case decision
        case initiativeCommand = "initiative_command"
        case initiativeFeedback = "initiative_feedback"
        case selfUpdateReview = "self_update_review"
        case purpose
        // Retired when goals moved to the chat: the phone no longer adds goals itself.
        case goalCreate = "goal_create"
        // In use.
        case correction
        case handoff
        case chatCreate = "chat_create"
        case chatMessage = "chat_message"
        case chatPermission = "chat_permission"
        case chatProactiveAction = "chat_proactive_action"
        case chatCancel = "chat_cancel"
        case briefOpener = "brief_opener"
        case weekDecline = "week_decline"
        case feedback = "brief_feedback"
        case goalEvent = "goal_event"
        case offerDecision = "offer_decision"
        /// A message to the chat with a topic attached, from the Topic screen (PROTOCOL.md 15.8).
        case topicMessage = "topic_message"
        case briefRefresh = "brief_refresh"
        case keepAwake = "keep_awake"
        case todayDraftSend = "today_draft_send"
        case calendarHold = "calendar_hold"
    }

    let key: String
    let operation: Operation
    /// What the write is about: the memory id for a correction, the chat id for a chat write, the topic id for a
    /// topic message, `<briefId>/<itemId>` for Feed feedback, or a fixed name such as "new-one-time-work" for a write that creates a record.
    let target: String
    /// The choice that was made, such as "reject_attribution", "create" or a permission option.
    let choice: String
    /// Digest of every other field the request carries, so a retry only reuses a key for the same request.
    let fingerprint: String
    let createdAt: Date
    var hubID: String? = nil
    var todayReplay: TodayReplay? = nil
    var todayRefresh: Bool? = nil
    var todayRefreshDispatchedAt: Date? = nil
    var todayFeedback: TodayFeedback? = nil
    var todayCorrection: TodayCorrection? = nil
    var todayDraftSend: TodayDraftSend? = nil
    var todayDeviceID: String? = nil
    var todayLifecycle: TodayLifecycle? = nil
    var calendarHold: CalendarHoldReplay? = nil
    /// The words of a first-read correction still waiting for the computer (P163), so a reopened card can show them.
    /// Optional so older journals still decode.
    var correctionWords: String? = nil

    var todayState: TodayState? {
        guard todayReplay != nil || todayRefresh == true || todayFeedback != nil || todayCorrection != nil else { return nil }
        if let todayLifecycle {
            // An unrecognised version must never make a write eligible for replay.
            return todayLifecycle.version == 1 ? todayLifecycle.state : .outcomeUnknown
        }
        if todayReplay?.dispatched == true || todayRefreshDispatchedAt != nil ||
            todayFeedback?.dispatchedAt != nil || todayCorrection?.dispatchedAt != nil { return .dispatched }
        return .reserved
    }

    var todayDispatchedAt: Date? {
        todayReplay?.dispatchedAt ?? todayRefreshDispatchedAt ?? todayFeedback?.dispatchedAt ?? todayCorrection?.dispatchedAt
    }
}

enum WriteFingerprint {
    /// SHA-256 over the length-prefixed parts, so different splits of the same text never collide.
    static func make(_ parts: [String]) -> String {
        let framed = parts.map { "\($0.utf8.count):\($0)" }.joined(separator: "|")
        return RelayCrypto.sha256Hex(Data(framed.utf8))
    }
}

/// Keeps pending writes in Application Support until the hub answers, so a retry after a crash or a
/// relaunch sends the same idempotency key and the hub replays the original receipt.
actor PendingWriteJournal {
    static let receiptLifetime: TimeInterval = 24 * 60 * 60
    /// Capture legacy chat identities before launch maintenance can prune old keys. The cache uses them only
    /// to identify hashed outgoing files; requests still reserve keys under the current hub.
    nonisolated let initialChatMessageTargets: [String]
    private let fileURL: URL
    private var loaded = false
    private var writes: [PendingWrite] = []
    private let hubID: @Sendable () -> String?
    private var legacyHubID: String?
    private let writeData: @Sendable (Data, URL) throws -> Void

    init(fileURL: URL, hubID: @escaping @Sendable () -> String? = { nil },
         writeData: @escaping @Sendable (Data, URL) throws -> Void = { data, url in
             try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
         }) {
        self.fileURL = fileURL
        self.hubID = hubID
        self.writeData = writeData
        legacyHubID = hubID()
        let original = Self.readWrites(from: fileURL)
        initialChatMessageTargets = (original ?? []).filter {
            $0.operation == .chatMessage && ($0.hubID == nil || $0.hubID == hubID())
        }.map(\.target)
        // Bind an upgrade's unanswered writes to the Mac paired at launch, even if another Mac is
        // paired before the first retry. A failed save is retried by reserve before anything is sent.
        if let legacyHubID, let original {
            loaded = true
            writes = original
            let adopted = original.map { write in
                var write = write
                if write.hubID == nil { write.hubID = legacyHubID }
                return write
            }
            if adopted != original {
                do {
                    try Self.persist(adopted, to: fileURL, writeData: writeData)
                    writes = adopted
                } catch {}
            }
        }
    }

    /// The file name predates the Desk's removal. It stays, so writes an older build left unanswered are
    /// still retried under their saved keys.
    static var defaultFileURL: URL {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return support.appendingPathComponent("Isoform", isDirectory: true)
            .appendingPathComponent("desk-writes.json")
    }

    private static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }()

    private static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }()

    func entries() -> [PendingWrite] {
        loadIfNeeded()
        return writes.filter { $0.hubID == hubID() }
    }

    /// Maintenance must also see old hubs and unscoped legacy entries so retention applies to the whole file.
    func allEntries() -> [PendingWrite] {
        loadIfNeeded()
        return writes
    }

    /// Keeps what a held write needs to go again, so a queued hold survives a relaunch.
    func attachCalendarHold(target: String, fingerprint: String, replay: PendingWrite.CalendarHoldReplay) throws {
        loadIfNeeded()
        guard let index = writes.firstIndex(where: {
            $0.hubID == hubID() && $0.operation == .calendarHold && $0.target == target && $0.fingerprint == fingerprint
        }) else { return }
        writes[index].calendarHold = replay
        try persist()
    }

    func existing(operation: PendingWrite.Operation, target: String, fingerprint: String) -> PendingWrite? {
        loadIfNeeded()
        return writes.first { $0.hubID == hubID() && $0.operation == operation && $0.target == target && $0.fingerprint == fingerprint }
    }

    // Reserve and match under one actor turn. Adopt an older journal once, before returning a key;
    // after that, writes from another Mac must never supply the key for the currently paired hub.
    func reserve(operation: PendingWrite.Operation, target: String, choice: String, fingerprint: String,
                 makeKey: @Sendable () -> String, createdAt: Date, correctionWords: String? = nil) throws -> PendingWrite {
        loadIfNeeded()
        let currentHubID = hubID()
        try adoptLegacyWrites(for: currentHubID)
        // Today writes have their own dispatch deadline and must not bypass it through the generic writer,
        // including a different choice for the same target.
        guard !writes.contains(where: {
            $0.hubID == currentHubID && $0.operation == operation && $0.target == target && $0.todayState != nil
        }) else { throw HubRequestError.conflict(message: "") }
        if let write = writes.first(where: {
            $0.hubID == currentHubID && $0.operation == operation && $0.target == target && $0.fingerprint == fingerprint
        }) { return write }
        var write = PendingWrite(key: makeKey(), operation: operation, target: target, choice: choice,
                                 fingerprint: fingerprint, createdAt: createdAt, hubID: currentHubID)
        write.correctionWords = correctionWords
        try insert(write)
        return write
    }

    /// Moves a write reserved under an older fingerprint to a new one, keeping its key, unless the new one is
    /// already reserved. True when a write moved.
    @discardableResult
    func refingerprint(operation: PendingWrite.Operation, target: String, from old: String, to new: String) throws -> Bool {
        loadIfNeeded()
        let currentHubID = hubID()
        guard !writes.contains(where: {
            $0.hubID == currentHubID && $0.operation == operation && $0.target == target && $0.fingerprint == new
        }), let index = writes.firstIndex(where: {
            $0.hubID == currentHubID && $0.operation == operation && $0.target == target && $0.fingerprint == old
        }) else { return false }
        let previous = writes
        let write = writes[index]
        writes[index] = PendingWrite(key: write.key, operation: write.operation, target: write.target,
                                     choice: write.choice, fingerprint: new, createdAt: write.createdAt,
                                     hubID: write.hubID)
        do {
            try persist()
        } catch {
            writes = previous
            throw error
        }
        return true
    }

    private func adoptLegacyWrites(for currentHubID: String?) throws {
        if legacyHubID == nil { legacyHubID = currentHubID }
        guard let legacyHubID, writes.contains(where: { $0.hubID == nil }) else { return }
        let previous = writes
        for index in writes.indices where writes[index].hubID == nil {
            writes[index].hubID = legacyHubID
        }
        do {
            try persist()
        } catch {
            // Nothing may be sent until this assignment is durable. Keep every original key so a
            // later attempt can retry the migration instead of silently creating another write.
            writes = previous
            throw error
        }
    }

    /// Persists the write before the caller sends it. If the file cannot be written, the write is dropped
    /// and the error is thrown, so nothing goes out without a saved key.
    func insert(_ write: PendingWrite) throws {
        loadIfNeeded()
        writes.append(write)
        do {
            try persist()
        } catch {
            writes.removeAll { $0.key == write.key }
            throw error
        }
    }

    func reserveToday(chatID: String, item: ChatProactiveView, actionID: String, deviceID: String,
                      makeKey: @Sendable () -> String, createdAt: Date) throws -> PendingWrite {
        loadIfNeeded()
        guard let currentHubID = hubID() else { throw HubRequestError.notPaired }
        let target = "proactive:" + WriteFingerprint.make([chatID, item.momentID])
        let fingerprint = WriteFingerprint.make([chatID, item.momentID, actionID])
        try removeStaleToday(for: currentHubID, deviceID: deviceID)
        if let previous = writes.first(where: { $0.hubID == currentHubID && $0.operation == .chatProactiveAction && $0.target == target }) {
            guard previous.fingerprint == fingerprint, previous.todayReplay != nil else { throw HubRequestError.conflict(message: "") }
            return previous
        }
        var write = PendingWrite(key: makeKey(), operation: .chatProactiveAction, target: target, choice: actionID,
                                 fingerprint: fingerprint, createdAt: createdAt, hubID: currentHubID)
        write.todayReplay = PendingWrite.TodayReplay(chatID: chatID, momentID: item.momentID,
                                                     cardDigest: try Self.cardDigest(item),
                                                     actionID: actionID, dispatched: false)
        write.todayDeviceID = deviceID
        try insert(write)
        return write
    }

    nonisolated static func cardDigest(_ item: ChatProactiveView) throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return RelayCrypto.sha256Hex(try encoder.encode(item))
    }

    func reserveTodayRefresh(deviceID: String, makeKey: @Sendable () -> String, createdAt: Date) throws -> PendingWrite {
        loadIfNeeded()
        guard let currentHubID = hubID() else { throw HubRequestError.notPaired }
        try removeStaleToday(for: currentHubID, deviceID: deviceID)
        if let existing = writes.first(where: { $0.hubID == currentHubID && $0.operation == .briefRefresh }) { return existing }
        var write = PendingWrite(key: makeKey(), operation: .briefRefresh, target: "today", choice: "refresh",
                                 fingerprint: WriteFingerprint.make(["today-refresh"]), createdAt: createdAt,
                                 hubID: currentHubID)
        write.todayRefresh = true
        write.todayDeviceID = deviceID
        try insert(write)
        return write
    }

    /// `dispatchedAt` is set only by the UI-test seed for a send the relay already took; the app never passes it.
    func reserveTodayDraft(taskID: String, revision: String, text: String?, deviceID: String,
                           makeKey: @Sendable () -> String, createdAt: Date,
                           dispatchedAt: Date? = nil, person: String? = nil) throws -> PendingWrite {
        loadIfNeeded()
        guard let hub = hubID() else { throw HubRequestError.notPaired }
        try removeStaleToday(for: hub, deviceID: deviceID)
        let fingerprint = WriteFingerprint.make([taskID, revision, text ?? ""])
        if let existing = writes.first(where: { $0.hubID == hub && $0.todayDeviceID == deviceID && $0.operation == .todayDraftSend && $0.target == taskID }) {
            guard existing.fingerprint == fingerprint else { throw HubRequestError.conflict(message: "") }
            return existing
        }
        var write = PendingWrite(key: makeKey(), operation: .todayDraftSend, target: taskID, choice: "send",
                                 fingerprint: fingerprint, createdAt: createdAt, hubID: hub)
        write.todayDraftSend = PendingWrite.TodayDraftSend(taskID: taskID, revision: revision, text: text, dispatchedAt: dispatchedAt, person: person)
        write.todayDeviceID = deviceID
        try insert(write)
        return write
    }

    func markTodayDraft(key: String, dispatchedAt: Date? = nil, failed: Bool = false) throws -> PendingWrite? {
        loadIfNeeded()
        guard let index = writes.firstIndex(where: { $0.key == key && $0.hubID == hubID() && $0.todayDraftSend != nil }) else { return nil }
        let previous = writes[index]
        if let dispatchedAt, writes[index].todayDraftSend?.dispatchedAt == nil {
            writes[index].todayDraftSend?.dispatchedAt = dispatchedAt
        }
        if failed { writes[index].todayDraftSend?.failed = true }
        do { try persist() } catch { writes[index] = previous; throw error }
        return writes[index]
    }

    private func removeStaleToday(for hub: String, deviceID: String) throws {
        let previous = writes
        writes.removeAll { $0.hubID == hub && ($0.todayReplay != nil || $0.todayRefresh == true || $0.todayFeedback != nil || $0.todayCorrection != nil) && $0.todayDeviceID != deviceID }
        guard writes != previous else { return }
        do { try persist() } catch { writes = previous; throw error }
    }

    func reserveTodayCorrection(_ correction: PendingWrite.TodayCorrection, deviceID: String,
                                makeKey: @Sendable () -> String, createdAt: Date) throws -> PendingWrite {
        loadIfNeeded()
        guard let currentHubID = hubID() else { throw HubRequestError.notPaired }
        try removeStaleToday(for: currentHubID, deviceID: deviceID)
        let fingerprint = WriteFingerprint.make([correction.memoryID, correction.reason])
        if let existing = writes.first(where: { $0.hubID == currentHubID && $0.operation == .correction && $0.target == correction.memoryID }) {
            guard existing.fingerprint == fingerprint, existing.todayCorrection != nil else { throw HubRequestError.conflict(message: "") }
            return existing
        }
        var write = PendingWrite(key: makeKey(), operation: .correction, target: correction.memoryID,
                                 choice: "reject_attribution", fingerprint: fingerprint, createdAt: createdAt, hubID: currentHubID)
        write.todayCorrection = correction
        write.todayDeviceID = deviceID
        try insert(write)
        return write
    }

    func markTodayCorrectionDispatched(key: String, at date: Date) throws -> PendingWrite? {
        try dispatchToday(key: key, at: date, matching: { $0.todayCorrection != nil })
    }

    func reserveTodayFeedback(_ feedback: PendingWrite.TodayFeedback, deviceID: String,
                              makeKey: @Sendable () -> String, createdAt: Date) throws -> PendingWrite {
        loadIfNeeded()
        guard let currentHubID = hubID() else { throw HubRequestError.notPaired }
        try removeStaleToday(for: currentHubID, deviceID: deviceID)
        let target = "\(feedback.briefID)/\(feedback.itemID)"
        let fingerprint = FeedStore.fingerprint(briefID: feedback.briefID, itemID: feedback.itemID,
                                                action: feedback.action, reason: feedback.reason)
        if let existing = writes.first(where: { $0.hubID == currentHubID && $0.operation == .feedback && $0.target == target }) {
            guard existing.fingerprint == fingerprint, existing.todayFeedback != nil else { throw HubRequestError.conflict(message: "") }
            return existing
        }
        var write = PendingWrite(key: makeKey(), operation: .feedback, target: target, choice: feedback.action.rawValue,
                                 fingerprint: fingerprint, createdAt: createdAt, hubID: currentHubID)
        write.todayFeedback = feedback
        write.todayDeviceID = deviceID
        try insert(write)
        return write
    }

    func markTodayFeedbackDispatched(key: String, at date: Date) throws -> PendingWrite? {
        try dispatchToday(key: key, at: date, matching: { $0.todayFeedback != nil })
    }

    func markTodayRefreshDispatched(key: String, at date: Date) throws -> PendingWrite? {
        try dispatchToday(key: key, at: date, matching: { $0.todayRefresh == true })
    }

    func markTodayDispatched(key: String, at date: Date) throws -> PendingWrite? {
        try dispatchToday(key: key, at: date, matching: { $0.todayReplay != nil })
    }

    /// The actor checks the deadline immediately before sending, including after a suspended caller resumes.
    private func dispatchToday(key: String, at date: Date, matching: (PendingWrite) -> Bool) throws -> PendingWrite? {
        loadIfNeeded()
        guard let index = writes.firstIndex(where: { $0.key == key && $0.hubID == hubID() && matching($0) }) else { return nil }
        if writes[index].todayState == .dispatched &&
            (writes[index].todayDispatchedAt.map { date.timeIntervalSince($0) >= Self.receiptLifetime } ?? true) {
            _ = try transitionToday(at: index, event: .expire)
            return nil
        }
        guard writes[index].todayState?.transition(.dispatch) != nil else { return nil }
        let previous = writes[index]
        if writes[index].todayReplay != nil {
            writes[index].todayReplay?.dispatched = true
            if writes[index].todayReplay?.dispatchedAt == nil { writes[index].todayReplay?.dispatchedAt = date }
        }
        if writes[index].todayRefresh == true && writes[index].todayRefreshDispatchedAt == nil { writes[index].todayRefreshDispatchedAt = date }
        if writes[index].todayFeedback != nil && writes[index].todayFeedback?.dispatchedAt == nil { writes[index].todayFeedback?.dispatchedAt = date }
        if writes[index].todayCorrection != nil && writes[index].todayCorrection?.dispatchedAt == nil { writes[index].todayCorrection?.dispatchedAt = date }
        do { _ = try transitionToday(at: index, event: .dispatch) } catch { writes[index] = previous; throw error }
        return writes[index]
    }

    /// Expiry records uncertainty; it never sends a request or asserts whether one was executed.
    func expireToday(at date: Date) throws {
        loadIfNeeded()
        for index in writes.indices where writes[index].hubID == hubID() && writes[index].todayState == .dispatched {
            if writes[index].todayDispatchedAt.map({ date.timeIntervalSince($0) >= Self.receiptLifetime }) ?? true {
                _ = try transitionToday(at: index, event: .expire)
            }
        }
    }

    @discardableResult
    func transitionToday(key: String, event: PendingWrite.TodayState.Event) throws -> Bool {
        loadIfNeeded()
        guard let index = writes.firstIndex(where: { $0.key == key && $0.hubID == hubID() }) else { return false }
        return try transitionToday(at: index, event: event)
    }

    private func transitionToday(at index: Int, event: PendingWrite.TodayState.Event) throws -> Bool {
        guard let next = writes[index].todayState?.transition(event) else { return false }
        let previous = writes
        if next == .resolved || next == .dismissed {
            writes.remove(at: index)
        } else {
            writes[index].todayLifecycle = PendingWrite.TodayLifecycle(version: 1, state: next)
        }
        do { try persist() } catch { writes = previous; throw error }
        return true
    }

    /// A draft send the relay took and never answered is gone from the relay after a day (PROTOCOL.md section 23), so
    /// Send again may take it back and send it as a new send. Before that, a sent draft cannot be undone.
    static func draftSendCanBeUndone(_ draft: PendingWrite.TodayDraftSend?, now: Date = Date()) -> Bool {
        guard let draft else { return false }
        guard let dispatchedAt = draft.dispatchedAt else { return true }
        return now.timeIntervalSince(dispatchedAt) >= TaskSendWait.relayRetention
    }

    func undoToday(key: String) throws -> Bool {
        loadIfNeeded()
        guard let index = writes.firstIndex(where: { $0.key == key && $0.hubID == hubID() }) else { return false }
        if writes[index].todayState != nil { return try transitionToday(at: index, event: .undo) }
        guard writes[index].todayDraftSend != nil && Self.draftSendCanBeUndone(writes[index].todayDraftSend) else { return false }
        let previous = writes
        writes.remove(at: index)
        do { try persist() } catch { writes = previous; throw error }
        return true
    }

    /// Forgets writes the hub has answered, or that can no longer be retried. A removal that cannot be
    /// saved leaves a stale entry; a later retry replays the same key, which the hub answers safely.
    func remove(keys: [String]) {
        loadIfNeeded()
        let before = writes.count
        writes.removeAll { keys.contains($0.key) }
        guard writes.count != before else { return }
        try? persist()
    }

    /// A file that exists but cannot be read (before the phone's first unlock after a restart, or an I/O error) is
    /// not taken as empty: the journal stays unloaded, reads it again on the next access, and `persist` refuses to
    /// replace it. Treated as empty, the next write replaced the file with only itself and every unanswered key was
    /// lost, so retries went out under new keys and could run twice. A file that reads but does not decode is
    /// damaged beyond saving and is replaced as before.
    private func loadIfNeeded() {
        guard !loaded else { return }
        guard FileManager.default.fileExists(atPath: fileURL.path) else {
            loaded = true
            return
        }
        guard let data = try? Data(contentsOf: fileURL) else { return }
        loaded = true
        writes = (try? Self.decoder.decode([DecodedWrite].self, from: data))?.compactMap(\.write) ?? []
    }

    /// Entries decode one by one: an operation this build does not know (from a newer build, or one retired without
    /// a case) skips that entry, never the whole journal and every other key in it.
    private static func readWrites(from fileURL: URL) -> [PendingWrite]? {
        guard let data = try? Data(contentsOf: fileURL),
              let decoded = try? decoder.decode([DecodedWrite].self, from: data) else { return nil }
        return decoded.compactMap(\.write)
    }

    private struct DecodedWrite: Decodable {
        let write: PendingWrite?

        init(from decoder: Decoder) throws {
            write = try? PendingWrite(from: decoder)
        }
    }

    private func persist() throws {
        guard loaded else {
            // The file could not be read, so what is in memory was never saved and is not the whole list.
            writes = []
            throw CocoaError(.fileReadNoPermission)
        }
        try Self.persist(writes, to: fileURL, writeData: writeData)
    }

    private static func persist(_ writes: [PendingWrite], to fileURL: URL,
                                writeData: (Data, URL) throws -> Void) throws {
        if writes.isEmpty {
            if FileManager.default.fileExists(atPath: fileURL.path) {
                try FileManager.default.removeItem(at: fileURL)
            }
            return
        }
        try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(),
                                                withIntermediateDirectories: true)
        let data = try encoder.encode(writes)
        try writeData(data, fileURL)
    }
}
