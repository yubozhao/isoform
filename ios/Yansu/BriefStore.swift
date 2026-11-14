import Combine
import Foundation
import os

enum BriefOpenError: LocalizedError, Equatable {
    case mismatchedEnvelope
    case unreadable

    var errorDescription: String? {
        switch self {
        case .mismatchedEnvelope:
            return String(localized: "The relay sent a brief that does not belong to this phone.")
        case .unreadable:
            return String(localized: "Isoform could not read the brief on this phone. Pair this phone again in Settings.")
        }
    }
}

struct HistoryEntry: Identifiable, Equatable, Hashable {
    let id: String
    let slot: String?
    let localDate: String?
    let asOf: Date?
    let narrativeStatus: String?
    let isCached: Bool
    var displaySlot: String? = nil
    var timezone: String? = nil
    var kind: String? = nil
    var dueAt: String? = nil
}

enum RefreshPhase: Equatable {
    case idle
    case requesting
    case waiting
}

/// What the newest Today snapshot names for one brief: the version it should hold, and whether the Mac predates
/// versions, so its body is read as one object.
struct BriefTarget: Equatable {
    let version: String?
    let legacy: Bool
}

// Latest brief, history and refresh for the Feed. Reads go to the relay (encrypted, stored briefs) and to the hub
// (history metadata, brief bodies, on-demand refresh). Every brief is verified here and saved to the library
// (BriefCache) before the UI shows it. The library is the only place a brief is read from to be shown.
@MainActor
final class BriefStore: ObservableObject {
    /// The current brief under the one rule (design section 1): the newest brief whose full text is in the library.
    @Published private(set) var current: MobileBriefV1?
    /// The last outcome of each brief fetch, by brief id. Diagnostic only; it holds no brief text.
    @Published private(set) var fetchOutcomes: [String: String] = [:]
    private static let fetchLog = Logger(subsystem: "com.isoform.companion", category: "brief-fetch")
    @Published private(set) var hubOnline: Bool?
    @Published private(set) var lastRefreshFailed = false
    @Published private(set) var isRefreshing = false
    @Published private(set) var refreshPhase: RefreshPhase = .idle
    @Published private(set) var cooldownUntil: Date?
    @Published private(set) var history: [HistoryEntry] = []
    /// Every brief in the library, newest first. The Feed is assembled from them.
    @Published private(set) var cachedBriefs: [MobileBriefV1] = []
    @Published private(set) var historyLoading = false
    @Published private(set) var historyNotice: String?
    @Published private(set) var repairNeeded = false
    @Published var notice: String?

    // Called when the first brief reaches this phone. Notification permission is asked after it.
    var onFirstBrief: (() -> Void)?
    var onDeleteCachedBriefs: (() -> Bool)?
    var onFreshRequest: ((BriefRefreshResult) -> Void)?
    // A received projection can withdraw audio without changing its generation.
    // Notify the shared player before publishing it, including history and offline launch.
    var onBriefReceived: ((MobileBriefV1, String, Bool, BriefAudioReceipt?) -> Void)?

    private let pairing: PairingStore
    private let vault: PairingVault
    private let cache: BriefCache
    private let hub: HubChannel

    func saveMorningWeekIntent(briefID: String, answer: String) async throws {
        guard Identifiers.isSafePathID(briefID) else { throw HubRequestError.invalidIdentifier }
        struct Request: Encodable { let idempotencyKey: String; let answer: String }
        struct Result: Decodable { let saved: Bool }
        let result = try await hub.post("/briefs/\(briefID)/week-intent",
                                        Request(idempotencyKey: UUID().uuidString, answer: answer), as: Result.self)
        guard result.saved else { throw HubRequestError.malformedResponse }
    }
    private let bodies: BriefBodyAPI
    private let relay: RelayClient
    private var historySummaries: [BriefSummary] = []
    private var generation = UUID()
    private var withdrawnIDs: Set<String> = []
    /// The briefs the newest Today snapshot names, the current brief first, with the version each should hold.
    private var targets: [String: BriefTarget] = [:]
    private var targetOrder: [String] = []
    /// One body fetch per brief id. A snapshot refresh leaves a fetch that is still named running.
    private var fetches: [String: (token: UUID, task: Task<Void, Never>)] = [:]
    private var todayGeneratedAt: Date?

    init(pairing: PairingStore, vault: PairingVault, cache: BriefCache, hub: HubChannel,
         relay: RelayClient = .shared, bodies: BriefBodyAPI? = nil) {
        self.pairing = pairing
        self.vault = vault
        self.cache = cache
        self.hub = hub
        self.relay = relay
        let source: BriefBodyAPI = bodies ?? hub
        self.bodies = source
    }

