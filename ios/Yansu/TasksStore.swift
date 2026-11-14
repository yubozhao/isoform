import Foundation
import UserNotifications

@MainActor
final class TasksStore: ObservableObject {
    enum LoadState: Equatable {
        case idle
        case loading
        case loaded
        case failed
    }

    @Published private(set) var page: MobileTasksPage? {
        didSet { updateIconBadge(enabled: UserDefaults.standard.bool(forKey: "phone.tasks.iconBadge")) }
    }
    @Published private(set) var loadState: LoadState = .idle
    @Published private(set) var firstCopyPending = false
    private static let firstCopyKey = "phone.firstCopy.tasks"
    @Published private(set) var repairNeeded = false
    @Published var notice: String?
    @Published private(set) var brainDumpAvailable = false
    @Published private(set) var detailLoadState: TaskDetailLoadState = .idle
    @Published private(set) var detailResetGeneration = 0
    /// Why the last reply or ··· action in a thread did not go through.
    @Published var threadNotice: String?
    /// The key of each reply the computer has not answered, by Mac, task and words.
    private var unansweredReplyKeys: [String: String] = [:]
    /// A send the computer held for the person's yes (PROTOCOL.md section 23).
    @Published var sendConfirmation: TaskSendConfirmation?
    @Published private(set) var stopFailureID: String?
    @Published private(set) var pendingDraftSends: [PendingWrite] = []
    private var badgeUpdate: Task<Void, Never>?
    private var draftReplayActive = false
    private var draftTimer: Task<Void, Never>?

    func updateIconBadge(enabled: Bool) {
        let count = Self.iconBadgeCount(enabled: enabled, needsYou: waitingCount)
        let previous = badgeUpdate
        badgeUpdate = Task {
            await previous?.value
            try? await UNUserNotificationCenter.current().setBadgeCount(count)
        }
    }

    static func iconBadgeCount(enabled: Bool, needsYou: Int) -> Int {
        enabled ? max(0, needsYou) : 0
    }

    var detail: MobileTaskDetail? {
        guard case .loaded(let detail) = detailLoadState else { return nil }
        return detail
    }

    private struct Saved: Codable {
        let hubID: String
        let page: MobileTasksPage
        var drafts: [String: CachedDraft]? = nil
    }

    private struct CachedDraft: Codable {
        let task: MobileTask
        let draft: MobileTaskDraft
        let actions: [String]

        var detail: MobileTaskDetail {
            MobileTaskDetail(id: task.id, title: task.title, status: task.status, group: task.group,
                             blockedReason: task.blockedReason, runner: task.runner, progress: task.progress,
                             createdAt: task.createdAt, updatedAt: task.updatedAt, startedAt: nil, steps: [],
                             outcome: task.outcome, unread: task.unread, actions: actions, draft: draft)
        }
    }
    private var cachedDrafts: [String: CachedDraft] = [:]

    private let hub: TasksHubAPI
    private let isPaired: @MainActor () -> Bool
    private let canRead: @MainActor () -> Bool
    private let stopPermission: @MainActor () -> Bool
    private let guardian: DecisionGuarding
    private let hubID: @MainActor () -> String?
    private let fileURL: URL
    private let journal: PendingWriteJournal?
    private let deviceID: @MainActor () -> String?
    private let computerOnline: @MainActor () -> Bool
    private var cacheHubID: String?
    private var detailRequestGeneration = 0
    private var refreshGeneration = 0
    private var threadWrites = 0
    private var refreshAfterWrite: String?

    init(hub: TasksHubAPI, isPaired: @escaping @MainActor () -> Bool,
         canRead: @escaping @MainActor () -> Bool = { true },
         canStop: @escaping @MainActor () -> Bool = { true },
         guardian: DecisionGuarding = DecisionGuard(),
         hubID: @escaping @MainActor () -> String? = { nil },
         journal: PendingWriteJournal? = nil,
         deviceID: @escaping @MainActor () -> String? = { nil },
         computerOnline: @escaping @MainActor () -> Bool = { true },
         fileURL: URL = TasksStore.defaultFileURL) {
        self.hub = hub
        self.isPaired = isPaired
        self.canRead = canRead
        self.stopPermission = canStop
        self.guardian = guardian
        self.hubID = hubID
        self.journal = journal
        self.deviceID = deviceID
        self.computerOnline = computerOnline
        self.fileURL = fileURL
        self.cacheHubID = nil
        loadCached()
        updateIconBadge(enabled: UserDefaults.standard.bool(forKey: "phone.tasks.iconBadge"))
    }

