import Combine
import Foundation
import UIKit

@MainActor
final class AppEnvironment: ObservableObject {
    static let shared = AppEnvironment(legacyBriefDirectory: BriefCache.legacyDirectory)

    let firstRun: FirstRunStore
    let vault: PairingVault
    let cache: BriefCache
    let hub: HubChannel
    /// What Isoform sees, from the computer (the hub itself outside UI tests).
    let seesHub: SeesHubAPI
    let firstReadHub: FirstReadHubAPI
    let journal: PendingWriteJournal
    let pairing: PairingStore
    let brief: BriefStore
    let today: TodayStore
    /// A seeded away state keeps its cause; the simulator's real connection check would replace it.
    private static var seedsAwayCause: Bool {
        #if DEBUG && targetEnvironment(simulator)
        ProcessInfo.processInfo.environment["YANSU_UITEST_AWAY_CAUSE"] != nil
        #else
        false
        #endif
    }
    let audioCache: BriefAudioCache
    let audioPlayer: BriefAudioPlayer
    /// The briefs on screen now, so the mini player steps aside while its brief is in view.
    let briefOnScreen = BriefOnScreen()
    let actions: ActionStore
    let weekDeclines: YourWeekDeclineStore
    let chat: ChatStore
    let feed: FeedStore
    let goals: GoalStore
    let ideas: IdeasStore
    let tasks: TasksStore
    let holds: HoldsStore
    let yourAgent: YourAgentStore
    let topics: TopicStore
    let push: PushRegistration
    let notifications: NotificationSettingsStore
    let keepAwake: KeepAwakeStore
    let localTodayNotices: LocalTodayNotices
    let receipts: NoticeReceipts
    let widget: WidgetSnapshotPublisher
    let router = NotificationRouter()
    @Published private(set) var pendingBriefPlay: String?
    @Published var briefPlayNotice: String?
    private let briefPlayQueue: BriefPlayQueue
    private var replayingBriefPlay = false
    /// Off only for the seeded UI test states, which have no relay to ask.
    var refreshesBriefs = true
    var refreshesTasks = true
    /// Off for the seeded UI test states too, except those whose topic routes are answered in-process.
    var refreshesTopics = true
    /// The Library reuses existing encrypted main-chat reads for its read-only decision history.
    var libraryHistoryAPI: LibraryHistoryAPI {
        #if DEBUG && targetEnvironment(simulator)
        if isLibraryUITest { return LibraryUITestHistory() }
        #endif
        return hub
    }
    #if DEBUG && targetEnvironment(simulator)
    private var isLibraryUITest = false
    #endif
    private var subscriptions = Set<AnyCancellable>()
    private let hubStateURL: URL
    private let localNotificationDefaults: UserDefaults
    private var localHubCleanupPending = false
    private var acceptingBackgroundToday = false

    private struct LocalHubState: Codable {
        let hubID: String?
        var needsCleanup: Bool? = nil
    }

