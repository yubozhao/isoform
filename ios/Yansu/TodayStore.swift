import Combine
import Foundation
import Network

protocol TodayHubAPI: Sendable {
    func today() async throws -> MobileTodaySnapshot
    func pollTodayEvents() async throws -> [MobileTodayEvent]
    func requestTodayBrief(key: String) async throws -> BriefRefreshResult
    func meetingPrep(id: String) async throws -> TodayMeetingPrep
}

extension TodayHubAPI {
    func meetingPrep(id: String) async throws -> TodayMeetingPrep {
        throw HubRequestError.rejected(status: 404, message: "")
    }
}

extension HubChannel: TodayHubAPI {
    func meetingPrep(id: String) async throws -> TodayMeetingPrep {
        guard !id.isEmpty, id.utf8.count <= 2048 else { throw HubRequestError.invalidIdentifier }
        guard try await status().todayMeetingPrep == true else {
            throw HubRequestError.rejected(status: 404, message: "")
        }
        return try await perform(.todayMeetingPrep(id: id)).payload(TodayMeetingPrep.self)
    }
    func requestTodayBrief(key: String) async throws -> BriefRefreshResult {
        struct Request: Encodable { let idempotencyKey: String }
        return try await post("/brief/refresh", Request(idempotencyKey: key), as: BriefRefreshResult.self)
    }
}

struct TodayPairingIdentity: Codable, Equatable, Sendable {
    let hubID: String
    let deviceID: String
}

private struct SavedToday: Codable {
    let pairing: TodayPairingIdentity
    let snapshot: MobileTodaySnapshot
}

// The serial queue keeps writes ordered and makes a pairing clear wait for any
// earlier write. Files use the same protection class as the brief cache.
final class TodayDiskCache: @unchecked Sendable {
    static var defaultURL: URL {
        let root = AppGroup.containerURL ?? FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return root.appendingPathComponent("Isoform", isDirectory: true).appendingPathComponent("today.json")
    }

    private let fileURL: URL
    private let queue = DispatchQueue(label: "com.isoform.today-cache")
    private var removalPending = false

    init(fileURL: URL = TodayDiskCache.defaultURL) { self.fileURL = fileURL }

    func load(for identity: TodayPairingIdentity) -> MobileTodaySnapshot? {
        queue.sync {
            guard finishPendingRemoval(), let data = try? Data(contentsOf: fileURL),
                  let saved = try? JSONDecoder().decode(SavedToday.self, from: data),
                  saved.pairing == identity else { return nil }
            return saved.snapshot
        }
    }

    func save(_ snapshot: MobileTodaySnapshot, for identity: TodayPairingIdentity) {
        queue.async { [self] in
            do {
                guard finishPendingRemoval() else { return }
                try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
                let data = try JSONEncoder().encode(SavedToday(pairing: identity, snapshot: snapshot))
                try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            } catch {
                // The existing on-disk snapshot remains intact on a failed replacement.
            }
        }
    }

    /// Notifications are posted only after the snapshot they open is durable.
    func saveConfirmed(_ snapshot: MobileTodaySnapshot, for identity: TodayPairingIdentity) async -> Bool {
        await withCheckedContinuation { continuation in
            queue.async { [self] in
                do {
                    guard finishPendingRemoval() else { continuation.resume(returning: false); return }
                    try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
                    let data = try JSONEncoder().encode(SavedToday(pairing: identity, snapshot: snapshot))
                    try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
                    continuation.resume(returning: true)
                } catch { continuation.resume(returning: false) }
            }
        }
    }

    @discardableResult
    func clear() -> Bool {
        queue.sync {
            removalPending = true
            return finishPendingRemoval()
        }
    }

    private func finishPendingRemoval() -> Bool {
        guard removalPending else { return true }
        do {
            if FileManager.default.fileExists(atPath: fileURL.path) { try FileManager.default.removeItem(at: fileURL) }
            removalPending = false
            return true
        } catch { return false }
    }
}

/// Why the computer went to sleep, from the relay goodbye `reason`. user_sleep, unknown or no reason keep the
/// plain asleep copy.
enum ComputerSleepReason: String, CaseIterable, Sendable {
    case batteryFloor = "battery_floor", lowPower = "low_power", lidClosed = "lid_closed"