    // Instant and offline. Runs on launch before any network call.
    func loadCached() {
        if let error = vault.loadError {
            notice = error.localizedDescription
            return
        }
        guard pairing.isPaired else {
            clearAll()
            return
        }
        cache.all().forEach { received($0.brief, fromCache: true, receipt: $0.audioReceipt) }
        rebuildHistory()
    }

    // Latest brief from the relay, verified and saved. Used on becoming active, pull to refresh and push taps.
    // One asked for while a read runs (a push tap) is not dropped: one more read follows the one in flight.
    func refresh() async {
        guard pairing.isPaired else { return }
        if isRefreshing {
            refreshAgain = true
            return
        }
        repeat {
            refreshAgain = false
            await refreshOnce()
        } while refreshAgain && !isRefreshing && pairing.isPaired
    }

    private var refreshAgain = false

    private func refreshOnce() async {
        guard pairing.isPaired, !isRefreshing else { return }
        let currentGeneration = generation
        isRefreshing = true
        defer { if generation == currentGeneration { isRefreshing = false } }
        do {
            let session = try vault.session()
            do {
                let presence = try await relay.presence(session.credentials)
                guard generation == currentGeneration else { return }
                hubOnline = presence.hubOnline
            } catch {
                guard generation == currentGeneration else { return }
                hubOnline = RemoteStatus.offline(for: error, remote: vault.isRemote)
            }
            let latest = try await relay.latestBrief(session.credentials)
            guard generation == currentGeneration else { return }
            if let record = latest {
                try accept(record, session: session)
            }
            guard generation == currentGeneration else { return }
            lastRefreshFailed = false
            repairNeeded = false
        } catch let error as RelayClientError {
            guard generation == currentGeneration else { return }
            hubOnline = RemoteStatus.offline(for: error, remote: vault.isRemote)
            handleRelayError(error)
        } catch BriefOpenError.unreadable {
            guard generation == currentGeneration else { return }
            repairNeeded = true
            notice = BriefOpenError.unreadable.localizedDescription
        } catch {
            guard generation == currentGeneration else { return }
            lastRefreshFailed = true
        }
    }

    // The hub queues a request during cooldown and publishes the result when ready.
    func requestFreshBrief() async {
        guard pairing.isPaired, refreshPhase == .idle else { return }
        let currentGeneration = generation
        refreshPhase = .requesting
        defer { if generation == currentGeneration { refreshPhase = .idle } }
        do {
            let result = try await hub.requestRefresh()
            guard generation == currentGeneration else { return }
            switch result.status {
            case "queued", "making", "started", "running":
                onFreshRequest?(result)
            default:
                notice = String(localized: "Your computer could not start a new brief. Try again in a moment.")
            }
        } catch HubRequestError.queueFull(let retryAfter) {
            guard generation == currentGeneration else { return }
            notice = HubRequestError.queueFull(retryAfter: retryAfter).localizedDescription
        } catch HubRequestError.noAnswer {
            guard generation == currentGeneration else { return }
            notice = HubRequestError.noAnswer.localizedDescription
        } catch let error as RelayClientError {
            guard generation == currentGeneration else { return }
            handleRelayError(error)
        } catch {
            guard generation == currentGeneration else { return }
            notice = error.localizedDescription
        }
    }

    // History: the relay's latest seven briefs are verified and saved, then the hub lists older ones.
    // Stored history does not move the receive cursor, because older briefs legitimately arrive later.
    func refreshHistory() async {
        guard pairing.isPaired, !historyLoading else { return }
        let currentGeneration = generation
        historyLoading = true
        defer { if generation == currentGeneration { historyLoading = false } }
        if let session = try? vault.session(),
           let records = try? await relay.briefs(session.credentials, limit: 7) {
            guard generation == currentGeneration else { return }
            for record in records {
                guard let opened = try? openPackage(record, session: session) else { continue }
                _ = try? storeRelayPackage(opened, record: record, session: session)
            }
        }
        guard generation == currentGeneration else { return }
        do {
            let summaries = try await hub.briefList(limit: 14)
            guard generation == currentGeneration else { return }
            historySummaries = summaries
            historyNotice = nil
        } catch {
            guard generation == currentGeneration else { return }
            historyNotice = String(localized: "Past briefs update when your computer is back.")
        }
        rebuildHistory()
    }

    /// A brief's text from this phone's library. Screens read it here and never fetch it.
    func cachedBrief(withID identifier: String) -> MobileBriefV1? {
        guard !withdrawnIDs.contains(identifier) else { return nil }
        return cache.load(identifier: identifier)?.brief
    }

    /// Fetch an opened history entry on demand, including entries outside the Today index.
    func fetchOpenedBrief(_ identifier: String) async -> MobileBriefV1? {
        try? await fetchOpenedBriefForPlay(identifier)
    }

