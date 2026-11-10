import Foundation

/// A card stays available on refusal or an unanswered write. Only a confirmed result updates its state.
enum ProactiveActionOutcome: Equatable, Sendable {
    case applied(state: String)
    case refused(message: String)
    case pending(message: String)
    case cancelled
}

/// Journaled writes: proactive cards, "Not right" on a memory, and saving one-time work.
/// Only proactive actions that may execute work or answer permissions require owner confirmation.
/// Every write reserves an idempotency key in the journal before it is sent, reuses that key on a retry, and
/// forgets it once the hub answers with a result the phone can act on.
@MainActor
final class ActionStore: ObservableObject {
    @Published var notice: String?
    @Published var todayNotice: String?
    @Published private(set) var pendingToday: [PendingWrite] = []

    /// How long an unanswered write keeps its key. A retry within this window replays the Mac's first answer.
    static let journalRetention: TimeInterval = PendingWriteJournal.receiptLifetime
    private static let oneTimeWorkTarget = "new-one-time-work"

    private let hub: ActionHubAPI
    private let proactiveHub: ProactiveActionHubAPI?
    private let journal: PendingWriteJournal
    private let guardian: DecisionGuarding
    private let isPaired: @MainActor () -> Bool
    private let deviceID: @MainActor () -> String?
    private let now: @Sendable () -> Date
    private let writer: JournaledWriter
    private var busyTargets: Set<String> = []
    private var generation = UUID()
    private var replayingToday = false

    init(hub: ActionHubAPI,
         journal: PendingWriteJournal,
         guardian: DecisionGuarding,
         isPaired: @escaping @MainActor () -> Bool,
         deviceID: @escaping @MainActor () -> String? = { nil },
         now: @escaping @Sendable () -> Date = { Date() },
         makeKey: @escaping @Sendable () -> String = { UUID().uuidString.lowercased() },
         proactiveHub: ProactiveActionHubAPI? = nil) {
        self.hub = hub
        self.proactiveHub = proactiveHub ?? (hub as? ProactiveActionHubAPI)
        self.journal = journal
        self.guardian = guardian
        self.isPaired = isPaired
        self.deviceID = deviceID
        self.now = now
        self.writer = JournaledWriter(journal: journal, makeKey: makeKey, now: now)
    }

    func dismissNotice() {
        notice = nil
        todayNotice = nil
    }

    func clearAfterUnpair() {
        generation = UUID()
        notice = nil
        todayNotice = nil
        busyTargets.removeAll()
        pendingToday = []
    }

    // MARK: - Proactive chat cards

    func loadPendingToday() async {
        let current = generation
        guard let device = deviceID() else { pendingToday = []; return }
        do { try await journal.expireToday(at: now()) }
        catch { todayNotice = String(localized: "Isoform could not save this change. Try again.") }
        let entries = await journal.entries().filter { ($0.todayReplay != nil || $0.todayCorrection != nil) && $0.todayDeviceID == device }
        guard current == generation, deviceID() == device else { return }
        pendingToday = entries
    }

    func queueTodayAction(chatID: String, item: ChatProactiveView, actionID: String,
                          locale: Locale = .current) async -> ProactiveActionOutcome {
        let copy = ChatPresentationLanguage(locale: locale)
        let unavailable = copy.text(en: "This action is not available. Refresh the chat and try again.",
                                    zhCN: "此操作不可用。请刷新聊天后重试。")
        let buttons = ChatCardButtons.make(for: item)
        let visibleActions = [buttons.main?.actionID, buttons.second?.actionID].compactMap { $0 }
        guard isPaired(), let device = deviceID(), proactiveHub != nil, Identifiers.isSafePathID(chatID),
              !item.momentID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              item.momentID.utf8.count <= 48 * 1024, Identifiers.isSafePathID(actionID),
              item.canAct, item.actionIDs.contains(actionID),
              actionID == "mute_kind" || visibleActions.contains(actionID) else {
            return .refused(message: unavailable)
        }
        let current = generation
        do {
            let effect: DecisionEffect = DecisionAuthenticationPolicy.requiresOwnerConfirmation(
                proactiveActionID: actionID, permissionRequest: item.permission != nil) ? .outsideAction : .localChange
            try await guardian.authorize(effect, reason: copy.text(en: "Confirm this action for Isoform on your computer.",
                                                                  zhCN: "请确认此操作，以便电脑上的 Isoform 执行。"))
            guard current == generation, isPaired(), deviceID() == device else { return .cancelled }
            _ = try await journal.reserveToday(chatID: chatID, item: item, actionID: actionID, deviceID: device,
                                               makeKey: writer.makeKey, createdAt: now())
            await loadPendingToday()
            return .pending(message: String(localized: "Waits for your computer"))
        } catch {
            return .refused(message: error is HubRequestError ? unavailable : Self.message(for: error))
        }
    }