    var detail: String {
        switch self {
        case .batteryFloor: return String(localized: "connection.sleepReason.batteryFloor")
        case .lowPower: return String(localized: "connection.sleepReason.lowPower")
        case .lidClosed: return String(localized: "connection.sleepReason.lidClosed")
        }
    }

    /// The lines Today and the Computer sheet both show, word for word (design's P147 wording). The waiting clause is
    /// added by `directions`, not here.
    var steps: [String] {
        switch self {
        case .batteryFloor:
            return [String(localized: "connection.sleepReason.batteryFloor.step"),
                    String(localized: "connection.sleepReason.picksUp")]
        case .lowPower: return [String(localized: "connection.sleepReason.lowPower.step")]
        case .lidClosed:
            return [String(localized: "connection.sleepReason.lidClosed.step"), String(localized: "connection.sleepReason.picksUp")]
        }
    }

    /// What Today and the Computer sheet tell the person to do while the computer is away (P147). A sleep with a known
    /// reason gets that reason's steps, so a closed lid reads "Open the lid." and not the generic line.
    static func directions(for cause: TodayStore.ConnectionCause, reason: ComputerSleepReason?, sentWaiting: Bool = false) -> String {
        var text = cause == .sleeping ? (reason.map { $0.steps.joined(separator: " ") } ?? cause.directions) : cause.directions
        // With something of the person's waiting to send, the line says it goes out too (P147).
        if sentWaiting, !text.isEmpty {
            text = String(text.dropLast()) + String(localized: "connection.sentWaiting")
        }
        return text
    }

    /// The Computer sheet's first line for a sleep with a known reason. `sleptAt` is when the computer went to sleep (the
    /// relay goodbye's time), so it reads "since"; without it, `asOf` is when Isoform last heard from the computer, so it
    /// reads "as of". The same phrase rule applies to every cause (P147 follow-up).
    func status(sleptAt: String?, asOf: String?) -> String {
        if let sleptAt { return String.localizedStringWithFormat(String(localized: "connection.asleepSince"), sleptAt, detail) }
        guard let asOf else { return "\(TodayStore.ConnectionCause.sleeping.title) · \(detail)" }
        return String.localizedStringWithFormat(String(localized: "connection.asleepAsOf"), asOf, detail)
    }

    /// "Asleep since {time} · {detail}", or the plain title with the detail when the relay has no time.
    func status(since: String?) -> String {
        guard let since else { return "\(TodayStore.ConnectionCause.sleeping.title) · \(detail)" }
        return String.localizedStringWithFormat(String(localized: "connection.asleepSince"), since, detail)
    }
}

@MainActor
final class TodayStore: ObservableObject {
    enum ConnectionCause: String, CaseIterable, Sendable {
        case sleeping, shuttingDown = "shutting_down", closing, notAnswering = "not_answering"
        case phoneOffline = "phone_offline", cantConnect = "cant_connect"

        var title: String {
            switch self {
            case .sleeping: return String(localized: "connection.sleeping")
            case .shuttingDown: return String(localized: "connection.shuttingDown")
            case .closing: return String(localized: "connection.closed")
            case .notAnswering: return String(localized: "connection.notAnswering")
            case .phoneOffline: return String(localized: "connection.phoneOffline")
            case .cantConnect: return String(localized: "connection.cantConnect")
            }
        }
        var directions: String {
            switch self {
            case .sleeping: return String(localized: "connection.sleeping.directions")
            case .shuttingDown: return String(localized: "connection.shuttingDown.directions")
            case .closing: return String(localized: "connection.closed.directions")
            case .notAnswering: return String(localized: "connection.notAnswering.directions")
            case .phoneOffline: return String(localized: "connection.phoneOffline.directions")
            case .cantConnect: return String(localized: "connection.cantConnect.directions")
            }
        }