    /// Preserve the failure for Play so an unavailable computer can be retried without
    /// treating a withdrawn or forbidden brief as an offline request.
    func fetchOpenedBriefForPlay(_ identifier: String) async throws -> MobileBriefV1 {
        if let cached = cachedBrief(withID: identifier) { return cached }
        guard pairing.isPaired, Identifiers.isSafePathID(identifier), !withdrawnIDs.contains(identifier),
              let pairedHub = vault.current()?.hubID else { throw HubRequestError.invalidIdentifier }
        let current = generation
        let target = targets[identifier] ?? BriefTarget(version: nil, legacy: true)
        try await fetchOnce(identifier, target: target, generation: current, pairedHub: pairedHub)
        guard let brief = cachedBrief(withID: identifier) else { throw HubRequestError.malformedResponse }
        return brief
    }

    /// Whether the library holds the version the newest Today snapshot names for this brief. A brief the snapshot
    /// does not name, or one made after the snapshot, counts as current.
    func cachedBodyIsCurrent(_ identifier: String) -> Bool {
        guard !withdrawnIDs.contains(identifier), let cached = cache.load(identifier: identifier) else { return false }
        guard let version = targets[identifier]?.version else { return true }
        if cached.version == version { return true }
        return (cached.brief.asOfDate ?? .distantPast) > (todayGeneratedAt ?? .distantFuture)
    }

    /// A newer hub snapshot withdraws briefs absent from it: they leave the library and are never current again.
    /// A relay brief made after the snapshot remains eligible until the next read. Then the briefs the snapshot
    /// names are fetched into the library.
    /// How far back the Mac's Today index names briefs (PROTOCOL.md 20.7a).
    static let macIndexInterval: TimeInterval = 14 * 24 * 60 * 60

    func reconcileToday(_ snapshot: MobileTodaySnapshot) {
        guard let generated = snapshot.generatedDate else { return }
        todayGeneratedAt = generated
        (targets, targetOrder) = Self.namedBriefs(in: snapshot)
        let allowed = Set(targets.keys)
        withdrawnIDs.subtract(allowed)
        // The Mac's index names the readable briefs of its last 14 days, so only a brief inside that window can be
        // withdrawn by its absence. An older brief left the index by age and stays for the library's 30 days.
        let indexStart = generated.addingTimeInterval(-Self.macIndexInterval)
        let stale = cache.all().map(\.brief).filter {
            let made = $0.asOfDate ?? .distantPast
            return !allowed.contains($0.identifier) && made <= generated && made >= indexStart
        }
        for value in stale {
            withdrawnIDs.insert(value.identifier)
            var withdrawn = value
            withdrawn.narrativeStatus = "unavailable"
            withdrawn.audio = nil
            received(withdrawn)
            try? cache.remove(identifier: value.identifier)
        }
        rebuildHistory()
        history.removeAll { withdrawnIDs.contains($0.id) }
        syncLibrary()
    }

    /// The briefs one snapshot names, the current brief first. A brief from a Mac before 1.0 has no version, so
    /// its bodyVersion stands in and it is read as one object.
    static func namedBriefs(in snapshot: MobileTodaySnapshot) -> ([String: BriefTarget], [String]) {
        var named: [String: BriefTarget] = [:]
        var order: [String] = []
        func add(_ id: String, _ target: BriefTarget) {
            if named[id] == nil { order.append(id) }
            named[id] = target
        }
        if let current = snapshot.currentBrief {
            add(current.id, BriefTarget(version: current.version, legacy: false))
        }
        for entry in [snapshot.latestBrief].compactMap({ $0 }) + snapshot.briefs {
            add(entry.id, BriefTarget(version: entry.version ?? entry.bodyVersion, legacy: entry.version == nil))
        }
        return (named, order)
    }

    /// The only code that fetches brief bodies. Each snapshot starts a fetch for every brief it names whose version
    /// the library lacks. A snapshot refresh never cancels a fetch that is still named.
    private func syncLibrary() {
        for identifier in targetOrder where fetches[identifier] == nil && !withdrawnIDs.contains(identifier) {
            guard let target = targets[identifier], needsFetch(identifier, target) else { continue }
            let token = UUID()
            let current = generation
            let pairedHub = vault.current()?.hubID
            let task = Task { [weak self] in
                guard let self else { return }
                await self.fetchUntilPresent(identifier, token: token, generation: current, pairedHub: pairedHub)
            }
            fetches[identifier] = (token, task)
        }
    }

    private func needsFetch(_ identifier: String, _ target: BriefTarget) -> Bool {
        guard let cached = cache.load(identifier: identifier) else { return true }
        guard let version = target.version else { return false }
        return cached.version != version
    }