    func undoToday(_ key: String) async {
        do { _ = try await journal.undoToday(key: key) }
        catch { todayNotice = String(localized: "Isoform could not save this change. Try again.") }
        await loadPendingToday()
    }

    func dismissUnknownToday(_ key: String) async {
        do { _ = try await journal.transitionToday(key: key, event: .dismiss) }
        catch { todayNotice = String(localized: "Isoform could not save this change. Try again.") }
        await loadPendingToday()
    }

    func queueTodayCorrection(memoryID: String, reason: String, briefID: String, bodyVersion: String?) async -> Bool {
        guard isPaired(), let device = deviceID(), Identifiers.isSafePathID(memoryID) else { return false }
        let text = Self.bounded(reason)
        guard !text.isEmpty else { return false }
        let current = generation
        do {
            let correction = PendingWrite.TodayCorrection(briefID: briefID, bodyVersion: bodyVersion,
                                                          memoryID: memoryID, reason: text)
            _ = try await journal.reserveTodayCorrection(correction, deviceID: device, makeKey: writer.makeKey, createdAt: now())
            guard current == generation, deviceID() == device else { return false }
            await loadPendingToday()
            return true
        } catch {
            todayNotice = String(localized: "Isoform could not save this change. Try again.")
            return false
        }
    }

    func reconcileToday(_ snapshot: MobileTodaySnapshot, isCurrent: @MainActor () -> Bool = { true }) async {
        let current = generation
        let liveItems = snapshot.needsYou.compactMap { $0.card?.proactive }
        let liveDigests = Set(liveItems.compactMap { try? PendingWriteJournal.cardDigest($0) })
        let device = deviceID()
        let obsolete = await journal.entries().filter { write in
            if let replay = write.todayReplay {
                return write.todayDeviceID != device || !liveDigests.contains(replay.cardDigest)
            }
            if let correction = write.todayCorrection {
                guard write.todayDeviceID == device,
                      let brief = snapshot.briefs.first(where: { $0.id == correction.briefID }) else { return true }
                return correction.bodyVersion != nil && correction.bodyVersion != brief.bodyVersion
            }
            return false
        }
        guard current == generation, deviceID() == device, isCurrent() else { return }
        for write in obsolete where write.todayState == .reserved {
            _ = try? await journal.transitionToday(key: write.key, event: .undo)
        }
        await loadPendingToday()
    }

    func retryToday() async {
        guard !replayingToday, isPaired(), let proactiveHub, let device = deviceID() else { return }
        replayingToday = true
        let current = generation
        defer { replayingToday = false }
        for pending in await journal.entries() where pending.todayReplay != nil && pending.todayDeviceID == device {
            guard current == generation, isPaired(), deviceID() == device, !Task.isCancelled else { break }
            var didDispatch = false
            do {
                guard let write = try await journal.markTodayDispatched(key: pending.key, at: now()),
                      let replay = write.todayReplay else { continue }
                didDispatch = true
                await loadPendingToday()
                _ = try await HubRequestScope.$hubID.withValue(write.hubID) {
                    try await HubRequestScope.$deviceID.withValue(write.todayDeviceID) {
                        if replay.actionID == "mute_kind" {
                            return try await proactiveHub.muteProactiveKind(chatID: replay.chatID, momentID: replay.momentID,
                                                                            idempotencyKey: write.key)
                        }
                        return try await proactiveHub.performProactiveAction(chatID: replay.chatID, momentID: replay.momentID,
                                                                             actionID: replay.actionID, idempotencyKey: write.key)
                    }
                }
                _ = try await journal.transitionToday(key: write.key, event: .answer)
            } catch {
                guard didDispatch else {
                    todayNotice = String(localized: "Isoform could not save this change. Try again.")
                    break
                }
                if JournaledWriter.disposition(for: error) != .keep {
                    _ = try? await journal.transitionToday(key: pending.key, event: .answer)
                    todayNotice = String(localized: "Your computer could not finish that. Try again.")
                }
                else { break }
            }
        }
        for pending in await journal.entries() where pending.todayCorrection != nil && pending.todayDeviceID == device {
            guard current == generation, isPaired(), deviceID() == device, !Task.isCancelled else { break }
            var didDispatch = false
            do {
                guard let write = try await journal.markTodayCorrectionDispatched(key: pending.key, at: now()),
                      let correction = write.todayCorrection else { continue }
                didDispatch = true
                await loadPendingToday()
                _ = try await HubRequestScope.$hubID.withValue(write.hubID) {
                    try await HubRequestScope.$deviceID.withValue(write.todayDeviceID) {
                        try await hub.rejectAttribution(memoryID: correction.memoryID, reason: correction.reason, idempotencyKey: write.key)
                    }
                }
                _ = try await journal.transitionToday(key: write.key, event: .answer)
            } catch {
                guard didDispatch else {
                    todayNotice = String(localized: "Isoform could not save this change. Try again.")
                    break
                }
                if JournaledWriter.disposition(for: error) != .keep {
                    _ = try? await journal.transitionToday(key: pending.key, event: .answer)
                    todayNotice = String(localized: "Your computer could not finish that. Try again.")
                } else { break }
            }
        }
        await loadPendingToday()
    }

