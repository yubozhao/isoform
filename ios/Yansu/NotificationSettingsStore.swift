import Foundation

enum NotificationSettingsLoadState: Equatable {
    case idle
    case loading
    case loaded
    case failed(String)
}

/// Computer preferences and a durable, ordered queue scoped to this pairing. A retry uses the same
/// idempotency key, including after the phone closes before receiving the computer's answer.
@MainActor
final class NotificationSettingsStore: ObservableObject {
    @Published private(set) var settings: NotificationSettings?
    @Published private(set) var loadState: NotificationSettingsLoadState = .idle
    @Published private(set) var busyKinds: Set<String> = []
    @Published private(set) var failure: String?
    @Published private(set) var pendingCount = 0
    @Published private(set) var pendingToday: [PendingNotificationChange] = []

    private struct SavedState: Codable {
        let pairingID: String?
        let confirmed: NotificationSettings?
        let pending: [PendingNotificationChange]
    }

    private let hub: NotificationSettingsAPI
    private let isPaired: @MainActor () -> Bool
    private let pairingID: @MainActor () -> String?
    private let hubID: @MainActor () -> String?
    private let makeKey: @Sendable () -> String
    private let fileURL: URL?
    private var confirmed: NotificationSettings?
    private var pending: [PendingNotificationChange] = []
    private var boundPairingID: String?
    private var refreshing = false
    private var replaying = false
    private var generation = UUID()
    private var mutationRevision = UUID()
    private var storageBlocked = false
    private var awaitingInitialPairing = false

    init(hub: NotificationSettingsAPI,
         isPaired: @escaping @MainActor () -> Bool,
         makeKey: @escaping @Sendable () -> String = { UUID().uuidString.lowercased() },
         fileURL: URL? = nil,
         pairingID: @escaping @MainActor () -> String? = { nil },
         hubID: @escaping @MainActor () -> String? = { nil }) {
        self.hub = hub
        self.isPaired = isPaired
        self.makeKey = makeKey
        self.fileURL = fileURL
        self.pairingID = pairingID
        self.hubID = hubID
        boundPairingID = pairingID()
        awaitingInitialPairing = !isPaired()
        if isPaired() { _ = restore() }
    }

    var hasPendingChanges: Bool { pendingCount > 0 }

    /// Preserve phone-side choices while a freshly read Mac preference is awaiting replay.
    func applyingPending(to current: NotificationSettings) -> NotificationSettings {
        pending.reduce(current) { $0.applying($1) }
    }

    var localNoticeRevision: UUID { mutationRevision }

    func applyingPending(to current: NotificationSettings, revision: UUID) -> NotificationSettings? {
        guard revision == mutationRevision, isCurrent(generation) else { return nil }
        return applyingPending(to: current)
    }

    func isBusy(_ kind: String) -> Bool { busyKinds.contains(kind) }

    func refresh() async {
        guard preparePairing() else { return }
        guard !refreshing else { return }
        let current = generation
        refreshing = true
        defer { if generation == current { refreshing = false } }
        loadState = .loading
        await replayPending()
        let revision = mutationRevision
        do {
            let loaded = try await HubRequestScope.$hubID.withValue(hubID()) {
                try await hub.notificationSettings()
            }
            guard isCurrent(current) else { return }
            if revision == mutationRevision {
                confirmed = loaded
                publish()
                // The computer answered: a copy this phone can't save is not a computer it can't reach, so the page
                // keeps its choices (P132) and a later change says what failed.
                try? persist()
            }
            loadState = .loaded
        } catch {
            guard isCurrent(current) else { return }
            loadState = .failed(Self.message(for: error))
        }
    }

    #if DEBUG && targetEnvironment(simulator)
    func seedForUITest() {
        func kind(_ name: String) -> NotificationKind {
            NotificationKind(kind: name, enabled: true, defaultOn: true, paused: false, mutedUntil: nil,
                             sent: 0, opened: 0, acted: 0, dismissed: 0, muted: 0)
        }
        settings = NotificationSettings(available: true,
                                        kinds: [kind("work_blocked"), kind("brief_ready"), kind("meeting_prep"), kind("work_finished")],
                                        quietStart: "22:00", quietEnd: "08:00", dailyInterruptBudget: 6,
                                        digestTimes: [], statsDays: 30)
        loadState = .loaded
    }
    #endif