    init(directory: URL = PairingVault.defaultDirectory,
         secrets: SecretStore = KeychainSecretStore(),
         briefDirectory: URL = BriefCache.defaultDirectory,
         legacyBriefDirectory: URL? = nil,
         journalURL: URL = PendingWriteJournal.defaultFileURL,
         chatDirectory: URL = ChatCache.defaultDirectory,
         feedURL: URL? = nil,
         audioDirectory: URL? = nil,
         audioRequest: (@MainActor (MobileBriefV1) async throws -> BriefAudioResource)? = nil,
         audioDownload: (@MainActor (BriefAudio) async throws -> Data)? = nil,
         widgetStore: WidgetSnapshotStore = WidgetSnapshotStore(fileURL: WidgetSnapshotStore.defaultURL),
         pushDefaults: UserDefaults = .standard,
         makePush: ((PairingVault, UserDefaults) -> PushRegistration)? = nil,
         pairingTransport: PairingTransport = RelayClient.shared,
         guardian: DecisionGuarding = DecisionGuard(),
         chatHub: ChatHubAPI? = nil,
         proactiveHub: ProactiveActionHubAPI? = nil,
         ideasHub: IdeasHubAPI? = nil,
         actionHub: ActionHubAPI? = nil,
         feedHub: FeedHubAPI? = nil,
         tasksHub: TasksHubAPI? = nil,
         holdsHub: HoldsHubAPI? = nil,
         topicHub: TopicHubAPI? = nil,
         todayHub: TodayHubAPI? = nil,
         weekDeclineHub: YourWeekDeclineAPI? = nil,
         keepAwakeHub: KeepAwakeAPI? = nil,
         notificationHub: NotificationSettingsAPI? = nil,
         seesHub: SeesHubAPI? = nil,
         firstReadHub: FirstReadHubAPI? = nil,
         todayURL: URL? = nil,
         notificationSettingsURL: URL? = nil,
         presenceOverride: (@Sendable () async throws -> Bool)? = nil,
         removeLocalItem: @escaping (URL) throws -> Void = { try FileManager.default.removeItem(at: $0) }) {
        let vault = PairingVault(directory: directory, secrets: secrets)
        RemoteAccess.shared.configure(vault: vault)
        let cache = BriefCache(directory: briefDirectory, removeItem: removeLocalItem)
        let hub = HubChannel(vault: vault)
        let push = makePush?(vault, pushDefaults) ?? PushRegistration(vault: vault, defaults: pushDefaults)
        let pairing = PairingStore(vault: vault, transport: pairingTransport,
                                   prepareForPairingChange: { await hub.prepareForPairingChange() },
                                   finishPairingChange: { await hub.finishPairingChange() },
                                   didStorePairing: { await push.pairingDidChange() })
        // One journal for every write the phone makes, so ActionStore and Chat never hold the same file twice.
        let journal = PendingWriteJournal(fileURL: journalURL, hubID: { vault.current()?.hubID })
        let presence: @Sendable () async throws -> Bool = presenceOverride ?? { try await AppEnvironment.hubOnline(vault: vault) }
        self.hubStateURL = directory.appendingPathComponent("local-hub.json")
        self.briefPlayQueue = BriefPlayQueue(fileURL: directory.appendingPathComponent("pending-brief-play.json"))
        self.localNotificationDefaults = pushDefaults
        self.firstRun = FirstRunStore(defaults: pushDefaults)
        self.vault = vault
        self.cache = cache
        self.hub = hub
        self.seesHub = seesHub ?? hub
        self.firstReadHub = firstReadHub ?? hub
        self.pairing = pairing
        var seededAgent: YourAgentStore?
        #if DEBUG && targetEnvironment(simulator)
        seededAgent = YourAgentUITestSeed.makeStore(directory: directory, secrets: secrets,
            hubID: { vault.current()?.hubID })
        #endif
        self.yourAgent = seededAgent ?? YourAgentStore(hub: hub,
            cache: YourAgentCache(directory: directory == PairingVault.defaultDirectory
                ? YourAgentCache.defaultDirectory : directory.appendingPathComponent("your-agent"), secrets: secrets),
            hubID: { vault.current()?.hubID },
            canRead: { pairing.paired?.scopes.contains("brief:read") == true })
        self.journal = journal
        let briefStore = BriefStore(pairing: pairing, vault: vault, cache: cache, hub: hub)
        self.brief = briefStore
        self.weekDeclines = YourWeekDeclineStore(hub: weekDeclineHub ?? hub, journal: journal, defaults: pushDefaults,
                                                 hubID: { vault.current()?.hubID }, online: { briefStore.hubOnline != false })
        let audioCache = BriefAudioCache(directory: audioDirectory ?? (directory == PairingVault.defaultDirectory
            ? BriefAudioCache.defaultDirectory : directory.appendingPathComponent("brief-audio", isDirectory: true)))
        self.audioCache = audioCache
        let prefetchAudioDirectory = (audioDirectory ?? (directory == PairingVault.defaultDirectory
            ? BriefAudioCache.defaultDirectory : directory.appendingPathComponent("brief-audio", isDirectory: true)))
            .deletingLastPathComponent().appendingPathComponent("brief-audio-prefetch", isDirectory: true)
        let audioPlayer = BriefAudioPlayer(cache: audioCache, prefetchCache: BriefAudioCache(directory: prefetchAudioDirectory),
                                           requestAudio: audioRequest ?? { brief in
            guard let paired = vault.current(), paired.scopes.contains("brief:read") else {
                throw HubRequestError.forbidden
            }
            return try await HubRequestScope.$hubID.withValue(paired.hubID) {
                try await hub.briefAudio(brief: brief)
            }
        }, downloadAudio: audioDownload ?? { audio in
            let session = try vault.session()
            guard vault.current()?.scopes.contains("brief:read") == true,
                  let attachment = audio.attachment, let id = attachment.id,
                  Identifiers.isSafePathID(id), let bytes = audio.bytes,
                  bytes > 0, bytes <= 16 * 1024 * 1024 else {
                throw BriefAudioError.notAvailable
            }
            let sealed: Data
            do {
                sealed = try await RelayClient.shared.attachment(session.credentials, id: id)
            } catch RelayClientError.notFound {
                throw BriefAudioError.notAvailable
            } catch RelayClientError.offline {
                throw BriefAudioError.computerOffline
            } catch RelayClientError.timedOut {
                throw BriefAudioError.computerOffline
            }
            try Task.checkCancellation()
            guard vault.current()?.hubID == session.hubID,
                  vault.current()?.deviceID == session.deviceID else { throw CancellationError() }
            guard sealed.count == bytes + 16 else { throw BriefAudioError.unreadable }
            let data = try BriefAudioCrypto.open(sealed, attachment: attachment, hub: session.hubID)
            guard data.count == bytes else { throw BriefAudioError.unreadable }
            return data
        }, authorizationReceipt: {
            guard let paired = vault.current() else { return nil }
            return BriefAudioReceipt(deviceID: paired.deviceID, pairingID: paired.secretID,
                                     sequence: paired.counters.lastAcceptedBrief)
        }, resolveAuthorizationReceipt: { [weak brief = briefStore] selected in
            await brief?.receiptForAudioAuthorization(for: selected)
        })
        self.audioPlayer = audioPlayer
        self.today = TodayStore(hub: todayHub ?? hub,
                                cache: TodayDiskCache(fileURL: todayURL ?? (directory == PairingVault.defaultDirectory
                                    ? TodayDiskCache.defaultURL : directory.appendingPathComponent("today.json"))),
                                brief: briefStore, journal: journal,
                                identity: {
                                    guard let paired = vault.current() else { return nil }
                                    return TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
                                },
                                presence: presence,
                                connectionPresence: {
                                    let session = try vault.session()
                                    return try await RelayClient.shared.presence(session.credentials)
                                },
                                prefetchAudio: { [weak audioPlayer, weak briefStore] brief, hubID in
                                    // Every brief of the last 7 days keeps its audio, the newest first.
                                    audioPlayer?.fetchRecentAudio([brief] + (briefStore?.cachedBriefs ?? []), hubID: hubID)
                                })
        self.actions = ActionStore(hub: actionHub ?? hub, journal: journal, guardian: guardian, isPaired: { pairing.isPaired },
                                   deviceID: { vault.current()?.deviceID },
                                   proactiveHub: proactiveHub)
        self.chat = ChatStore(hub: chatHub ?? hub, journal: journal, guardian: guardian, presence: presence,
                              isPaired: { pairing.isPaired },
                              cache: ChatCache(directory: chatDirectory, removeItem: removeLocalItem),
                              isRemote: { vault.isRemote })
        // Messages that waited for the computer go as soon as the phone hears from it again.
        // The Feed's file sits beside the write journal, so a test that isolates the journal isolates it too.
        self.feed = FeedStore(hub: feedHub ?? hub, journal: journal, guardian: LocalDecisionGuard(), isPaired: { pairing.isPaired },
                              deviceID: { vault.current()?.deviceID },
                              fileURL: feedURL ?? journalURL.deletingLastPathComponent()
                                  .appendingPathComponent(FeedStore.defaultFileURL.lastPathComponent))
        self.goals = GoalStore(hub: hub, journal: journal, isPaired: { pairing.isPaired },
                               fileURL: journalURL.deletingLastPathComponent().appendingPathComponent("goals.json"))
        self.ideas = IdeasStore(hub: ideasHub ?? hub, journal: journal,
                               isPaired: { pairing.isPaired },
                               fileURL: journalURL.deletingLastPathComponent().appendingPathComponent("ideas.json"),
                               canRead: { pairing.paired?.scopes.contains("offers:read") == true },
                               canDecide: { pairing.paired?.scopes.contains("offers:decide") == true },
                               hubID: { vault.current()?.hubID },
                               guardian: guardian)
        self.tasks = TasksStore(hub: tasksHub ?? hub, isPaired: { pairing.isPaired },
                                canRead: { pairing.paired?.scopes.contains("brief:read") == true },
                                canStop: { pairing.paired?.scopes.contains("handoff:create") == true },
                                guardian: guardian,
                                hubID: { vault.current()?.hubID },
                                journal: journal,
                                deviceID: { vault.current()?.deviceID },
                                computerOnline: {
                                    #if DEBUG && targetEnvironment(simulator)
                                    if ProcessInfo.processInfo.environment["YANSU_UITEST_DRAFT_OFFLINE"] == "1" { return false }
                                    #endif
                                    return briefStore.hubOnline != false
                                },
                                fileURL: journalURL.deletingLastPathComponent().appendingPathComponent("tasks.json"))
        self.holds = HoldsStore(hub: holdsHub ?? hub, journal: journal, pairingID: {
            guard let device = vault.current() else { return nil }
            return "\(device.hubID)/\(device.deviceID)"
        })
        let heardChat = self.chat
        let heardTasks = self.tasks
        self.holds.onChange = { [weak heardTasks] in
            guard let heardTasks else { return }
            await heardTasks.refresh()
            if let id = heardTasks.detail?.id { _ = await heardTasks.loadDetail(id: id) }
        }
        let heardWeekDeclines = self.weekDeclines
        Task {
            await heardTasks.loadPendingDraftSends()
            await heardTasks.retryDraftSends()
            await hub.onHeardFromComputer {
                Task { @MainActor in
                    heardChat.heardFromComputer()
                    await heardTasks.retryDraftSends(computerResponded: true)
                    await heardWeekDeclines.retryPending()
                }
            }
        }
        self.topics = TopicStore(hub: topicHub ?? hub, journal: journal, isPaired: { pairing.isPaired },
                                 scopes: { pairing.paired?.scopes ?? [] },
                                 computerOnline: { [weak briefStore] in briefStore?.hubOnline })
        self.receipts = NoticeReceipts(relay: RelayClient(requestTimeout: 10, resourceTimeout: 15), secrets: secrets)
        self.push = push
        self.notifications = NotificationSettingsStore(hub: notificationHub ?? hub, isPaired: { pairing.isPaired },
                                                       fileURL: notificationSettingsURL ?? journalURL.deletingLastPathComponent()
                                                           .appendingPathComponent("notification-settings.json"),
                                                       pairingID: { pairing.paired.map { "\($0.hubID)/\($0.deviceID)" } },
                                                       hubID: { pairing.paired?.hubID })
        self.keepAwake = KeepAwakeStore(hub: keepAwakeHub ?? hub, journal: journal, identity: {
            guard let paired = vault.current() else { return nil }
            return TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
        }, defaults: pushDefaults)
        let noticeRoot = directory == PairingVault.defaultDirectory
            ? (AppGroup.containerURL ?? directory).appendingPathComponent("Isoform", isDirectory: true)
            : directory
        self.localTodayNotices = LocalTodayNotices(fileURL: noticeRoot.appendingPathComponent("local-notices.json"))
        let widget = WidgetSnapshotPublisher(store: widgetStore, removeItem: removeLocalItem)
        self.widget = widget
        self.brief.onDeleteCachedBriefs = { [weak self, weak audioPlayer] in
            guard audioPlayer?.clearCache() ?? true else { return false }
            Task { await LocalBriefNotices.cancelPending() }
            return self?.today.clearCachedSnapshot() ?? true
        }
        self.brief.onFirstBrief = { [weak push] in
            Task { @MainActor in
                await push?.offerAfterFirstBrief()
            }
        }
        self.brief.onFreshRequest = { [weak self] result in
            self?.today.noteBriefRequest(result)
        }
        self.today.onSnapshot = { [weak self] snapshot, previous in
            self?.keepAwake.accept(snapshot)
            if self?.refreshesTasks == true && (snapshot.needsYou != previous?.needsYou || snapshot.tasksVersion != previous?.tasksVersion) {
                Task { await self?.tasks.refresh() }
            }
            if let self, let paired = self.vault.current() {
                let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
                LocalBriefNotices.observe(snapshot, previous: previous, pairing: identity,
                    defaults: self.localNotificationDefaults)
                if !self.acceptingBackgroundToday {
                    if UIApplication.shared.applicationState == .active {
                        self.localTodayNotices.observeForeground(snapshot, pairing: identity)
                    } else {
                        _ = self.localTodayNotices.record(current: snapshot, previous: previous, pairing: identity,
                            suppress: self.keepAwake.instantPush || !self.push.isOptedIn)
                    }
                }
            }
            BackgroundTodayRefresh.schedule(self?.keepAwake.briefSchedule ?? [], paired: self?.pairing.isPaired == true)
            Task { [weak self] in
                guard let self else { return }
                guard self.today.snapshot == snapshot else { return }
                if let paired = self.vault.current() {
                    let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
                    await LocalBriefNotices.reconcile(currentID: self.brief.current?.identifier,
                        isCurrent: { self.today.snapshot == snapshot && self.vault.current()?.deviceID == identity.deviceID && self.vault.current()?.hubID == identity.hubID })
                }
                guard self.today.snapshot == snapshot else { return }
                await self.actions.reconcileToday(snapshot, isCurrent: { self.today.snapshot == snapshot })
                guard self.today.snapshot == snapshot else { return }
                await self.actions.retryToday()
                await self.tasks.retryDraftSends()
                guard self.today.snapshot == snapshot else { return }
                await self.feed.reconcileToday(snapshot, isCurrent: { self.today.snapshot == snapshot })
                guard self.today.snapshot == snapshot else { return }
                await self.feed.retryToday()
                guard self.today.snapshot == snapshot else { return }
                await self.notifications.replayPending(includeDeferred: true)
                await self.keepAwake.replay()
                guard self.today.snapshot == snapshot else { return }
                await self.today.loadPendingFresh()
                await self.today.retryFresh()
                await self.scheduleLocalBrief(snapshot)
            }
        }
        Task { [weak self] in await self?.actions.loadPendingToday() }
        Task { [weak self] in await self?.feed.loadPendingToday() }
        Task { [weak self] in await self?.today.loadPendingFresh() }
        keepAwake.$instantPush
            .removeDuplicates()
            .sink { [weak self, weak push] instant in
                push?.setLocalDelivery(!instant)
                if instant {
                    self?.discardPendingLocalBrief()
                    Task { await LocalBriefNotices.cancelPending() }
                    Task { await LocalTodayNotices.cancelPending() }
                }
            }
            .store(in: &subscriptions)
        push.$state
            .sink { [weak self] state in
                if state == .off || state == .denied {
                    self?.discardPendingLocalBrief()
                    Task { await LocalBriefNotices.cancelPending() }
                    Task { await LocalTodayNotices.cancelPending() }
                }
                if state == .on, let self, let snapshot = self.today.snapshot {
                    Task { await self.scheduleLocalBrief(snapshot) }
                }
            }
            .store(in: &subscriptions)
        keepAwake.$capabilitiesKnown
            .sink { [weak self] known in
                if known, let self, let snapshot = self.today.snapshot {
                    Task { await self.scheduleLocalBrief(snapshot) }
                }
            }
            .store(in: &subscriptions)
        keepAwake.$briefSchedule
            .sink { [weak self] schedule in
                BackgroundTodayRefresh.schedule(schedule, paired: self?.pairing.isPaired == true)
            }
            .store(in: &subscriptions)
        notifications.$settings
            .sink { [weak self] settings in
                if let settings, !settings.kinds.contains(where: { $0.kind == "brief_ready" && $0.effectivelyEnabled }) {
                    Task { await LocalBriefNotices.cancelPending() }
                }
                if let self, let snapshot = self.today.snapshot {
                    Task { await self.scheduleLocalBrief(snapshot) }
                }
            }
            .store(in: &subscriptions)
        // The "Your brief is ready" notice waits for the audio of the current brief (design section 6).
        self.audioPlayer.onAudioStored = { [weak self] _, _ in
            guard let self, let snapshot = self.today.snapshot else { return }
            Task { await self.scheduleLocalBrief(snapshot) }
        }
        self.brief.onBriefReceived = { [weak self, weak audioPlayer, weak today = self.today] brief, hubID, fromCache, receipt in
            audioPlayer?.receive(brief: brief, hubID: hubID, fromCache: fromCache, receipt: receipt)
            guard !fromCache else { return }
            // A brief saved to the library fetches its audio, with the other briefs of the last 7 days that lack it, and
            // the local notice can now name the current brief.
            Task { @MainActor [weak self, weak audioPlayer, weak today] in
                audioPlayer?.fetchRecentAudio([brief] + (self?.brief.cachedBriefs ?? []), hubID: hubID)
                if let snapshot = today?.snapshot, let self {
                    await self.scheduleLocalBrief(snapshot)
                }
            }
        }
        Publishers.CombineLatest4(today.$snapshot, brief.$current, tasks.$page, audioPlayer.$state)
            .sink { [weak self] _, _, _, _ in
                Task { @MainActor [weak self] in
                    self?.refreshWidget()
                    self?.finishPendingBriefPlayIfPlaying()
                }
            }
            .store(in: &subscriptions)
        brief.$hubOnline
            .removeDuplicates()
            .sink { [weak self] online in
                guard online == true else { return }
                Task { @MainActor [weak self] in await self?.replayBriefPlay() }
            }
            .store(in: &subscriptions)
        today.$hubUnreachable
            .removeDuplicates()
            .dropFirst()
            .sink { [weak self] unreachable in
                guard !unreachable else { return }
                Task { @MainActor [weak self] in await self?.replayBriefPlay() }
            }
            .store(in: &subscriptions)
        today.$connectionCause
            .removeDuplicates()
            .sink { [weak self] cause in
                guard let self, !Self.seedsAwayCause else { return }
                self.chat.noteConnection(cause: cause, reason: self.today.connectionReason,
                                         since: self.today.connectionSince)
            }
            .store(in: &subscriptions)
        today.$connectionCause
            .removeDuplicates()
            .dropFirst()
            .sink { [weak self] cause in
                guard cause == nil else { return }
                Task { @MainActor [weak self] in await self?.replayBriefPlay() }
            }
            .store(in: &subscriptions)
        pairing.onPairingCommitted = { [weak self] in
            self?.audioPlayer.stop()
            if self?.vault.current()?.scopes.contains("brief:read") != true,
               self?.audioPlayer.clearCache() == false {
                self?.brief.notice = String(localized: "Isoform could not delete the cached audio. Unlock this phone and try again.")
            }
            self?.synchronizeLocalHubState()
            self?.chat.pairingCommitted()
            self?.ideas.pairingCommitted()
            self?.tasks.pairingCommitted()
            self?.today.loadCached()
            if let paired = self?.vault.current() {
                let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
                TodayBriefPlaceholder.reset(hubID: paired.hubID)
                self?.tasks.beginFirstCopy(identity: identity)
            }
            self?.today.startListening()
            Task { await self?.today.refresh() }
        }
        pairing.canRevokePending = { [weak push] in push?.isReadyForRevocation ?? false }
        push.onRegistrationReady = { [weak pairing] in await pairing?.retryPendingRevocations() }
        // Legacy briefs have no hub identity. Move them only before the first hub check, so that
        // check can clear them; importing them after its marker is saved could restore another Mac's cache.
        if vault.loadError == nil, let legacyBriefDirectory, !FileManager.default.fileExists(atPath: hubStateURL.path) {
            BriefCacheMigration.moveLegacyFiles(from: legacyBriefDirectory, to: briefDirectory)
        }
        synchronizeLocalHubState()
    }

