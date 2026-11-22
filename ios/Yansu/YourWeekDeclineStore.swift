import Combine
import Foundation

protocol YourWeekDeclineAPI: Sendable {
    func weekDeclined(briefID: String, version: String) async throws -> Bool
    func declineWeek(briefID: String, version: String, idempotencyKey: String) async throws -> Bool
}

extension HubChannel: YourWeekDeclineAPI {
    func weekDeclined(briefID: String, version: String) async throws -> Bool {
        guard try await abilities().briefWeekDecline == true else { throw HubRequestError.rejected(status: 404, message: "") }
        struct Result: Decodable { let declined: Bool }
        return try await perform(.briefWeekDecline(id: briefID, version: version)).payload(Result.self).declined
    }

    func declineWeek(briefID: String, version: String, idempotencyKey: String) async throws -> Bool {
        guard Identifiers.isSafeBriefID(briefID), try await abilities().briefWeekDecline == true else {
            throw HubRequestError.rejected(status: 404, message: "")
        }
        struct Request: Encodable { let idempotencyKey: String; let version: String }
        struct Result: Decodable { let declined: Bool }
        return try await post("/briefs/\(briefID)/week-decline", Request(idempotencyKey: idempotencyKey, version: version), as: Result.self).declined
    }
}

@MainActor
final class YourWeekDeclineStore: ObservableObject {
    @Published private(set) var declined: Set<String> = []
    @Published private(set) var pending: Set<String> = []
    @Published private(set) var failed: Set<String> = []
    private let hub: YourWeekDeclineAPI
    private let journal: PendingWriteJournal
    private let writer: JournaledWriter
    private let defaults: UserDefaults
    private let hubID: @MainActor () -> String?
    private let online: @MainActor () -> Bool
    private var sending = Set<String>()
    private var loadedHubID: String?

    init(hub: YourWeekDeclineAPI, journal: PendingWriteJournal, defaults: UserDefaults = .standard,
         hubID: @escaping @MainActor () -> String?, online: @escaping @MainActor () -> Bool) {
        self.hub = hub
        self.journal = journal
        self.writer = JournaledWriter(journal: journal, makeKey: { UUID().uuidString }, now: Date.init)
        self.defaults = defaults
        self.hubID = hubID
        self.online = online
    }

    private func weekKey(_ week: MobileBriefWeek) -> String { "\(week.start)|\(week.end)" }
    private func cacheKey(_ week: String) -> String { "your-week-decline|\(hubID() ?? "")|\(week)" }
    private func synchronizeIdentity() -> String? {
        let current = hubID()
        if loadedHubID != current {
            declined = []; pending = []; failed = []; sending = []
            loadedHubID = current
        }
        return current
    }

    func load(brief: MobileBriefV1, week: MobileBriefWeek) async {
        guard let identity = synchronizeIdentity(), let version = brief.conversationKey else { return }
        let weekID = weekKey(week)
        if defaults.bool(forKey: cacheKey(weekID)) { declined.insert(weekID) }
        let entries = await journal.entries()
        guard hubID() == identity else { _ = synchronizeIdentity(); return }
        if entries.contains(where: { $0.operation == .weekDecline && $0.choice.hasSuffix("|\(weekID)") }) {
            pending.insert(weekID)
        }
        guard online() else { return }
        do {
            let saved = try await hub.weekDeclined(briefID: brief.identifier, version: version)
            guard hubID() == identity else { _ = synchronizeIdentity(); return }
            if saved { declined.insert(weekID); defaults.set(true, forKey: cacheKey(weekID)) }
            else if !pending.contains(weekID) { declined.remove(weekID); defaults.removeObject(forKey: cacheKey(weekID)) }
        } catch { /* Keep the last confirmed state when a read fails. */ }
        await retryPending()
    }

    func isDeclined(_ week: MobileBriefWeek) -> Bool { loadedHubID == hubID() && declined.contains(weekKey(week)) }
    func isPending(_ week: MobileBriefWeek) -> Bool { loadedHubID == hubID() && pending.contains(weekKey(week)) }
    func hasFailed(_ week: MobileBriefWeek) -> Bool { loadedHubID == hubID() && failed.contains(weekKey(week)) }

    func decline(brief: MobileBriefV1, week: MobileBriefWeek) async {
        guard let identity = synchronizeIdentity(), let version = brief.conversationKey else { return }
        let weekID = weekKey(week)
        let choice = "\(version)|\(weekID)"
        let fingerprint = WriteFingerprint.make([brief.identifier, version, weekID])
        do {
            _ = try await journal.reserve(operation: .weekDecline, target: brief.identifier, choice: choice,
                                          fingerprint: fingerprint, makeKey: { UUID().uuidString }, createdAt: Date())
            guard hubID() == identity else { _ = synchronizeIdentity(); return }
            pending.insert(weekID)
            failed.remove(weekID)
            if online() { await retryPending() }
        } catch { failed.insert(weekID) }
    }

    func retryPending() async {
        guard online(), let identity = synchronizeIdentity() else { return }
        let entries = await journal.entries().filter { $0.operation == .weekDecline }
        guard hubID() == identity else { _ = synchronizeIdentity(); return }
        for entry in entries {
            let parts = entry.choice.split(separator: "|", omittingEmptySubsequences: false).map(String.init)
            guard parts.count == 3 else { continue }
            let weekID = "\(parts[1])|\(parts[2])"
            guard sending.insert(entry.key).inserted else { continue }
            let result = await writer.send(.weekDecline, target: entry.target, choice: entry.choice, fingerprint: entry.fingerprint) { key in
                guard self.hubID() == identity else { throw CancellationError() }
                return try await self.hub.declineWeek(briefID: entry.target, version: parts[0], idempotencyKey: key)
            }
            guard hubID() == identity else { _ = synchronizeIdentity(); return }
            sending.remove(entry.key)
            switch result {
            case .sent(true):
                pending.remove(weekID); failed.remove(weekID); declined.insert(weekID)
                defaults.set(true, forKey: cacheKey(weekID))
            case .failed(_, .drop), .failed(_, .changed), .sent(false), .notSaved:
                pending.remove(weekID); failed.insert(weekID)
            case .failed(_, .keep), .cancelled: pending.insert(weekID)
            }
        }
    }
}