        /// The Computer sheet's first line, and Today's away line under the date (P133). A sleep with a known reason
        /// reads "since" when the goodbye time is known and "as of" otherwise; other causes read "{title} · as of {time}".
        static func firstLine(_ cause: ConnectionCause, reason: ComputerSleepReason?, sleptAt: String?, asOf: String?) -> String {
            if cause == .sleeping, let reason { return reason.status(sleptAt: sleptAt, asOf: asOf) }
            if cause == .sleeping, let sleptAt {
                return String.localizedStringWithFormat(String(localized: "connection.sleepingSince"), sleptAt)
            }
            if let asOf { return String.localizedStringWithFormat(String(localized: "connection.causeAsOf"), cause.title, asOf) }
            return cause.title
        }
    }
    @Published private(set) var snapshot: MobileTodaySnapshot?
    @Published private(set) var hubUnreachable = false
    @Published private(set) var connectionCause: ConnectionCause?
    /// The relay goodbye's optional reason and time, for Chat's thread line.
    @Published private(set) var connectionReason: String?
    @Published private(set) var connectionSince: Date?
    private var probedGoodbye: (reason: String?, at: Date?) = (nil, nil)
    @Published private(set) var pendingFresh: PendingWrite?
    /// True once the Mac refused a request for a new brief, until the person asks again or a request is accepted.
    @Published private(set) var freshFailed = false
    var onSnapshot: (@MainActor (MobileTodaySnapshot, MobileTodaySnapshot?) -> Void)?
    private let hub: TodayHubAPI
    private let cache: TodayDiskCache
    private let brief: BriefStore
    private let journal: PendingWriteJournal?
    private let identity: @MainActor () -> TodayPairingIdentity?
    private let prefetchAudio: @MainActor (MobileBriefV1, String) async -> Void
    /// The relay's answer on whether the Mac is online. Used only to tell a slow Mac from an unreachable one.
    private let presence: @Sendable () async throws -> Bool
    private let connectionPresence: (@Sendable () async throws -> RelayPresence)?
    private var networkAvailable = true
    private var connectionFailureTask: Task<Void, Never>?
    private var connectionWatchTask: Task<Void, Never>?
    private var connectionRevision = 0
    private var generation = UUID()
    private var revision = 0
    private var refreshing = false
    private var sendingFresh = false
    private var prefetchTask: Task<Void, Never>?
    private var eventTask: Task<Void, Never>?
    private var resyncTask: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    private var retryAttempt = 0
    private var listenerID = UUID()
    private var pathMonitor: NWPathMonitor?
    private var wantsListening = false

    func meetingPrep(id: String) async throws -> TodayMeetingPrep {
        guard let pairing = identity() else { throw HubRequestError.notPaired }
        let result = try await HubRequestScope.$hubID.withValue(pairing.hubID) {
            try await hub.meetingPrep(id: id)
        }
        guard identity() == pairing, result.meetingId == id else { throw HubRequestError.notPaired }
        return result
    }

    init(hub: TodayHubAPI, cache: TodayDiskCache, brief: BriefStore,
         journal: PendingWriteJournal? = nil,
         identity: @escaping @MainActor () -> TodayPairingIdentity?,
         presence: @escaping @Sendable () async throws -> Bool = { true },
         connectionPresence: (@Sendable () async throws -> RelayPresence)? = nil,
         prefetchAudio: @escaping @MainActor (MobileBriefV1, String) async -> Void = { _, _ in }) {
        self.presence = presence
        self.connectionPresence = connectionPresence
        self.hub = hub
        self.cache = cache
        self.brief = brief
        self.journal = journal
        self.identity = identity
        self.prefetchAudio = prefetchAudio
    }

    func loadCached() {
        guard let identity = identity() else { snapshot = nil; return }
        snapshot = cache.load(for: identity)
        if let snapshot { brief.reconcileToday(snapshot) }
    }

    /// The current brief, the one rule in BriefStore: the newest brief whose full text is on this phone. A brief
    /// whose text has not arrived is never shown, so there is no stand-in for it.
    func newestBrief() -> MobileBriefV1? {
        brief.current
    }

    var asOf: Date? {
        [snapshot?.generatedDate, newestBrief()?.asOfDate].compactMap { $0 }.max()
    }