    /// Fetches one brief into the library, retrying with a backoff of up to a minute. It stops once the brief is
    /// saved, on a pairing change (the generation or the paired Mac changes), when the brief is withdrawn or no longer
    /// named, or when the Mac says it will not serve it (403, an invalid id, 404).
    private func fetchUntilPresent(_ identifier: String, token: UUID, generation current: UUID, pairedHub: String?) async {
        defer {
            if fetches[identifier]?.token == token { fetches[identifier] = nil }
            // A retrying line must not outlive its fetch: whatever ended the loop, the last outcome is no longer a retry.
            if fetchOutcomes[identifier]?.hasPrefix("attempt") == true {
                record(identifier, "ended: fetch stopped while retrying")
            }
        }
        var delay = 5
        var attempt = 0
        while !Task.isCancelled, generation == current, vault.current()?.hubID == pairedHub, !withdrawnIDs.contains(identifier),
              let target = targets[identifier], needsFetch(identifier, target) {
            attempt += 1
            do {
                try await fetchOnce(identifier, target: target, generation: current, pairedHub: pairedHub)
                record(identifier, "saved after \(attempt) attempt(s)")
                return
            } catch {
                if Self.stopsFetching(error) {
                    record(identifier, "stopped after \(attempt) attempt(s): \(Self.outcomeKind(error))")
                    return
                }
                record(identifier, "attempt \(attempt) failed: \(Self.outcomeKind(error)); retrying in \(delay)s")
            }
            do { try await Task.sleep(for: .seconds(delay)) } catch { return }
            delay = min(delay * 2, 60)
        }
        if Task.isCancelled || generation != current || vault.current()?.hubID != pairedHub {
            record(identifier, "ended: pairing changed or fetch cancelled")
        } else if withdrawnIDs.contains(identifier) {
            record(identifier, "ended: brief withdrawn")
        }
    }

    /// The last outcome of each brief fetch, for diagnosis (walk 1.0.0, P125). It names the brief and the error's kind,
    /// never brief text or an error message, and goes to the device log.
    private func record(_ identifier: String, _ outcome: String) {
        fetchOutcomes[identifier] = outcome
        BriefStore.fetchLog.info("brief \(identifier, privacy: .public): \(outcome, privacy: .public)")
    }

    /// Debug for the #631 seed mismatch: the outcome dictionary as this store holds it, at a named point.
    func logFetchOutcomes(at point: String) {
        BriefStore.fetchLog.info("outcomes at \(point, privacy: .public): \(String(describing: self.fetchOutcomes), privacy: .public) store=\(ObjectIdentifier(self).hashValue)")
    }

    /// What Today says about today's brief while it is missing (#631): retrying while the phone is actually trying,
    /// stopped once it has stopped, and none when there is nothing to say.
    enum TodayFetchState: Equatable { case none, retrying, stopped }

    func todayFetchState(localDate: String) -> TodayFetchState {
        #if DEBUG && targetEnvironment(simulator)
        // UI-test seam: frames of the retrying and stopped states, without a network read (see the PR).
        if ProcessInfo.processInfo.environment["YANSU_UITEST_FETCH_RETRY"] == "1" { return .retrying }
        if ProcessInfo.processInfo.environment["YANSU_UITEST_FETCH_STOPPED"] == "1" { return .stopped }
        #endif
        return Self.todayFetchState(fetchOutcomes, localDate: localDate)
    }

    static func todayFetchState(_ outcomes: [String: String], localDate: String) -> TodayFetchState {
        let today = outcomes.filter { $0.key.hasPrefix(localDate) }.values
        if today.contains(where: { $0.hasPrefix("attempt") }) { return .retrying }
        if today.contains(where: { $0.hasPrefix("stopped") || $0.hasPrefix("ended") }) { return .stopped }
        return .none
    }

    /// UI tests only: shows the retrying state for a brief without a network read.
    func recordFetchOutcomeForUITest(_ identifier: String, _ outcome: String) {
        record(identifier, outcome)
    }

