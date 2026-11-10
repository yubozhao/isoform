import Foundation

/// What this iPhone remembers about the Feed: the interest sentence from the prompt box, and the Like or Not right
/// given on each card. Feedback goes to the Mac through the write journal, so a retry after a lost answer reuses its
/// key, and an answer the Mac did not get yet is sent again when the Feed next opens.
@MainActor
final class FeedStore: ObservableObject {
    enum Delivery: String, Codable, Sendable {
        /// The Mac answered.
        case sent
        /// Saved here; the Mac has not answered yet.
        case pending
    }

    struct Choice: Codable, Equatable, Sendable {
        var action: FeedFeedbackAction
        var delivery: Delivery
        var at: Date
        /// Where the feedback goes, so it can be sent again after a relaunch.
        var briefID: String
        var itemID: String
    }

    struct DecisionApproval: Sendable {
        let action: FeedFeedbackAction
        let card: FeedCardModel
        let reason: String?
        fileprivate let generation: UUID
    }

    private struct Saved: Codable {
        var interest: String?
        var choices: [String: Choice] = [:]
    }

    @Published private(set) var interest: String?
    /// By card key.
    @Published private(set) var choices: [String: Choice] = [:]
    @Published var notice: String?
    @Published var todayNotice: String?
    @Published private(set) var pendingToday: [PendingWrite] = []

    static let interestMaxLength = 200
    static let choiceLimit = 500

    private let hub: FeedHubAPI
    private let journal: PendingWriteJournal
    private let guardian: DecisionGuarding
    private let isPaired: @MainActor () -> Bool
    private let deviceID: @MainActor () -> String?
    private let fileURL: URL
    private let now: @Sendable () -> Date
    private let writer: JournaledWriter
    private var busy: Set<String> = []
    private var generation = UUID()
    private var replayingToday = false

    init(hub: FeedHubAPI,
         journal: PendingWriteJournal,
         guardian: DecisionGuarding,
         isPaired: @escaping @MainActor () -> Bool,
         deviceID: @escaping @MainActor () -> String? = { nil },
         fileURL: URL = FeedStore.defaultFileURL,
         now: @escaping @Sendable () -> Date = { Date() },
         makeKey: @escaping @Sendable () -> String = { UUID().uuidString.lowercased() }) {
        self.hub = hub
        self.journal = journal
        self.guardian = guardian
        self.isPaired = isPaired
        self.deviceID = deviceID
        self.fileURL = fileURL
        self.now = now
        self.writer = JournaledWriter(journal: journal, makeKey: makeKey, now: now)
        if let data = try? Data(contentsOf: fileURL), let saved = try? Self.decoder.decode(Saved.self, from: data) {
            interest = saved.interest
            choices = saved.choices
        }
    }

    nonisolated static var defaultFileURL: URL {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return support.appendingPathComponent("Isoform", isDirectory: true).appendingPathComponent("feed.json")
    }

    // MARK: Interest

    /// Keeps the sentence from "Make me a feed about..." on this iPhone. No route carries it to the Mac yet.
    func setInterest(_ text: String) {
        let trimmed = String(text.trimmingCharacters(in: .whitespacesAndNewlines).prefix(Self.interestMaxLength))
        interest = trimmed.isEmpty ? nil : trimmed
        save()
    }

    // MARK: Feedback

    func loadPendingToday() async {
        let current = generation
        guard let device = deviceID() else { pendingToday = []; return }
        do { try await journal.expireToday(at: now()) }
        catch { todayNotice = String(localized: "Isoform could not save this change. Try again.") }
        let values = await journal.entries().filter { $0.todayFeedback != nil && $0.todayDeviceID == device }
        guard current == generation, deviceID() == device else { return }
        pendingToday = values
    }