    func performProactiveAction(chatID: String, momentID: String, actionID: String, permissionRequest: Bool = true,
                                locale: Locale = .current) async -> ProactiveActionOutcome {
        let copy = ChatPresentationLanguage(locale: locale)
        let unavailable = copy.text(en: "This action is not available. Refresh the chat and try again.",
                                    zhCN: "此操作不可用。请刷新聊天后重试。")
        let waiting = copy.text(en: "Your computer has not confirmed this action yet. Tap the same action to check again.",
                                zhCN: "电脑尚未确认此操作。请再次点按同一操作进行确认。")
        guard isPaired() else { return .refused(message: Self.message(for: HubRequestError.notPaired)) }
        guard Identifiers.isSafePathID(chatID), !momentID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              momentID.utf8.count <= 48 * 1024,
              Identifiers.isSafePathID(actionID) else { return .refused(message: unavailable) }
        guard let proactiveHub else { return .refused(message: unavailable) }
        // Work identities can include a blocked reason. Keep display text out of the write journal.
        let target = "proactive:" + WriteFingerprint.make([chatID, momentID])
        let busyTarget = target
        guard claim(busyTarget) else { return .cancelled }
        let current = generation
        defer { if current == generation { release(busyTarget) } }
        let fingerprint = WriteFingerprint.make([chatID, momentID, actionID])
        // Do not send a contradictory choice while the Mac may already have applied the first one.
        let pending = await journal.entries().first {
            $0.operation == .chatProactiveAction && $0.target == target && $0.fingerprint != fingerprint
        }
        guard current == generation else { return .cancelled }
        guard pending == nil else {
            return .pending(message: copy.text(
                en: "Your previous action is still waiting for your computer. Tap that action again to check the result.",
                zhCN: "上一个操作仍在等待电脑确认。请再次点按该操作以查看结果。"))
        }
        do {
            let effect: DecisionEffect = DecisionAuthenticationPolicy.requiresOwnerConfirmation(
                proactiveActionID: actionID, permissionRequest: permissionRequest) ? .outsideAction : .localChange
            try await guardian.authorize(effect, reason: copy.text(en: "Confirm this action for Isoform on your computer.",
                                                            zhCN: "请确认此操作，以便电脑上的 Isoform 执行。"))
        } catch {
            guard current == generation else { return .cancelled }
            return .refused(message: Self.message(for: error))
        }
        guard current == generation, isPaired() else { return .cancelled }
        let outcome = await writer.send(.chatProactiveAction, target: target, choice: actionID,
                                         fingerprint: fingerprint) { key in
            guard current == self.generation else { throw CancellationError() }
            if actionID == "mute_kind" {
                return try await proactiveHub.muteProactiveKind(chatID: chatID, momentID: momentID,
                                                                idempotencyKey: key)
            }
            return try await proactiveHub.performProactiveAction(chatID: chatID, momentID: momentID,
                                                                  actionID: actionID, idempotencyKey: key)
        }
        guard current == generation else { return .cancelled }
        switch outcome {
        case .sent(let state):
            return .applied(state: state)
        case .notSaved:
            return .refused(message: copy.text(en: Copy.saveFailed,
                                               zhCN: "Isoform 无法在此手机上保存，因此未向电脑发送任何内容。请重试。"))
        case .cancelled, .failed(_, .keep):
            return .pending(message: waiting)
        case .failed(let error, .drop), .failed(let error, .changed):
            return .refused(message: Self.message(for: error))
        }
    }