    func setEnabled(_ kind: String, _ enabled: Bool, deferForToday: Bool = false) async {
        guard Identifiers.isSafePathID(kind) else {
            failure = String(localized: "notifications.error.invalidKind")
            return
        }
        var change = PendingNotificationChange(idempotencyKey: makeKey(), kind: kind, enabled: enabled)
        if deferForToday { change.fromToday = true; change.dispatched = false }
        await enqueue(change, deferSending: deferForToday)
    }

    @discardableResult
    func setQuietHours(start: String, end: String) async -> Bool {
        guard Self.validClock(start), Self.validClock(end) else {
            failure = String(localized: "notifications.error.invalidTime")
            return false
        }
        return await enqueue(PendingNotificationChange(idempotencyKey: makeKey(), quietStart: start, quietEnd: end))
    }

    /// Which failure a refused change shows (P132). Only a phone that is really unpaired is told to pair; a paired
    /// phone with an unreadable queue gets the neutral change message, and a storage block keeps its own message.
    static func pairingFailureKey(isPaired: Bool, storageBlocked: Bool) -> String? {
        if !isPaired { return "notifications.error.notPaired" }
        if storageBlocked { return nil }
        return "notifications.error.change"
    }

    @discardableResult
    private func enqueue(_ change: PendingNotificationChange, deferSending: Bool = false) async -> Bool {
        guard preparePairing() else {
            if let key = Self.pairingFailureKey(isPaired: isPaired(), storageBlocked: storageBlocked) {
                failure = String(localized: String.LocalizationValue(key))
            }
            return false
        }
        guard pending.count < 100 else {
            failure = String(localized: "notifications.error.queueFull")
            return false
        }
        failure = nil
        pending.append(change)
        do {
            try persist()
        } catch {
            pending.removeLast()
            failure = String(localized: "notifications.error.save")
            return false
        }
        mutationRevision = UUID()
        publish()
        let current = generation
        if !deferSending { await replayPending() }
        return isCurrent(current) && (pending.contains { $0.idempotencyKey == change.idempotencyKey } || failure == nil)
    }

    func undoToday(_ key: String) {
        guard let index = pending.firstIndex(where: { $0.idempotencyKey == key && $0.fromToday == true && $0.dispatched == false }) else { return }
        let previous = pending
        pending.remove(at: index)
        do { try persist(); mutationRevision = UUID(); publish() }
        catch { pending = previous; failure = String(localized: "notifications.error.save") }
    }

    func replayPending(includeDeferred: Bool = false) async {
        guard preparePairing(), !replaying else { return }
        let current = generation
        replaying = true
        defer {
            if generation == current {
                replaying = false
                busyKinds.removeAll()
            }
        }
        while var change = pending.first {
            if change.fromToday == true && change.dispatched == false && !includeDeferred { return }
            if let kind = change.kind { busyKinds = [kind] }
            if change.fromToday == true && change.dispatched == false {
                change.dispatched = true
                pending[0] = change
                do { try persist(); publish() }
                catch { pending[0].dispatched = false; failure = String(localized: "notifications.error.save"); return }
            }
            do {
                let loaded = try await HubRequestScope.$hubID.withValue(hubID()) {
                    if let kind = change.kind, let enabled = change.enabled {
                        return try await hub.setNotificationKind(kind, enabled: enabled, idempotencyKey: change.idempotencyKey)
                    } else if let start = change.quietStart, let end = change.quietEnd {
                        return try await hub.setNotificationPreferences(SetNotificationPreferencesRequest(
                            idempotencyKey: change.idempotencyKey, quietStart: start, quietEnd: end))
                    } else {
                        throw HubRequestError.invalidIdentifier
                    }
                }
                guard isCurrent(current) else { return }
                let previousConfirmed = confirmed
                confirmed = loaded
                pending.removeFirst()
                mutationRevision = UUID()
                publish()
                do {
                    try persist()
                } catch {
                    confirmed = previousConfirmed
                    pending.insert(change, at: 0)
                    publish()
                    failure = String(localized: "notifications.error.save")
                    return
                }
                failure = nil
            } catch {
                guard isCurrent(current) else { return }
                if Self.isRetryable(error) {
                    // Keeping this key is essential when only the response was lost.
                    publish()
                    return
                }
                if pending.first?.idempotencyKey == change.idempotencyKey { pending.removeFirst() }
                mutationRevision = UUID()
                publish()
                do { try persist() } catch { failure = String(localized: "notifications.error.save"); return }
                failure = Self.message(for: error)
                return
            }
        }
    }

