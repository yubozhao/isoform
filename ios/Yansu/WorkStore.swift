import Foundation

/// What the phone needs from the computer for Your work (B2 and B3).
protocol WorkHubAPI: Sendable {
    func workAvailable() async throws -> Bool
    /// The computer takes a Yes without sending, then Send (PROTOCOL.md 26.1).
    func workSendAvailable() async throws -> Bool
    func work() async throws -> WorkPage
    func answerWork(id: String, request: WorkAnswerRequest) async throws -> WorkItem
}

extension HubChannel: WorkHubAPI {
    func workAvailable() async throws -> Bool { try await abilities().work == true }
    func workSendAvailable() async throws -> Bool { try await abilities().workSend == true }

    func work() async throws -> WorkPage {
        try await perform(.work).payload(WorkPage.self)
    }

    func answerWork(id: String, request: WorkAnswerRequest) async throws -> WorkItem {
        guard let path = WorkPath.answer(id) else { throw HubRequestError.invalidIdentifier }
        let body = try JSONEncoder().encode(request)
        return try await perform(.write(path: path, body: String(decoding: body, as: UTF8.self)))
            .payload(WorkAnswerResult.self).item
    }
}

/// The answer route for an item id, percent-encoded (PROTOCOL.md 26.1): ids carry `:` and are the computer's.
enum WorkPath {
    static func answer(_ id: String) -> String? {
        guard !id.isEmpty, id.utf8.count <= 256, !id.unicodeScalars.contains(where: { $0.properties.generalCategory == .control }) else {
            return nil
        }
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~:")
        guard let encoded = id.addingPercentEncoding(withAllowedCharacters: allowed) else { return nil }
        return "/work/\(encoded)/answer"
    }

    /// A result's file, read-only (phone-agent v1.2.0 rule 3).
    static func file(_ id: String) -> String? {
        answer(id).map { String($0.dropLast("/answer".count)) + "/file" }
    }
}

/// A readable file of a result, as the computer sends it.
struct WorkFileText: Decodable, Equatable, Sendable {
    let name: String
    let text: String
}

protocol WorkFileAPI: Sendable {
    func workFile(itemID: String, fileID: String) async throws -> WorkFileText
}

extension HubChannel: WorkFileAPI {
    func workFile(itemID: String, fileID: String) async throws -> WorkFileText {
        guard let path = WorkPath.file(itemID) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.workFile(path: path, file: fileID)).payload(WorkFileText.self)
    }
}

/// Your work on the phone (phone-today v1.0.0 section 2). It draws from what the phone saved at once, refreshes in
/// place, and loads on its own: a failure here never touches the brief above it (phone-map section 1).
@MainActor
final class WorkStore: ObservableObject {
    /// Whether this computer serves Your work. Nil until asked; false on a computer without B2, where Today shows the
    /// rows it already has in the same places.
    @Published private(set) var supported: Bool?
    @Published private(set) var page: WorkPage?
    /// Answers given on the phone that the computer has not taken yet, oldest first, sent in order.
    @Published private(set) var pending: [Pending] = []
    @Published private(set) var failed = false
    /// Whether a Yes on offered work is recorded first and sent on the hand-off's Send (26.1).
    @Published private(set) var sendsSeparately = false

    struct Pending: Codable, Equatable, Sendable {
        let itemID: String
        let request: WorkAnswerRequest
        /// The item as it showed before the answer, for Undo before the computer took it.
        let before: WorkItem
    }

    private struct Saved: Codable {
        let hubID: String
        let page: WorkPage?
        let pending: [Pending]
    }

    private let hub: WorkHubAPI
    private let isPaired: () -> Bool
    private let canRead: () -> Bool
    private let hubID: () -> String?
    private let fileURL: URL
    private var refreshGeneration = 0
    private var sending = false

    init(hub: WorkHubAPI, isPaired: @escaping () -> Bool, canRead: @escaping () -> Bool,
         hubID: @escaping () -> String?, fileURL: URL) {
        self.hub = hub
        self.isPaired = isPaired
        self.canRead = canRead
        self.hubID = hubID
        self.fileURL = fileURL
    }