    func refresh() async {
        guard identity() != nil, !refreshing else { return }
        let currentGeneration = generation
        let initialRevision = revision
        refreshing = true
        defer { if generation == currentGeneration { refreshing = false } }
        do {
            let value: MobileTodaySnapshot
            do { value = try await hub.today() }
            catch HubRequestError.conflict { value = try await hub.today() }
            guard generation == currentGeneration, revision == initialRevision else { return }
            hubUnreachable = false
            clearConnectionCause()
            retryAttempt = 0
            retryTask?.cancel()
            retryTask = nil
            accept(value)
        } catch {
            guard generation == currentGeneration else { return }
            if error is CancellationError { return }
            // A slow or refused answer keeps the last snapshot and its copy quiet; only a Mac the relay
            // reports offline is shown as asleep. Either way the read is retried.
            let unreachable = await Self.macUnreachable(after: error, presence: presence)
            guard generation == currentGeneration else { return }
            hubUnreachable = unreachable
            await inspectConnection()
            scheduleRetry()
        }
    }

    private func clearConnectionCause() {
        connectionRevision += 1
        connectionFailureTask?.cancel()
        connectionFailureTask = nil
        connectionReason = nil
        connectionSince = nil
        connectionCause = nil
    }

    private func probeConnection() async -> (ConnectionCause?, Bool) {
        guard networkAvailable else { return (.phoneOffline, false) }
        guard let connectionPresence else {
            return ((try? await presence()) == false ? .notAnswering : nil, false)
        }
        do {
            let value = try await connectionPresence()
            probedGoodbye = (value.goodbyeReason, value.goodbyeAt.flatMap(RelayTime.parse))
            guard !value.hubOnline else { return (nil, false) }
            if let raw = value.goodbyeCause, let cause = ConnectionCause(rawValue: raw),
               [.sleeping, .shuttingDown, .closing].contains(cause) { return (cause, true) }
            return (.notAnswering, false)
        } catch { return (.cantConnect, false) }
    }