    /// The error's case name only (for example "rejected" or "noAnswer"), so no status text or message is logged.
    private static func outcomeKind(_ error: Error) -> String {
        if let hub = error as? HubRequestError { return "hub \("\(hub)".components(separatedBy: "(").first ?? "error")" }
        if let relay = error as? RelayClientError { return "relay \("\(relay)".components(separatedBy: "(").first ?? "error")" }
        return "error \(String(describing: type(of: error)))"
    }

    /// One attempt: read the brief, verify it, save it to the library, then acknowledge a package so the Mac stops
    /// resending it. A Mac before 1.0 has no package, so its brief is saved under the bodyVersion it was named by.
    private func fetchOnce(_ identifier: String, target: BriefTarget, generation current: UUID, pairedHub: String?) async throws {
        if target.legacy {
            let brief = try await bodies.brief(id: identifier)
            try Task.checkCancellation()
            guard generation == current, vault.current()?.hubID == pairedHub, !withdrawnIDs.contains(identifier),
                  brief.identifier == identifier else {
                throw CancellationError()
            }
            try cache.save(brief, version: target.version)
            received(brief)
            rebuildHistory()
            return
        }
        let fetched = try await bodies.briefPackage(id: identifier)
        try Task.checkCancellation()
        guard generation == current, vault.current()?.hubID == pairedHub, !withdrawnIDs.contains(identifier) else {
            throw CancellationError()
        }
        let brief = try cache.save(package: fetched.package, version: fetched.version, identifier: identifier)
        received(brief)
        rebuildHistory()
        acknowledge(identifier, version: fetched.version)
    }

    private static func stopsFetching(_ error: Error) -> Bool {
        if error is CancellationError { return true }
        guard let hubError = error as? HubRequestError else { return false }
        switch hubError {
        case .forbidden, .invalidIdentifier, .notPaired:
            return true
        case .rejected(let status, _):
            return status == 404
        default:
            return false
        }
    }

    /// Best effort. A missed acknowledgement only means the Mac sends the same package again.
    private func acknowledge(_ identifier: String, version: String) {
        let bodies = self.bodies
        Task { try? await bodies.briefReceived(id: identifier, version: version) }
    }

    private func cancelFetches() {
        for value in fetches.values { value.task.cancel() }
        fetches.removeAll()
    }

    // Old cached briefs lack receipt scope, and history does not advance the latest cursor.
    // Only a verified envelope can associate that old history receipt with this pairing.
    func receiptForAudioAuthorization(for brief: MobileBriefV1) async -> BriefAudioReceipt? {
        guard let cached = cache.load(identifier: brief.identifier),
              BriefAudioResource.versionKey(for: cached.brief) == BriefAudioResource.versionKey(for: brief),
              let session = try? vault.session() else { return nil }
        if let saved = cached.audioReceipt {
            guard saved.deviceID == session.deviceID, saved.pairingID == vault.current()?.secretID else { return nil }
            return saved
        }
        guard cached.brief.audioIsWithdrawn, cached.relayVersion != nil else { return nil }
        let currentGeneration = generation
        guard let records = try? await relay.briefs(session.credentials, limit: 7),
              generation == currentGeneration else { return nil }
        for record in records.sorted(by: { $0.version > $1.version }) {
            guard let opened = try? openPackage(record, session: session),
                  BriefAudioResource.versionKey(for: opened.brief) == BriefAudioResource.versionKey(for: brief) else { continue }
            let verifiedReceipt = receipt(for: record, session: session)
            _ = try? storeRelayPackage(opened, record: record, session: session)
            return verifiedReceipt
        }
        return nil
    }

    func deleteCachedBriefs() {
        guard onDeleteCachedBriefs?() ?? true else {
            notice = String(localized: "Isoform could not delete the saved brief. Unlock this phone and try again.")
            return
        }
        cancelFetches()
        do {
            try cache.removeAll()
        } catch {
            notice = String(localized: "Isoform could not delete the saved brief. Unlock this phone and try again.")
            return
        }
        current = nil
        historySummaries = []
        history = []
        cachedBriefs = []
        notice = nil
    }

    @discardableResult
    func clearAfterUnpair() -> Bool {
        clearAll()
    }

    func dismissNotice() {
        notice = nil
    }

    // MARK: Accepting briefs

    private func accept(_ record: RelayBriefRecord, session: SessionContext) throws {
        let opened = try openPackage(record, session: session)
        do {
            _ = try vault.acceptBrief(seq: record.version, deviceID: session.deviceID)
        } catch {
            // Older than a brief already held: a rollback. Keep what is on screen.
            return
        }
        let wasEmpty = current == nil
        let saved = try storeRelayPackage(opened, record: record, session: session)
        guard !withdrawnIDs.contains(saved.brief.identifier) else { return }
        received(saved.brief, receipt: receipt(for: record, session: session))
        if saved.saved { acknowledge(saved.brief.identifier, version: BriefCache.sha256Hex(opened.package)) }
        rebuildHistory()
        if wasEmpty, current != nil {
            onFirstBrief?()
        }
    }

    /// Verifies the envelope header against the record and decrypts the padded package (section 4). Returns the
    /// package bytes with the padding removed, whose SHA-256 is the version, and the brief they hold.
    private func openPackage(_ record: RelayBriefRecord, session: SessionContext) throws
        -> (package: Data, brief: MobileBriefV1) {
        let env = record.env
        guard env.v == RelayCrypto.protocolVersion, env.kind == "brief", env.dir == "h2d",
              env.hub == session.hubID, env.dev == session.deviceID, env.seq == record.version else {
            throw BriefOpenError.mismatchedEnvelope
        }
        do {
            let ciphertext = try RelayCrypto.base64URLDecode(env.ct)
            let padded = try RelayCrypto.openEnvelope(ciphertext, key: session.keys.h2d, hub: env.hub,
                                                      dev: env.dev, dir: env.dir, seq: env.seq, kind: env.kind)
            let package = try RelayCrypto.unpadBrief(padded)
            let brief = try JSONDecoder().decode(MobileBriefV1.self, from: package)
            return (package, brief)
        } catch {
            throw BriefOpenError.unreadable
        }
    }

    /// Saves a relay package as the library entry for its brief, and returns the brief with whether it was saved.
    /// An older package never replaces a newer one of the same brief: the relay can hold an older copy after the
    /// Mac made the brief again.
    private func storeRelayPackage(_ opened: (package: Data, brief: MobileBriefV1), record: RelayBriefRecord,
                                   session: SessionContext) throws -> (brief: MobileBriefV1, saved: Bool) {
        // A relay copy of a brief the Mac withdrew must not bring its text back.
        guard !withdrawnIDs.contains(opened.brief.identifier) else { return (opened.brief, false) }
        let stored = cache.load(identifier: opened.brief.identifier)?.brief
        let replaces = (stored?.asOfDate ?? .distantPast) <= (opened.brief.asOfDate ?? .distantPast)
        guard replaces else { return (opened.brief, false) }
        try cache.save(package: opened.package, version: BriefCache.sha256Hex(opened.package),
                       relayVersion: record.version, audioReceipt: receipt(for: record, session: session))
        rebuildHistory()
        return (opened.brief, true)
    }

    private func handleRelayError(_ error: RelayClientError) {
        switch error {
        case .unauthorized, .forbidden:
            repairNeeded = true
            notice = error.localizedDescription
        case .rateLimited:
            lastRefreshFailed = true
        default:
            lastRefreshFailed = true
        }
    }

    // MARK: Helpers

    private func receipt(for record: RelayBriefRecord, session: SessionContext) -> BriefAudioReceipt {
        BriefAudioReceipt(deviceID: session.deviceID, pairingID: vault.current()?.secretID, sequence: record.version)
    }

    private func received(_ brief: MobileBriefV1, fromCache: Bool = false, receipt: BriefAudioReceipt? = nil) {
        guard let hubID = vault.current()?.hubID else { return }
        onBriefReceived?(brief, hubID, fromCache, receipt)
    }

    /// Recomputes the library's readable lists and the current brief. Every change to the library goes through here.
    private func rebuildHistory() {
        var entries: [String: HistoryEntry] = [:]
        let cached = cache.all()
        cachedBriefs = cached.map(\.brief)
        let next = BriefCache.current(in: cached)?.brief
        if current != next { current = next }
        for cached in cached {
            let brief = cached.brief
            entries[brief.identifier] = HistoryEntry(id: brief.identifier, slot: brief.slot,
                                                     localDate: brief.localDate, asOf: brief.asOfDate,
                                                     narrativeStatus: brief.narrativeStatus, isCached: true,
                                                     displaySlot: brief.displaySlot, timezone: brief.timezone,
                                                     kind: brief.kind, dueAt: brief.dueAt)
        }
        for summary in historySummaries where entries[summary.id] == nil {
            entries[summary.id] = HistoryEntry(id: summary.id, slot: summary.slot, localDate: summary.localDate,
                                               asOf: summary.asOfDate, narrativeStatus: summary.narrativeStatus,
                                               isCached: false)
        }
        history = entries.values.sorted { ($0.asOf ?? .distantPast) > ($1.asOf ?? .distantPast) }
    }

    @discardableResult
    private func clearAll() -> Bool {
        generation = UUID()
        withdrawnIDs.removeAll()
        targets.removeAll()
        targetOrder.removeAll()
        cancelFetches()
        todayGeneratedAt = nil
        isRefreshing = false
        historyLoading = false
        refreshPhase = .idle
        let cleared = (try? cache.removeAll()) != nil
        current = nil
        history = []
        cachedBriefs = []
        historySummaries = []
        hubOnline = nil
        lastRefreshFailed = false
        repairNeeded = false
        cooldownUntil = nil
        historyNotice = nil
        notice = nil
        return cleared
    }
}


// The sidebar reads existing brief and chat data. It never retains permission options,
// file paths or actions, and it owns no additional disk cache.
struct LibraryBrief: Identifiable, Equatable {
    let entry: HistoryEntry
    let summary: String
    var id: String { entry.id }
}

struct LibraryDay: Identifiable, Equatable {
    let localDate: String
    let entries: [LibraryBrief]
    var id: String { localDate }
}

struct LibraryRecord: Identifiable, Equatable, Sendable {
    let id: String
    let title: String
    let detail: String
    let status: String?
    let date: Date?
}

protocol LibraryHistoryAPI: Sendable {
    func libraryDecisions() async throws -> [LibraryRecord]
}

extension HubChannel: LibraryHistoryAPI {
    func libraryDecisions() async throws -> [LibraryRecord] {
        guard let main = try await chats(limit: 1).first(where: { $0.kind == .main }) else { return [] }
        guard Identifiers.isSafePathID(main.id) else { throw HubRequestError.invalidIdentifier }
        return try await perform(.chat(id: main.id, after: nil, limit: 50))
            .payload(LibraryDecisionPage.self).records
    }
}

/// Decodes only display data from the existing main-chat page. Malformed rows, pending
/// requests and unknown states cannot become a completed decision or an actionable request.
struct LibraryDecisionPage: Decodable {
    let records: [LibraryRecord]

    private struct Item: Decodable {
        let record: LibraryRecord?

        init(from decoder: Decoder) throws {
            let values = try decoder.container(keyedBy: ChatCodingKeys.self)
            func text(_ key: String) -> String {
                ((try? values.decode(String.self, forKey: ChatCodingKeys(key))) ?? "")
                    .trimmingCharacters(in: .whitespacesAndNewlines)
            }
            guard text("kind") == "proactive",
                  let proactive = try? values.nestedContainer(keyedBy: ChatCodingKeys.self,
                                                               forKey: ChatCodingKeys("proactive")) else {
                record = nil
                return
            }
            func field(_ key: String) -> String {
                ((try? proactive.decode(String.self, forKey: ChatCodingKeys(key))) ?? "")
                    .trimmingCharacters(in: .whitespacesAndNewlines)
            }
            let state = field("state")
            let identity = field("momentId")
            guard field("kind") == "decision_due", ["acted", "expired"].contains(state), !identity.isEmpty else {
                record = nil
                return
            }
            let headline = field("headline")
            let body = field("body")
            let fallback = text("text")
            guard !headline.isEmpty || !fallback.isEmpty else {
                record = nil
                return
            }
            record = LibraryRecord(id: identity, title: headline.isEmpty ? fallback : headline,
                                   detail: body.isEmpty ? fallback : body,
                                   status: state == "acted" ? String(localized: "Answered") : String(localized: "Expired"),
                                   date: RelayTime.parse(text("at")))
        }
    }

    private struct LenientItem: Decodable {
        let value: Item?
        init(from decoder: Decoder) throws { value = try? Item(from: decoder) }
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: ChatCodingKeys.self)
        let items = (try? values.decode([LenientItem].self, forKey: ChatCodingKeys("items"))) ?? []
        var seen = Set<String>()
        records = items.compactMap { $0.value?.record }.reversed()
            .filter { seen.insert($0.id).inserted }
            .sorted { lhs, rhs in
                if lhs.date != rhs.date { return (lhs.date ?? .distantPast) > (rhs.date ?? .distantPast) }
                return lhs.id < rhs.id
            }
    }
}

/// A pure projection keeps the Library consistent with the brief cache's existing lifetime.
struct LibraryProjection {
    let days: [LibraryDay]
    let artifacts: [LibraryRecord]
    let work: [LibraryRecord]