    /// The list with the phone's own answers laid over the computer's copy, so an answer shows at once.
    var items: [WorkItem] {
        guard let page else { return [] }
        var items = page.items
        for answer in pending {
            guard let index = items.firstIndex(where: { $0.id == answer.itemID }),
                  let action = WorkAction(rawValue: answer.request.action) else { continue }
            items[index] = YourWork.answered(items[index], action: action,
                                             reason: answer.request.reason.flatMap(WorkReason.init(rawValue:)),
                                             at: Date(), waits: true)
        }
        return items
    }

    func groups(now: Date) -> YourWork.Groups { YourWork.groups(items, now: now) }

    func loadCached() {
        guard let data = try? Data(contentsOf: fileURL),
              let saved = try? JSONDecoder().decode(Saved.self, from: data),
              isPaired(), saved.hubID == hubID() else { return }
        page = saved.page
        pending = saved.pending
        if saved.page != nil { supported = true }
    }

    func refresh() async {
        refreshGeneration += 1
        let generation = refreshGeneration
        guard isPaired(), canRead(), let hubID = hubID() else { return }
        do {
            let available = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.workAvailable() }
            guard generation == refreshGeneration, self.hubID() == hubID else { return }
            supported = available
            guard available else { return }
            sendsSeparately = (try? await HubRequestScope.$hubID.withValue(hubID) { try await hub.workSendAvailable() }) == true
            let result = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.work() }
            guard generation == refreshGeneration, self.hubID() == hubID else { return }
            page = result
            failed = false
            persist(hubID: hubID)
            await sendPending()
        } catch {
            guard generation == refreshGeneration else { return }
            failed = true
        }
    }

    /// One answer: shown at once, kept on the phone, and sent when the computer can be reached.
    func answer(_ item: WorkItem, action: WorkAction, reason: WorkReason? = nil, before: String? = nil,
                after: String? = nil, send: Bool? = nil) async {
        guard let hubID = hubID() else { return }
        let request = WorkAnswerRequest(idempotencyKey: UUID().uuidString.lowercased(), action: action.rawValue,
                                        reason: reason?.rawValue, before: before, after: after,
                                        answeredAt: RelayTime.format(Date()), seq: Self.nextSeq(), send: send)
        pending.append(Pending(itemID: item.id, request: request, before: item))
        persist(hubID: hubID)
        await sendPending()
    }

    /// An answer sent at once, outside the queue, for an item the computer may not list (a brief's sentence): the
    /// caller hears whether it was taken.
    func answerNow(_ item: WorkItem, action: WorkAction, reason: WorkReason? = nil) async throws {
        guard supported == true, let hubID = hubID() else { throw HubRequestError.notPaired }
        let request = WorkAnswerRequest(idempotencyKey: UUID().uuidString.lowercased(), action: action.rawValue,
                                        reason: reason?.rawValue, answeredAt: RelayTime.format(Date()), seq: Self.nextSeq())
        _ = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.answerWork(id: item.id, request: request) }
    }

    /// Undo on a done line. An answer still on the phone is simply dropped; one the computer took is undone there.
    func undo(_ item: WorkItem) async {
        if let index = pending.lastIndex(where: { $0.itemID == item.id }) {
            pending.remove(at: index)
            if let hubID = hubID() { persist(hubID: hubID) }
            return
        }
        await answer(item, action: .undo)
    }

    /// Adds a reason to a No already given. The No itself was recorded on the first tap.
    func addReason(_ reason: WorkReason, to item: WorkItem) async {
        if let index = pending.lastIndex(where: { $0.itemID == item.id && $0.request.action == WorkAction.no.rawValue }) {
            var request = pending[index].request
            request.reason = reason.rawValue
            pending[index] = Pending(itemID: item.id, request: request, before: pending[index].before)
            if let hubID = hubID() { persist(hubID: hubID) }
            await sendPending()
            return
        }
        await answer(item, action: .no, reason: reason)
    }

    /// The phone's answer sequence (B3): it only grows, so the computer applies one item's answers in order.
    private static func nextSeq(defaults: UserDefaults = .standard) -> Int {
        let next = defaults.integer(forKey: "work.answerSeq") + 1
        defaults.set(next, forKey: "work.answerSeq")
        return next
    }

    /// The hand-off's Send, at once and not queued: the sheet needs the answer to know whether the agent took it.
    /// A 409 (`agent_not_connected`, or no Yes waiting) is thrown, so the sheet offers the agent's own way in.
    func sendHandOff(_ item: WorkItem, agentID: String?, removed: [String]) async throws {
        guard let hubID = hubID() else { throw HubRequestError.notPaired }
        let request = WorkAnswerRequest(idempotencyKey: UUID().uuidString.lowercased(), action: WorkAction.send.rawValue,
                                        answeredAt: RelayTime.format(Date()), seq: Self.nextSeq(),
                                        removed: removed.isEmpty ? nil : removed, agentId: agentID)
        let updated = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.answerWork(id: item.id, request: request) }
        if var page, let index = page.items.firstIndex(where: { $0.id == updated.id }) {
            page.items[index] = updated
            self.page = page
            persist(hubID: hubID)
        }
    }

    /// Open on your computer: the computer opens the result's file there (v1.2.0 rule 3). Not an answer: the item
    /// keeps its state, and nothing goes in the ledger.
    func openFile(_ item: WorkItem, fileID: String) async throws {
        guard let hubID = hubID() else { throw HubRequestError.notPaired }
        let request = WorkAnswerRequest(idempotencyKey: UUID().uuidString.lowercased(), action: WorkAction.openFile.rawValue,
                                        answeredAt: RelayTime.format(Date()), file: fileID)
        _ = try await HubRequestScope.$hubID.withValue(hubID) { try await hub.answerWork(id: item.id, request: request) }
    }

    #if DEBUG
    func seedSendsSeparatelyForUITest() { sendsSeparately = true }
    #endif

    func sendPending() async {
        guard !sending, supported == true, let hubID = hubID() else { return }
        sending = true
        var changed = false
        defer {
            sending = false
            if changed { Task { await refresh() } }
        }
        while let next = pending.first {
            do {
                let updated = try await HubRequestScope.$hubID.withValue(hubID) {
                    try await hub.answerWork(id: next.itemID, request: next.request)
                }
                guard self.hubID() == hubID else { return }
                pending.removeFirst()
                if var page, let index = page.items.firstIndex(where: { $0.id == updated.id }) {
                    page.items[index] = updated
                    page.toAnswer = page.items.filter(\.isOpen).count
                    self.page = page
                }
                persist(hubID: hubID)
            } catch HubRequestError.conflict {
                // Answered elsewhere, or past Undo (409): drop this answer and read the list again.
                pending.removeFirst()
                persist(hubID: hubID)
                changed = true
            } catch HubRequestError.malformedResponse, is DecodingError {
                // The computer took it but its reply can't be read: never let it hold every later answer back.
                pending.removeFirst()
                persist(hubID: hubID)
                changed = true
            } catch HubRequestError.rejected(let status, _) where status == 400 || status == 404 {
                // Gone (404) or not fitting the item (400): retrying never helps.
                pending.removeFirst()
                persist(hubID: hubID)
                changed = true
            } catch {
                // Kept on the phone; the computer learns it waited (B3 queuedAt).
                if pending.first?.request.queuedAt == nil, var first = pending.first {
                    var request = first.request
                    request.queuedAt = request.answeredAt
                    first = Pending(itemID: first.itemID, request: request, before: first.before)
                    pending[0] = first
                    persist(hubID: hubID)
                }
                return
            }
        }
    }

    func clear() {
        page = nil
        pending = []
        supported = nil
        try? FileManager.default.removeItem(at: fileURL)
    }

    private func persist(hubID: String) {
        guard let data = try? JSONEncoder().encode(Saved(hubID: hubID, page: page, pending: pending)) else { return }
        try? data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    #if DEBUG
    func seedForUITest(_ page: WorkPage) {
        supported = true
        self.page = page
    }
    #endif
}