    func recordProactiveOpened(chatID: String, momentID: String) async {
        guard isPaired(), Identifiers.isSafePathID(chatID),
              Identifiers.isSafePathID(momentID), let proactiveHub else { return }
        try? await proactiveHub.recordProactiveOpened(chatID: chatID, momentID: momentID,
                                                       idempotencyKey: UUID().uuidString.lowercased())
    }

    // MARK: - Not right on a memory

    /// Asks the Mac to stop using a memory for the item it supported. Returns true only when the Mac removed it.
    @discardableResult
    func rejectAttribution(memoryID: String, reason: String) async -> Bool {
        guard isPaired(), Identifiers.isSafePathID(memoryID) else { return false }
        let text = Self.bounded(reason)
        guard !text.isEmpty else {
            notice = Copy.reasonNeeded
            return false
        }
        let target = "memory:\(memoryID)"
        guard claim(target) else { return false }
        let current = generation
        defer { if current == generation { release(target) } }
        let fingerprint = WriteFingerprint.make([memoryID, text])
        let removed: Bool? = await submit(.correction, target: memoryID, choice: "reject_attribution",
                                          fingerprint: fingerprint) { key in
            try await self.hub.rejectAttribution(memoryID: memoryID, reason: text, idempotencyKey: key)
        }
        guard current == generation, let removed else { return false }
        notice = removed ? Copy.rejected : Copy.memoryGone
        return removed
    }

    // MARK: - One-time work

    /// Saves one-time work on the Mac. The Mac keeps it waiting until it is started from One-time work on the Mac,
    /// so the only change here is the notice.
    @discardableResult
    func startOneTimeWork(_ draft: OneTimeWorkDraft) async -> Bool {
        guard isPaired() else { return false }
        guard draft.canStart else {
            notice = Copy.workNeedsDetails
            return false
        }
        guard draft.trimmedWorkDir.isEmpty || draft.trimmedWorkDir.hasPrefix("/") else {
            notice = Copy.workFolderNotAbsolute
            return false
        }
        guard claim(Self.oneTimeWorkTarget) else { return false }
        let current = generation
        defer { if current == generation { release(Self.oneTimeWorkTarget) } }
        let fingerprint = WriteFingerprint.make([draft.trimmedTitle, draft.trimmedBrief, draft.trimmedWorkDir])
        let created: String? = await submit(.handoff, target: Self.oneTimeWorkTarget, choice: "create",
                                            fingerprint: fingerprint) { key in
            try await self.hub.startOneTimeWork(draft.request(idempotencyKey: key))
        }
        guard current == generation, created != nil else { return false }
        notice = Copy.workStarted
        return true
    }

    // MARK: - Journal

    @discardableResult
    func runOneTimeWork(_ draft: OneTimeWorkDraft, offerID: String) async -> Bool {
        guard isPaired() else { notice = HubRequestError.notPaired.errorDescription; return false }
        guard draft.canStart else { notice = Copy.workNeedsDetails; return false }
        guard draft.trimmedWorkDir.isEmpty || draft.trimmedWorkDir.hasPrefix("/") else {
            notice = Copy.workFolderNotAbsolute
            return false
        }
        let target = "start-offer:" + offerID
        guard claim(target) else { return false }
        let current = generation
        defer { if current == generation { release(target) } }
        let fingerprint = WriteFingerprint.make([draft.trimmedTitle, draft.trimmedBrief, draft.trimmedWorkDir])
        let started: Bool? = await submit(.handoff, target: target, choice: "start", fingerprint: fingerprint) { key in
            let id = try await self.hub.startOneTimeWork(draft.request(idempotencyKey: key + "-create"))
            guard current == self.generation else { throw CancellationError() }
            do {
                try await self.hub.runOneTimeWork(id: id, idempotencyKey: key + "-start")
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                throw WorkStartFailure(underlying: error)
            }
            return true
        }
        guard current == generation, started == true else { return false }
        notice = String(localized: "Work started on your computer")
        return true
    }