    init(history: [HistoryEntry], briefs: [MobileBriefV1]) {
        let ordered = briefs.sorted {
            if $0.asOfDate != $1.asOfDate { return ($0.asOfDate ?? .distantPast) > ($1.asOfDate ?? .distantPast) }
            return $0.identifier < $1.identifier
        }
        var cached: [String: MobileBriefV1] = [:]
        for brief in ordered where cached[brief.identifier] == nil { cached[brief.identifier] = brief }
        let rows = history.sorted {
            if $0.asOf != $1.asOf { return ($0.asOf ?? .distantPast) > ($1.asOf ?? .distantPast) }
            return $0.id < $1.id
        }.map { entry in
            let brief = cached[entry.id]
            let paragraph = brief?.narrative.lazy.compactMap(\.text)
                .map { $0.split(whereSeparator: \.isWhitespace).joined(separator: " ") }
                .first { !$0.isEmpty }
            let summary: String
            if let brief, brief.narrativeStatus != "unavailable", brief.narrativeStatus != "skipped",
               let paragraph {
                summary = paragraph
            } else {
                summary = brief == nil ? String(localized: "Open this brief to read its summary.")
                    : String(localized: "Summary unavailable.")
            }
            return LibraryBrief(entry: entry, summary: summary)
        }
        let grouped = Dictionary(grouping: rows) { $0.entry.localDate ?? "" }
        days = grouped.keys.sorted(by: >).map { LibraryDay(localDate: $0, entries: grouped[$0] ?? []) }
        artifacts = Self.records(from: ordered, isWork: false)
        work = Self.records(from: ordered, isWork: true)
    }