    private func inspectConnection() async {
        let current = generation
        let revision = connectionRevision
        let (cause, definitive) = await probeConnection()
        guard generation == current, connectionRevision == revision, !Task.isCancelled else { return }
        guard let cause else { clearConnectionCause(); return }
        if definitive {
            connectionFailureTask?.cancel()
            connectionFailureTask = nil
            connectionReason = probedGoodbye.reason
            connectionSince = probedGoodbye.at
            connectionCause = cause
            return
        }
        guard connectionFailureTask == nil else { return }
        connectionFailureTask = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(8)) } catch { return }
            guard let self, self.generation == current, self.connectionRevision == revision else { return }
            let (retry, explicit) = await self.probeConnection()
            guard self.generation == current, self.connectionRevision == revision, !Task.isCancelled else { return }
            if !explicit { do { try await Task.sleep(for: .seconds(2)) } catch { return } }
            guard self.generation == current, self.connectionRevision == revision, !Task.isCancelled else { return }
            self.connectionReason = nil
            self.connectionSince = nil
            self.connectionCause = retry
            self.connectionFailureTask = nil
        }
    }

    /// Whether the Mac is really unreachable. Only a relay presence answer that says the Mac is offline, or a
    /// phone with no route to the relay, counts. A read that timed out or was refused while the relay still
    /// reports the Mac online is not asleep, and neither is a presence check that itself timed out.
    static func macUnreachable(after error: Error, presence: @Sendable () async throws -> Bool) async -> Bool {
        if let relay = error as? RelayClientError, case .offline = relay { return true }
        do { return try await presence() == false }
        catch RelayClientError.offline { return true }
        catch { return false }
    }

    /// Retries a failed snapshot read quietly, backing off to a minute. A success, a pairing change or a new
    /// refresh stops it.
    private func scheduleRetry() {
        guard retryTask == nil else { return }
        let current = generation
        let delay = min(10 * (1 << min(retryAttempt, 3)), 60)
        retryAttempt += 1
        retryTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard let self, !Task.isCancelled, self.generation == current else { return }
            self.retryTask = nil
            await self.refresh()
        }
    }

    func accept(_ value: MobileTodaySnapshot) {
        guard let identity = identity() else { return }
        if let previous = snapshot?.generatedDate, let incoming = value.generatedDate, incoming < previous { return }
        let previous = snapshot
        revision += 1
        snapshot = value
        // The library fetches the bodies this snapshot names; a later snapshot does not cancel them.
        brief.reconcileToday(value)
        cache.save(value, for: identity)
        onSnapshot?(value, previous)
        prefetchTask?.cancel()
        let currentGeneration = generation
        prefetchTask = Task { [weak self] in
            guard let self, !Task.isCancelled, self.generation == currentGeneration else { return }
            if let latest = self.newestBrief() {
                await self.prefetchAudio(latest, identity.hubID)
            }
        }
    }

    func persistForNotification(_ value: MobileTodaySnapshot, identity expected: TodayPairingIdentity) async -> Bool {
        guard snapshot == value, identity() == expected else { return false }
        let saved = await cache.saveConfirmed(value, for: expected)
        return saved && snapshot == value && identity() == expected
    }

    func noteBriefRequest(_ result: BriefRefreshResult) {
        guard let readyAt = result.readyAt, let state = result.status,
              state == "queued" || state == "making", var value = snapshot,
              let identity = identity() else { return }
        value.briefRequest = TodayBriefRequest(state: state, readyAt: readyAt)
        revision += 1
        snapshot = value
        cache.save(value, for: identity)
    }

    func loadPendingFresh() async {
        guard let journal else { return }
        let current = generation
        guard let device = identity()?.deviceID else { pendingFresh = nil; return }
        do { try await journal.expireToday(at: Date()) }
        catch { brief.notice = String(localized: "Isoform could not save this change. Try again.") }
        let value = await journal.entries().first { $0.todayRefresh == true && $0.todayDeviceID == device }
        guard current == generation, identity()?.deviceID == device else { return }
        pendingFresh = value
    }

    /// The states the Mac answers a request for a new brief with when it accepted it (BriefStore.requestFreshBrief).
    static let acceptedFreshStates: Set<String> = ["queued", "making", "started", "running"]

    func requestFresh() async {
        guard let journal, let identity = identity(), snapshot?.briefRequest == nil else { return }
        freshFailed = false
        do {
            _ = try await journal.reserveTodayRefresh(deviceID: identity.deviceID, makeKey: { UUID().uuidString.lowercased() }, createdAt: Date())
            await loadPendingFresh()
            if !hubUnreachable && brief.hubOnline != false { await retryFresh() }
        } catch {
            brief.notice = String(localized: "Isoform could not save this change. Try again.")
        }
    }

    func undoFresh() async {
        guard let journal, let pendingFresh else { return }
        do { _ = try await journal.undoToday(key: pendingFresh.key) }
        catch { brief.notice = String(localized: "Isoform could not save this change. Try again.") }
        await loadPendingFresh()
    }

    func dismissUnknownFresh() async {
        guard let journal, let pendingFresh else { return }
        do { _ = try await journal.transitionToday(key: pendingFresh.key, event: .dismiss) }
        catch { brief.notice = String(localized: "Isoform could not save this change. Try again.") }
        await loadPendingFresh()
    }

    func retryFresh() async {
        guard let journal, !sendingFresh, let pendingFresh, let selectedIdentity = identity(),
              pendingFresh.todayDeviceID == selectedIdentity.deviceID else { return }
        sendingFresh = true
        let current = generation
        defer { sendingFresh = false }
        var didDispatch = false
        do {
            guard let write = try await journal.markTodayRefreshDispatched(key: pendingFresh.key, at: Date()) else {
                await loadPendingFresh()
                return
            }
            didDispatch = true
            await loadPendingFresh()
            let result = try await HubRequestScope.$hubID.withValue(write.hubID) {
                try await HubRequestScope.$deviceID.withValue(write.todayDeviceID) {
                    try await hub.requestTodayBrief(key: write.key)
                }
            }
            guard current == generation, identity() == selectedIdentity else { return }
            _ = try await journal.transitionToday(key: write.key, event: .answer)
            await loadPendingFresh()
            if let sent = write.todayRefreshDispatchedAt,
               let latest = snapshot?.latestBrief?.madeDate, latest >= sent, snapshot?.briefRequest == nil { return }
            freshFailed = !Self.acceptedFreshStates.contains(result.status ?? "")
            noteBriefRequest(result)
        } catch {
            guard current == generation, identity() == selectedIdentity else { return }
            guard didDispatch else {
                brief.notice = String(localized: "Isoform could not save this change. Try again.")
                return
            }
            if JournaledWriter.disposition(for: error) != .keep {
                _ = try? await journal.transitionToday(key: pendingFresh.key, event: .answer)
                await loadPendingFresh()
                freshFailed = true
            }
        }
    }

    func startListening() {
        wantsListening = true
        if connectionWatchTask == nil, connectionPresence != nil {
            connectionWatchTask = Task { [weak self] in
                while !Task.isCancelled {
                    guard let self, self.wantsListening, self.identity() != nil else { return }
                    await self.inspectConnection()
                    do { try await Task.sleep(for: .seconds(3)) } catch { return }
                }
            }
        }
        if resyncTask == nil {
            resyncTask = Task { [weak self] in
                while !Task.isCancelled {
                    do { try await Task.sleep(for: .seconds(15 * 60)) }
                    catch { return }
                    guard let self, self.wantsListening else { return }
                    await self.refresh()
                }
            }
        }
        if pathMonitor == nil {
            let monitor = NWPathMonitor()
            pathMonitor = monitor
            monitor.pathUpdateHandler = { [weak self] path in
                let available = path.status == .satisfied
                Task { @MainActor [weak self] in
                    guard let self, self.wantsListening, self.identity() != nil else { return }
                    self.networkAvailable = available
                    guard available else { await self.inspectConnection(); return }
                    self.startListening()
                    await self.refresh()
                }
            }
            monitor.start(queue: DispatchQueue(label: "com.isoform.today-network"))
        }
        guard eventTask == nil, identity() != nil else { return }
        let currentGeneration = generation
        let currentListener = UUID()
        listenerID = currentListener
        eventTask = Task { [weak self] in
            guard let self else { return }
            defer { if self.listenerID == currentListener { self.eventTask = nil } }
            while !Task.isCancelled, self.identity() != nil, self.generation == currentGeneration {
                do {
                    let events = try await self.hub.pollTodayEvents()
                    guard self.generation == currentGeneration, !Task.isCancelled else { return }
                    for event in events {
                        if let snapshot = event.snapshot { self.accept(snapshot) }
                        else { await self.refresh() }
                    }
                } catch {
                    if error is CancellationError { return }
                    let unreachable = await Self.macUnreachable(after: error, presence: self.presence)
                    guard self.generation == currentGeneration else { return }
                    self.hubUnreachable = unreachable
                    await self.inspectConnection()
                    // A slow or refused poll is not the Mac being asleep: pause, then keep listening.
                    if unreachable { return }
                    do { try await Task.sleep(for: .seconds(5)) } catch { return }
                }
            }
        }
    }

    func stopListening() {
        wantsListening = false
        connectionRevision += 1
        connectionWatchTask?.cancel()
        connectionWatchTask = nil
        connectionFailureTask?.cancel()
        connectionFailureTask = nil
        pathMonitor?.cancel()
        pathMonitor = nil
        resyncTask?.cancel()
        resyncTask = nil
        listenerID = UUID()
        eventTask?.cancel()
        eventTask = nil
    }

    @discardableResult
    func clearCachedSnapshot() -> Bool {
        prefetchTask?.cancel()
        prefetchTask = nil
        revision += 1
        snapshot = nil
        return cache.clear()
    }

    @discardableResult
    func clearAfterPairingChange() -> Bool {
        generation = UUID()
        stopListening()
        prefetchTask?.cancel()
        prefetchTask = nil
        retryTask?.cancel()
        retryTask = nil
        retryAttempt = 0
        snapshot = nil
        pendingFresh = nil
        freshFailed = false
        hubUnreachable = false
        clearConnectionCause()
        refreshing = false
        revision = 0
        return cache.clear()
    }
}
