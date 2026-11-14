import Foundation
import XCTest
@testable import Yansu

final class TodayStoreTests: XCTestCase {
    private actor UnusedHub: TodayHubAPI {
        func today() async throws -> MobileTodaySnapshot { throw HubRequestError.noAnswer }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { throw HubRequestError.noAnswer }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult { throw HubRequestError.noAnswer }
    }

    private actor RecordingHub: TodayHubAPI {
        var online = false
        var keys: [String] = []
        let value: MobileTodaySnapshot
        init(value: MobileTodaySnapshot) { self.value = value }
        func setOnline() { online = true }
        func today() async throws -> MobileTodaySnapshot {
            guard online else { throw HubRequestError.noAnswer }
            return value
        }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { [] }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult {
            keys.append(key)
            guard online else { throw HubRequestError.noAnswer }
            return BriefRefreshResult(status: "queued", nextAllowedAt: nil, readyAt: "2026-10-27T13:00:00Z")
        }
    }
    private func snapshot() -> MobileTodaySnapshot {
        MobileTodaySnapshot(generatedAt: "2026-10-27T12:00:00Z", latestBrief: nil, briefs: [],
                            needsYou: [], needsYouCount: 0,
                            day: TodayDay(next: nil, free: [], calendarConnected: false), topics: [],
                            briefRequest: nil, schedule: MobileBriefSchedule(morning: "07:00", evening: "18:00"))
    }

    func testReadyTimeIsHiddenOnceItPasses() throws {
        let request = TodayBriefRequest(state: "making", readyAt: "2026-10-27T12:00:00Z")
        let ready = try XCTUnwrap(request.readyDate)
        XCTAssertEqual(request.futureReadyDate(at: ready.addingTimeInterval(-1)), ready)
        XCTAssertNil(request.futureReadyDate(at: ready))
        XCTAssertNil(request.futureReadyDate(at: ready.addingTimeInterval(1)))
    }