    private static func records(from briefs: [MobileBriefV1], isWork: Bool) -> [LibraryRecord] {
        var seen = Set<String>()
        var records: [LibraryRecord] = []
        for brief in briefs {
            let items = isWork ? (brief.sections.moved + brief.sections.openLoops)
                .filter { $0.kind == "one_time_work" } : brief.sections.deliverables
            for (index, item) in items.enumerated() {
                let title = item.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                guard !title.isEmpty else { continue }
                let source = brief.sources(for: item.refs).first {
                    $0.sourceKind == (isWork ? "history" : "deliverable") && !($0.sourceId ?? "").isEmpty
                }
                let id: String
                if let source, let sourceID = source.sourceId {
                    id = "\(source.sourceKind ?? ""):\(sourceID)"
                } else if let key = item.key, !key.isEmpty {
                    id = key
                } else {
                    let itemID = item.id.flatMap { $0.isEmpty ? nil : $0 } ?? String(index)
                    id = "\(brief.identifier):\(itemID)"
                }
                guard seen.insert(id).inserted else { continue }
                let status = item.status?.trimmingCharacters(in: .whitespacesAndNewlines)
                records.append(LibraryRecord(id: id, title: title, detail: item.detail ?? "",
                                             status: status?.isEmpty == false ? status : nil,
                                             date: item.atDate ?? brief.asOfDate))
            }
        }
        return records.sorted {
            if $0.date != $1.date { return ($0.date ?? .distantPast) > ($1.date ?? .distantPast) }
            return $0.id < $1.id
        }
    }
}

@MainActor
final class LibraryStore: ObservableObject {
    @Published private(set) var days: [LibraryDay] = []
    @Published private(set) var artifacts: [LibraryRecord] = []
    @Published private(set) var work: [LibraryRecord] = []
    @Published private(set) var decisions: [LibraryRecord] = []
    @Published private(set) var decisionNotice: String?
    @Published private(set) var decisionsLoading = false