    @discardableResult
    func clearAfterUnpair() -> Bool {
        generation = UUID()
        mutationRevision = UUID()
        refreshing = false
        replaying = false
        busyKinds.removeAll()
        confirmed = nil
        pending.removeAll()
        boundPairingID = pairingID()
        awaitingInitialPairing = false
        storageBlocked = false
        publish()
        loadState = .idle
        failure = nil
        guard let fileURL, FileManager.default.fileExists(atPath: fileURL.path) else { return true }
        do {
            try FileManager.default.removeItem(at: fileURL)
            return true
        } catch {
            storageBlocked = true
            failure = String(localized: "notifications.error.save")
            return false
        }
    }

    private func preparePairing() -> Bool {
        guard isPaired() else {
            settings = nil
            loadState = .idle
            return false
        }
        if awaitingInitialPairing {
            // A locked or unreadable pairing at launch is not an unpair. Once it
            // can be read, restore only the queue belonging to that exact pairing.
            boundPairingID = pairingID()
            awaitingInitialPairing = false
            if !restore() { return false }
        } else if boundPairingID != pairingID() {
            clearAfterUnpair()
        }
        if storageBlocked && !restore() { return false }
        if settings == nil, confirmed != nil { publish() }
        return true
    }

    private func restore() -> Bool {
        guard let fileURL, FileManager.default.fileExists(atPath: fileURL.path) else {
            storageBlocked = false
            return true
        }
        do {
            let saved = try JSONDecoder().decode(SavedState.self, from: Data(contentsOf: fileURL))
            guard saved.pending.count <= 100 else { throw HubRequestError.malformedResponse }
            if saved.pairingID == boundPairingID {
                confirmed = saved.confirmed
                pending = saved.pending
                publish()
            }
            storageBlocked = false
            failure = nil
            return true
        } catch {
            storageBlocked = true
            failure = String(localized: "notifications.error.restore")
            loadState = .failed(String(localized: "notifications.error.restore"))
            return false
        }
    }

    private func isCurrent(_ current: UUID) -> Bool {
        generation == current && boundPairingID == pairingID() && isPaired()
    }

    private func publish() {
        settings = pending.reduce(confirmed) { $0?.applying($1) }
        pendingCount = pending.count
        pendingToday = pending.filter { $0.fromToday == true }
    }

    private func persist() throws {
        guard let fileURL else { return }
        try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        let saved = SavedState(pairingID: boundPairingID, confirmed: confirmed, pending: pending)
        try JSONEncoder().encode(saved).write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    static func validClock(_ raw: String) -> Bool {
        let parts = raw.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 2, parts[0].count == 2, parts[1].count == 2,
              raw.utf8.allSatisfy({ (48...57).contains($0) || $0 == 58 }),
              let hour = Int(parts[0]), let minute = Int(parts[1]) else { return false }
        return (0..<24).contains(hour) && (0..<60).contains(minute)
    }

    private static func isRetryable(_ error: Error) -> Bool {
        JournaledWriter.disposition(for: error) == .keep
    }

    private static func message(for error: Error) -> String {
        if let relayError = error as? RelayClientError,
           [.unauthorized, .forbidden, .gone].contains(relayError) {
            return String(localized: "notifications.error.forbidden")
        }
        switch error as? HubRequestError {
        case .forbidden: return String(localized: "notifications.error.forbidden")
        // A paired phone whose computer does not answer is told that, never to pair again (P132).
        case .notPaired: return String(localized: "notifications.couldNotReach")
        case .noAnswer: return String(localized: "notifications.error.offline")
        default: return String(localized: "notifications.error.change")
        }
    }
}