    nonisolated static var defaultFileURL: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Isoform", isDirectory: true).appendingPathComponent("tasks.json")
    }

    var canStop: Bool { stopPermission() }
    var computerReachable: Bool { computerOnline() }

    var waitingItems: [TodayNeed] {
        guard let page else { return [] }
        if let waiting = page.waitingItems { return waiting }
        return TaskGroups.make(from: page.tasks, threads: page.threads == true).needsYou.map {
            TodayNeed(id: $0.id, chatId: nil, card: nil, task: $0)
        }
    }

    var waitingCount: Int { page?.waitingCount ?? page?.counts.needsYou ?? 0 }

    func doneForToday(now: Date, briefMadeAt: Date?) -> [MobileTask] {
        TaskGroups.make(from: page?.tasks ?? [], threads: page?.threads == true).done.filter { task in
            guard !task.id.hasPrefix("draft."), let outcome = task.outcome, outcome.by == "isoform",
                  let at = outcome.date, at <= now, now.timeIntervalSince(at) <= 24 * 60 * 60 else { return false }
            if let opened = task.openedAt.flatMap(RelayTime.parse), opened >= at,
               let briefMadeAt, briefMadeAt > opened { return false }
            return true
        }
    }

    func loadPendingDraftSends() async {
        guard let journal, let device = deviceID() else { pendingDraftSends = []; return }
        let entries = await journal.entries().filter { $0.todayDraftSend != nil && $0.todayDeviceID == device }
        guard deviceID() == device else { return }
        pendingDraftSends = entries
    }

    func queueDraftSend(taskID: String, revision: String, text: String?, person: String? = nil) async -> Bool {
        guard let journal, let device = deviceID(), isPaired(), Identifiers.isSafePathID(taskID),
              !revision.isEmpty else { return false }
        do {
            if let failed = pendingDraftSends.first(where: { $0.todayDraftSend?.taskID == taskID && $0.todayDraftSend?.failed == true }) {
                await journal.remove(keys: [failed.key])
                if await journal.entries().contains(where: { $0.key == failed.key }) { return false }
            }
            _ = try await journal.reserveTodayDraft(taskID: taskID, revision: revision, text: text, deviceID: device,
                                                    makeKey: { UUID().uuidString.lowercased() }, createdAt: Date(),
                                                    person: RecipientName.name(person))
            await loadPendingDraftSends()
            scheduleDraftReplay(after: 5)
            return true
        } catch { return false }
    }

    func undoDraftSend(_ key: String) async {
        guard let journal else { return }
        _ = try? await journal.undoToday(key: key)
        await loadPendingDraftSends()
    }

    /// Yes to the ask: the held reply goes now as a live send. The journal entry goes only once it is sent.
    func confirmHeldSend(_ held: TaskSendConfirmation) async {
        sendConfirmation = nil
        let sent = await act(id: held.taskID, action: "send", text: held.text, draftRevision: held.draftRevision)
        if sent, !held.key.isEmpty, let journal {
            await journal.remove(keys: [held.key])
            await loadPendingDraftSends()
        }
    }

    /// Not now: the held reply stays as a draft.
    func setHeldSendAside(_ held: TaskSendConfirmation) async {
        sendConfirmation = nil
        if !held.key.isEmpty { await undoDraftSend(held.key) }
    }

    /// Drafted sends the relay has held a day with no answer: they did not happen (PROTOCOL.md section 23).
    var expiredDraftSends: [PendingWrite] {
        let cutoff = Date().addingTimeInterval(-TaskSendWait.relayRetention)
        return pendingDraftSends.filter { $0.createdAt < cutoff && $0.todayDraftSend?.failed != true }
    }

    /// Send again: the draft's words go out as a new send. Until then they stay on the draft.
    func sendExpiredAgain(_ pending: PendingWrite) async {
        guard let draft = pending.todayDraftSend else { return }
        await undoDraftSend(pending.key)
        _ = await queueDraftSend(taskID: draft.taskID, revision: draft.revision, text: draft.text, person: draft.person)
    }

    private func scheduleDraftReplay(after delay: TimeInterval) {
        draftTimer?.cancel()
        draftTimer = Task { @MainActor [weak self] in
            do { try await Task.sleep(for: .seconds(max(0, delay))) } catch { return }
            await self?.retryDraftSends()
        }
    }

    func retryDraftSends(computerResponded: Bool = false) async {
        guard !draftReplayActive, isPaired(), let journal, let device = deviceID(), let pairedHubID = hubID() else { return }
        draftReplayActive = true
        defer { draftReplayActive = false }
        await loadPendingDraftSends()
        for pending in pendingDraftSends {
            guard isPaired(), deviceID() == device, hubID() == pairedHubID,
                  pending.hubID == pairedHubID, let draft = pending.todayDraftSend, draft.failed != true else { continue }
            let remaining = pending.createdAt.addingTimeInterval(5).timeIntervalSinceNow
            if remaining > 0 { scheduleDraftReplay(after: remaining); continue }
            guard computerResponded || computerOnline() else { break }
            // An unanswered send must never be retried after receipts can expire.
            if let sent = draft.dispatchedAt, Date().timeIntervalSince(sent) >= ActionStore.journalRetention { continue }
            do {
                guard let saved = try await journal.markTodayDraft(key: pending.key, dispatchedAt: Date()) else { continue }
                await loadPendingDraftSends()
                let request = MobileTaskActionRequest(idempotencyKey: saved.key, action: "send", text: draft.text,
                                                      draftRevision: draft.revision,
                                                      queuedAt: TaskSendWait.queuedAt(pending.createdAt))
                let result = try await HubRequestScope.$hubID.withValue(saved.hubID) {
                    try await HubRequestScope.$deviceID.withValue(saved.todayDeviceID) {
                        try await hub.actOnTask(id: draft.taskID, request: request)
                    }
                }
                guard isPaired(), deviceID() == device, hubID() == pairedHubID else { return }
                accept(result)
                await journal.remove(keys: [saved.key])
                await loadPendingDraftSends()
                await refresh()
            } catch {
                if case .confirmSend(let person)? = error as? HubRequestError {
                    // Held: the reply waits as a set-aside draft until the person says yes or not now.
                    sendConfirmation = TaskSendConfirmation(key: pending.key, taskID: draft.taskID, text: draft.text,
                                                            draftRevision: draft.revision, person: person)
                    _ = try? await journal.markTodayDraft(key: pending.key, failed: true)
                    continue
                }
                if JournaledWriter.disposition(for: error) != .keep {
                    _ = try? await journal.markTodayDraft(key: pending.key, failed: true)
                } else { break }
            }
        }
        await loadPendingDraftSends()
    }

    func restoreFirstCopy(identity: TodayPairingIdentity) {
        firstCopyPending = UserDefaults.standard.string(forKey: Self.firstCopyKey) == "\(identity.hubID)/\(identity.deviceID)"
    }

    func beginFirstCopy(identity: TodayPairingIdentity) {
        UserDefaults.standard.set("\(identity.hubID)/\(identity.deviceID)", forKey: Self.firstCopyKey)
        firstCopyPending = true
    }

    func loadCached() {
        guard let data = try? Data(contentsOf: fileURL),
              let saved = try? JSONDecoder().decode(Saved.self, from: data),
              isPaired(), canRead(), let hubID = hubID() else { return }
        cacheHubID = saved.hubID
        guard saved.hubID == hubID else { return }
        page = saved.page
        cachedDrafts = saved.drafts ?? [:]
    }

    func refresh() async {
        refreshGeneration += 1
        let requestGeneration = refreshGeneration
        guard isPaired(), canRead() else {
            repairNeeded = isPaired()
            return
        }
        guard let hubID = hubID() else { return }
        let brainDump = try? await HubRequestScope.$hubID.withValue(hubID) { try await hub.brainDumpAvailable() }
        guard requestGeneration == refreshGeneration, isPaired(), self.hubID() == hubID else { return }
        if let brainDump { brainDumpAvailable = brainDump }
        loadState = .loading
        do {
            let result = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.tasks() }
            guard requestGeneration == refreshGeneration, isPaired(), canRead(), self.hubID() == hubID else { return }
            page = result
            cachedDrafts = cachedDrafts.filter { id, cached in
                result.tasks.contains { $0.id == id && $0.group == "needs_you" && $0.updatedAt == cached.task.updatedAt }
            }
            if firstCopyPending {
                firstCopyPending = false
                UserDefaults.standard.removeObject(forKey: Self.firstCopyKey)
            }
            repairNeeded = false
            notice = nil
            loadState = .loaded
            try persist(hubID: hubID, page: result)
        } catch HubRequestError.forbidden {
            guard requestGeneration == refreshGeneration else { return }
            repairNeeded = true
            loadState = .failed
        } catch HubRequestError.rejected(let status, _) where status == 404 {
            guard requestGeneration == refreshGeneration else { return }
            notice = String(localized: "Update Isoform on your computer to see tasks here")
            loadState = .failed
        } catch {
            guard requestGeneration == refreshGeneration else { return }
            loadState = .failed
        }
    }

    func parseBrainDump(_ text: String, key: String) async throws -> [BrainDumpItem] {
        guard let id = hubID(), isPaired() else { throw HubRequestError.noAnswer }
        return try await HubRequestScope.$hubID.withValue(id) { try await hub.parseBrainDump(text: text, idempotencyKey: key) }
    }

    func refreshBrainDumpCapability() async {
        guard let id = hubID(), isPaired() else { brainDumpAvailable = false; return }
        let available = try? await HubRequestScope.$hubID.withValue(id) { try await hub.brainDumpAvailable() }
        guard let available, isPaired(), hubID() == id else { return }
        brainDumpAvailable = available
    }

    func addBrainDump(_ items: [BrainDumpItem], key: String) async throws {
        guard let id = hubID(), isPaired() else { throw HubRequestError.noAnswer }
        try await HubRequestScope.$hubID.withValue(id) { try await hub.addBrainDump(items: items, idempotencyKey: key) }
        await refresh()
    }

    @discardableResult
    func loadDetail(id: String) async -> Result<MobileTaskDetail, TaskDetailFailure>? {
        detailRequestGeneration += 1
        let requestGeneration = detailRequestGeneration
        let resetGeneration = detailResetGeneration
        detailLoadState = .loading(id: id)
        guard Identifiers.isSafePathID(id) else {
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .unavailable)
        }
        guard isPaired() else {
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .offline)
        }
        guard canRead() else {
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .forbidden)
        }
        guard let hubID = hubID() else {
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .offline)
        }
        do {
            let result = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.task(id: id) }
            guard !Task.isCancelled, ownsDetailRequest(requestGeneration, resetGeneration) else { return nil }
            guard isPaired(), self.hubID() == hubID else {
                return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                                  failure: .offline)
            }
            guard canRead() else {
                return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                                  failure: .forbidden)
            }
            guard ownsDetailRequest(requestGeneration, resetGeneration) else { return nil }
            detailLoadState = .loaded(result)
            if let draft = result.draft, let actions = result.actions, result.group == "needs_you" {
                cachedDrafts[id] = CachedDraft(task: result.task, draft: draft, actions: actions)
                if let page { try? persist(hubID: hubID, page: page) }
            }
            return .success(result)
        } catch HubRequestError.forbidden {
            guard !Task.isCancelled, ownsDetailRequest(requestGeneration, resetGeneration) else { return nil }
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .forbidden)
        } catch HubRequestError.rejected(let status, _) where status == 404 {
            guard !Task.isCancelled, ownsDetailRequest(requestGeneration, resetGeneration) else { return nil }
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .unavailable)
        } catch is CancellationError {
            return nil
        } catch let error as URLError where error.code == .cancelled {
            return nil
        } catch {
            guard !Task.isCancelled, ownsDetailRequest(requestGeneration, resetGeneration) else { return nil }
            if let cached = cachedDrafts[id], page?.tasks.contains(where: { $0.id == id && $0.updatedAt == cached.task.updatedAt }) == true {
                let detail = cached.detail
                detailLoadState = .loaded(detail)
                return .success(detail)
            }
            return failDetail(id: id, generation: requestGeneration, resetGeneration: resetGeneration,
                              failure: .offline)
        }
    }

    func stop(id: String) async -> Bool {
        stopFailureID = nil
        guard Identifiers.isSafePathID(id), isPaired(), canRead(), canStop,
              let hubID = hubID() else { stopFailureID = id; return false }
        do {
            try await guardian.authorize(reason: String(localized: "Stop this task on your computer"))
            guard isPaired(), canRead(), canStop, self.hubID() == hubID else { stopFailureID = id; return false }
            let request = MobileTaskStopRequest(idempotencyKey: UUID().uuidString.lowercased())
            _ = try await HubRequestScope.$hubID.withValue(hubID) {
                try await hub.stopTask(id: id, request: request)
            }
            await refresh()
            return true
        } catch {
            stopFailureID = id
            return false
        }
    }

    /// Sends a reply that stays in this task. Your words show at once; on failure they go back to the box.
    @discardableResult
    func reply(id: String, text: String) async -> Bool {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, Identifiers.isSafePathID(id), isPaired(), canRead(), let hubID = hubID() else { return false }
        threadWrites += 1
        defer { finishThreadWrite() }
        let pending = MobileTaskEvent(id: "pending-" + UUID().uuidString.lowercased(), kind: "you",
                                      at: ISO8601DateFormatter().string(from: Date()), text: trimmed)
        if case .loaded(var detail) = detailLoadState, detail.id == id {
            detail.events.append(pending)
            detailLoadState = .loaded(detail)
        }
        // A reply the computer did not answer may still be queued at the relay and run when the computer wakes.
        // Sending the same words to the same task again reuses its key, so the computer runs it once (PROTOCOL.md
        // section 23); a new key posted the reply twice.
        let replyKey = "\(hubID)\n\(id)\n\(trimmed)"
        let idempotencyKey = unansweredReplyKeys[replyKey] ?? UUID().uuidString.lowercased()
        do {
            let request = MobileTaskReplyRequest(idempotencyKey: idempotencyKey, text: trimmed)
            unansweredReplyKeys[replyKey] = idempotencyKey
            let updated = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.replyToTask(id: id, request: request) }
            unansweredReplyKeys[replyKey] = nil
            guard self.hubID() == hubID else { return false }
            accept(updated)
            threadNotice = nil
            return true
        } catch {
            if JournaledWriter.disposition(for: error) != .keep { unansweredReplyKeys[replyKey] = nil }
            if case .loaded(var detail) = detailLoadState, detail.id == id {
                detail.events.removeAll { $0.id == pending.id }
                detailLoadState = .loaded(detail)
            }
            threadNotice = Self.threadFailure(error)
            return false
        }
    }

    /// Runs one ··· item or a tapped choice and shows the task as the computer now has it.
    @discardableResult
    func act(id: String, action: String, title: String? = nil, eventID: String? = nil, choiceID: String? = nil,
             text: String? = nil, draftRevision: String? = nil) async -> Bool {
        guard Identifiers.isSafePathID(id), isPaired(), canRead(), let hubID = hubID() else { return false }
        threadWrites += 1
        defer { finishThreadWrite() }
        if action == "choose", case .loaded(var detail) = detailLoadState, detail.id == id,
           let index = detail.events.firstIndex(where: { $0.id == eventID }),
           let choice = detail.events[index].openChoices.first(where: { $0.id == choiceID }) {
            // The tap becomes your message and the buttons go, before the computer answers.
            detail.events[index].chosenId = choice.id
            detail.events.insert(MobileTaskEvent(id: "pending-choice", kind: "you",
                                                 at: ISO8601DateFormatter().string(from: Date()), text: choice.label),
                                 at: index + 1)
            detailLoadState = .loaded(detail)
        }
        do {
            // A send made while the computer is away carries its queue time, so the computer asks again if it is late.
            let queuedAt = action == "send" && !computerOnline() ? TaskSendWait.queuedAt(Date()) : nil
            let request = MobileTaskActionRequest(idempotencyKey: UUID().uuidString.lowercased(), action: action,
                                                  title: title, eventId: eventID, choiceId: choiceID, text: text,
                                                  draftRevision: draftRevision, queuedAt: queuedAt)
            let updated = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.actOnTask(id: id, request: request) }
            guard self.hubID() == hubID else { return false }
            accept(updated)
            threadNotice = nil
            if ["mark_done", "not_now", "send"].contains(action) { await refresh() }
            return true
        } catch {
            if case .confirmSend(let person)? = error as? HubRequestError {
                sendConfirmation = TaskSendConfirmation(key: "", taskID: id, text: text, draftRevision: draftRevision,
                                                        person: person)
                return false
            }
            if action != "opened" {
                threadNotice = Self.threadFailure(error)
                _ = await loadDetail(id: id)
            }
            return false
        }
    }

    /// Terminal chat events arrive only after the computer has stored the answer.
    /// Task details do not name their chat, so re-read the open thread for each.
    func receiveTurn(_ event: MobileChatTurnEvent, for id: String) async {
        guard event.event == "chat_turn", event.state == "done" || event.state == "error",
              detailLoadState.taskID == id else { return }
        if threadWrites > 0 {
            refreshAfterWrite = id
            return
        }
        _ = await loadDetail(id: id)
    }

    private func finishThreadWrite() {
        threadWrites -= 1
        guard threadWrites == 0, let id = refreshAfterWrite else { return }
        refreshAfterWrite = nil
        guard detailLoadState.taskID == id else { return }
        Task { _ = await loadDetail(id: id) }
    }

    /// Clears the unread dot at once and tells the computer, without waiting for it.
    func markOpened(_ task: MobileTask) {
        guard task.unread == true || (task.group == "done" && task.outcome?.byYou == false) else { return }
        replaceRow(task.withUnread(false))
        Task { await act(id: task.id, action: "opened") }
    }

    private func accept(_ detail: MobileTaskDetail) {
        // A list read begun before this write must not replace its newer row.
        refreshGeneration += 1
        if case .loaded(let current) = detailLoadState, current.id == detail.id {
            detailRequestGeneration += 1
            detailLoadState = .loaded(detail)
        } else if case .loading(let loadingID) = detailLoadState, loadingID == detail.id {
            // A read begun before this write must not replace its newer response.
            detailRequestGeneration += 1
            detailLoadState = .loaded(detail)
        }
        replaceRow(detail.task)
    }

    private func replaceRow(_ task: MobileTask) {
        guard let current = page, let index = current.tasks.firstIndex(where: { $0.id == task.id }) else { return }
        let wasWaiting = current.tasks[index].group == "needs_you"
        var tasks = current.tasks
        tasks[index] = task
        var waiting = current.waitingItems
        var waitingCount = current.waitingCount
        var counts = current.counts
        if wasWaiting && task.group != "needs_you" {
            if let waitingIndex = waiting?.firstIndex(where: { $0.task?.id == task.id }) {
                waiting?.remove(at: waitingIndex)
                if let count = waitingCount { waitingCount = max(0, count - 1) }
            }
            counts = MobileTaskCounts(needsYou: max(0, counts.needsYou - 1), running: counts.running,
                                      doneThisWeek: counts.doneThisWeek)
        } else if let waitingIndex = waiting?.firstIndex(where: { $0.task?.id == task.id }) {
            waiting?[waitingIndex].task = task
        }
        page = MobileTasksPage(tasks: tasks, counts: counts, threads: current.threads,
                               scheduled: current.scheduled, waitingItems: waiting, waitingCount: waitingCount)
    }

    static func threadFailure(_ error: Error) -> String {
        switch error {
        case HubRequestError.rejected(let status, _) where status == 404:
            return String(localized: "Update Isoform on your computer to reply here")
        case HubRequestError.conflict(let message):
            return message
        case HubRequestError.forbidden:
            return String(localized: "Pair this phone again to reply here.")
        default:
            return String(localized: "Couldn't reach your computer")
        }
    }

    #if DEBUG && targetEnvironment(simulator)
    func seedBrainDumpCapabilityForUITest() { brainDumpAvailable = true }
    func seedForUITest(_ page: MobileTasksPage) {
        self.page = page
        loadState = .loaded
    }
    /// A held reply waiting for the person's yes, named for \(person) when there is one (PROTOCOL.md section 23).
    func seedSendConfirmationForUITest(person: String?) {
        sendConfirmation = TaskSendConfirmation(key: "", taskID: "ui-held-thread", text: "Yes, Thursday works.",
                                                draftRevision: nil, person: person)
    }
    /// A drafted reply the relay held a day with no answer, written through the journal as the app writes one.
    func seedExpiredDraftForUITest(person: String? = nil) async {
        guard let journal, let device = deviceID() else { return }
        let made = Date().addingTimeInterval(-(TaskSendWait.relayRetention + 3600))
        // The relay took this send a day ago and never answered. Written with its dispatch time in one insert, so the
        // launch replay never sees it undispatched and sends it again: the same state a real held send is in.
        _ = try? await journal.reserveTodayDraft(taskID: "draft.ui-sow", revision: "r1", text: "Yes, Thursday works.",
                                                 deviceID: device, makeKey: { "ui-expired-draft" }, createdAt: made,
                                                 dispatchedAt: made, person: person)
        await loadPendingDraftSends()
    }
    #endif

    func pairingCommitted() {
        refreshGeneration += 1
        guard isPaired(), canRead(), let currentHub = hubID(), currentHub == cacheHubID else {
            _ = clearAfterUnpair()
            return
        }
        resetDetail()
        loadState = .idle
        repairNeeded = false
        notice = nil
        stopFailureID = nil
    }

    @discardableResult
    func clearAfterUnpair() -> Bool {
        refreshGeneration += 1
        brainDumpAvailable = false
        page = nil
        cachedDrafts = [:]
        pendingDraftSends = []
        draftTimer?.cancel()
        resetDetail()
        loadState = .idle
        repairNeeded = false
        notice = nil
        cacheHubID = hubID()
        do {
            if FileManager.default.fileExists(atPath: fileURL.path) {
                try FileManager.default.removeItem(at: fileURL)
            }
            return true
        } catch {
            return false
        }
    }

    private func persist(hubID: String, page: MobileTasksPage) throws {
        try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(),
                                                withIntermediateDirectories: true)
        try JSONEncoder().encode(Saved(hubID: hubID, page: page, drafts: cachedDrafts))
            .write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        cacheHubID = hubID
    }

    private func resetDetail() {
        detailRequestGeneration += 1
        refreshAfterWrite = nil
        detailLoadState = .idle
        detailResetGeneration += 1
    }

    private func ownsDetailRequest(_ requestGeneration: Int, _ resetGeneration: Int) -> Bool {
        requestGeneration == detailRequestGeneration && resetGeneration == detailResetGeneration
    }

    private func failDetail(id: String, generation: Int, resetGeneration: Int,
                            failure: TaskDetailFailure) -> Result<MobileTaskDetail, TaskDetailFailure>? {
        guard ownsDetailRequest(generation, resetGeneration), !Task.isCancelled else { return nil }
        detailLoadState = .failed(id: id, failure)
        return .failure(failure)
    }
}