    func queueToday(_ approval: DecisionApproval, bodyVersion: String?) async -> Bool {
        guard approval.generation == generation, isPaired(), let device = deviceID() else { return false }
        let card = approval.card
        let pending = PendingWrite.TodayFeedback(briefID: card.briefID, itemID: card.feedbackItemID,
                                                cardKey: card.key, title: card.item.title ?? String(localized: "Untitled"),
                                                action: approval.action, reason: approval.reason, bodyVersion: bodyVersion)
        do {
            _ = try await journal.reserveTodayFeedback(pending, deviceID: device, makeKey: writer.makeKey, createdAt: now())
            guard approval.generation == generation, deviceID() == device else { return false }
            await loadPendingToday()
            return true
        } catch {
            todayNotice = String(localized: "Isoform could not save this change. Try again.")
            return false
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

    func reconcileToday(_ snapshot: MobileTodaySnapshot, isCurrent: @MainActor () -> Bool = { true }) async {
        let current = generation
        let device = deviceID()
        let obsolete = await journal.entries().filter { write in
            guard let feedback = write.todayFeedback else { return false }
            guard write.todayDeviceID == device,
                  let brief = snapshot.briefs.first(where: { $0.id == feedback.briefID }) else { return true }
            return feedback.bodyVersion != nil && feedback.bodyVersion != brief.bodyVersion
        }
        guard current == generation, deviceID() == device, isCurrent() else { return }
        for write in obsolete where write.todayState == .reserved {
            _ = try? await journal.transitionToday(key: write.key, event: .undo)
        }
        await loadPendingToday()
    }

    func retryToday() async {
        guard !replayingToday, isPaired(), let device = deviceID() else { return }
        replayingToday = true
        let current = generation
        defer { replayingToday = false }
        for pending in await journal.entries() where pending.todayFeedback != nil && pending.todayDeviceID == device {
            guard current == generation, deviceID() == device, isPaired(), !Task.isCancelled else { break }
            var didDispatch = false
            do {
                guard let write = try await journal.markTodayFeedbackDispatched(key: pending.key, at: now()),
                      let feedback = write.todayFeedback else { continue }
                didDispatch = true
                await loadPendingToday()
                _ = try await HubRequestScope.$hubID.withValue(write.hubID) {
                    try await HubRequestScope.$deviceID.withValue(write.todayDeviceID) {
                        try await hub.sendFeedback(briefID: feedback.briefID, itemID: feedback.itemID,
                                                   action: feedback.action, reason: feedback.reason, idempotencyKey: write.key)
                    }
                }
                guard current == generation, deviceID() == device else { return }
                record(feedback.cardKey, Choice(action: feedback.action, delivery: .sent, at: write.createdAt,
                                                briefID: feedback.briefID, itemID: feedback.itemID))
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
        await loadPendingToday()
    }

    /// Sends Like or Not right for one card and shows it at once. Not right on a memory removes the memory on the
    /// Mac, so it needs a reason and the owner's confirmation first, and shows only once the Mac confirms it.
    /// Returns true once the Mac recorded the feedback.
    @discardableResult
    func send(_ action: FeedFeedbackAction, for card: FeedCardModel, reason: String? = nil) async -> Bool {
        guard let approval = await authorizeFeedback(action, for: card, reason: reason) else { return false }
        return await commitFeedback(approval)
    }

    func authorizeFeedback(_ action: FeedFeedbackAction, for card: FeedCardModel,
                          reason: String? = nil) async -> DecisionApproval? {
        guard isPaired(), choices[card.key]?.delivery != .sent, !busy.contains(card.key) else { return nil }
        let removesMemory = action == .notRight && card.memoryID != nil
        let bounded = reason.map(CorrectionReason.bounded)
        let text = bounded?.isEmpty == false ? bounded : nil
        if removesMemory && text == nil {
            notice = FeedCopy.reasonNeeded
            return nil
        }
        busy.insert(card.key)
        let current = generation
        defer { if current == generation { busy.remove(card.key) } }
        if removesMemory {
            do {
                try await guardian.authorize(reason: FeedCopy.confirmMemory)
            } catch {
                guard current == generation else { return nil }
                notice = (error as? LocalizedError)?.errorDescription ?? FeedCopy.generic
                return nil
            }
            guard current == generation, isPaired() else { return nil }
        }
        return DecisionApproval(action: action, card: card, reason: text, generation: current)
    }

    @discardableResult
    func commitFeedback(_ approval: DecisionApproval) async -> Bool {
        let card = approval.card
        let action = approval.action
        guard approval.generation == generation, isPaired(),
              choices[card.key]?.delivery != .sent, !busy.contains(card.key) else { return false }
        let removesMemory = action == .notRight && card.memoryID != nil
        busy.insert(card.key)
        let current = generation
        defer { if current == generation { busy.remove(card.key) } }
        if !removesMemory {
            record(card.key, Choice(action: action, delivery: .pending, at: now(), briefID: card.briefID,
                                    itemID: card.feedbackItemID))
        }
        let payload = await deliver(action, briefID: card.briefID, itemID: card.feedbackItemID,
                                    reason: approval.reason, cardKey: card.key, keepsPending: !removesMemory,
                                    announces: true, generation: current)
        return payload != nil
    }

    /// Sends again the feedback the Mac has not answered, under the keys the journal kept. A choice whose key the
    /// journal no longer holds (it keeps keys for a day) is forgotten, so it can be given again, and the notice says
    /// so: sending it under a new key could apply it twice.
    func retryPending() async {
        guard isPaired() else { return }
        let current = generation
        var expired = 0
        defer {
            if current == generation, expired > 0 {
                notice = FeedCopy.expired(expired)
            }
        }
        for (key, choice) in choices where choice.delivery == .pending && !busy.contains(key) {
            let fingerprint = Self.fingerprint(briefID: choice.briefID, itemID: choice.itemID, action: choice.action,
                                               reason: nil)
            guard await journal.existing(operation: .feedback, target: Self.target(choice.briefID, choice.itemID),
                                         fingerprint: fingerprint) != nil else {
                guard current == generation else { return }
                forget(key)
                expired += 1
                continue
            }
            guard current == generation else { return }
            busy.insert(key)
            _ = await deliver(choice.action, briefID: choice.briefID, itemID: choice.itemID, reason: nil,
                              cardKey: key, keepsPending: true, announces: false, generation: current)
            if current == generation { busy.remove(key) }
        }
    }

    func dismissNotice() {
        notice = nil
    }

    func clearAfterUnpair() {
        generation = UUID()
        busy.removeAll()
        interest = nil
        choices = [:]
        pendingToday = []
        todayNotice = nil
        notice = nil
        try? FileManager.default.removeItem(at: fileURL)
    }

    // MARK: Helpers

    private func deliver(_ action: FeedFeedbackAction, briefID: String, itemID: String, reason: String?,
                         cardKey: String, keepsPending: Bool, announces: Bool,
                         generation current: UUID) async -> BriefFeedbackPayload? {
        let fingerprint = Self.fingerprint(briefID: briefID, itemID: itemID, action: action, reason: reason)
        let outcome = await writer.send(.feedback, target: Self.target(briefID, itemID), choice: action.rawValue,
                                        fingerprint: fingerprint) { key in
            guard current == self.generation else { throw CancellationError() }
            return try await self.hub.sendFeedback(briefID: briefID, itemID: itemID, action: action, reason: reason,
                                                   idempotencyKey: key)
        }
        guard current == generation else { return nil }
        switch outcome {
        case .sent(let payload):
            record(cardKey, Choice(action: action, delivery: .sent, at: choices[cardKey]?.at ?? now(),
                                   briefID: briefID, itemID: itemID))
            if payload.effect == "memory_removed" {
                notice = FeedCopy.memoryRemoved
            }
            return payload
        case .failed(_, .keep):
            // No answer yet. The journal keeps the key, so the next try is applied once on the Mac.
            if keepsPending {
                if announces { notice = FeedCopy.waiting }
            } else {
                notice = FeedCopy.noAnswer
            }
            return nil
        case .failed(let error, .drop), .failed(let error, .changed):
            forget(cardKey)
            notice = Self.message(for: error)
            return nil
        case .notSaved:
            forget(cardKey)
            notice = FeedCopy.saveFailed
            return nil
        case .cancelled:
            return nil
        }
    }

    private static func target(_ briefID: String, _ itemID: String) -> String {
        "\(briefID)/\(itemID)"
    }

    private func record(_ key: String, _ choice: Choice) {
        choices[key] = choice
        if choices.count > Self.choiceLimit {
            let oldest = choices.sorted { $0.value.at < $1.value.at }.prefix(choices.count - Self.choiceLimit)
            for (key, _) in oldest { choices[key] = nil }
        }
        save()
    }

    private func forget(_ key: String) {
        choices[key] = nil
        save()
    }

    private func save() {
        let saved = Saved(interest: interest, choices: choices)
        guard let data = try? Self.encoder.encode(saved) else { return }
        try? FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(),
                                                 withIntermediateDirectories: true)
        try? data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    nonisolated static func fingerprint(briefID: String, itemID: String, action: FeedFeedbackAction, reason: String?) -> String {
        WriteFingerprint.make([briefID, itemID, action.rawValue, reason ?? ""])
    }

    private static func message(for error: Error) -> String {
        if case HubRequestError.rejected(let status, _) = error, status == 404 {
            return FeedCopy.gone
        }
        return (error as? LocalizedError)?.errorDescription ?? FeedCopy.generic
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
}

enum FeedCopy {
    static let generic = "Isoform could not finish that. Try again."
    static let saveFailed = "Isoform could not save this on this phone, so nothing was sent to your computer. Try again."
    static let waiting = "Saved on this phone. It reaches your computer when your computer answers."
    static let gone = "This card is no longer on your computer. Nothing changed."
    static let reasonNeeded = "Write a short reason before you send this to your computer."
    static let confirmMemory = "Confirm that you want Isoform to stop using this memory on your computer."
    static let memoryRemoved = "Recorded on your computer. Isoform will stop using that memory."
    static let noAnswer = "Your computer has not answered yet. Nothing changed. Try again when your computer is online."

    static func expired(_ count: Int) -> String {
        count == 1
            ? "Your computer did not get one of your answers in time. Give it again if you still mean it."
            : "Your computer did not get \(count) of your answers in time. Give them again if you still mean them."
    }
}