    nonisolated private static func hubOnline(vault: PairingVault) async throws -> Bool {
        do {
            let session = try vault.session()
            return try await RelayClient.shared.presence(session.credentials).hubOnline
        } catch {
            if let status = RemoteStatus.offline(for: error, remote: vault.isRemote) { return status }
            throw error
        }
    }

    func refreshWidget() {
        // Pairing cleanup clears all three sources. Do not recreate a snapshot
        // from queued observer callbacks after the previous pairing is removed.
        guard let paired = pairing.paired, let committed = vault.current(),
              paired.hubID == committed.hubID, paired.deviceID == committed.deviceID,
              today.snapshot != nil || brief.current != nil || tasks.page != nil else { return }
        widget.updateToday(today.snapshot, brief: brief.current, tasks: tasks.page,
                           privateContent: UserDefaults.standard.bool(forKey: "faceIDToOpen"),
                           isPlaying: audioPlayer.state == .playing)
    }

    func launch() {
        KeychainMigration.runIfNeeded()
        LegacyPurge.runIfNeeded(LegacyPurge.appTargets)
        vault.reconcileNoticeSecrets()
        brief.loadCached()
        today.loadCached()
        if let paired = vault.current() {
            let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
            pendingBriefPlay = briefPlayQueue.load(pairing: identity)?.briefID
        }
        if brief.hubOnline == true { Task { await replayBriefPlay() } }
        if let paired = vault.current() {
            tasks.restoreFirstCopy(identity: TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID))
        }
        audioPlayer.restoreRecentSession(hubID: vault.current()?.hubID) { brief.cachedBrief(withID: $0) }
        if let snapshot = today.snapshot { keepAwake.accept(snapshot) }
        BackgroundTodayRefresh.schedule(keepAwake.briefSchedule, paired: pairing.isPaired)
        Task {
            await push.resumeIfOptedIn()
            await notifications.refresh()
            await keepAwake.refresh()
            let queuedKeys = await chat.queuedMessageKeys()
            await actions.pruneJournal(keepingKeys: queuedKeys)
            if push.isReadyForRevocation { await pairing.retryPendingRevocations() }
        }
        chat.resumeQueuedMessages()
        Task { await weekDeclines.retryPending() }
    }

    func backgroundTodayRefresh() async -> Bool {
        guard let paired = vault.current() else { return false }
        let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
        do {
            let status = try await hub.perform(.status, timeout: 10).payload(HubStatus.self)
            guard vault.current()?.deviceID == identity.deviceID, vault.current()?.hubID == identity.hubID else { return false }
            keepAwake.accept(status)
            let previous = today.snapshot
            let snapshot = try await hub.today(timeout: 10)
            try Task.checkCancellation()
            guard vault.current()?.deviceID == identity.deviceID, vault.current()?.hubID == identity.hubID else { return false }
            acceptingBackgroundToday = true
            today.accept(snapshot)
            acceptingBackgroundToday = false
            guard await today.persistForNotification(snapshot, identity: identity) else { return false }
            let suppress = keepAwake.instantPush || !push.isOptedIn
            guard localTodayNotices.record(current: snapshot, previous: previous, pairing: identity, suppress: suppress) else { return false }
            if suppress { return !Task.isCancelled }
            let preferenceRevision = notifications.localNoticeRevision
            let fetched = try await hub.perform(.notifications, timeout: 10).payload(NotificationSettings.self)
            guard let preferences = notifications.applyingPending(to: fetched, revision: preferenceRevision) else { return false }
            guard !Task.isCancelled, today.snapshot == snapshot else { return false }
            await scheduleLocalBrief(snapshot, preferences: preferences, revision: preferenceRevision)
            let pending = await journal.entries()
            let handled = LocalTodayNotices.pendingActionNoticeIDs(snapshot.needsYou, writes: pending, pairing: identity)
            await localTodayNotices.process(current: snapshot, previous: previous, pairing: identity,
                settings: preferences, instantPush: false, excludedIDs: handled,
                isCurrent: { [weak self] in
                    guard let self, let paired = self.vault.current() else { return false }
                    return paired.hubID == identity.hubID && paired.deviceID == identity.deviceID &&
                        self.today.snapshot == snapshot && !self.keepAwake.instantPush && self.push.isOptedIn &&
                        self.notifications.localNoticeRevision == preferenceRevision
                })
            return !Task.isCancelled
        } catch { return false }
    }

    private func scheduleLocalBrief(_ snapshot: MobileTodaySnapshot, preferences: NotificationSettings? = nil,
                                    revision: UUID? = nil) async {
        guard keepAwake.capabilitiesKnown, !keepAwake.instantPush, push.isOptedIn,
              let paired = vault.current(), let current = brief.current else { return }
        let fetched: NotificationSettings
        let preferenceRevision = revision ?? notifications.localNoticeRevision
        if let preferences {
            fetched = preferences
        } else {
            guard let latest = try? await hub.perform(.notifications, timeout: 10).payload(NotificationSettings.self) else { return }
            fetched = latest
        }
        guard let settings = notifications.applyingPending(to: fetched, revision: preferenceRevision), settings.available else { return }
        let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
        // The notice is for the phone's current brief, and only once that brief is pending (a new one the Mac named).
        guard LocalBriefNotices.isPending(current.identifier, pairing: identity, defaults: localNotificationDefaults) else { return }
        guard brief.cachedBodyIsCurrent(current.identifier) else { return }
        guard !Task.isCancelled, today.snapshot == snapshot, brief.cachedBodyIsCurrent(current.identifier),
              vault.current()?.hubID == identity.hubID, vault.current()?.deviceID == identity.deviceID,
              !keepAwake.instantPush, push.isOptedIn else { return }
        let hasAudio = audioPlayer.hasAudioOnPhone(brief: current, hubID: identity.hubID)
        await LocalBriefNotices.schedule(current, hasAudio: hasAudio, pairing: identity, settings: settings,
            defaults: localNotificationDefaults,
            isCurrent: { [weak self] in
                guard let self, let paired = self.vault.current() else { return false }
                return paired.hubID == identity.hubID && paired.deviceID == identity.deviceID &&
                    self.today.snapshot == snapshot && self.brief.cachedBodyIsCurrent(current.identifier) &&
                    !self.keepAwake.instantPush && self.push.isOptedIn &&
                    self.notifications.localNoticeRevision == preferenceRevision
            })
    }

    /// Read it or Play from the brief ready notice: opens the brief view, and Play also starts the brief from this phone's copy.
    func openBriefFromNotice(_ briefID: String, play: Bool) async {
        router.openBrief(briefID, landsOnSpoken: play)
        guard play, let paired = vault.current() else { return }
        let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
        let entry: MobileBriefV1
        do {
            entry = try await brief.fetchOpenedBriefForPlay(briefID)
        } catch {
            guard vault.current()?.deviceID == identity.deviceID, vault.current()?.hubID == identity.hubID else { return }
            if BriefPlayQueue.shouldQueue(after: error) { queueBriefPlay(briefID, identity: identity) }
            return
        }
        guard vault.current()?.deviceID == identity.deviceID, vault.current()?.hubID == identity.hubID else { return }
        await audioPlayer.toggle(brief: entry, hubID: identity.hubID)
        guard vault.current()?.deviceID == identity.deviceID, vault.current()?.hubID == identity.hubID else { return }
        if audioPlayer.state == .unavailable(.computerOffline) { queueBriefPlay(briefID, identity: identity) }
    }

    private func queueBriefPlay(_ id: String, identity: TodayPairingIdentity) {
        do {
            try briefPlayQueue.save(.init(hubID: identity.hubID, deviceID: identity.deviceID, briefID: id))
            pendingBriefPlay = id
            briefPlayNotice = nil
        } catch {
            briefPlayNotice = String(localized: "connection.playNotSaved")
        }
    }

    private func replayBriefPlay() async {
        guard !replayingBriefPlay, let paired = vault.current() else { return }
        let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
        guard let request = briefPlayQueue.load(pairing: identity) else { pendingBriefPlay = nil; return }
        if audioPlayer.state == .playing, audioPlayer.brief?.identifier == request.briefID,
           audioPlayer.hubID == identity.hubID {
            finishPendingBriefPlayIfPlaying()
            return
        }
        replayingBriefPlay = true
        defer { replayingBriefPlay = false }
        guard let entry = await brief.fetchOpenedBrief(request.briefID), vault.current()?.hubID == identity.hubID,
              vault.current()?.deviceID == identity.deviceID else { return }
        await audioPlayer.toggle(brief: entry, hubID: identity.hubID)
        guard audioPlayer.state == .playing else { return }
        finishPendingBriefPlayIfPlaying()
    }

    private func finishPendingBriefPlayIfPlaying() {
        guard let paired = vault.current(), let pendingBriefPlay, audioPlayer.state == .playing,
              audioPlayer.hubID == paired.hubID,
              audioPlayer.brief?.identifier == pendingBriefPlay else { return }
        do {
            try briefPlayQueue.clear()
            self.pendingBriefPlay = nil
        } catch {
            briefPlayNotice = String(localized: "connection.playNotSaved")
        }
    }

    private func discardPendingLocalBrief() {
        guard let paired = vault.current() else { return }
        LocalBriefNotices.discardPending(pairing: TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID),
                                         defaults: localNotificationDefaults)
    }

    func unpair() async throws {
        try await pairing.unpair()
        localHubCleanupPending = true
        synchronizeLocalHubState()
    }

    func forgetPairingLocally() throws {
        try pairing.forgetLocally()
        localHubCleanupPending = true
        synchronizeLocalHubState()
    }

    func retrySavedPairing() {
        pairing.retrySavedPairing()
        guard vault.loadError == nil else { return }
        vault.reconcileNoticeSecrets()
        synchronizeLocalHubState()
        brief.dismissNotice()
        brief.loadCached()
        Task {
            await push.resumeIfOptedIn()
            await notifications.refresh()
            if push.isReadyForRevocation { await pairing.retryPendingRevocations() }
        }
    }

    // Mark cleanup pending before clearing, so a failed clear followed by another pairing cannot
    // make partially replaced caches look scoped to the old Mac. Unscoped caches are cleared once.
    private func synchronizeLocalHubState() {
        let hubID = vault.current()?.hubID
        guard vault.loadError == nil else { return }
        let saved = (try? Data(contentsOf: hubStateURL)).flatMap {
            try? JSONDecoder().decode(LocalHubState.self, from: $0)
        }
        guard localHubCleanupPending || saved == nil || saved?.hubID != hubID || saved?.needsCleanup == true else {
            return
        }
        localHubCleanupPending = true
        try? writeLocalHubState(LocalHubState(hubID: saved?.hubID, needsCleanup: true))
        guard clearLocalState() else { return }
        do {
            try writeLocalHubState(LocalHubState(hubID: hubID, needsCleanup: false))
            localHubCleanupPending = false
        } catch {
            // Keep cleanup pending so the next pairing or launch checks again before reading caches.
        }
    }

    private func writeLocalHubState(_ state: LocalHubState) throws {
        try FileManager.default.createDirectory(at: hubStateURL.deletingLastPathComponent(),
                                                withIntermediateDirectories: true)
        try JSONEncoder().encode(state).write(
            to: hubStateURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    @discardableResult
    private func clearLocalState() -> Bool {
        Task { await LocalBriefNotices.cancelPending() }
        Task { await LocalTodayNotices.cancelPending() }
        localTodayNotices.clear()
        let audioCleared = audioPlayer.resetForPairing(hubID: vault.current()?.hubID)
        let briefCleared = brief.clearAfterUnpair()
        let chatCleared = chat.clearAfterUnpair()
        let notificationsCleared = notifications.clearAfterUnpair()
        keepAwake.clear()
        let todayCleared = today.clearAfterPairingChange()
        actions.clearAfterUnpair()
        feed.clearAfterUnpair()
        goals.clearAfterUnpair()
        let ideasCleared = ideas.clearAfterUnpair()
        let tasksCleared = tasks.clearAfterUnpair()
        let yourAgentCleared = yourAgent.clearAfterUnpair()
        topics.clearAfterUnpair()
        let widgetCleared = widget.clear()
        let playCleared: Bool
        do { try briefPlayQueue.clear(); pendingBriefPlay = nil; playCleared = true }
        catch { playCleared = false }
        return audioCleared && briefCleared && todayCleared && chatCleared && ideasCleared && tasksCleared
            && widgetCleared && notificationsCleared && yourAgentCleared && playCleared
    }
}

#if DEBUG && targetEnvironment(simulator)
/// The owner check of a seeded UI test run that declines, as when the owner cancels Face ID.
private struct DecliningOwnerCheck: DecisionGuarding {
    func authorize(reason: String) async throws {
        throw DecisionGuardError.notConfirmed
    }
}

/// The owner check of a seeded UI test run that confirms at once, as when the owner passes Face ID.
private struct AllowingOwnerCheck: DecisionGuarding {
    func authorize(reason: String) async throws {}
}

extension AppEnvironment {
    // Settings and Feed layout tests use persisted local state in a fresh directory. No pairing credentials
    // are seeded, so the real relay clients cannot contact a hub or alter an existing pairing.
    static func settingsUITestEnvironment() -> AppEnvironment? {
        guard let state = ProcessInfo.processInfo.environment["YANSU_UITEST_SETTINGS_STATE"],
              ["first-run", "unpaired", "paired", "unreadable", "older-pairing", "push-unavailable", "push-denied", "feed", "feed-empty",
               "feed-no-history", "feed-catchup", "feed-offline", "feed-review", "feed-first-brief", "feed-fetching", "feed-fetch-failed",
               "feed-audio", "feed-audio-rendering", "feed-words", "feed-conversation", "feed-your-week", "feed-topics", "feed-topics-no-chat-read", "chat",
               "chat-not-sent", "chat-sent", "chat-away", "chat-live", "chat-live-cold", "chat-proactive", "chat-status", "chat-draft", "chat-cards", "chat-empty", "goals",
               "goals-empty", "library", "ideas", "ideas-empty", "tasks-seeded", "brain-dump", "holds-seeded", "launch-demo", "day-one"].contains(state) else { return nil }
        let motionCapture = state == "tasks-seeded"
            && ProcessInfo.processInfo.environment["YANSU_UITEST_MOTION_CAPTURE"] == "1"
        if motionCapture { UserDefaults.standard.removeObject(forKey: "tasks.deck.leanShown") }
        // Relaunch tests keep their isolated records. Accept only a UUID, never an arbitrary path or defaults suite.
        let identifier = ProcessInfo.processInfo.environment["YANSU_UITEST_SETTINGS_ID"]
            .flatMap(UUID.init(uuidString:))?.uuidString ?? UUID().uuidString
        // The first open after pairing shows the placeholder once; each launch of this state is that first open.
        if state == "feed-no-history", ProcessInfo.processInfo.environment["YANSU_UITEST_KEEP_PLACEHOLDER"] != "1" {
            TodayBriefPlaceholder.reset(hubID: "settings-ui-hub")
        }
        if ProcessInfo.processInfo.environment["YANSU_UITEST_RESET_AWAKE_OFFER"] == "1" {
            UserDefaults.standard.removeObject(forKey: "today.keepAwakeOfferDismissed")
        }
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("isoform-settings-ui-tests", isDirectory: true)
            .appendingPathComponent(identifier, isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let pairingURL = directory.appendingPathComponent("pairing.json")
            let seedMarker = directory.appendingPathComponent("fixture-seeded")
            let isFirstLaunch = !FileManager.default.fileExists(atPath: seedMarker.path)
            // The Agent of You states keep their pairing in memory: the Keychain is unavailable to them here.
            let secrets: SecretStore = ProcessInfo.processInfo.environment["YANSU_UITEST_YOUR_AGENT_STATE"] != nil
                ? YourAgentUITestSecrets()
                : KeychainSecretStore(service: "com.yansu.settings-ui-tests.\(identifier)", accessGroup: nil)
            switch isFirstLaunch ? state : "" {
            case "unreadable":
                try Data("unreadable saved pairing".utf8).write(to: pairingURL)
            case "chat", "chat-not-sent", "chat-sent", "chat-away", "chat-live", "chat-live-cold", "chat-proactive", "chat-status", "chat-draft", "chat-cards", "chat-empty", "goals", "goals-empty", "ideas", "ideas-empty", "feed-conversation", "feed-your-week",
                 "feed-topics", "feed-topics-no-chat-read", "launch-demo", "day-one":
                // Real session keys for a relay that does not exist, so a message or a goal's check-off goes out the
                // way it does on a phone whose Mac cannot be reached, and waits. The chat-live states answer the chat
                // routes in-process instead (ChatUITestHub).
                let hubKeys = RelayCrypto.makeDeviceKeyPair()
                let device = RelayCrypto.makeDeviceKeyPair()
                let paired = PairedDevice(
                    hubID: "settings-ui-hub", hubName: "Settings Test Mac",
                    hubPublicKey: RelayCrypto.base64URLEncode(hubKeys.publicKey),
                    relayOrigin: "https://settings-ui.invalid", deviceID: "settings-ui-phone",
                    deviceName: "Settings Test iPhone",
                    // A phone paired before it could read the chat lists topics but cannot open one.
                    scopes: state == "feed-topics-no-chat-read"
                        ? PairingStore.requestedScopes.filter { $0 != "chat:read" } : PairingStore.requestedScopes,
                    pairedAt: Date(timeIntervalSince1970: 1_790_000_000),
                    expiresAt: Date(timeIntervalSince1970: 1_900_000_000), counters: SequenceCounters())
                try PairingVault(directory: directory, secrets: secrets)
                    .store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                           bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
            case "paired" where ProcessInfo.processInfo.environment["YANSU_UITEST_YOUR_AGENT_STATE"] != nil:
                // The Agent of You states read their hub from the vault, so their pairing is stored the way the chat
                // states store theirs. Other paired states keep the plain record below.
                let hubKeys = RelayCrypto.makeDeviceKeyPair()
                let device = RelayCrypto.makeDeviceKeyPair()
                let paired = PairedDevice(
                    hubID: "settings-ui-hub", hubName: "Settings Test Mac",
                    hubPublicKey: RelayCrypto.base64URLEncode(hubKeys.publicKey),
                    relayOrigin: "https://settings-ui.invalid", deviceID: "settings-ui-phone",
                    deviceName: "Settings Test iPhone",
                    scopes: PairingStore.requestedScopes,
                    pairedAt: Date(timeIntervalSince1970: 1_790_000_000),
                    expiresAt: Date(timeIntervalSince1970: 1_900_000_000), counters: SequenceCounters())
                try PairingVault(directory: directory, secrets: secrets)
                    .store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                           bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
            case "paired", "older-pairing", "push-unavailable", "push-denied", "feed", "feed-empty", "feed-no-history", "feed-first-brief", "feed-fetching", "feed-fetch-failed",
                 "feed-audio", "feed-audio-rendering", "feed-words", "feed-catchup", "feed-offline", "feed-review", "library", "tasks-seeded", "brain-dump", "holds-seeded":
                let paired = PairedDevice(
                    hubID: "settings-ui-hub", hubName: "Settings Test Mac", hubPublicKey: "",
                    relayOrigin: "https://settings-ui.invalid", deviceID: "settings-ui-phone",
                    deviceName: "Settings Test iPhone",
                    scopes: state == "older-pairing" ? ["brief:read", "push"] : PairingStore.requestedScopes,
                    pairedAt: Date(timeIntervalSince1970: 1_790_000_000),
                    expiresAt: Date(timeIntervalSince1970: 1_900_000_000), counters: SequenceCounters(),
                    previousDeviceRemovalNeeded: state == "older-pairing")
                let encoder = JSONEncoder()
                encoder.dateEncodingStrategy = .iso8601
                try encoder.encode(paired).write(to: pairingURL)
            default:
                break
            }
            let defaults = UserDefaults(suiteName: "com.yansu.settings-ui-tests.\(identifier)")!
            if isFirstLaunch && state == "push-unavailable" {
                defaults.set(true, forKey: PushRegistration.optInKey)
            }
            let makePush: ((PairingVault, UserDefaults) -> PushRegistration)?
            if ["push-unavailable", "push-denied"].contains(state) {
                // Exercise the production failure and restore paths without changing system permission or
                // contacting APNs. These states contain no relay credentials or notification token.
                makePush = { vault, defaults in
                    PushRegistration(vault: vault, defaults: defaults,
                                     authorize: { state != "push-denied" },
                                     authorizationStatus: { state == "push-denied" ? .denied : .authorized },
                                     registerRemoteNotifications: {}, unregisterRemoteNotifications: {})
                }
            } else {
                makePush = nil
            }
            // A seeded run can answer the owner check without the system prompt: decline it, to show what a declined
            // check says, or allow it, to show what happens after.
            let guardian: DecisionGuarding
            switch ProcessInfo.processInfo.environment["YANSU_UITEST_OWNER_CHECK"] {
            case "decline": guardian = DecliningOwnerCheck()
            case "allow": guardian = AllowingOwnerCheck()
            default: guardian = DecisionGuard()
            }
            // Keep presentation fixtures identical in cache and on refresh, without an offline banner obscuring
            // the card. These canned responses exercise the view and journal, not the live relay integration.
            let presentationItem = state == "chat-draft"
                ? try ChatUITestSeed.draftItem(now: Date())
                : ["chat-proactive", "chat-status"].contains(state)
                    ? try ChatUITestSeed.presentationItem(status: state == "chat-status", now: Date()) : nil
            let presentationItems = state == "day-one" ? [try ChatUITestSeed.firstReadItem(now: Date())]
                : state == "chat-cards" ? try ChatUITestSeed.proactiveCards(now: Date())
                : state == "chat-empty" ? [ChatItemView]() : presentationItem.map { [$0] }
            let dayOneHub = state == "day-one" ? ChatUITestHub(now: Date(), main: presentationItems) : nil
            // The Needs you frames with a queued card read the feed's needs, where the card is (FeedTasksUITestHub).
            let feedNeedsCard = state == "feed-review" && ProcessInfo.processInfo.environment["YANSU_UITEST_TODAY_WAITS"] == "1"
            let tasksTestHub = ["tasks-seeded", "day-one", "feed-review", "brain-dump"].contains(state) && !feedNeedsCard
                ? TasksUITestHub(brainDump: state == "brain-dump") : nil
            let holdsTestHub = state == "holds-seeded" ? HoldsUITestHub() : nil
            // The launch demo's morning (LaunchDemoUITestSeed): its brief, Today, chat and Priya's Send.
            let demoBrief = state == "launch-demo" ? LaunchDemoUITestSeed.brief() : nil
            let demoHub = state == "launch-demo" ? LaunchDemoHub() : nil
            let todayFixture = demoBrief != nil ? try LaunchDemoUITestSeed.today(duration: BriefAudioUITestSeed.duration)
                : try FeedUITestSeed.snapshot(state: state, now: Date())
            // The words brief's audio is still on the computer: the download at arrival failed, and the one a tap
            // starts succeeds (the Today card's Play must not wait for the first one).
            let audioOnComputer = state == "feed-words"
                && ProcessInfo.processInfo.environment["YANSU_UITEST_AUDIO_ON_COMPUTER"] == "1"
            let computerAudio = BriefAudioUITestSeed.OnComputer()
            let blockedDirectory = directory.appendingPathComponent("blocked-file")
            let failQuietSave = ProcessInfo.processInfo.environment["YANSU_UITEST_FAIL_QUIET_SAVE"] == "1"
            let failAwakeSave = ProcessInfo.processInfo.environment["YANSU_UITEST_FAIL_AWAKE_SAVE"] == "1"
            let awayPhase = ProcessInfo.processInfo.environment["YANSU_UITEST_AWAY_PHASE"] ?? "waiting"
            let askPresence: (@Sendable () async throws -> Bool)? = state == "feed-conversation"
                ? { ProcessInfo.processInfo.environment["YANSU_UITEST_ASK_OFFLINE"] != "1" }
                : state == "chat-away" ? { awayPhase == "back" } : nil
            // "Talk about this brief" frames: offline means the computer is already known to be away.
            let talkPresence: (@Sendable () async throws -> Bool)? =
                ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_TALK"] == "offline" ? { false } : nil
            if failQuietSave || failAwakeSave { try Data().write(to: blockedDirectory) }
            let failBriefDeletion = ProcessInfo.processInfo.environment["YANSU_UITEST_FAIL_BRIEF_DELETE"] == "1"
            let environment = AppEnvironment(
                directory: directory,
                secrets: secrets,
                briefDirectory: directory.appendingPathComponent("briefs", isDirectory: true),
                journalURL: (failAwakeSave ? blockedDirectory : directory).appendingPathComponent("pending-writes.json"),
                chatDirectory: directory.appendingPathComponent("chat", isDirectory: true),
                feedURL: directory.appendingPathComponent("feed.json"),
                audioRequest: audioOnComputer ? { brief in
                    guard let audio = brief.audio else { throw BriefAudioError.notAvailable }
                    return BriefAudioResource(briefId: brief.identifier, version: BriefAudioResource.versionKey(for: brief),
                                              audio: audio)
                } : state.hasPrefix("feed-audio") || state == "feed-words" ? { _ in throw BriefAudioError.rendering } : nil,
                audioDownload: audioOnComputer ? { _ in try computerAudio.download() } : nil,
                widgetStore: WidgetSnapshotStore(fileURL: directory.appendingPathComponent("widget.json")),
                pushDefaults: defaults,
                makePush: makePush,
                guardian: guardian,
                chatHub: demoHub.map { $0 as ChatHubAPI } ?? dayOneHub.map { $0 as ChatHubAPI } ?? (state.hasPrefix("chat-live") || state.hasPrefix("ideas") || state == "feed-conversation"
                    || state == "feed-your-week"
                    || ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_TALK"] != nil
                    || state == "chat-cards" || state.hasPrefix("feed-topics") || presentationItems != nil
                    ? ChatUITestHub(now: Date(), main: presentationItems) : nil),
                proactiveHub: demoHub.map { LaunchDemoProactiveHub(hub: $0) as ProactiveActionHubAPI }
                    ?? (["chat-proactive", "chat-draft", "chat-cards"].contains(state) ? ChatUITestProactiveHub(
                    refusesAction: ProcessInfo.processInfo.environment["YANSU_UITEST_PROACTIVE_ACTION"] == "refuse")
                    : state.hasPrefix("feed-topics") ? TopicUITestProactiveHub() : nil),
                ideasHub: holdsTestHub.map { $0 as IdeasHubAPI } ?? (state.hasPrefix("ideas") || ["tasks-seeded", "day-one"].contains(state)
                    ? IdeasUITestHub(empty: state == "ideas-empty", motionCapture: motionCapture || state == "day-one") : nil),
                actionHub: tasksTestHub,
                feedHub: state == "feed" ? FeedFeedbackUITestHub() : nil,
                tasksHub: holdsTestHub.map { $0 as TasksHubAPI } ?? tasksTestHub.map { $0 as TasksHubAPI } ?? (state.hasPrefix("feed") ? FeedTasksUITestHub(snapshot: todayFixture) : nil),
                holdsHub: holdsTestHub,
                topicHub: demoHub.map { $0 as TopicHubAPI } ?? (state.hasPrefix("feed-topics") || state == "tasks-seeded" || state == "feed-review"
                    ? TopicUITestHub(now: Date()) : nil),
                todayHub: state.hasPrefix("feed") || demoBrief != nil ? TodayUITestHub(snapshot: todayFixture, offline: state == "feed-offline" || FeedUITestSeed.moment == "waiting"
                    || ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_TALK"] == "offline") : nil,
                weekDeclineHub: state == "feed-your-week" ? YourWeekDeclineUITestHub(defaults: defaults) : nil,
                keepAwakeHub: ProcessInfo.processInfo.environment["YANSU_UITEST_KEEP_AWAKE"].map {
                    KeepAwakeUITestHub(on: $0 == "on", offline: state == "feed-offline",
                                      instantPush: ProcessInfo.processInfo.environment["YANSU_UITEST_INSTANT_PUSH"] == "true")
                },
                notificationHub: state == "tasks-seeded" ? NotificationUITestHub() : nil,
                seesHub: ProcessInfo.processInfo.environment["YANSU_UITEST_SEES"] == "1" ? SeesUITestHub() : nil,
                firstReadHub: dayOneHub,
                notificationSettingsURL: failQuietSave ? blockedDirectory.appendingPathComponent("notification-settings.json") : nil,
                presenceOverride: askPresence ?? talkPresence,
                removeLocalItem: { url in
                    if failBriefDeletion && url.lastPathComponent == "briefs" { throw CocoaError(.fileWriteNoPermission) }
                    try FileManager.default.removeItem(at: url)
                })
            if let value = ProcessInfo.processInfo.environment["YANSU_UITEST_KEEP_AWAKE"] {
                environment.keepAwake.accept(HubStatus(hubName: nil, appVersion: nil, protocolVersion: nil,
                    narrativeAvailable: nil, lastBriefAt: nil, keepAwake: value == "on",
                    instantPush: ProcessInfo.processInfo.environment["YANSU_UITEST_INSTANT_PUSH"] == "true"))
            }
            if isFirstLaunch && state == "push-unavailable" {
                environment.push.receiveRegistrationFailure()
            }
            if isFirstLaunch && state == "push-denied" {
                Task { await environment.push.enable() }
            }
            if !isFirstLaunch && ["push-unavailable", "push-denied"].contains(state) {
                Task { await environment.push.resumeIfOptedIn() }
            }
            if state != "first-run" { environment.firstRun.complete() }
            environment.refreshesBriefs = false
            environment.refreshesTasks = state.hasPrefix("feed") || state == "tasks-seeded" || state == "brain-dump" || state == "holds-seeded"
            environment.refreshesTopics = state.hasPrefix("feed-topics") || state == "tasks-seeded" || demoBrief != nil
            environment.isLibraryUITest = state == "library"
            switch ProcessInfo.processInfo.environment["YANSU_UITEST_TAB"] {
            case "feed", "brief", "desk", "today": environment.router.selectedTab = .today
            case "chat", "chat-not-sent", "chat-away": environment.router.selectedTab = .chat
            case "ideas", "tasks": environment.router.selectedTab = .tasks
            case "goals": environment.router.selectedTab = .today
            default: break
            }
            if isFirstLaunch && ["chat", "chat-not-sent", "chat-sent", "chat-live"].contains(state) {
                // The environment cleared its caches for the new hub; the seed goes in after that.
                try ChatUITestSeed.seed(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                        answered: state != "chat-sent", refused: state == "chat-not-sent", now: Date())
            }
            if isFirstLaunch && state == "chat-away" {
                let cause = ProcessInfo.processInfo.environment["YANSU_UITEST_AWAY_CAUSE"].flatMap(ChatAwayCause.init(rawValue:))
                try ChatUITestSeed.seedAway(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                            cause: cause ?? .asleep, phase: awayPhase, now: Date())
                environment.chat.reloadAwayPeriodsForUITest()
            }
            // Today's line for a send the computer never answered, with that send in Chat (the feed's Today frame).
            if isFirstLaunch && state == "feed-review" && ProcessInfo.processInfo.environment["YANSU_UITEST_CHAT_EXPIRED"] == "1" {
                // One sleep time for both away lines: Chat's stretch starts there, and Today reads it as its goodbye time.
                // The test passes the goodbye time (YANSU_UITEST_GOODBYE_AT), which Today reads as well.
                let sleptAt = ProcessInfo.processInfo.environment["YANSU_UITEST_GOODBYE_AT"].flatMap(RelayTime.parse)
                    ?? Date().addingTimeInterval(-26 * 3600)
                try ChatUITestSeed.seedAway(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                            cause: .asleep, phase: "long", startedAt: sleptAt, now: Date())
                environment.chat.reloadAwayPeriodsForUITest()
            }
            if isFirstLaunch, let presentationItems {
                try ChatUITestSeed.seedPresentation(
                    ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                    items: presentationItems, now: Date())
            }
            #if DEBUG && targetEnvironment(simulator)
            // A paired Today with a drafted reply the relay held a day (the 24-hour line).
            if isFirstLaunch && state == "feed-review", ProcessInfo.processInfo.environment["YANSU_UITEST_HELD_SEND"] == "expired" {
                let person = ProcessInfo.processInfo.environment["YANSU_UITEST_HELD_PERSON"]
                Task { await environment.tasks.seedExpiredDraftForUITest(person: person) }
            }
            // A proactive card queued while the computer sleeps (the Needs-you "Waits for your computer" section).
            if isFirstLaunch && state == "feed-review", ProcessInfo.processInfo.environment["YANSU_UITEST_TODAY_WAITS"] == "1",
               let item = try ChatUITestSeed.proactiveCards(now: Date()).compactMap(\.proactive).first,
               let actionID = item.actionIDs.first {
                // Launch loads the held writes once the vault has its device; a seed that lands before that load is cleared.
                Task {
                    try? await Task.sleep(for: .seconds(2))
                    await environment.actions.seedWaitingTodayForUITest(chatID: ChatUITestSeed.mainChatID, item: item, actionID: actionID)
                }
            }
            #endif
            if isFirstLaunch && ["feed", "feed-empty", "feed-no-history", "feed-audio", "feed-audio-rendering", "feed-catchup", "feed-offline", "feed-review", "feed-fetch-failed"].contains(state) {
                if state != "feed-no-history" {
                    // The fetch-failed state keeps yesterday cached: today's brief is named but its text never arrived.
                    var seeded = FeedUITestSeed.briefs(today: state != "feed-empty" && state != "feed-fetch-failed", now: Date())
                    #if DEBUG && targetEnvironment(simulator)
                    // A brief from a computer that has answered before carries its conversation key, so Talk opens
                    // while the computer sleeps instead of asking it (PROTOCOL.md section 23).
                    if ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_TALK"] == "offline" {
                        for index in seeded.indices { seeded[index].conversationKey = "ui-conversation-key" }
                    }
                    #endif
                    if state.hasPrefix("feed-audio"), let index = seeded.indices.last {
                        seeded[index].audio = BriefAudio(mime: "audio/wav", bytes: BriefAudioUITestSeed.wav().count,
                                                         durationSeconds: BriefAudioUITestSeed.duration)
                        // Still rendering: the brief has its text, the computer says the recording is being made, and the
                        // Today card keeps Play hidden and shows "Recording on its way" (P138).
                        if state == "feed-audio-rendering" {
                            seeded[index].audio?.status = "rendering"
                            seeded[index].narrative = FeedUITestSeed.wordsParagraphs.map { BriefParagraph(text: $0) }
                        }
                    }
                    for brief in seeded { try environment.cache.save(brief) }
                }
                environment.brief.loadCached()
                if state == "feed-fetch-failed" {
                    // An evening id the snapshot does not name, so the real fetch of the morning brief cannot overwrite it.
                    environment.brief.recordFetchOutcomeForUITest("\(FeedAssembly.localDate(for: Date()))-evening", "attempt 1 failed: hub noAnswer; retrying in 5s")
                    environment.brief.logFetchOutcomes(at: "seed")
                }
                if state == "feed-audio", let brief = environment.brief.current {
                    let data = BriefAudioUITestSeed.wav()
                    let audio = BriefAudio(mime: "audio/wav", bytes: data.count,
                                           durationSeconds: BriefAudioUITestSeed.duration)
                    try environment.audioCache.store(data, brief: brief,
                                                     hubID: "settings-ui-hub", audio: audio)
                }
                try IdeasUITestSeed.write(to: directory.appendingPathComponent("ideas.json"), empty: true, now: Date())
                environment.ideas.loadCached()
            }
            if isFirstLaunch && state == "feed-words" {
                // The brief with its words and audio on the phone; the audio has a start time for each paragraph.
                let data = BriefAudioUITestSeed.wav()
                var words = FeedUITestSeed.wordsBrief(now: Date())
                let audio = BriefAudio(mime: "audio/wav", bytes: data.count, durationSeconds: BriefAudioUITestSeed.duration,
                                       paragraphs: FeedUITestSeed.wordsAudioParagraphs())
                words.audio = audio
                try environment.cache.save(words)
                if !audioOnComputer {
                    try environment.audioCache.store(data, brief: words, hubID: "settings-ui-hub", audio: audio)
                }
                environment.brief.loadCached()
                try IdeasUITestSeed.write(to: directory.appendingPathComponent("ideas.json"), empty: true, now: Date())
                environment.ideas.loadCached()
                // A newer brief can arrive after the test has started playing the words brief.
                if let delay = ProcessInfo.processInfo.environment["YANSU_UITEST_NEWER_BRIEF_AFTER"].flatMap(Double.init) {
                    Task { @MainActor in
                        try? await Task.sleep(for: .seconds(delay))
                        try? environment.cache.save(FeedUITestSeed.newerBrief(after: words))
                        environment.brief.loadCached()
                    }
                }
            }
            if isFirstLaunch && state == "feed-conversation" {
                try environment.cache.save(FeedUITestSeed.conversationBrief(now: Date()))
                environment.brief.loadCached()
                try ChatUITestSeed.seed(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                        answered: true, now: Date())
            }
            if isFirstLaunch && state == "feed-your-week" {
                try environment.cache.save(FeedUITestSeed.yourWeekBrief(now: Date()))
                environment.brief.loadCached()
                try ChatUITestSeed.seed(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                        answered: true, now: Date())
            }
            if isFirstLaunch && state.hasPrefix("feed-topics") {
                // The earlier days stay under the new parts of the Feed.
                for brief in FeedUITestSeed.briefs(today: false, now: Date()) {
                    try environment.cache.save(brief)
                }
                try environment.cache.save(TopicUITestSeed.brief(now: Date()))
                environment.brief.loadCached()
                try ChatUITestSeed.seed(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                        answered: true, now: Date())
            }
            if isFirstLaunch, var demoBrief {
                let data = BriefAudioUITestSeed.wav()
                let audio = BriefAudio(mime: "audio/wav", bytes: data.count, durationSeconds: BriefAudioUITestSeed.duration,
                                       paragraphs: LaunchDemoUITestSeed.audioParagraphs(duration: BriefAudioUITestSeed.duration))
                demoBrief.audio = audio
                try environment.cache.save(demoBrief)
                try environment.audioCache.store(data, brief: demoBrief, hubID: "settings-ui-hub", audio: audio)
                environment.brief.loadCached()
                try ChatUITestSeed.seedPresentation(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                                    items: LaunchDemoUITestSeed.transcript(), now: LaunchDemoUITestSeed.now())
                try IdeasUITestSeed.write(to: directory.appendingPathComponent("ideas.json"), empty: true, now: Date())
                environment.ideas.loadCached()
            }
            if let todayFixture { environment.today.accept(todayFixture) }
            if state == "brain-dump" { environment.tasks.seedBrainDumpCapabilityForUITest() }
            if state == "feed-review" { environment.tasks.seedForUITest(TaskUITestSeed.page) }
            if demoBrief != nil, ProcessInfo.processInfo.environment["YANSU_UITEST_TOUCHES"] != "0" { LaunchDemoTouches.install() }
            if isFirstLaunch && state == "library" {
                for brief in FeedUITestSeed.libraryBriefs(now: Date()) {
                    try environment.cache.save(brief)
                }
                environment.brief.loadCached()
            }
            if isFirstLaunch && state == "tasks-seeded" {
                environment.tasks.seedForUITest(TaskUITestSeed.page)
                if ProcessInfo.processInfo.environment["YANSU_UITEST_FIRST_COPY"] != "1" {
                    for saved in FeedUITestSeed.briefs(today: true, now: Date()) {
                        try environment.cache.save(saved)
                    }
                    environment.brief.loadCached()
                }
                try IdeasUITestSeed.write(to: directory.appendingPathComponent("ideas.json"), empty: false, now: Date())
                environment.ideas.loadCached()
                try ChatUITestSeed.seed(
                    ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                    answered: true, now: Date()
                )
                environment.chat.prepareMainChat()
                environment.notifications.seedForUITest()
                #if DEBUG && targetEnvironment(simulator)
                switch ProcessInfo.processInfo.environment["YANSU_UITEST_HELD_SEND"] {
                case "confirm": environment.tasks.seedSendConfirmationForUITest(person: "Sam")
                default: break
                }
                #endif
            }
            if isFirstLaunch && state == "day-one" {
                environment.tasks.seedForUITest(TaskUITestSeed.page)
                try IdeasUITestSeed.write(to: directory.appendingPathComponent("ideas.json"), empty: false, now: Date())
                environment.ideas.loadCached()
            }
            if state == "tasks-seeded", ProcessInfo.processInfo.environment["YANSU_UITEST_FIRST_COPY"] == "1",
               let paired = environment.vault.current() {
                let identity = TodayPairingIdentity(hubID: paired.hubID, deviceID: paired.deviceID)
                TodayBriefPlaceholder.reset(hubID: paired.hubID)
                environment.tasks.beginFirstCopy(identity: identity)
                Task { @MainActor in
                    try? await Task.sleep(for: .seconds(12))
                    if let snapshot = try? FeedUITestSeed.snapshot(state: "feed", now: Date()) {
                        environment.today.accept(snapshot)
                    }
                }
            }
            if isFirstLaunch && state == "goals" {
                try GoalUITestSeed.write(to: directory.appendingPathComponent("goals.json"), now: Date())
                environment.goals.loadCached()
                // Create a goal hands off to the chat, so the chat this iPhone last saw is there too.
                try ChatUITestSeed.seed(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                        answered: true, now: Date())
            }
            if isFirstLaunch && ["ideas", "ideas-empty"].contains(state) {
                try IdeasUITestSeed.write(to: directory.appendingPathComponent("ideas.json"),
                                          empty: state == "ideas-empty", now: Date())
                environment.ideas.loadCached()
                try ChatUITestSeed.seed(ChatCache(directory: directory.appendingPathComponent("chat", isDirectory: true)),
                                        answered: true, now: Date())
            }
            if isFirstLaunch { try Data().write(to: seedMarker, options: .atomic) }
            return environment
        } catch {
            preconditionFailure("Could not prepare isolated Settings UI state: \(error)")
        }
    }
}

/// A silent PCM file exercises the real player and its timer without a voice model, relay or bundled asset.
/// What Isoform sees on a computer with Mail off and Claude Code as the person's AI.
private actor SeesUITestHub: SeesHubAPI {
    func sees() async throws -> MobileSees {
        MobileSees(sources: [
            .init(job: "mail", on: false), .init(job: "calendar", on: true), .init(job: "calls", on: true),
            .init(job: "screen", on: true), .init(job: "assistant", on: true), .init(job: "contacts", on: true)
        ], assistant: "claude_code")
    }

    func showOnComputer(job: String) async throws {}
}

private actor NotificationUITestHub: NotificationSettingsAPI {
    private var settings = NotificationSettings(available: true, kinds: NotificationKind.phoneKinds.map {
        NotificationKind(kind: $0, enabled: true, defaultOn: true, paused: false, mutedUntil: nil,
                         sent: 0, opened: 0, acted: 0, dismissed: 0, muted: 0)
    }, quietStart: "22:00", quietEnd: "08:00", dailyInterruptBudget: 6, digestTimes: [], statsDays: 30)

    func notificationSettings() async throws -> NotificationSettings { settings }

    func setNotificationKind(_ kind: String, enabled: Bool, idempotencyKey: String) async throws -> NotificationSettings {
        settings = settings.applying(PendingNotificationChange(idempotencyKey: idempotencyKey, kind: kind, enabled: enabled))
        return settings
    }

    func setNotificationPreferences(_ request: SetNotificationPreferencesRequest) async throws -> NotificationSettings {
        settings = settings.applying(PendingNotificationChange(idempotencyKey: request.idempotencyKey,
                                                                 quietStart: request.quietStart, quietEnd: request.quietEnd))
        return settings
    }
}

private actor TasksUITestHub: TasksHubAPI, ActionHubAPI {
    private let brainDump: Bool
    init(brainDump: Bool = false) { self.brainDump = brainDump }
    private var created: [String: StartOneTimeWorkRequest] = [:]
    private var running: [MobileTask] = []
    private var listScheduled: [MobileScheduledTask] = []
    private var addedKeys: Set<String> = []
    private var failedStop = false

    func brainDumpAvailable() async throws -> Bool { brainDump }

    func parseBrainDump(text: String, idempotencyKey: String) async throws -> [BrainDumpItem] {
        let lines = text.components(separatedBy: .newlines).filter { !$0.trimmingCharacters(in: .whitespaces).isEmpty }
        return lines.enumerated().map { index, line in
            switch index {
            case 0: return BrainDumpItem(id: "draft", title: line, who: "draft", person: "Lena")
            case 1: return BrainDumpItem(id: "reminder", title: line, who: "reminder", when: TaskUITestSeed.timestamp(Date().addingTimeInterval(86400)))
            default: return BrainDumpItem(id: "code", title: line, who: "claude_code")
            }
        }
    }

    func addBrainDump(items: [BrainDumpItem], idempotencyKey: String) async throws {
        guard addedKeys.insert(idempotencyKey).inserted else { return }
        let now = TaskUITestSeed.timestamp(Date())
        for item in items {
            let id = "list-" + item.id
            if item.who == "reminder" {
                listScheduled.append(MobileScheduledTask(id: id, kind: "reminder", title: item.title, at: item.when ?? ""))
            } else {
                let task = MobileTask(id: id, title: item.title, status: item.who == "draft" ? "drafting" : "in_progress", group: "running",
                                      blockedReason: nil, runner: item.who == "draft" ? nil : item.who, progress: nil,
                                      createdAt: now, updatedAt: now, source: "list")
                running.append(task)
                threads[id] = MobileTaskDetail(id: id, title: item.title, status: task.status, group: "running",
                    blockedReason: nil, runner: task.runner, progress: nil, createdAt: now, updatedAt: now,
                    startedAt: nil, steps: [], origin: MobileTaskOrigin(kind: "list", at: now),
                    events: [MobileTaskEvent(id: "start", kind: "isoform", at: now,
                        text: item.who == "draft" ? String(localized: "Drafting it now. It lands here for you to send.") : String(localized: "Claude Code on your computer is on it. You review the result here."))], actions: ["rename"])
            }
        }
    }

    func rejectAttribution(memoryID: String, reason: String, idempotencyKey: String) async throws -> Bool { true }

    func startOneTimeWork(_ request: StartOneTimeWorkRequest) async throws -> String {
        let id = "hf-" + request.idempotencyKey
        created[id] = request
        return id
    }

    func runOneTimeWork(id: String, idempotencyKey: String) async throws {
        guard let request = created[id] else { throw HubRequestError.invalidIdentifier }
        guard !running.contains(where: { $0.id == id }) else { return }
        let now = ISO8601DateFormatter().string(from: Date())
        running.append(MobileTask(id: id, title: request.title, status: "in_progress", group: "running",
                                  blockedReason: nil, runner: nil, progress: nil, createdAt: now, updatedAt: now))
    }

    func tasks() async throws -> MobileTasksPage {
        if ProcessInfo.processInfo.environment["YANSU_UITEST_FIRST_COPY"] == "1" {
            try await Task.sleep(for: .seconds(12))
        }
        let page = brainDump ? MobileTasksPage(tasks: [], counts: MobileTaskCounts(needsYou: 0, running: 0, doneThisWeek: 0), threads: true, scheduled: []) : TaskUITestSeed.page
        let addsWaiting = ProcessInfo.processInfo.environment["YANSU_UITEST_EXTRA_WAITING"] == "1"
        let extra = addsWaiting ? [MobileTask(id: "extra-ui", title: "Check the launch date", status: "active",
                                             group: "needs_you", blockedReason: nil, runner: nil, progress: nil,
                                             createdAt: TaskUITestSeed.timestamp(Date()), updatedAt: TaskUITestSeed.timestamp(Date().addingTimeInterval(-600)))] : []
        let updated = page.tasks.map { threads[$0.id]?.task ?? $0 }
        let needsChange = updated.filter { $0.group == "needs_you" }.count - page.tasks.filter { $0.group == "needs_you" }.count
        let doneChange = updated.filter { $0.group == "done" }.count - page.tasks.filter { $0.group == "done" }.count
        return MobileTasksPage(tasks: updated + running + extra,
                               counts: MobileTaskCounts(needsYou: page.counts.needsYou + needsChange + extra.count,
                                                       running: page.counts.running + running.count,
                                                       doneThisWeek: page.counts.doneThisWeek + doneChange),
                               threads: page.threads, scheduled: (page.scheduled ?? []) + listScheduled)
    }

    private var threads: [String: MobileTaskDetail] = [:]

    func task(id: String) async throws -> MobileTaskDetail {
        if let thread = threads[id] { return thread }
        guard let task = (TaskUITestSeed.page.tasks + running).first(where: { $0.id == id }) else {
            throw HubRequestError.rejected(status: 404, message: "not found")
        }
        return TaskUITestSeed.detail(for: task)
    }

    func replyToTask(id: String, request: MobileTaskReplyRequest) async throws -> MobileTaskDetail {
        var thread = try await task(id: id)
        thread.events.append(MobileTaskEvent(id: "you-" + request.idempotencyKey, kind: "you",
                                             at: TaskUITestSeed.timestamp(Date()), text: request.text))
        threads[id] = thread
        return thread
    }

    func actOnTask(id: String, request: MobileTaskActionRequest) async throws -> MobileTaskDetail {
        var thread = try await task(id: id)
        let now = TaskUITestSeed.timestamp(Date())
        switch request.action {
        case "choose":
            guard let index = thread.events.firstIndex(where: { $0.id == request.eventId }),
                  let choice = thread.events[index].openChoices.first(where: { $0.id == request.choiceId }) else {
                throw HubRequestError.conflict(message: "that choice is no longer open")
            }
            thread.events[index].chosenId = choice.id
            thread.events.insert(MobileTaskEvent(id: "you-" + request.idempotencyKey, kind: "you", at: now,
                                                 text: choice.label), at: index + 1)
        case "send":
            if ProcessInfo.processInfo.environment["YANSU_UITEST_DRAFT_FAILURE"] == "1" {
                throw HubRequestError.conflict(message: "draft changed")
            }
            guard let draft = thread.draft, draft.state == "ready" else {
                throw HubRequestError.conflict(message: "this draft was already sent")
            }
            let sent = MobileTaskDraft(channel: draft.channel, why: draft.why, to: draft.to, subject: draft.subject,
                                       body: request.text ?? draft.body, attachment: draft.attachment,
                                       recipients: draft.recipients, voice: draft.voice, state: "sent", sentAt: now)
            thread = MobileTaskDetail(id: thread.id, title: thread.title, status: "completed", group: "done",
                                      blockedReason: nil, runner: nil, progress: nil, createdAt: thread.createdAt,
                                      updatedAt: now, startedAt: thread.startedAt, steps: thread.steps,
                                      outcome: MobileTaskOutcome(kind: "sent", by: "isoform", at: now,
                                                                 params: ["person": "Priya", "thing": "the signed SOW"]),
                                      origin: thread.origin, events: thread.events, actions: ["rename"], draft: sent)
        case "mark_done":
            thread = MobileTaskDetail(id: thread.id, title: thread.title, status: "completed", group: "done",
                                      blockedReason: nil, runner: nil, progress: nil, createdAt: thread.createdAt,
                                      updatedAt: now, startedAt: thread.startedAt, steps: thread.steps,
                                      outcome: MobileTaskOutcome(kind: "marked_done", by: "you", at: now),
                                      origin: thread.origin, events: thread.events, actions: ["rename"])
        case "rename":
            thread = MobileTaskDetail(id: thread.id, title: request.title ?? thread.title, status: thread.status,
                                      group: thread.group, blockedReason: thread.blockedReason, runner: thread.runner,
                                      progress: thread.progress, createdAt: thread.createdAt, updatedAt: thread.updatedAt,
                                      startedAt: thread.startedAt, steps: thread.steps, outcome: thread.outcome,
                                      origin: thread.origin, events: thread.events, actions: thread.actions)
        default:
            break
        }
        threads[id] = thread
        return thread
    }

    func stopTask(id: String, request: MobileTaskStopRequest) async throws -> MobileTask {
        if ProcessInfo.processInfo.environment["YANSU_UITEST_FAIL_STOP"] == "1", !failedStop {
            failedStop = true
            throw HubRequestError.noAnswer
        }
        guard let task = TaskUITestSeed.page.tasks.first(where: { $0.id == id }) else {
            throw HubRequestError.invalidIdentifier
        }
        return MobileTask(id: task.id, title: task.title, status: "stopped", group: "stopped",
                          blockedReason: task.blockedReason, runner: task.runner, progress: task.progress,
                          createdAt: task.createdAt, updatedAt: task.updatedAt)
    }
}

private enum TaskUITestSeed {
    /// The Tasks board in the phone-threads scene (Phone Threads v1.0.0): a task that needs you with its thread,
    /// running work with steps and a file, and a Done row with its outcome.
    static var page: MobileTasksPage {
        let now = Date()
        return MobileTasksPage(tasks: [
            MobileTask(id: "draft.ui-sow", title: "Send Priya the signed SOW", status: "draft_ready",
                       group: "needs_you", blockedReason: nil, runner: nil, progress: nil,
                       createdAt: timestamp(now), updatedAt: timestamp(now.addingTimeInterval(30)), unread: true, source: "mail"),
            MobileTask(id: "review-ui", title: "Review the onboarding flow", status: "needs_review",
                       group: "needs_you", blockedReason: nil, runner: nil,
                       progress: MobileTaskProgress(done: 3, total: 5),
                       createdAt: timestamp(now), updatedAt: timestamp(now), unread: true),
            MobileTask(id: "blocked-ui", title: "Update the release notes", status: "blocked",
                       group: "needs_you", blockedReason: "interrupted", runner: nil, progress: nil,
                       createdAt: timestamp(now), updatedAt: timestamp(now.addingTimeInterval(-60))),
            MobileTask(id: "running-ui", title: "Run iOS compatibility tests", status: "in_progress",
                       group: "running", blockedReason: nil, runner: "claude_code",
                       progress: MobileTaskProgress(done: 2, total: 5),
                       createdAt: timestamp(now), updatedAt: timestamp(now.addingTimeInterval(-180))),
            MobileTask(id: "done-ui", title: "Build the Today tab", status: "completed",
                       group: "done", blockedReason: nil, runner: nil, progress: nil,
                       createdAt: timestamp(now), updatedAt: timestamp(now.addingTimeInterval(-3600)),
                       outcome: MobileTaskOutcome(kind: "completed", by: "isoform",
                                                  at: timestamp(now.addingTimeInterval(-3600)),
                                                  params: ["summary": "The Today tab builds and its tests pass."])),
            MobileTask(id: "done-bare", title: "An older task with no outcome", status: "completed",
                       group: "done", blockedReason: nil, runner: nil, progress: nil,
                       createdAt: timestamp(now), updatedAt: timestamp(now.addingTimeInterval(-7200)))
        ], counts: MobileTaskCounts(needsYou: 3, running: 1, doneThisWeek: 1), threads: true,
           scheduled: [MobileScheduledTask(id: "brief.evening", kind: "brief", slot: "evening",
                                           at: timestamp(now.addingTimeInterval(3 * 3600)))])
    }

    static func detail(for task: MobileTask) -> MobileTaskDetail {
        let now = Date()
        let started = now.addingTimeInterval(-1800)
        switch task.id {
        case "review-ui":
            return MobileTaskDetail(
                id: task.id, title: task.title, status: task.status, group: task.group, blockedReason: nil,
                runner: nil, progress: task.progress, createdAt: task.createdAt, updatedAt: task.updatedAt,
                startedAt: started, steps: [], unread: task.unread,
                origin: MobileTaskOrigin(kind: "up_next", at: timestamp(now.addingTimeInterval(-4200))),
                events: [
                    MobileTaskEvent(id: "q", kind: "isoform", at: timestamp(now.addingTimeInterval(-2400)),
                                    text: "The onboarding flow builds and four of five screens match the design. The pairing screen still shows the old QR frame.\n\nShip it now, or fix the QR frame first?",
                                    choices: [MobileTaskChoice(id: "ship", label: "Ship it now"),
                                              MobileTaskChoice(id: "fix", label: "Fix the QR frame first")])
                ],
                actions: ["mark_done", "rename", "not_now"])
        case "draft.ui-sow":
            return MobileTaskDetail(
                id: task.id, title: task.title, status: task.status, group: task.group, blockedReason: nil,
                runner: nil, progress: nil, createdAt: task.createdAt, updatedAt: task.updatedAt,
                startedAt: nil, steps: [], unread: task.unread,
                origin: MobileTaskOrigin(kind: "up_next", at: timestamp(now.addingTimeInterval(-4200))),
                events: [
                    MobileTaskEvent(id: "q", kind: "isoform", at: timestamp(now.addingTimeInterval(-2400)),
                                    text: "Priya asked for the signed SOW by today. You signed v3 at 9:30 AM, with Dan's redlines in. Lena's pricing notes come Monday.\n\nSend the signed v3 now, or wait for Lena?",
                                    choices: [MobileTaskChoice(id: "now", label: "Send v3 now"),
                                              MobileTaskChoice(id: "wait", label: "Wait for Lena")],
                                    chosenId: "now"),
                    MobileTaskEvent(id: "y", kind: "you", at: timestamp(now.addingTimeInterval(-1920)), text: "Send v3 now"),
                    MobileTaskEvent(id: "r", kind: "isoform", at: timestamp(now),
                                    text: "Here's the reply. Nothing goes until you tap Send.")
                ],
                actions: ["send", "mark_done", "rename", "not_now"],
                draft: MobileTaskDraft(
                    channel: "mail",
                    why: IdeaEvidence(text: "Priya asked on Tuesday for the signed SOW by today.", source: "mail",
                                      at: "2026-10-27T23:12:00Z"),
                    to: "Priya Raman", subject: "Re: Halcyon SOW",
                    body: "Priya,\n\nSigned SOW attached. It's v3, with Dan's redlines in. Lena's pricing notes come Monday and don't change the scope.\n\nBo",
                    attachment: MobileTaskAttachment(name: "Halcyon SOW v3, signed.pdf", size: 217_088),
                    recipients: MobileTaskRecipients(to: ["Priya"], notIncluded: ["Dan"]),
                    voice: MobileTaskVoice(sampleCount: 6, person: "Priya", traits: ["short", "no_greeting"]),
                    state: "ready", revision: "ui-revision"))
        case "running-ui":
            return MobileTaskDetail(
                id: task.id, title: task.title, status: task.status, group: task.group, blockedReason: nil,
                runner: nil, progress: task.progress, createdAt: task.createdAt, updatedAt: task.updatedAt,
                startedAt: started,
                steps: [MobileTaskStep(text: "Read the test plan and last week's notes", state: "done"),
                        MobileTaskStep(text: "Ran the tests on iOS 17", state: "done"),
                        MobileTaskStep(text: "Running the tests on iOS 26", state: "current"),
                        MobileTaskStep(text: "Compare the screenshots", state: "pending"),
                        MobileTaskStep(text: "Show you the result", state: "pending")],
                origin: MobileTaskOrigin(kind: "chat", at: timestamp(now.addingTimeInterval(-86_400))),
                events: [MobileTaskEvent(id: "f", kind: "file", at: timestamp(started), file: MobileTaskFile(name: "ios17-results.md"))],
                actions: ["rename", "made", "stop"])
        default:
            return MobileTaskDetail(
                id: task.id, title: task.title, status: task.status, group: task.group,
                blockedReason: task.blockedReason, runner: task.runner, progress: task.progress,
                createdAt: task.createdAt, updatedAt: task.updatedAt, startedAt: RelayTime.parse(task.createdAt),
                steps: [MobileTaskStep(text: "Run the requested work", state: "current"),
                        MobileTaskStep(text: "Review the result", state: "pending")],
                outcome: task.outcome, events: [], actions: task.group == "done" ? ["rename"] : ["mark_done", "rename", "not_now"])
        }
    }

    static func timestamp(_ date: Date) -> String {
        ISO8601DateFormatter().string(from: date)
    }
}

private enum BriefAudioUITestSeed {
    static let duration: Double = 30

    /// The computer's copy of the audio in UI tests: the first download fails, every later one succeeds.
    @MainActor
    final class OnComputer {
        private var downloads = 0

        func download() throws -> Data {
            downloads += 1
            guard downloads > 1 else { throw BriefAudioError.computerOffline }
            return BriefAudioUITestSeed.wav()
        }
    }

    static func wav() -> Data {
        let sampleRate: UInt32 = 8_000
        let sampleBytes: UInt16 = 2
        let payloadBytes = UInt32(duration) * sampleRate * UInt32(sampleBytes)
        var data = Data()
        func append<T: FixedWidthInteger>(_ value: T) {
            var littleEndian = value.littleEndian
            withUnsafeBytes(of: &littleEndian) { data.append(contentsOf: $0) }
        }
        data.append(contentsOf: "RIFF".utf8)
        append(UInt32(36) + payloadBytes)
        data.append(contentsOf: "WAVEfmt ".utf8)
        append(UInt32(16))
        append(UInt16(1)) // PCM
        append(UInt16(1)) // Mono
        append(sampleRate)
        append(sampleRate * UInt32(sampleBytes))
        append(sampleBytes)
        append(UInt16(16))
        data.append(contentsOf: "data".utf8)
        append(payloadBytes)
        for index in 0..<Int(duration * Double(sampleRate)) {
            let seconds = Double(index) / Double(sampleRate)
            let amplitude = 0.15 + 0.6 * abs(sin(seconds * 2.1))
            append(Int16(sin(seconds * 2 * .pi * 220) * amplitude * 16000))
        }
        return data
    }
}
#endif