    private let hub: LibraryHistoryAPI
    private var paired: PairedDevice?
    private var history: [HistoryEntry] = []
    private var briefs: [MobileBriefV1] = []
    private var subscriptions = Set<AnyCancellable>()
    private var generation = UUID()

    init(brief: BriefStore, pairing: PairingStore, hub: LibraryHistoryAPI) {
        self.hub = hub
        paired = pairing.paired
        brief.$history.combineLatest(brief.$cachedBriefs).sink { [weak self] history, briefs in
            self?.history = history
            self?.briefs = briefs
            self?.rebuild()
        }.store(in: &subscriptions)
        pairing.$paired.sink { [weak self] device in
            guard let self else { return }
            let changed = self.paired?.hubID != device?.hubID || self.paired?.deviceID != device?.deviceID ||
                self.paired?.scopes != device?.scopes
            if changed {
                self.generation = UUID()
                self.decisions = []
                self.decisionNotice = nil
                self.decisionsLoading = false
                if self.paired?.hubID != device?.hubID {
                    self.history = []
                    self.briefs = []
                }
            }
            self.paired = device
            self.rebuild()
        }.store(in: &subscriptions)
    }

    func refreshDecisions() async {
        guard paired?.scopes.contains("chat:read") == true else {
            decisions = []
            decisionNotice = String(localized: "Pair this phone again in Settings to read decisions from your computer.")
            return
        }
        guard !decisionsLoading else { return }
        let current = generation
        decisionsLoading = true
        defer { if generation == current { decisionsLoading = false } }
        do {
            let records = try await HubRequestScope.$hubID.withValue(paired?.hubID) {
                try await hub.libraryDecisions()
            }
            guard generation == current, !Task.isCancelled else { return }
            decisions = records
            decisionNotice = nil
        } catch {
            guard generation == current, !Task.isCancelled else { return }
            if Self.clearsDecisions(for: error) { decisions = [] }
            decisionNotice = String(localized: "Recent decisions appear here when your computer is online and allows this phone to read them.")
        }
    }

    // Leaving History invalidates its read even when the transport cannot finish cancellation
    // immediately. A reopened sheet can refresh while that older request is still unwinding.
    func cancelDecisionRefresh() {
        generation = UUID()
        decisionsLoading = false
    }

    private func rebuild() {
        guard paired?.scopes.contains("brief:read") == true else {
            days = []
            artifacts = []
            work = []
            return
        }
        let projection = LibraryProjection(history: history, briefs: briefs)
        days = projection.days
        artifacts = projection.artifacts
        work = projection.work
    }

    private static func clearsDecisions(for error: Error) -> Bool {
        switch error {
        case HubRequestError.notPaired, HubRequestError.forbidden, HubRequestError.rejectedEnvelope,
             RelayClientError.unauthorized, RelayClientError.forbidden, RelayClientError.gone:
            return true
        case HubRequestError.rejected(let status, _) where [401, 403, 404, 410].contains(status):
            return true
        default:
            return false
        }
    }
}