    /// Forgets journal entries that can no longer matter: writes older than a day without queued text, and every write of a kind
    /// this build no longer sends (the Desk's decisions, initiative commands, reviews and priorities). The journal
    /// is shared with Chat, so this runs once at launch rather than from either store's writes.
    func pruneJournal(keepingKeys: Set<String> = []) async {
        let cutoff = now().addingTimeInterval(-Self.journalRetention)
        let stale = await journal.allEntries().filter { write in
            (write.operation != .keepAwake && write.operation != .weekDecline && write.todayReplay == nil && write.todayRefresh != true && write.todayFeedback == nil && write.todayCorrection == nil && write.todayDraftSend == nil && write.calendarHold == nil && write.createdAt < cutoff && !keepingKeys.contains(write.key)) || Self.isRetired(write)
        }
        if !stale.isEmpty {
            await journal.remove(keys: stale.map(\.key))
        }
    }

    static func isRetired(_ write: PendingWrite) -> Bool {
        switch write.operation {
        case .decision, .initiativeCommand, .initiativeFeedback, .selfUpdateReview, .purpose, .goalCreate:
            return true
        case .correction:
            // Revising or dismissing a priority left with the Desk; "Not right" on a memory remains.
            return write.choice != "reject_attribution"
        case .handoff, .chatCreate, .chatMessage, .chatPermission, .chatProactiveAction, .chatCancel, .briefOpener, .weekDecline, .feedback, .goalEvent,
             .offerDecision, .topicMessage, .briefRefresh, .keepAwake, .todayDraftSend, .calendarHold:
            return false
        }
    }

    // MARK: - Writes

    /// Runs one write under a journaled key and shows the answer. Returns the hub's value, or nil when the write did
    /// not succeed.
    private func submit<Value>(_ operation: PendingWrite.Operation, target: String, choice: String,
                               fingerprint: String, send: (String) async throws -> Value) async -> Value? {
        let current = generation
        let outcome = await writer.send(operation, target: target, choice: choice, fingerprint: fingerprint) { key in
            guard current == self.generation else { throw CancellationError() }
            return try await send(key)
        }
        guard current == generation else { return nil }
        switch outcome {
        case .sent(let value):
            return value
        case .notSaved:
            notice = Copy.saveFailed
            return nil
        case .cancelled:
            return nil
        case .failed(let error, .keep), .failed(let error, .drop):
            notice = Self.message(for: error)
            return nil
        case .failed(_, .changed):
            notice = Copy.changed
            return nil
        }
    }

    private func claim(_ target: String) -> Bool {
        guard !busyTargets.contains(target) else { return false }
        busyTargets.insert(target)
        return true
    }

    private func release(_ target: String) {
        busyTargets.remove(target)
    }

    private static func bounded(_ text: String) -> String {
        CorrectionReason.bounded(text)
    }

    private static func message(for error: Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? Copy.generic
    }
}

private struct WorkStartFailure: LocalizedError {
    let underlying: Error
    var errorDescription: String? { (underlying as? LocalizedError)?.errorDescription ?? String(localized: "Couldn't start this task. Try again.") }
}

private enum Copy {
    static let generic = "Isoform could not finish that. Try again."
    static let saveFailed = "Isoform could not save this on this phone, so nothing was sent to your computer. Try again."
    static let changed = "This changed on your computer. Review it again."
    static let reasonNeeded = "Write a short reason before you send this to your computer."
    static let rejected = "Recorded on your computer. Isoform will stop using that memory."
    static let memoryGone = "Your computer no longer has that memory. Nothing changed."
    static let workNeedsDetails = "Add a title and what to do first."
    static let workFolderNotAbsolute = "Use the full folder path on your computer, starting with /, or leave the folder empty."
    static let workStarted = "Saved on your computer as one-time work. It starts when you start it from One-time work on your computer."
}

#if DEBUG && targetEnvironment(simulator)
extension ActionStore {
    /// A proactive action queued while the computer sleeps, written through the journal as the app writes one. Today's
    /// Needs-you section then shows "Waits for your computer" for it (PROTOCOL.md section 23).
    func seedWaitingTodayForUITest(chatID: String, item: ChatProactiveView, actionID: String) async {
        guard isPaired(), let device = deviceID() else { return }
        _ = try? await journal.reserveToday(chatID: chatID, item: item, actionID: actionID, deviceID: device,
                                            makeKey: writer.makeKey, createdAt: now())
        await loadPendingToday()
    }
}
#endif