    @MainActor
    func testAsOfUsesTheNewerSnapshotOrBriefTime() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
        // The brief's text is in the library, so it is the current brief and its time counts.
        try cache.save(MobileBriefV1(briefId: "new", asOf: "2026-10-27T13:00:00Z", narrativeStatus: "ok",
                                     narrative: [BriefParagraph(text: "New")]))
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault))
        let store = TodayStore(hub: UnusedHub(), cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief,
                               identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
        var value = snapshot()
        value.latestBrief = TodayBriefIndex(id: "new", slot: "morning", localDate: "2026-10-27", scheduledAt: nil,
                                            madeAt: "2026-10-27T13:00:00Z", catchUp: nil, durationSeconds: nil, lead: "New")
        value.briefs = [value.latestBrief!]
        store.accept(value)
        XCTAssertEqual(store.asOf, RelayTime.parse("2026-10-27T13:00:00Z"))
        value.generatedAt = "2026-10-27T14:00:00Z"
        store.accept(value)
        XCTAssertEqual(store.asOf, RelayTime.parse("2026-10-27T14:00:00Z"))
    }

    func testDiskCacheRoundTripIsScopedToPairingAndCanBeCleared() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let cache = TodayDiskCache(fileURL: directory.appendingPathComponent("today.json"))
        let identity = TodayPairingIdentity(hubID: "mac-a", deviceID: "phone-a")
        cache.save(snapshot(), for: identity)
        // load synchronizes with the preceding asynchronous save.
        XCTAssertEqual(cache.load(for: identity), snapshot())
        XCTAssertNil(cache.load(for: TodayPairingIdentity(hubID: "mac-b", deviceID: "phone-a")))
        XCTAssertTrue(cache.clear())
        XCTAssertNil(cache.load(for: identity))
    }

    func testTasksWaitingFieldsRoundTripAndOlderCopiesDecode() throws {
        let page = MobileTasksPage(tasks: [], counts: MobileTaskCounts(needsYou: 0, running: 0, doneThisWeek: 0))
        let legacy = try JSONDecoder().decode(MobileTasksPage.self, from: JSONEncoder().encode(page))
        XCTAssertNil(legacy.waitingItems)
        XCTAssertNil(legacy.waitingCount)
        var current = page
        current.waitingItems = []
        current.waitingCount = 4
        let decoded = try JSONDecoder().decode(MobileTasksPage.self, from: JSONEncoder().encode(current))
        XCTAssertEqual(decoded.waitingCount, 4)
        XCTAssertEqual(decoded.waitingItems, [])
    }

    func testDraftSendIsDurableUndoableBeforeDispatchAndKeepsItsKey() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("draft-journal.json")
        let journal = PendingWriteJournal(fileURL: url, hubID: { "computer" })
        let saved = try await journal.reserveTodayDraft(taskID: "draft.1", revision: "revision-a", text: "My edit",
                                                        deviceID: "phone", makeKey: { "send-key" }, createdAt: Date())
        let reopened = PendingWriteJournal(fileURL: url, hubID: { "computer" })
        let recovered = await reopened.entries()
        XCTAssertEqual(recovered.first?.key, saved.key)
        XCTAssertEqual(recovered.first?.todayDraftSend?.text, "My edit")
        let undone = try await reopened.undoToday(key: saved.key)
        XCTAssertTrue(undone)
        let next = try await reopened.reserveTodayDraft(taskID: "draft.1", revision: "revision-a", text: nil,
                                                        deviceID: "phone", makeKey: { "next-key" }, createdAt: Date())
        _ = try await reopened.markTodayDraft(key: next.key, dispatchedAt: Date())
        let lateUndo = try await reopened.undoToday(key: next.key)
        XCTAssertFalse(lateUndo)
        let retry = try await reopened.reserveTodayDraft(taskID: "draft.1", revision: "revision-a", text: nil,
                                                         deviceID: "phone", makeKey: { "wrong-key" }, createdAt: Date())
        XCTAssertEqual(retry.key, "next-key")
    }

    func testQueuedActionSurvivesReopenAndUndoCannotCancelADispatchedWrite() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("journal.json")
        let journal = PendingWriteJournal(fileURL: url, hubID: { "mac" })
        let item = try JSONDecoder().decode(ChatProactiveView.self, from: Data(#"{"momentId":"m1","kind":"reminder","state":"pending","actionIds":["done"]}"#.utf8))
        let write = try await journal.reserveToday(chatID: "chat", item: item, actionID: "done", deviceID: "phone",
                                                   makeKey: { "key" }, createdAt: Date())
        let reopened = PendingWriteJournal(fileURL: url, hubID: { "mac" })
        let restored = await reopened.entries()
        XCTAssertEqual(restored.first?.todayReplay?.momentID, "m1")
        XCTAssertEqual(restored.first?.key, write.key)
        let undone = try await reopened.undoToday(key: write.key)
        let remaining = await reopened.entries()
        XCTAssertTrue(undone)
        XCTAssertTrue(remaining.isEmpty)

        let next = try await reopened.reserveToday(chatID: "chat", item: item, actionID: "done", deviceID: "phone",
                                                   makeKey: { "next" }, createdAt: Date())
        _ = try await reopened.markTodayDispatched(key: next.key, at: Date())
        let dispatchedUndo = try await reopened.undoToday(key: next.key)
        XCTAssertFalse(dispatchedUndo)
        let replacement = try await reopened.reserveToday(chatID: "chat", item: item, actionID: "done", deviceID: "new-phone",
                                                          makeKey: { "new-pairing" }, createdAt: Date())
        let afterPairing = await reopened.entries()
        XCTAssertEqual(afterPairing.map(\.key), [replacement.key])
    }

    @MainActor
    func testNewestReadableBriefIsCurrentAndATextlessOneNeverIs() {
        func brief(_ id: String, _ date: String, text: String) -> MobileBriefV1 {
            MobileBriefV1(briefId: id, asOf: date, narrativeStatus: "ok",
                          narrative: [BriefParagraph(text: text)])
        }
        func entries(_ briefs: [MobileBriefV1]) -> [CachedBrief] {
            briefs.map { CachedBrief(brief: $0, storedAt: Date()) }
        }
        let old = brief("old", "2026-10-26T18:00:00Z", text: "Old")
        let newest = brief("new", "2026-10-27T06:10:00Z", text: "New")
        let empty = brief("empty", "2026-10-27T07:00:00Z", text: "")
        XCTAssertEqual(BriefCache.current(in: entries([old, empty, newest]))?.brief.identifier, "new")
        XCTAssertEqual(BriefCache.current(in: entries([old, empty]))?.brief.identifier, "old")
        XCTAssertNil(BriefCache.current(in: entries([empty])))
    }

    @MainActor
    func testSnapshotFetchesMissingBodiesAndKeepsTheOnesItHas() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"),
                               now: { RelayTime.parse("2026-10-27T14:00:00Z")! })
        let bodies = FakeBriefBodies(savedProbe: { _, _ in false })
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault),
                               bodies: bodies)
        let cached = MobileBriefV1(briefId: "cached", asOf: "2026-10-27T12:00:00Z", narrativeStatus: "ok",
                                   narrative: [BriefParagraph(text: "Saved")])
        try cache.save(cached)
        let missing = MobileBriefV1(briefId: "missing", asOf: "2026-10-27T13:00:00Z", narrativeStatus: "ok",
                                    narrative: [BriefParagraph(text: "New")])
        await bodies.serveLegacy("missing", missing)
        let store = TodayStore(hub: UnusedHub(), cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief, identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
        var value = snapshot()
        value.briefs = [TodayBriefIndex(id: "cached", slot: "morning", localDate: "2026-10-27", scheduledAt: nil,
                                        madeAt: "2026-10-27T12:00:00Z", catchUp: nil, durationSeconds: nil, lead: "Saved"),
                        TodayBriefIndex(id: "missing", slot: "evening", localDate: "2026-10-27", scheduledAt: nil,
                                        madeAt: "2026-10-27T13:00:00Z", catchUp: nil, durationSeconds: nil, lead: "New")]
        store.accept(value)
        let fetched = await awaitCondition { cache.load(identifier: "missing") != nil }
        XCTAssertTrue(fetched)
        let requests = await bodies.legacyRequests
        XCTAssertEqual(requests, ["missing"], "only the brief the library lacks is fetched")
    }

    @MainActor
    func testSnapshotPrefetchesTheCurrentBriefsAudio() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let keys = RelayCrypto.makeDeviceKeyPair()
        let paired = PairedDevice(hubID: "mac", hubName: "Mac", hubPublicKey: RelayCrypto.base64URLEncode(keys.publicKey),
                                  relayOrigin: "https://relay.example", deviceID: "phone", deviceName: "Phone",
                                  scopes: ["brief:read"], pairedAt: Date(), counters: SequenceCounters())
        try vault.store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(keys.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"),
                               now: { RelayTime.parse("2026-10-27T14:00:00Z")! })
        let latest = MobileBriefV1(briefId: "new", asOf: "2026-10-27T13:00:00Z", narrativeStatus: "ok",
                                   narrative: [BriefParagraph(text: "New")], audio: BriefAudio(mime: "audio/wav", bytes: 3))
        try cache.save(latest)
        let bodies = FakeBriefBodies(savedProbe: { _, _ in false })
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault),
                               bodies: bodies)
        brief.loadCached()
        let prefetched = expectation(description: "current audio prefetched")
        let store = TodayStore(hub: UnusedHub(), cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief,
                               identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") },
                               prefetchAudio: { item, hub in
            XCTAssertEqual(item, latest)
            XCTAssertEqual(hub, "mac")
            prefetched.fulfill()
        })
        var value = snapshot()
        value.latestBrief = TodayBriefIndex(id: "new", slot: "morning", localDate: "2026-10-27", scheduledAt: nil,
                                            madeAt: "2026-10-27T13:00:00Z", catchUp: nil, durationSeconds: nil, lead: "New")
        value.briefs = [value.latestBrief!]
        store.accept(value)
        await fulfillment(of: [prefetched], timeout: 5)
    }

    @MainActor
    func testChangedBodyRevisionIsFetchedEvenWhenTheIDIsCached() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"),
                               now: { RelayTime.parse("2026-10-27T14:00:00Z")! })
        let saved = MobileBriefV1(briefId: "brief", asOf: "2026-10-27T12:00:00Z", narrativeStatus: "ok",
                                  narrative: [BriefParagraph(text: "Old")])
        try cache.save(saved, version: "old")
        let updated = MobileBriefV1(briefId: "brief", asOf: "2026-10-27T12:00:00Z", narrativeStatus: "ok",
                                    narrative: [BriefParagraph(text: "Updated")])
        let bodies = FakeBriefBodies(savedProbe: { _, _ in false })
        await bodies.serveLegacy("brief", updated)
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault),
                               bodies: bodies)
        let store = TodayStore(hub: UnusedHub(), cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief, identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
        var value = snapshot()
        value.briefs = [TodayBriefIndex(id: "brief", slot: "morning", localDate: "2026-10-27", scheduledAt: nil,
                                        madeAt: "2026-10-27T12:00:00Z", catchUp: nil, durationSeconds: nil,
                                        lead: "Updated", bodyVersion: "new")]
        store.accept(value)
        let fetched = await awaitCondition { cache.load(identifier: "brief")?.version == "new" }
        XCTAssertTrue(fetched)
        let requests = await bodies.legacyRequests
        XCTAssertEqual(requests, ["brief"])
    }

    @MainActor
    func testABriefWithoutItsTextIsNeverShownAndNoLeadTakesItsPlace() throws {
        // Bo's phone, 2026-10-28: the brief arrived in full by push, the Mac then listed a newer revision of it,
        // and Today replaced the full brief with its lead, "Good morning.". The full brief must stay on screen,
        // and a brief whose text is not on the phone shows nothing rather than its lead.
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
        let full = MobileBriefV1(briefId: "brief", slot: "on_request", asOf: "2026-10-28T15:41:54Z", narrativeStatus: "ok",
                                 narrative: [BriefParagraph(text: "Good morning. The forwarded email has no reply yet."),
                                             BriefParagraph(text: "Second paragraph.")])
        try cache.save(full)
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault),
                               bodies: FakeBriefBodies(savedProbe: { _, _ in false }))
        let store = TodayStore(hub: UnusedHub(), cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief, identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
        let entry = TodayBriefIndex(id: "brief", slot: "on_request", localDate: "2026-10-28", scheduledAt: nil,
                                    madeAt: "2026-10-28T15:41:54Z", catchUp: nil, durationSeconds: nil,
                                    lead: "Good morning.", bodyVersion: "newer")
        var value = snapshot()
        value.generatedAt = "2026-10-28T16:00:00Z"
        value.latestBrief = entry
        value.briefs = [entry]
        store.accept(value)
        XCTAssertEqual(store.newestBrief()?.narrative.count, 2)

        try cache.remove(identifier: "brief")
        store.accept(value)
        XCTAssertNil(store.newestBrief(), "the lead is not a brief: nothing shows until the text arrives")
    }

    func testTodayWriteStateTransitions() {
        typealias State = PendingWrite.TodayState
        typealias Event = State.Event
        let allowed: [State: [Event: State]] = [
            .reserved: [.dispatch: .dispatched, .undo: .dismissed],
            .dispatched: [.dispatch: .dispatched, .expire: .outcomeUnknown, .answer: .resolved],
            .outcomeUnknown: [.answer: .resolved, .dismiss: .dismissed],
            .resolved: [:], .dismissed: [:]
        ]
        for state in State.allCases {
            for event in Event.allCases {
                XCTAssertEqual(state.transition(event), allowed[state]?[event], "\(state) + \(event)")
            }
        }
    }

    func testExpiredAmbiguousRefreshSurvivesRelaunchAndCanBeDismissed() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("journal.json")
        let sent = Date(timeIntervalSince1970: 1_800_000_000)
        let first = PendingWriteJournal(fileURL: url, hubID: { "mac" })
        let write = try await first.reserveTodayRefresh(deviceID: "phone", makeKey: { "old" }, createdAt: sent)
        _ = try await first.markTodayRefreshDispatched(key: write.key, at: sent)
        let reopened = PendingWriteJournal(fileURL: url, hubID: { "mac" })
        let expired = sent.addingTimeInterval(ActionStore.journalRetention)
        let replay = try await reopened.markTodayRefreshDispatched(key: write.key, at: expired)
        XCTAssertNil(replay, "an ambiguous write must not be replayed after receipt expiry")
        let restored = await reopened.entries()
        XCTAssertEqual(restored.first?.todayState, .outcomeUnknown)
        XCTAssertEqual(restored.first?.todayLifecycle?.version, 1)
        let blocked = try await reopened.reserveTodayRefresh(deviceID: "phone", makeKey: { "new" }, createdAt: expired)
        XCTAssertEqual(blocked.key, write.key)
        let dismissed = try await reopened.transitionToday(key: write.key, event: .dismiss)
        XCTAssertTrue(dismissed)
        let fresh = try await reopened.reserveTodayRefresh(deviceID: "phone", makeKey: { "new" }, createdAt: expired)
        XCTAssertEqual(fresh.key, "new")
        let late = try await reopened.transitionToday(key: write.key, event: .answer)
        XCTAssertFalse(late, "a late answer must not remove a newer request")
        let remaining = await reopened.entries()
        XCTAssertEqual(remaining.map(\.key), ["new"])
    }

    func testLateAnswerResolvesUnknownWithoutReplaying() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("journal.json"), hubID: { "mac" })
        let sent = Date(timeIntervalSince1970: 1_800_000_000)
        let write = try await journal.reserveTodayRefresh(deviceID: "phone", makeKey: { "key" }, createdAt: sent)
        _ = try await journal.markTodayRefreshDispatched(key: write.key, at: sent)
        try await journal.expireToday(at: sent.addingTimeInterval(ActionStore.journalRetention))
        let answered = try await journal.transitionToday(key: write.key, event: .answer)
        XCTAssertTrue(answered)
        let entries = await journal.entries()
        XCTAssertTrue(entries.isEmpty)
    }

    func testEachTodayWriteExpiresWithoutReplaying() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("journal.json"), hubID: { "mac" })
        let sent = Date(timeIntervalSince1970: 1_800_000_000)
        var action = PendingWrite(key: "action", operation: .chatProactiveAction, target: "a", choice: "done", fingerprint: "a", createdAt: sent, hubID: "mac")
        action.todayReplay = PendingWrite.TodayReplay(chatID: "chat", momentID: "moment", cardDigest: "digest", actionID: "done", dispatched: false)
        var feedback = PendingWrite(key: "feedback", operation: .feedback, target: "b", choice: "like", fingerprint: "b", createdAt: sent, hubID: "mac")
        feedback.todayFeedback = PendingWrite.TodayFeedback(briefID: "brief", itemID: "item", cardKey: "card", title: "title", action: .like, reason: nil, bodyVersion: nil)
        var correction = PendingWrite(key: "correction", operation: .correction, target: "c", choice: "reject", fingerprint: "c", createdAt: sent, hubID: "mac")
        correction.todayCorrection = PendingWrite.TodayCorrection(briefID: "brief", bodyVersion: nil, memoryID: "memory", reason: "reason")
        var refresh = PendingWrite(key: "refresh", operation: .briefRefresh, target: "d", choice: "refresh", fingerprint: "d", createdAt: sent, hubID: "mac")
        refresh.todayRefresh = true
        for write in [action, feedback, correction, refresh] { try await journal.insert(write) }
        _ = try await journal.markTodayDispatched(key: action.key, at: sent)
        _ = try await journal.markTodayFeedbackDispatched(key: feedback.key, at: sent)
        _ = try await journal.markTodayCorrectionDispatched(key: correction.key, at: sent)
        _ = try await journal.markTodayRefreshDispatched(key: refresh.key, at: sent)
        try await journal.expireToday(at: sent.addingTimeInterval(ActionStore.journalRetention))
        let writes = await journal.entries()
        XCTAssertEqual(writes.map(\.todayState), Array(repeating: .outcomeUnknown, count: 4))
        let replay = try await journal.markTodayDispatched(key: action.key, at: sent.addingTimeInterval(ActionStore.journalRetention))
        XCTAssertNil(replay)
        do {
            _ = try await journal.reserve(operation: action.operation, target: action.target, choice: action.choice,
                                          fingerprint: action.fingerprint, makeKey: { "other" }, createdAt: sent)
            XCTFail("The generic writer must not replay a Today write")
        } catch HubRequestError.conflict { }
    }

    func testFailedExpirySaveKeepsDispatchedKeyAndDoesNotReplay() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("journal.json")
        let sent = Date(timeIntervalSince1970: 1_800_000_000)
        let journal = PendingWriteJournal(fileURL: url, hubID: { "mac" })
        let write = try await journal.reserveTodayRefresh(deviceID: "phone", makeKey: { "key" }, createdAt: sent)
        _ = try await journal.markTodayRefreshDispatched(key: write.key, at: sent)
        let failing = PendingWriteJournal(fileURL: url, hubID: { "mac" }, writeData: { _, _ in throw CocoaError(.fileWriteNoPermission) })
        do {
            _ = try await failing.markTodayRefreshDispatched(key: write.key, at: sent.addingTimeInterval(ActionStore.journalRetention))
            XCTFail("The expired write must not be sent when the unknown state cannot be saved")
        } catch {}
        let retained = await failing.entries()
        XCTAssertEqual(retained.first?.todayState, .dispatched)
        XCTAssertEqual(retained.first?.key, write.key)
    }

    @MainActor
    func testOfflineFreshRequestCanBeUndoneOrSentAfterReconnect() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault))
        let identity = TodayPairingIdentity(hubID: "mac", deviceID: "phone")
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("journal.json"), hubID: { "mac" })
        let hub = RecordingHub(value: snapshot())
        // The relay reports the Mac offline, so the failed read is shown as asleep.
        let store = TodayStore(hub: hub, cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief, journal: journal, identity: { identity },
                               presence: { false })
        await store.refresh()
        XCTAssertTrue(store.hubUnreachable)
        await store.requestFresh()
        XCTAssertNotNil(store.pendingFresh)
        await store.undoFresh()
        XCTAssertNil(store.pendingFresh)
        await store.requestFresh()
        let key = try XCTUnwrap(store.pendingFresh?.key)
        await hub.setOnline()
        await store.refresh()
        await store.retryFresh()
        XCTAssertNil(store.pendingFresh)
        let keys = await hub.keys
        XCTAssertEqual(keys, [key])
        XCTAssertEqual(store.snapshot?.briefRequest?.state, "queued")
    }

    private actor RefusingFreshHub: TodayHubAPI {
        let value: MobileTodaySnapshot
        init(value: MobileTodaySnapshot) { self.value = value }
        func today() async throws -> MobileTodaySnapshot { value }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { [] }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult {
            throw HubRequestError.rejected(status: 400, message: "refused")
        }
    }

    @MainActor
    func testRefusedFreshRequestShowsAFailureThatAFreshRequestClears() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault))
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("journal.json"), hubID: { "mac" })
        let store = TodayStore(hub: RefusingFreshHub(value: snapshot()),
                               cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief, journal: journal,
                               identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
        await store.refresh()
        XCTAssertFalse(store.freshFailed)
        await store.requestFresh()
        XCTAssertTrue(store.freshFailed, "the card says the brief could not be made")
        XCTAssertNil(store.pendingFresh, "a refused request is not kept for retry")
        XCTAssertNil(store.snapshot?.briefRequest)
    }

    private actor ThrottledFreshHub: TodayHubAPI {
        let value: MobileTodaySnapshot
        let error: Error
        init(value: MobileTodaySnapshot, error: Error) { self.value = value; self.error = error }
        func today() async throws -> MobileTodaySnapshot { value }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { [] }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult { throw error }
    }

    /// A brief made at 9:02 and "Make my brief now" at 9:04: the Mac answers 429 (one a device every 10 minutes),
    /// or the relay's queue is full. Nothing will be made, so the row must not wait a day under "Waits for your
    /// computer" with no Undo while every new tap returns the same stuck entry.
    @MainActor
    func testAThrottledFreshRequestSaysItFailedAndCanBeAskedAgain() async throws {
        for error: Error in [HubRequestError.rejected(status: 429, message: "too soon"),
                             HubRequestError.rejected(status: 503, message: "not ready"),
                             HubRequestError.queueFull(retryAfter: 60)] {
            let directory = try makeTemporaryDirectory()
            defer { try? FileManager.default.removeItem(at: directory) }
            let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
            let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault,
                                   cache: BriefCache(directory: directory.appendingPathComponent("briefs")),
                                   hub: HubChannel(vault: vault))
            let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("journal.json"), hubID: { "mac" })
            let store = TodayStore(hub: ThrottledFreshHub(value: snapshot(), error: error),
                                   cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                                   brief: brief, journal: journal,
                                   identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
            await store.refresh()
            await store.requestFresh()
            XCTAssertNil(store.pendingFresh, "\(error): nothing waits for the computer")
            XCTAssertTrue(store.freshFailed, "\(error): the card offers Make it now")
            let entries = await journal.entries()
            XCTAssertTrue(entries.isEmpty, "\(error): a later tap sends a new request")
        }
    }

    /// The Mac was asleep when the request went out, then woke and made the brief: its snapshot settles the row.
    @MainActor
    func testAnUnansweredFreshRequestSettlesWhenTheMacShowsTheBrief() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault,
                               cache: BriefCache(directory: directory.appendingPathComponent("briefs")),
                               hub: HubChannel(vault: vault))
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("journal.json"), hubID: { "mac" })
        let store = TodayStore(hub: ThrottledFreshHub(value: snapshot(), error: HubRequestError.noAnswer),
                               cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: brief, journal: journal,
                               identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })
        await store.refresh()
        await store.requestFresh()
        XCTAssertNotNil(store.pendingFresh?.todayRefreshDispatchedAt, "no answer: the request may still run")
        XCTAssertFalse(store.freshFailed)
        var making = snapshot()
        making.generatedAt = "2099-01-01T00:00:00Z"
        making.briefRequest = TodayBriefRequest(state: "making", readyAt: "2099-01-01T00:05:00Z")
        store.accept(making)
        for _ in 0..<50 where store.pendingFresh != nil { try await Task.sleep(for: .milliseconds(20)) }
        XCTAssertNil(store.pendingFresh, "the Mac took the request")
        let entries = await journal.entries()
        XCTAssertTrue(entries.isEmpty)
    }

    /// The first read waits until released and answers the snapshot from before a change; later reads answer
    /// the newer one.
    private actor GatedTodayHub: TodayHubAPI {
        let old: MobileTodaySnapshot
        let new: MobileTodaySnapshot
        private(set) var reads = 0
        private var gate: CheckedContinuation<Void, Never>?
        private var waiting: CheckedContinuation<Void, Never>?
        init(old: MobileTodaySnapshot, new: MobileTodaySnapshot) { self.old = old; self.new = new }
        func today() async throws -> MobileTodaySnapshot {
            reads += 1
            guard reads == 1 else { return new }
            await withCheckedContinuation { gate = $0; waiting?.resume(); waiting = nil }
            return old
        }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { [] }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult { throw HubRequestError.noAnswer }
        func waitUntilHeld() async { if gate == nil { await withCheckedContinuation { waiting = $0 } } }
        func release() { gate?.resume(); gate = nil }
    }

    /// A change event arrives while a read is in flight: its refresh used to be dropped, and the read in flight,
    /// served before the change, left Today stale until the next event or the 15-minute resync.
    @MainActor
    func testARefreshAskedForDuringARefreshReadsAgain() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        var newer = snapshot()
        newer.generatedAt = "2026-10-27T12:05:00Z"
        newer.needsYouCount = 3
        let hub = GatedTodayHub(old: snapshot(), new: newer)
        let store = makeStore(directory: directory, hub: hub)
        let first = Task { await store.refresh() }
        await hub.waitUntilHeld()
        await store.refresh()
        await hub.release()
        await first.value
        let reads = await hub.reads
        XCTAssertEqual(reads, 2, "one more read after the one in flight")
        XCTAssertEqual(store.snapshot?.needsYouCount, 3, "Today shows the change")
    }

    private actor SlowHub: TodayHubAPI {
        func today() async throws -> MobileTodaySnapshot { throw HubRequestError.noAnswer }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { [] }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult { throw HubRequestError.noAnswer }
    }

    @MainActor
    private func makeStore(directory: URL, hub: TodayHubAPI, presence: @escaping @Sendable () async throws -> Bool = { true },
                           connectionPresence: (@Sendable () async throws -> RelayPresence)? = nil,
                           bodies: BriefBodyAPI? = nil) -> TodayStore {
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
        let brief = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache, hub: HubChannel(vault: vault),
                               bodies: bodies ?? FakeBriefBodies(savedProbe: { _, _ in false }))
        return TodayStore(hub: hub, cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                          brief: brief,
                          identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") },
                          presence: presence, connectionPresence: connectionPresence)
    }

    @MainActor
    func testExplicitGoodbyesNameTheCauseWithoutGuessing() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        for cause in [TodayStore.ConnectionCause.sleeping, .shuttingDown, .closing] {
            let store = makeStore(directory: directory, hub: SlowHub(), presence: { false },
                                  connectionPresence: { RelayPresence(hubOnline: false, lastSeenAt: nil,
                                                                      goodbyeCause: cause.rawValue) })
            await store.refresh()
            XCTAssertEqual(store.connectionCause, cause)
            store.stopListening()
        }
        let online = makeStore(directory: directory, hub: SlowHub(), presence: { true },
                               connectionPresence: { RelayPresence(hubOnline: true, lastSeenAt: nil,
                                                                   goodbyeCause: "sleeping") })
        await online.refresh()
        XCTAssertNil(online.connectionCause)
    }

    func testPresenceDecidesWhetherASlowMacIsAsleep() async {
        // A slow or refused read while the relay says the Mac is online is not asleep.
        let slow = await TodayStore.macUnreachable(after: HubRequestError.noAnswer, presence: { true })
        XCTAssertFalse(slow)
        // The relay says the Mac is offline: asleep.
        let asleep = await TodayStore.macUnreachable(after: HubRequestError.noAnswer, presence: { false })
        XCTAssertTrue(asleep)
        // The phone has no route to the relay at all: asleep.
        let noRoute = await TodayStore.macUnreachable(after: RelayClientError.offline, presence: { throw RelayClientError.timedOut })
        XCTAssertTrue(noRoute)
        // A presence check that timed out says nothing about the Mac.
        let presenceTimedOut = await TodayStore.macUnreachable(after: HubRequestError.noAnswer, presence: { throw RelayClientError.timedOut })
        XCTAssertFalse(presenceTimedOut)
        let presenceOffline = await TodayStore.macUnreachable(after: HubRequestError.noAnswer, presence: { throw RelayClientError.offline })
        XCTAssertTrue(presenceOffline)
    }

    @MainActor
    func testSlowSnapshotReadKeepsTheCopyQuietWhileTheRelaySaysOnline() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = makeStore(directory: directory, hub: SlowHub(), presence: { true })
        await store.refresh()
        XCTAssertFalse(store.hubUnreachable)
    }

    @MainActor
    func testSnapshotReadIsAsleepOnlyWhenTheRelaySaysTheMacIsOffline() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = makeStore(directory: directory, hub: SlowHub(), presence: { false })
        await store.refresh()
        XCTAssertTrue(store.hubUnreachable)
    }

    @MainActor
    func testLatestBriefBodyIsRetriedUntilItArrives() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let bodies = FakeBriefBodies(savedProbe: { _, _ in false })
        await bodies.fail("latest", times: 1)
        await bodies.serveLegacy("latest", MobileBriefV1(briefId: "latest", asOf: "2026-10-28T13:00:00Z",
                                                         narrativeStatus: "ok",
                                                         narrative: [BriefParagraph(text: "Good morning. The day is planned.")]))
        let store = makeStore(directory: directory, hub: SlowHub(), bodies: bodies)
        var value = snapshot()
        value.latestBrief = TodayBriefIndex(id: "latest", slot: "morning", localDate: "2026-10-28", scheduledAt: nil,
                                            madeAt: "2026-10-28T13:00:00Z", catchUp: nil, durationSeconds: nil,
                                            lead: "Good morning.")
        // The latest brief is named once, as the latest; the library's fetch retries it until it arrives.
        value.briefs = []
        store.accept(value)
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
        let arrived = await awaitCondition(timeout: 15) { cache.load(identifier: "latest") != nil }
        XCTAssertTrue(arrived)
        let requests = await bodies.legacyRequests
        XCTAssertEqual(requests, ["latest", "latest"], "one failed attempt, then the body")
    }

    func testNeedsYouRowsAreEmptyWhenNothingIsLeftToShow() throws {
        XCTAssertTrue(FeedView.needsYouRows([], pendingIDs: []).isEmpty)
        let json = #"{"id":"m1","chatId":"chat","card":{"proactive":{"momentId":"m1","kind":"reminder","state":"pending","actionIds":["done"]}}}"#
        let need = try JSONDecoder().decode(TodayNeed.self, from: Data(json.utf8))
        // A row whose write is in flight is not shown, so the section hides with it.
        XCTAssertTrue(FeedView.needsYouRows([need], pendingIDs: ["m1"]).isEmpty)
        XCTAssertEqual(FeedView.needsYouRows([need], pendingIDs: []).count, 1)
    }
}
