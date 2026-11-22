import Foundation
import XCTest
@testable import Yansu

// Covers the local brief library that BriefStore reads and writes: round trip, as-of ordering,
// 30-day text retention, and file naming that cannot escape the library folder.
final class BriefStoreTests: XCTestCase {
    private let fixedNow = RelayTime.parse("2026-10-20T12:00:00Z")!

    private func brief(id: String, asOf: String) -> MobileBriefV1 {
        MobileBriefV1(briefId: id, slot: "morning", localDate: String(asOf.prefix(10)), asOf: asOf,
                      hubName: "Test Mac", narrativeStatus: "ok",
                      narrative: [BriefParagraph(text: "A full sentence about the day.")])
    }

    private func makeCache(now: Date? = nil) throws -> (BriefCache, URL) {
        let directory = try makeTemporaryDirectory()
        let date = now ?? fixedNow
        return (BriefCache(directory: directory, now: { date }), directory)
    }

    func testCacheRoundTripKeepsTheBriefAndItsRelayVersion() throws {
        let (cache, _) = try makeCache()
        let saved = brief(id: "2026-10-19-morning", asOf: "2026-10-19T07:00:00Z")
        try cache.save(saved, relayVersion: 42)
        let loaded = try XCTUnwrap(cache.load(identifier: "2026-10-19-morning"))
        XCTAssertEqual(loaded.brief, saved)
        XCTAssertEqual(loaded.relayVersion, 42)
        XCTAssertNil(cache.load(identifier: "2026-10-18-morning"))
    }

    func testVersionSurvivesIdenticalRelayCopyButNotChangedContent() throws {
        let (cache, _) = try makeCache()
        var saved = brief(id: "brief", asOf: "2026-10-19T07:00:00Z")
        saved.narrative = [BriefParagraph(text: "First version")]
        try cache.save(saved, version: "v1")
        try cache.save(saved, relayVersion: 42)
        XCTAssertEqual(cache.load(identifier: "brief")?.version, "v1")
        saved.narrative = [BriefParagraph(text: "Updated")]
        try cache.save(saved, relayVersion: 43)
        XCTAssertNil(cache.load(identifier: "brief")?.version)
    }

    func testLatestIsTheBriefWithTheNewestAsOf() throws {
        let (cache, _) = try makeCache()
        try cache.save(brief(id: "2026-10-18-evening", asOf: "2026-10-18T18:00:00Z"))
        try cache.save(brief(id: "2026-10-19-morning", asOf: "2026-10-19T07:00:00Z"))
        try cache.save(brief(id: "2026-10-17-morning", asOf: "2026-10-17T07:00:00Z"))
        XCTAssertEqual(cache.all().first?.brief.identifier, "2026-10-19-morning")
        XCTAssertEqual(cache.all().map(\.brief.identifier),
                       ["2026-10-19-morning", "2026-10-18-evening", "2026-10-17-morning"])
    }

    func testAsOfParsesRelayTimesWithAndWithoutFractionalSeconds() {
        XCTAssertNotNil(brief(id: "a", asOf: "2026-10-09T07:00:12Z").asOfDate)
        XCTAssertNotNil(RelayTime.parse("2026-10-09T07:00:12.250Z"))
        XCTAssertNil(RelayTime.parse("not a time"))
    }

    func testBriefsOlderThanThirtyDaysAreRemovedOnWrite() throws {
        let (cache, _) = try makeCache()
        try cache.save(brief(id: "2026-09-10-morning", asOf: "2026-09-10T07:00:00Z"))
        try cache.save(brief(id: "2026-10-01-morning", asOf: "2026-10-01T07:00:00Z"))
        try cache.save(brief(id: "2026-10-19-morning", asOf: "2026-10-19T07:00:00Z"))
        XCTAssertNil(cache.load(identifier: "2026-09-10-morning"))
        XCTAssertNotNil(cache.load(identifier: "2026-10-01-morning"))
        XCTAssertNotNil(cache.load(identifier: "2026-10-19-morning"))
        XCTAssertEqual(cache.all().count, 2)
    }

    func testExplicitPruneRemovesEntriesPastRetention() throws {
        let earlier = RelayTime.parse("2026-09-01T12:00:00Z")!
        let directory = try makeTemporaryDirectory()
        let writer = BriefCache(directory: directory, now: { earlier })
        try writer.save(brief(id: "2026-09-01-morning", asOf: "2026-09-01T07:00:00Z"))
        let reader = BriefCache(directory: directory, now: { self.fixedNow })
        try reader.prune()
        XCTAssertTrue(reader.all().isEmpty)
    }

    func testFileNamesAreHashedSoBriefIdsCannotEscapeTheFolder() throws {
        let (cache, directory) = try makeCache()
        try cache.save(brief(id: "../../escape", asOf: "2026-10-19T07:00:00Z"))
        let files = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
        XCTAssertEqual(files.count, 1)
        XCTAssertEqual(files.first?.pathExtension, "json")
        XCTAssertFalse(files.first?.lastPathComponent.contains("escape") ?? true)
        XCTAssertEqual(cache.load(identifier: "../../escape")?.brief.briefId, "../../escape")
    }

    func testRemoveAllDeletesEveryCachedBrief() throws {
        let (cache, directory) = try makeCache()
        try cache.save(brief(id: "2026-10-19-morning", asOf: "2026-10-19T07:00:00Z"))
        try cache.removeAll()
        XCTAssertFalse(FileManager.default.fileExists(atPath: directory.path))
        XCTAssertNil(cache.all().first)
    }

    func testFailedRemovalHidesBriefsAndRefusesWritesUntilCleanupSucceeds() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        var blocked = false
        let cache = BriefCache(directory: directory, now: { self.fixedNow }, removeItem: { url in
            if blocked { throw YansuError.message("Injected removal failure") }
            try FileManager.default.removeItem(at: url)
        })
        let old = brief(id: "old", asOf: "2026-10-19T07:00:00Z")
        let new = brief(id: "new", asOf: "2026-10-20T07:00:00Z")
        try cache.save(old)
        blocked = true

        XCTAssertThrowsError(try cache.removeAll())
        XCTAssertNil(cache.load(identifier: old.identifier))
        XCTAssertNil(cache.all().first)
        XCTAssertTrue(cache.all().isEmpty)
        XCTAssertThrowsError(try cache.save(new))
        XCTAssertThrowsError(try cache.prune())

        blocked = false
        try cache.save(new)
        XCTAssertNil(cache.load(identifier: old.identifier))
        XCTAssertEqual(cache.all().first?.brief, new)
    }

    func testIdentifierFallsBackToDateAndSlotWhenBriefIdIsMissing() {
        let anonymous = MobileBriefV1(slot: "evening", localDate: "2026-10-09")
        XCTAssertEqual(anonymous.identifier, "2026-10-09-evening")
    }
}

@MainActor
final class AppEnvironmentPairingIsolationTests: XCTestCase {
    private struct Fixture {
        let directory: URL
        let secrets: InMemorySecretStore
        let defaults: UserDefaults
        let transport: StubPairingTransport
        let widgetStore: WidgetSnapshotStore
        let environment: AppEnvironment

        var vaultDirectory: URL { directory.appendingPathComponent("vault", isDirectory: true) }
        var briefDirectory: URL { directory.appendingPathComponent("briefs", isDirectory: true) }
        var legacyBriefDirectory: URL { directory.appendingPathComponent("legacy-briefs", isDirectory: true) }
        var chatDirectory: URL { directory.appendingPathComponent("chats", isDirectory: true) }
        var journalURL: URL { directory.appendingPathComponent("journal.json") }
    }

    func testPairingWithAnotherMacClearsThePreviousMacsCachesAndVisibleState() async throws {
        let fixture = try makeFixture()
        let firstHub = TestHub()
        try await pair(fixture, hub: firstHub)
        let saved = try await populateLocalState(fixture)
        XCTAssertEqual(fixture.environment.brief.current, saved)
        XCTAssertEqual(fixture.environment.brief.history.map(\.id), [saved.identifier])
        XCTAssertEqual(fixture.environment.brief.cachedBriefs, [saved], "the Feed reads the cached briefs")
        fixture.environment.feed.setInterest("AI agents")
        XCTAssertNotNil(ChatCache(directory: fixture.chatDirectory).load(chatID: "chat-a"))
        XCTAssertNotNil(fixture.environment.notifications.failure)
        XCTAssertNotNil(fixture.widgetStore.read())

        let secondHub = TestHub()
        try await pair(fixture, hub: secondHub)

        XCTAssertEqual(fixture.environment.vault.current()?.hubID, secondHub.hubID)
        assertLocalStateCleared(fixture.environment, fixture: fixture)
        // Any widget publication already queued for the old brief must remain discarded.
        await Task.yield()
        XCTAssertNil(fixture.widgetStore.read())
    }

    func testPairingAgainWithTheSameMacPreservesItsCachedContent() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        let originalDeviceID = fixture.environment.vault.current()?.deviceID
        let saved = try await populateLocalState(fixture)

        try await pair(fixture, hub: hub)

        XCTAssertEqual(fixture.environment.vault.current()?.hubID, hub.hubID)
        XCTAssertNotEqual(fixture.environment.vault.current()?.deviceID, originalDeviceID)
        XCTAssertEqual(fixture.environment.cache.all().first?.brief, saved)
        XCTAssertEqual(fixture.environment.brief.current, saved)
        XCTAssertEqual(fixture.environment.brief.history.map(\.id), [saved.identifier])
        XCTAssertEqual(ChatCache(directory: fixture.chatDirectory).load(chatID: "chat-a")?.items.first?.text,
                       "Content from Mac A")
        XCTAssertNotNil(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID))
        XCTAssertEqual(fixture.widgetStore.read()?.hubName, "Mac A")
    }

    func testDeletingCachedBriefsAlsoDeletesAudioAndStopsThePlayer() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        let saved = try await populateLocalState(fixture)
        await fixture.environment.audioPlayer.prepare(brief: saved, hubID: hub.hubID)
        XCTAssertEqual(fixture.environment.audioPlayer.state, .ready)

        fixture.environment.brief.deleteCachedBriefs()

        XCTAssertNil(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID))
        XCTAssertEqual(fixture.environment.audioPlayer.state, .idle)
        XCTAssertNil(fixture.environment.brief.current)
    }

    func testCachedHistoryKeepsDisplaySlotAndTimezone() async throws {
        let fixture = try makeFixture()
        try await pair(fixture, hub: TestHub())
        // At this UTC time the legacy clock says morning, but a configured slot says evening.
        // The day is yesterday, not a fixed date: the cache keeps a brief's text for 30 days, so a
        // brief dated 2026-10-20 left the history on 2026-11-20 and this test began to fail.
        let day = String(Date().addingTimeInterval(-24 * 60 * 60).ISO8601Format().prefix(10))
        let saved = MobileBriefV1(briefId: "requested", slot: "on_request", displaySlot: "evening",
            localDate: day, timezone: "America/Los_Angeles", asOf: "\(day)T09:00:00Z")
        try fixture.environment.cache.save(saved)
        fixture.environment.brief.loadCached()
        let entry = try XCTUnwrap(fixture.environment.brief.history.first)
        XCTAssertEqual(entry.slot, "on_request")
        XCTAssertEqual(entry.displaySlot, "evening")
        XCTAssertEqual(entry.timezone, "America/Los_Angeles")
        XCTAssertEqual(entry.slotLabel, saved.slotLabel)
        var legacy = saved
        legacy.briefId = "legacy"
        legacy.displaySlot = nil
        try fixture.environment.cache.save(legacy)
        fixture.environment.brief.loadCached()
        let legacyEntry = try XCTUnwrap(fixture.environment.brief.history.first { $0.id == "legacy" })
        XCTAssertEqual(legacyEntry.slotLabel, legacy.slotLabel)
    }

    func testLoadingAReceivedWithdrawalInvalidatesTheSelectedRecording() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        let saved = try await populateLocalState(fixture)
        let recording = try XCTUnwrap(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID))
        try fixture.environment.audioCache.savePosition(7, brief: saved, hubID: hub.hubID)
        await fixture.environment.audioPlayer.prepare(brief: saved, hubID: hub.hubID)
        XCTAssertEqual(fixture.environment.audioPlayer.state, .ready)
        XCTAssertEqual(fixture.environment.audioPlayer.position, 7)

        var withdrawn = saved
        withdrawn.audio = nil
        withdrawn.narrativeStatus = "unavailable"
        try fixture.environment.cache.save(withdrawn, relayVersion: 8)
        fixture.environment.brief.loadCached()

        XCTAssertNil(fixture.environment.brief.current, "a withdrawn brief is never current")
        XCTAssertEqual(fixture.environment.audioPlayer.state, .unavailable(.notAvailable))
        XCTAssertEqual(fixture.environment.audioPlayer.position, 0)
        XCTAssertEqual(fixture.environment.audioPlayer.duration, 0)
        XCTAssertNil(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID))
        XCTAssertFalse(FileManager.default.fileExists(atPath: recording.url.path))
        let reopened = BriefAudioCache(directory: fixture.vaultDirectory.appendingPathComponent("brief-audio"))
        XCTAssertNil(reopened.cached(brief: saved, hubID: hub.hubID),
                     "An older in-memory brief must not recover a recording withdrawn in the persisted brief cache")
    }

    func testLoadingCachedHistoryAppliesWithdrawalEvenWhenAnotherBriefIsLatest() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        let saved = try await populateLocalState(fixture)
        await fixture.environment.audioPlayer.prepare(brief: saved, hubID: hub.hubID)
        XCTAssertEqual(fixture.environment.audioPlayer.state, .ready)
        var withdrawn = saved
        withdrawn.audio = nil
        withdrawn.narrativeStatus = "unavailable"
        try fixture.environment.cache.save(withdrawn, relayVersion: 8)
        let timestamp = Date().addingTimeInterval(60).ISO8601Format()
        let latest = MobileBriefV1(briefId: "brief-b", slot: "evening", localDate: String(timestamp.prefix(10)),
                                   asOf: timestamp, hubName: "Mac A", narrativeStatus: "ok", narrative: [BriefParagraph(text: "A full sentence about the day.")])
        try fixture.environment.cache.save(latest, relayVersion: 9)

        fixture.environment.brief.loadCached()

        XCTAssertEqual(fixture.environment.brief.current, latest)
        XCTAssertTrue(fixture.environment.brief.cachedBriefs.contains(withdrawn))
        XCTAssertEqual(fixture.environment.audioPlayer.state, .unavailable(.notAvailable))
        XCTAssertNil(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID),
                     "Loading only the latest brief would leave a withdrawn history recording playable")
    }

    func testLoadingCachedWithdrawalPreservesAudioAuthorizedAfterThatWithdrawal() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        var withdrawn = try await populateLocalState(fixture)
        withdrawn.audio = nil
        withdrawn.narrativeStatus = "unavailable"
        try fixture.environment.cache.save(withdrawn, relayVersion: 8)
        fixture.environment.brief.loadCached()
        XCTAssertNil(fixture.environment.audioCache.cached(brief: withdrawn, hubID: hub.hubID))
        // A successful fresh metadata request and download can authorize audio again while
        // the local brief projection still contains the earlier withdrawal.
        let recording = try fixture.environment.audioCache.store(Data([1, 2, 3, 4]), brief: withdrawn,
            hubID: hub.hubID, audio: BriefAudio(mime: "audio/wav", bytes: 4, durationSeconds: 12),
            reauthorizedAfterWithdrawal: true)
        try fixture.environment.audioCache.savePosition(5, brief: withdrawn, hubID: hub.hubID)

        fixture.environment.brief.loadCached()
        await fixture.environment.audioPlayer.prepare(brief: withdrawn, hubID: hub.hubID)

        XCTAssertEqual(fixture.environment.audioPlayer.state, .ready)
        XCTAssertEqual(fixture.environment.audioPlayer.position, 5)
        XCTAssertEqual(fixture.environment.audioCache.cached(brief: withdrawn, hubID: hub.hubID)?.url, recording)
        let reopened = AppEnvironment(directory: fixture.vaultDirectory, secrets: fixture.secrets,
                                      briefDirectory: fixture.briefDirectory,
                                      legacyBriefDirectory: fixture.legacyBriefDirectory,
                                      journalURL: fixture.journalURL,
                                      chatDirectory: fixture.chatDirectory, widgetStore: fixture.widgetStore,
                                      pushDefaults: fixture.defaults, pairingTransport: fixture.transport)
        reopened.brief.loadCached()
        await reopened.audioPlayer.prepare(brief: withdrawn, hubID: hub.hubID)

        XCTAssertNil(reopened.brief.current, "a withdrawn brief is never current")
        XCTAssertEqual(reopened.audioPlayer.state, .ready,
                       "Reopening offline must retain the recording authorized after the cached withdrawal")
        XCTAssertEqual(reopened.audioPlayer.position, 5)
        XCTAssertEqual(reopened.audioCache.cached(brief: withdrawn, hubID: hub.hubID)?.url, recording)
    }

    func testAcceptedEncryptedWithdrawalInvalidatesAudioBeforeTheBriefIsReopened() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        let saved = try await populateLocalState(fixture)
        try fixture.environment.audioCache.store(Data([1, 2, 3, 4]), brief: saved, hubID: hub.hubID,
            audio: BriefAudio(mime: "audio/wav", bytes: 4, durationSeconds: 12), reauthorizedAfterWithdrawal: true)
        await fixture.environment.audioPlayer.prepare(brief: saved, hubID: hub.hubID)
        XCTAssertEqual(fixture.environment.audioPlayer.state, .ready)
        var withdrawn = saved
        withdrawn.audio = nil
        withdrawn.narrativeStatus = "unavailable"
        let session = try fixture.environment.vault.session()
        let ciphertext = try RelayCrypto.sealEnvelope(RelayCrypto.padBrief(JSONEncoder().encode(withdrawn)),
            key: session.keys.h2d, hub: session.hubID, dev: session.deviceID, dir: "h2d", seq: 8, kind: "brief")
        let envelope = RelayEnvelope(v: 1, hub: session.hubID, dev: session.deviceID, dir: "h2d", seq: 8,
                                     kind: "brief", ct: RelayCrypto.base64URLEncode(ciphertext))
        let envelopeObject = try JSONSerialization.jsonObject(with: JSONEncoder().encode(envelope))
        let latestResponse = try JSONSerialization.data(withJSONObject: ["ok": true, "data": [
            "version": 8, "asOf": try XCTUnwrap(saved.asOf), "env": envelopeObject,
        ]])
        StubURLProtocol.install { request, _ in
            let response: Data
            switch request.url?.path {
            case "/v1/presence":
                response = Data("{\"ok\":true,\"data\":{\"hubOnline\":true}}".utf8)
            case "/v1/brief/latest":
                response = latestResponse
            default:
                throw URLError(.unsupportedURL)
            }
            return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1",
                                     headerFields: nil)!, response)
        }
        let store = BriefStore(pairing: fixture.environment.pairing, vault: fixture.environment.vault,
                               cache: fixture.environment.cache, hub: fixture.environment.hub,
                               relay: RelayClient(session: StubURLProtocol.session()))
        store.onBriefReceived = fixture.environment.brief.onBriefReceived

        await store.refresh()

        XCTAssertNil(store.current, "a withdrawn brief is never current")
        XCTAssertEqual(fixture.environment.vault.lastAcceptedBrief(), 8)
        XCTAssertEqual(fixture.environment.audioPlayer.state, .unavailable(.notAvailable))
        XCTAssertNil(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID),
                     "A fresh withdrawal overrides authorization obtained before that receipt")
        XCTAssertEqual(StubURLProtocol.requests.compactMap { $0.0.url?.path },
                       ["/v1/presence", "/v1/brief/latest"])
    }

    func testScopeDowngradeReadsTheCommittedVaultBeforeThePairingViewUpdates() async throws {
        let fixture = try makeFixture()
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        let saved = try await populateLocalState(fixture)
        var committed = try XCTUnwrap(fixture.environment.vault.current())
        committed.scopes.removeAll { $0 == "brief:read" }
        let keys = RelayCrypto.makeDeviceKeyPair()
        try fixture.environment.vault.store(committed,
            devicePrivateKey: RelayCrypto.base64URLEncode(keys.privateKey), bearer: "replacement-bearer")
        XCTAssertTrue(fixture.environment.pairing.paired?.scopes.contains("brief:read") == true)

        fixture.environment.pairing.onPairingCommitted?()

        XCTAssertNil(fixture.environment.audioCache.cached(brief: saved, hubID: hub.hubID))
        XCTAssertEqual(fixture.environment.audioPlayer.state, .idle)
    }

    func testFailedAudioCleanupReportsFailureAndKeepsTheBriefAvailableForRetry() async throws {
        let fixture = try makeFixture()
        try await pair(fixture, hub: TestHub())
        let saved = try await populateLocalState(fixture)
        fixture.environment.brief.onDeleteCachedBriefs = { false }

        fixture.environment.brief.deleteCachedBriefs()

        XCTAssertEqual(fixture.environment.brief.current, saved)
        XCTAssertEqual(fixture.environment.brief.notice,
                       "Isoform could not delete the saved brief. Unlock this phone and try again.")
    }

    func testReopeningAfterPairingCommitBeforeCleanupClearsTheOldHubCaches() async throws {
        let fixture = try makeFixture()
        try await pair(fixture, hub: TestHub())
        _ = try await populateLocalState(fixture)
        let replacement = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: replacement.hubID, hubName: "Mac B",
                                  hubPublicKey: replacement.hubPublicText, relayOrigin: replacement.relay,
                                  deviceID: UUID().uuidString.lowercased(), deviceName: "Test iPhone",
                                  scopes: PairingStore.requestedScopes, pairedAt: Date(), counters: SequenceCounters())
        // Commit directly to simulate termination before PairingStore calls onPairingCommitted.
        try fixture.environment.vault.store(record,
            devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey), bearer: "replacement-bearer")
        XCTAssertNotNil(fixture.environment.cache.all().first)
        XCTAssertNotNil(fixture.widgetStore.read())

        let reopened = AppEnvironment(directory: fixture.vaultDirectory, secrets: fixture.secrets,
                                      briefDirectory: fixture.briefDirectory,
                                      legacyBriefDirectory: fixture.legacyBriefDirectory,
                                      journalURL: fixture.journalURL,
                                      chatDirectory: fixture.chatDirectory, widgetStore: fixture.widgetStore,
                                      pushDefaults: fixture.defaults, pairingTransport: fixture.transport)
        reopened.brief.loadCached()

        XCTAssertEqual(reopened.pairing.paired?.hubID, replacement.hubID)
        assertLocalStateCleared(reopened, fixture: fixture)
        await Task.yield()
        XCTAssertNil(fixture.widgetStore.read())
    }

    func testUnreadablePairingPreservesCachesAndCanRecoverWithoutPairingAgain() async throws {
        let fixture = try makeFixture()
        try await pair(fixture, hub: TestHub())
        let savedBrief = try await populateLocalState(fixture)
        let originalDevice = try XCTUnwrap(fixture.environment.vault.current())
        let recordURL = fixture.vaultDirectory.appendingPathComponent("pairing.json")
        let record = try Data(contentsOf: recordURL)
        let markerURL = fixture.vaultDirectory.appendingPathComponent("local-hub.json")
        let marker = try Data(contentsOf: markerURL)
        let noticeSecrets = SharedNoticeStore.load(secrets: fixture.secrets)
        try Data("unreadable pairing from a newer build".utf8).write(to: recordURL)

        let reopened = AppEnvironment(directory: fixture.vaultDirectory, secrets: fixture.secrets,
                                      briefDirectory: fixture.briefDirectory,
                                      legacyBriefDirectory: fixture.legacyBriefDirectory,
                                      journalURL: fixture.journalURL,
                                      chatDirectory: fixture.chatDirectory, widgetStore: fixture.widgetStore,
                                      pushDefaults: fixture.defaults, pairingTransport: fixture.transport)
        reopened.brief.loadCached()
        let message = try XCTUnwrap(reopened.pairing.recoveryMessage)
        XCTAssertEqual(reopened.pairing.phase, .failed(message))
        XCTAssertFalse(reopened.pairing.isPaired)
        reopened.pairing.prepareToPair()
        reopened.pairing.cancel()
        reopened.pairing.acknowledgeOutcome()
        XCTAssertEqual(reopened.pairing.phase, .failed(message))
        XCTAssertNil(reopened.pairing.begin(invitation: "isoform://pair#ignored"))
        XCTAssertEqual(reopened.brief.notice, message)
        XCTAssertEqual(reopened.cache.all().first?.brief, savedBrief)
        XCTAssertNotNil(ChatCache(directory: fixture.chatDirectory).load(chatID: "chat-a"))
        XCTAssertEqual(fixture.widgetStore.read()?.hubName, "Mac A")
        XCTAssertEqual(try Data(contentsOf: markerURL), marker)
        XCTAssertEqual(SharedNoticeStore.load(secrets: fixture.secrets), noticeSecrets)

        // Recovery also finishes notice provisioning if approval committed before that step ran.
        try fixture.secrets.delete(SharedNoticeAccount.key)
        try record.write(to: recordURL)
        reopened.retrySavedPairing()

        XCTAssertNil(reopened.pairing.recoveryMessage)
        XCTAssertEqual(reopened.pairing.phase, .paired)
        XCTAssertEqual(reopened.pairing.paired?.deviceID, originalDevice.deviceID)
        XCTAssertEqual(reopened.brief.current, savedBrief)
        XCTAssertNil(reopened.brief.notice)
        XCTAssertEqual(try reopened.vault.session().deviceID, originalDevice.deviceID)
        XCTAssertTrue(reopened.vault.hasNoticeSecrets())
        XCTAssertEqual(fixture.transport.claims.count, 1, "Recovery does not send another pairing claim")
    }

    func testRemovingAnUnreadableSavedPairingClearsLocalStateAndAllowsANewClaim() async throws {
        let fixture = try makeFixture()
        try await pair(fixture, hub: TestHub())
        let savedBrief = try await populateLocalState(fixture)
        let savedAccounts = fixture.secrets.accounts
        XCTAssertFalse(savedAccounts.isEmpty)
        let recordURL = fixture.vaultDirectory.appendingPathComponent("pairing.json")
        let corruptRecord = Data("unreadable saved pairing".utf8)
        try corruptRecord.write(to: recordURL)
        let reopened = AppEnvironment(directory: fixture.vaultDirectory, secrets: fixture.secrets,
                                      briefDirectory: fixture.briefDirectory,
                                      legacyBriefDirectory: fixture.legacyBriefDirectory,
                                      journalURL: fixture.journalURL,
                                      chatDirectory: fixture.chatDirectory, widgetStore: fixture.widgetStore,
                                      pushDefaults: fixture.defaults, pairingTransport: fixture.transport)
        reopened.brief.loadCached()

        // Until the destructive action is confirmed, entering or leaving recovery removes nothing.
        let recoveryMessage = try XCTUnwrap(reopened.pairing.recoveryMessage)
        reopened.pairing.cancel()
        XCTAssertEqual(reopened.pairing.phase, .failed(recoveryMessage))
        XCTAssertEqual(try Data(contentsOf: recordURL), corruptRecord)
        XCTAssertEqual(fixture.secrets.accounts, savedAccounts)
        XCTAssertEqual(reopened.cache.all().first?.brief, savedBrief)
        XCTAssertNotNil(ChatCache(directory: fixture.chatDirectory).load(chatID: "chat-a"))
        XCTAssertNotNil(fixture.widgetStore.read())
        XCTAssertEqual(fixture.transport.claims.count, 1)

        try reopened.forgetPairingLocally()

        XCTAssertNil(reopened.vault.current())
        XCTAssertNil(reopened.vault.loadError)
        XCTAssertFalse(FileManager.default.fileExists(atPath: recordURL.path))
        XCTAssertTrue(fixture.secrets.accounts.isEmpty)
        XCTAssertNil(SharedNoticeStore.load(secrets: fixture.secrets))
        XCTAssertNil(reopened.pairing.paired)
        XCTAssertNil(reopened.pairing.recoveryMessage)
        XCTAssertEqual(reopened.pairing.phase, .idle)
        assertLocalStateCleared(reopened, fixture: fixture)
        let markerURL = fixture.vaultDirectory.appendingPathComponent("local-hub.json")
        let marker = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any])
        XCTAssertNil(marker["hubID"])
        XCTAssertEqual(marker["needsCleanup"] as? Bool, false)

        let newHub = TestHub()
        fixture.transport.script([{ claim in try newHub.approval(for: claim) }])
        let expiry = Int64(Date().addingTimeInterval(600).timeIntervalSince1970)
        let claim = try XCTUnwrap(reopened.pairing.begin(invitation: newHub.invitationText(exp: expiry)))
        await claim.value

        XCTAssertEqual(reopened.pairing.phase, .paired)
        XCTAssertEqual(reopened.vault.current()?.hubID, newHub.hubID)
        XCTAssertEqual(try reopened.vault.session().deviceID, reopened.pairing.paired?.deviceID)
        XCTAssertNil(reopened.pairing.recoveryMessage)
        XCTAssertEqual(fixture.transport.claims.count, 2)
    }

    func testLocalRemovalRetriesFailedCacheCleanupBeforePairingWithTheSameMac() async throws {
        var blocked = false
        let fixture = try makeFixture(removeLocalItem: { url in
            if blocked && url.lastPathComponent == "chats" {
                throw YansuError.message("Injected removal failure")
            }
            try FileManager.default.removeItem(at: url)
        })
        let hub = TestHub()
        try await pair(fixture, hub: hub)
        _ = try await populateLocalState(fixture)
        blocked = true

        try fixture.environment.forgetPairingLocally()

        XCTAssertNil(fixture.environment.pairing.paired)
        let markerURL = fixture.vaultDirectory.appendingPathComponent("local-hub.json")
        let pending = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any])
        XCTAssertEqual(pending["needsCleanup"] as? Bool, true)
        blocked = false

        try await pair(fixture, hub: hub)

        assertLocalStateCleared(fixture.environment, fixture: fixture)
        let completed = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any])
        XCTAssertEqual(completed["hubID"] as? String, hub.hubID)
        XCTAssertEqual(completed["needsCleanup"] as? Bool, false)
    }

    func testLegacyBriefsAreClearedBeforeTheFirstHubMarkerIsSaved() throws {
        let timestamp = Date().ISO8601Format()
        let oldBrief = MobileBriefV1(briefId: "legacy-a", slot: "morning", localDate: String(timestamp.prefix(10)),
                                     asOf: timestamp, hubName: "Old Mac", narrativeStatus: "ok", narrative: [BriefParagraph(text: "A full sentence about the day.")])
        let fixture = try makeFixture(legacyBrief: oldBrief)

        XCTAssertNil(BriefCache(directory: fixture.legacyBriefDirectory).all().first, "the legacy file was migrated")
        XCTAssertNil(fixture.environment.cache.all().first, "unscoped migrated files are cleared by the first hub check")
        XCTAssertTrue(FileManager.default.fileExists(atPath:
            fixture.vaultDirectory.appendingPathComponent("local-hub.json").path))
    }

    func testAnExistingHubMarkerPreventsLegacyBriefsFromReturning() async throws {
        let fixture = try makeFixture()
        try await pair(fixture, hub: TestHub())
        let saved = try await populateLocalState(fixture)
        let legacyCache = BriefCache(directory: fixture.legacyBriefDirectory)
        let timestamp = Date().addingTimeInterval(60).ISO8601Format()
        let oldBrief = MobileBriefV1(briefId: "legacy-other-mac", slot: "morning", localDate: String(timestamp.prefix(10)),
                                     asOf: timestamp, hubName: "Other Mac", narrativeStatus: "ok", narrative: [BriefParagraph(text: "A full sentence about the day.")])
        try legacyCache.save(oldBrief)

        let reopened = AppEnvironment(directory: fixture.vaultDirectory, secrets: fixture.secrets,
                                      briefDirectory: fixture.briefDirectory,
                                      legacyBriefDirectory: fixture.legacyBriefDirectory,
                                      journalURL: fixture.journalURL,
                                      chatDirectory: fixture.chatDirectory, widgetStore: fixture.widgetStore,
                                      pushDefaults: fixture.defaults, pairingTransport: fixture.transport)
        reopened.brief.loadCached()

        XCTAssertEqual(reopened.brief.current, saved)
        XCTAssertEqual(reopened.brief.history.map(\.id), [saved.identifier])
        XCTAssertNil(reopened.cache.load(identifier: oldBrief.identifier))
        XCTAssertEqual(legacyCache.all().first?.brief, oldBrief, "a scoped environment never imports an unscoped cache")
    }

    func testAFailedCacheClearKeepsTheOldHubMarkerUntilCleanupSucceeds() async throws {
        for failedItem in ["briefs", "chats", "widget.json"] {
            var blocked = false
            let removeItem: (URL) throws -> Void = { url in
                if blocked && url.lastPathComponent == failedItem {
                    throw YansuError.message("Injected removal failure")
                }
                try FileManager.default.removeItem(at: url)
            }
            let fixture = try makeFixture(removeLocalItem: removeItem)
            try await pair(fixture, hub: TestHub())
            _ = try await populateLocalState(fixture)
            let markerURL = fixture.vaultDirectory.appendingPathComponent("local-hub.json")
            let oldMarker = try Data(contentsOf: markerURL)
            let oldHubID = fixture.environment.vault.current()?.hubID
            blocked = true

            try await pair(fixture, hub: TestHub())
            let pending = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any])
            XCTAssertEqual(pending["hubID"] as? String, oldHubID, failedItem)
            XCTAssertEqual(pending["needsCleanup"] as? Bool, true, failedItem)
            fixture.environment.brief.loadCached()
            XCTAssertNil(fixture.environment.brief.current, failedItem)
            XCTAssertNil(fixture.environment.cache.all().first, failedItem)

            blocked = false
            let reopened = AppEnvironment(directory: fixture.vaultDirectory, secrets: fixture.secrets,
                                          briefDirectory: fixture.briefDirectory,
                                          legacyBriefDirectory: fixture.legacyBriefDirectory,
                                          journalURL: fixture.journalURL,
                                          chatDirectory: fixture.chatDirectory, widgetStore: fixture.widgetStore,
                                          pushDefaults: fixture.defaults, pairingTransport: fixture.transport,
                                          removeLocalItem: removeItem)
            reopened.brief.loadCached()

            XCTAssertNotEqual(try Data(contentsOf: markerURL), oldMarker, failedItem)
            assertLocalStateCleared(reopened, fixture: fixture)
        }
    }

    func testReturningToTheOriginalMacAfterPartialCleanupStillClearsTheOtherMacsBrief() async throws {
        var blocked = false
        let fixture = try makeFixture(removeLocalItem: { url in
            if blocked && url.lastPathComponent == "chats" {
                throw YansuError.message("Injected removal failure")
            }
            try FileManager.default.removeItem(at: url)
        })
        let firstHub = TestHub()
        try await pair(fixture, hub: firstHub)
        _ = try await populateLocalState(fixture)
        blocked = true
        try await pair(fixture, hub: TestHub())
        let timestamp = Date().ISO8601Format()
        let secondBrief = MobileBriefV1(briefId: "brief-b", slot: "morning", localDate: String(timestamp.prefix(10)),
                                        asOf: timestamp, hubName: "Mac B", narrativeStatus: "ok", narrative: [BriefParagraph(text: "A full sentence about the day.")])
        try fixture.environment.cache.save(secondBrief)
        fixture.environment.brief.loadCached()
        XCTAssertEqual(fixture.environment.brief.current, secondBrief)

        try await pair(fixture, hub: firstHub)

        XCTAssertNil(fixture.environment.cache.all().first)
        XCTAssertNil(fixture.environment.brief.current)
        let markerURL = fixture.vaultDirectory.appendingPathComponent("local-hub.json")
        let pending = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: markerURL)) as? [String: Any])
        XCTAssertEqual(pending["hubID"] as? String, firstHub.hubID)
        XCTAssertEqual(pending["needsCleanup"] as? Bool, true)
    }

    private func makeFixture(legacyBrief: MobileBriefV1? = nil,
                             removeLocalItem: @escaping (URL) throws -> Void = {
                                 try FileManager.default.removeItem(at: $0)
                             }) throws -> Fixture {
        let directory = try makeTemporaryDirectory()
        let suite = "yansu.tests.hub-isolation.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        addTeardownBlock {
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: directory)
        }
        let secrets = InMemorySecretStore()
        let transport = StubPairingTransport()
        let widgetStore = WidgetSnapshotStore(fileURL: directory.appendingPathComponent("widget.json"))
        let legacyDirectory = directory.appendingPathComponent("legacy-briefs", isDirectory: true)
        if let legacyBrief {
            try BriefCache(directory: legacyDirectory).save(legacyBrief)
        }
        let environment = AppEnvironment(directory: directory.appendingPathComponent("vault", isDirectory: true),
                                         secrets: secrets,
                                         briefDirectory: directory.appendingPathComponent("briefs", isDirectory: true),
                                         legacyBriefDirectory: legacyDirectory,
                                         journalURL: directory.appendingPathComponent("journal.json"),
                                         chatDirectory: directory.appendingPathComponent("chats", isDirectory: true),
                                         widgetStore: widgetStore, pushDefaults: defaults, pairingTransport: transport,
                                         removeLocalItem: removeLocalItem)
        return Fixture(directory: directory, secrets: secrets, defaults: defaults, transport: transport,
                       widgetStore: widgetStore, environment: environment)
    }

    private func pair(_ fixture: Fixture, hub: TestHub) async throws {
        fixture.transport.script([{ claim in try hub.approval(for: claim) }])
        let expiry = Int64(Date().addingTimeInterval(600).timeIntervalSince1970)
        let task = try XCTUnwrap(fixture.environment.pairing.begin(invitation: hub.invitationText(exp: expiry)))
        await task.value
        XCTAssertEqual(fixture.environment.pairing.phase, .paired)
        XCTAssertEqual(fixture.environment.vault.current()?.hubID, hub.hubID)
    }

    private func populateLocalState(_ fixture: Fixture) async throws -> MobileBriefV1 {
        let timestamp = Date().ISO8601Format()
        let brief = MobileBriefV1(briefId: "brief-a", slot: "morning", localDate: String(timestamp.prefix(10)),
                                  asOf: timestamp, hubName: "Mac A", narrativeStatus: "ok", narrative: [BriefParagraph(text: "A full sentence about the day.")])
        try fixture.environment.cache.save(brief, relayVersion: 7)
        fixture.environment.brief.loadCached()
        let hubID = try XCTUnwrap(fixture.environment.vault.current()?.hubID)
        try fixture.environment.audioCache.store(Data([1, 2, 3, 4]), brief: brief, hubID: hubID,
                                                  audio: BriefAudio(mime: "audio/wav", bytes: 4, durationSeconds: 12))
        fixture.environment.brief.notice = "Brief notice from Mac A"
        fixture.environment.chat.notice = "Chat notice from Mac A"
        fixture.environment.actions.notice = "Action notice from Mac A"
        let item = ChatItemView(seq: 1, role: .assistant, kind: .text, text: "Content from Mac A",
                                toolName: "", at: timestamp)
        try ChatCache(directory: fixture.chatDirectory).save(
            ChatHeaderView(id: "chat-a", title: "Mac A chat", state: .idle), items: [item])
        // Route validation fails locally, seeding a settings notice without a relay request.
        await fixture.environment.notifications.setEnabled("invalid/kind", false)
        try fixture.widgetStore.write(WidgetSnapshot(generatedAt: Date(), hubName: "Mac A",
                                                     lastBriefAt: brief.asOfDate))
        return brief
    }

    private func assertLocalStateCleared(_ environment: AppEnvironment, fixture: Fixture,
                                         file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertNil(environment.cache.all().first, file: file, line: line)
        XCTAssertFalse(FileManager.default.fileExists(atPath:
            fixture.vaultDirectory.appendingPathComponent("brief-audio").path), file: file, line: line)
        XCTAssertNil(environment.brief.current, file: file, line: line)
        XCTAssertTrue(environment.brief.history.isEmpty, file: file, line: line)
        XCTAssertTrue(environment.brief.cachedBriefs.isEmpty, file: file, line: line)
        XCTAssertNil(environment.feed.interest, file: file, line: line)
        XCTAssertNil(environment.brief.notice, file: file, line: line)
        XCTAssertNil(ChatCache(directory: fixture.chatDirectory).load(chatID: "chat-a"), file: file, line: line)
        XCTAssertNil(environment.chat.notice, file: file, line: line)
        XCTAssertNil(environment.actions.notice, file: file, line: line)
        XCTAssertNil(environment.notifications.failure, file: file, line: line)
        XCTAssertEqual(environment.notifications.loadState, .idle, file: file, line: line)
        XCTAssertNil(fixture.widgetStore.read(), file: file, line: line)
    }
}

@MainActor
final class LibraryStoreTests: XCTestCase {
    private struct Fixture {
        let vault: PairingVault
        let pairing: PairingStore
        let cache: BriefCache
        let brief: BriefStore
        let api: LibraryHistoryStub
        let library: LibraryStore
    }

    private let timestamp = "2026-10-20T12:00:00Z"

    func testFailedCacheDeletionKeepsDisplayedBriefAndReportsFailure() throws {
        let fixture = try makeFixture(removeItem: { _ in throw CocoaError(.fileWriteNoPermission) })
        let saved = MobileBriefV1(briefId: "brief", slot: "morning", localDate: "2026-10-20", asOf: timestamp,
                                  narrativeStatus: "ok", narrative: [BriefParagraph(text: "Today")])
        try fixture.cache.save(saved)
        fixture.brief.loadCached()
        XCTAssertEqual(fixture.brief.current, saved)
        fixture.brief.deleteCachedBriefs()
        XCTAssertEqual(fixture.brief.current, saved)
        XCTAssertNotNil(fixture.brief.notice)
    }

    func testDaysUseComputerLocalDateAndSummariesUseNarrativeOnly() {
        let first = MobileBriefV1(briefId: "a", localDate: "2026-10-21", asOf: timestamp,
                                  narrativeStatus: "ok", narrative: [BriefParagraph(text: "  "),
                                      BriefParagraph(text: "Finish the proposal.\n Then follow up.")])
        let second = MobileBriefV1(briefId: "b", localDate: "2026-10-21", asOf: timestamp,
                                   narrativeStatus: "unavailable", narrative: [BriefParagraph(text: "Old summary")])
        let uncached = HistoryEntry(id: "remote", slot: "morning", localDate: "2026-10-19",
                                   asOf: nil, narrativeStatus: "ok", isCached: false)
        let projection = LibraryProjection(history: [entry(second), uncached, entry(first)], briefs: [second, first])
        XCTAssertEqual(projection.days.map(\.localDate), ["2026-10-21", "2026-10-19"])
        XCTAssertEqual(projection.days[0].entries.map(\.id), ["a", "b"], "ties have a stable order")
        XCTAssertEqual(projection.days[0].entries[0].summary, "Finish the proposal. Then follow up.")
        XCTAssertEqual(projection.days[0].entries[1].summary, "Summary unavailable.")
        XCTAssertEqual(projection.days[1].entries[0].summary, "Open this brief to read its summary.")
    }

    func testArtifactsExcludeObservedFilesAndPreserveDistinctEqualTitles() {
        let first = BriefItem(id: "one", title: "Report", detail: "Prepared for review", status: "completed",
                              refs: ["e1"], url: "file:///private/report.pdf")
        let second = BriefItem(id: "two", title: "Report", refs: ["e2"])
        let observed = BriefItem(id: "read", title: "A file the user opened", kind: "file")
        let brief = MobileBriefV1(briefId: "brief", localDate: "2026-10-20", asOf: timestamp,
                                  sections: BriefSections(doing: [observed], deliverables: [first, second]),
                                  evidence: [BriefEvidence(ref: "e1", sourceKind: "deliverable", sourceId: "one"),
                                             BriefEvidence(ref: "e2", sourceKind: "deliverable", sourceId: "two")])
        let projection = LibraryProjection(history: [], briefs: [brief])
        XCTAssertEqual(projection.artifacts.map(\.id), ["deliverable:one", "deliverable:two"])
        XCTAssertEqual(projection.artifacts.map(\.title), ["Report", "Report"])
        XCTAssertEqual(projection.artifacts[0].detail, "Prepared for review")
        XCTAssertTrue(projection.work.isEmpty)
    }

    func testNewestWorkSnapshotDeduplicatesAcrossSectionsAndRetainsLegacyRows() {
        let source = BriefEvidence(ref: "work", sourceKind: "history", sourceId: "hf-one")
        let old = BriefItem(id: "old", key: "openLoops.abc", title: "Research", status: "active",
                            kind: "one_time_work", refs: ["work"])
        let latest = BriefItem(id: "new", key: "moved.abc", title: "Research", status: "completed",
                               kind: "one_time_work", refs: ["work"])
        let older = MobileBriefV1(briefId: "old", asOf: "2026-10-19T12:00:00Z",
                                  sections: BriefSections(openLoops: [old]), evidence: [source])
        let current = MobileBriefV1(briefId: "new", asOf: timestamp,
                                    sections: BriefSections(moved: [latest], openLoops: [latest,
                                        BriefItem(id: "legacy-a", title: "Same title", kind: "one_time_work"),
                                        BriefItem(id: "legacy-b", title: "Same title", kind: "one_time_work"),
                                        BriefItem(id: "legacy-a", title: "Same title", kind: "one_time_work"),
                                        BriefItem(title: "  ", kind: "one_time_work"),
                                        BriefItem(title: "Goal", kind: "commitment")]), evidence: [source])
        let records = LibraryProjection(history: [], briefs: [older, current]).work
        XCTAssertEqual(records.count, 3)
        XCTAssertEqual(records.first { $0.id == "history:hf-one" }?.status, "completed")
        XCTAssertEqual(records.filter { $0.title == "Same title" }.count, 2)
    }

    func testDecisionPageKeepsOnlyKnownTerminalDecisionsAndToleratesMalformedRows() throws {
        let json = #"""
        {"items":[null,3,{"kind":"text","text":"A decision in a message"},
          {"kind":"proactive","at":"2026-10-20T12:00:00Z","text":"Fallback",
           "proactive":{"momentId":"answered","kind":"decision_due","state":"acted","headline":"Save the report?","body":"The request was answered.","permission":{"requestId":"secret","approveOptionId":"allow"}}},
          {"kind":"proactive","proactive":{"momentId":"pending","kind":"decision_due","state":"pending","headline":"Pending"}},
          {"kind":"proactive","proactive":{"momentId":"future","kind":"decision_due","state":"future","headline":"Unknown state"}},
          {"kind":"proactive","proactive":{"momentId":"work","kind":"work_finished","state":"acted","headline":"Not a decision"}},
          {"kind":"proactive","text":"Expired fallback","proactive":{"momentId":"expired","kind":"decision_due","state":"expired"}},
          {"kind":"proactive","proactive":{"momentId":9,"kind":"decision_due","state":"acted","headline":"Bad identity"}}]}
        """#
        let page = try JSONDecoder().decode(LibraryDecisionPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.records.map(\.id), ["answered", "expired"])
        XCTAssertEqual(page.records[0].status, "Answered", "the wire does not say approve or decline")
        XCTAssertEqual(page.records[0].title, "Save the report?")
        XCTAssertEqual(page.records[0].detail, "The request was answered.")
        XCTAssertEqual(page.records[1].title, "Expired fallback")
        XCTAssertEqual(page.records[1].status, "Expired")
    }

    func testDecisionDecodeUsesNewestDuplicateAndDoesNotFailForUnknownPayload() throws {
        let json = #"""
        {"items":[
          {"kind":"proactive","proactive":{"momentId":"same","kind":"decision_due","state":"acted","headline":"Old"}},
          {"kind":"proactive","proactive":{"momentId":"same","kind":"decision_due","state":"expired","headline":"Newest"}},
          {"kind":"proactive","proactive":false}]}
        """#
        let page = try JSONDecoder().decode(LibraryDecisionPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.records.count, 1)
        XCTAssertEqual(page.records.first?.title, "Newest")
        XCTAssertTrue(try JSONDecoder().decode(LibraryDecisionPage.self, from: Data(#"{"items":false}"#.utf8)).records.isEmpty)
    }

    func testStoreObservesBriefChangesAndCacheDeletion() throws {
        let fixture = try makeFixture()
        let saved = MobileBriefV1(briefId: "saved", localDate: "2026-10-20", asOf: timestamp,
                                  sections: BriefSections(deliverables: [BriefItem(title: "Report")]))
        try fixture.cache.save(saved)
        fixture.brief.loadCached()
        XCTAssertEqual(fixture.library.days.first?.entries.first?.id, "saved")
        XCTAssertEqual(fixture.library.artifacts.first?.title, "Report")
        fixture.brief.deleteCachedBriefs()
        XCTAssertTrue(fixture.library.days.isEmpty)
        XCTAssertTrue(fixture.library.artifacts.isEmpty)
    }

    func testMissingScopesKeepLibraryAndDecisionsEmptyWithoutCallingHub() async throws {
        let fixture = try makeFixture(scopes: [])
        try fixture.cache.save(MobileBriefV1(briefId: "hidden", localDate: "2026-10-20", asOf: timestamp))
        fixture.brief.loadCached()
        await fixture.library.refreshDecisions()
        XCTAssertTrue(fixture.library.days.isEmpty)
        XCTAssertTrue(fixture.library.decisions.isEmpty)
        XCTAssertNotNil(fixture.library.decisionNotice)
        let calls = await fixture.api.calls
        XCTAssertEqual(calls, 0)
    }

    func testTransientDecisionFailurePreservesSnapshotButAuthorizationFailureClearsIt() async throws {
        let fixture = try makeFixture()
        let record = decision()
        await fixture.api.succeed([record])
        await fixture.library.refreshDecisions()
        XCTAssertEqual(fixture.library.decisions, [record])
        let requestedHub = await fixture.api.requestedHub
        XCTAssertEqual(requestedHub, fixture.pairing.paired?.hubID)
        await fixture.api.fail(.noAnswer)
        await fixture.library.refreshDecisions()
        XCTAssertEqual(fixture.library.decisions, [record])
        XCTAssertNotNil(fixture.library.decisionNotice)
        await fixture.api.fail(.forbidden)
        await fixture.library.refreshDecisions()
        XCTAssertTrue(fixture.library.decisions.isEmpty)
        XCTAssertFalse(fixture.library.decisionsLoading)
    }

    func testPairingChangeDiscardsInFlightDecisionAndPreviousBriefs() async throws {
        let fixture = try makeFixture()
        try fixture.cache.save(MobileBriefV1(briefId: "old", localDate: "2026-10-20", asOf: timestamp))
        fixture.brief.loadCached()
        await fixture.api.pause()
        let refresh = Task { await fixture.library.refreshDecisions() }
        await fixture.api.waitUntilRequested()
        XCTAssertTrue(fixture.library.decisionsLoading)
        try storePairing(in: fixture.vault, hub: TestHub(), scopes: PairingStore.requestedScopes)
        fixture.pairing.retrySavedPairing()
        XCTAssertTrue(fixture.library.days.isEmpty)
        XCTAssertFalse(fixture.library.decisionsLoading)
        await fixture.api.resume([decision()])
        await refresh.value
        XCTAssertTrue(fixture.library.decisions.isEmpty)
        XCTAssertNil(fixture.library.decisionNotice)
    }

    func testUnpairClearsDecisionsAndProjectionImmediately() async throws {
        let fixture = try makeFixture()
        await fixture.api.succeed([decision()])
        await fixture.library.refreshDecisions()
        try fixture.cache.save(MobileBriefV1(briefId: "old", localDate: "2026-10-20", asOf: timestamp))
        fixture.brief.loadCached()
        try fixture.vault.wipe()
        fixture.pairing.retrySavedPairing()
        XCTAssertTrue(fixture.library.decisions.isEmpty)
        XCTAssertTrue(fixture.library.days.isEmpty)
    }

    func testTodaySnapshotWithdrawsRemovedBodiesButKeepsNewerRelayBriefs() async throws {
        let fixture = try makeFixture()
        let old = MobileBriefV1(briefId: "old", localDate: "2026-10-19", asOf: "2026-10-19T07:00:00Z",
                                narrativeStatus: "ok", narrative: [BriefParagraph(text: "Old")])
        let newer = MobileBriefV1(briefId: "new", localDate: "2026-10-20", asOf: "2026-10-20T11:00:00Z",
                                  narrativeStatus: "ok", narrative: [BriefParagraph(text: "New")])
        try fixture.cache.save(old)
        try fixture.cache.save(newer)
        fixture.brief.loadCached()
        let snapshot = MobileTodaySnapshot(generatedAt: "2026-10-20T10:00:00Z", latestBrief: nil, briefs: [],
                                           needsYou: [], needsYouCount: 0,
                                           day: TodayDay(next: nil, free: [], calendarConnected: false), topics: [],
                                           briefRequest: nil, schedule: MobileBriefSchedule(morning: "07:00", evening: "18:00"))
        fixture.brief.reconcileToday(snapshot)
        XCTAssertNil(fixture.cache.load(identifier: "old"))
        XCTAssertNotNil(fixture.cache.load(identifier: "new"))
        XCTAssertEqual(fixture.brief.cachedBriefs.map(\.identifier), ["new"])
        XCTAssertNil(fixture.brief.cachedBrief(withID: "old"), "a withdrawn brief is not read from the library")
    }

    func testReopeningHistoryCanRefreshBeforeCancelledReadReturns() async throws {
        let fixture = try makeFixture()
        let previous = decision()
        await fixture.api.succeed([previous])
        await fixture.library.refreshDecisions()
        await fixture.api.pause()
        let oldRefresh = Task { await fixture.library.refreshDecisions() }
        await fixture.api.waitUntilRequested()
        XCTAssertTrue(fixture.library.decisionsLoading)

        oldRefresh.cancel()
        fixture.library.cancelDecisionRefresh()
        XCTAssertFalse(fixture.library.decisionsLoading)
        XCTAssertEqual(fixture.library.decisions, [previous])
        let newest = LibraryRecord(id: "new", title: "New decision", detail: "Answered later.",
                                   status: "Answered", date: RelayTime.parse(timestamp))
        await fixture.api.allowNextRequest()
        await fixture.api.succeed([newest])
        await fixture.library.refreshDecisions()
        XCTAssertEqual(fixture.library.decisions, [newest])
        XCTAssertFalse(fixture.library.decisionsLoading)

        await fixture.api.resume([previous])
        await oldRefresh.value
        XCTAssertEqual(fixture.library.decisions, [newest])
        XCTAssertFalse(fixture.library.decisionsLoading)
        XCTAssertNil(fixture.library.decisionNotice)
    }

    private func entry(_ brief: MobileBriefV1) -> HistoryEntry {
        HistoryEntry(id: brief.identifier, slot: brief.slot, localDate: brief.localDate,
                     asOf: brief.asOfDate, narrativeStatus: brief.narrativeStatus, isCached: true)
    }

    private func decision() -> LibraryRecord {
        LibraryRecord(id: "decision", title: "Prepare the report", detail: "Answered on your computer.",
                      status: "Answered", date: RelayTime.parse(timestamp))
    }

    private func makeFixture(scopes: [String] = PairingStore.requestedScopes,
                             removeItem: @escaping (URL) throws -> Void = { try FileManager.default.removeItem(at: $0) }) throws -> Fixture {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory.appendingPathComponent("vault"), secrets: InMemorySecretStore())
        try storePairing(in: vault, hub: TestHub(), scopes: scopes)
        let pairing = PairingStore(vault: vault, transport: StubPairingTransport())
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"),
                               now: { RelayTime.parse("2026-10-20T12:00:00Z")! }, removeItem: removeItem)
        let brief = BriefStore(pairing: pairing, vault: vault, cache: cache, hub: HubChannel(vault: vault))
        let api = LibraryHistoryStub()
        return Fixture(vault: vault, pairing: pairing, cache: cache, brief: brief, api: api,
                       library: LibraryStore(brief: brief, pairing: pairing, hub: api))
    }

    private func storePairing(in vault: PairingVault, hub: TestHub, scopes: [String]) throws {
        let key = RelayCrypto.makeDeviceKeyPair()
        let device = PairedDevice(hubID: hub.hubID, hubName: "Computer", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Phone", scopes: scopes, pairedAt: Date(), counters: SequenceCounters())
        try vault.store(device, devicePrivateKey: RelayCrypto.base64URLEncode(key.privateKey), bearer: "test-bearer")
    }
}

private actor LibraryHistoryStub: LibraryHistoryAPI {
    private var result: Result<[LibraryRecord], HubRequestError> = .success([])
    private var paused = false
    private var requested = false
    private var requestWaiter: CheckedContinuation<Void, Never>?
    private var responseWaiter: CheckedContinuation<[LibraryRecord], Never>?
    private(set) var calls = 0
    private(set) var requestedHub: String?

    func succeed(_ records: [LibraryRecord]) { result = .success(records) }
    func fail(_ error: HubRequestError) { result = .failure(error) }
    func pause() {
        paused = true
        requested = false
    }
    func allowNextRequest() { paused = false }

    func libraryDecisions() async throws -> [LibraryRecord] {
        calls += 1
        requestedHub = HubRequestScope.hubID
        requested = true
        requestWaiter?.resume()
        requestWaiter = nil
        if paused {
            return await withCheckedContinuation { responseWaiter = $0 }
        }
        return try result.get()
    }

    func waitUntilRequested() async {
        if requested { return }
        await withCheckedContinuation { requestWaiter = $0 }
    }

    func resume(_ records: [LibraryRecord]) {
        paused = false
        responseWaiter?.resume(returning: records)
        responseWaiter = nil
    }
}

// Exercises the encrypted latest/history receipt paths, not only direct player callbacks.
// A relay receipt can outlive the audio route authorization that follows it.
@MainActor
final class BriefAudioReceiptTests: XCTestCase {
    private final class SilentEngine: BriefAudioPlayback {
        var duration: TimeInterval = 60
        var currentTime: TimeInterval = 0
        var rate: Float = 1
        private(set) var playing = false
        func play() -> Bool { playing = true; return true }
        func pause() { playing = false }
        func stop() { playing = false }
    }

    private struct OfflineHubTransport: HubTransport {
        func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
            throw HubRequestError.noAnswer
        }
        func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
            throw HubRequestError.noAnswer
        }
        func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {}
    }

    @MainActor
    private final class Fixture {
        let directory: URL
        let secrets = InMemorySecretStore()
        var session: SessionContext
        let readable: MobileBriefV1
        let withdrawn: MobileBriefV1
        let initialReceipt: RelayBriefRecord
        var vault: PairingVault
        var cache: BriefCache
        var audioCache: BriefAudioCache
        var engine = SilentEngine()
        var store: BriefStore!
        var player: BriefAudioPlayer!
        var metadataRequests = 0
        var downloads = 0
        var computerOnline = true

        init() throws {
            directory = try makeTemporaryDirectory()
            let hubKeys = RelayCrypto.makeDeviceKeyPair()
            let deviceKeys = RelayCrypto.makeDeviceKeyPair()
            vault = PairingVault(directory: directory.appendingPathComponent("pairing"), secrets: secrets)
            let paired = PairedDevice(hubID: UUID().uuidString.lowercased(), hubName: "Test computer",
                hubPublicKey: RelayCrypto.base64URLEncode(hubKeys.publicKey), relayOrigin: "https://relay.example",
                deviceID: UUID().uuidString.lowercased(), deviceName: "Test phone", scopes: ["brief:read"],
                pairedAt: Date(), counters: SequenceCounters())
            try vault.store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(deviceKeys.privateKey),
                            bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
            session = try vault.session()
            cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
            audioCache = BriefAudioCache(directory: directory.appendingPathComponent("audio"))
            let audio = BriefAudio(mime: "audio/wav", bytes: 3, durationSeconds: 60,
                attachment: BriefAudioAttachment(id: UUID().uuidString.lowercased(), key: nil, nonce: nil))
            readable = MobileBriefV1(briefId: "receipt-morning", asOf: Date().ISO8601Format(),
                                    narrativeStatus: "ok", audio: audio)
            var unavailable = readable
            unavailable.audio = nil
            unavailable.narrativeStatus = "unavailable"
            withdrawn = unavailable
            initialReceipt = try Self.seal(withdrawn, sequence: 8, session: session)
            try audioCache.store(Data([1, 2, 3]), brief: readable, hubID: session.hubID, audio: audio)
            makePlayerAndStore()
        }

        private func makePlayerAndStore() {
            player = BriefAudioPlayer(cache: audioCache, requestAudio: { [unowned self] brief in
                self.metadataRequests += 1
                guard self.computerOnline else { throw HubRequestError.noAnswer }
                return BriefAudioResource(briefId: brief.identifier, version: BriefAudioResource.versionKey(for: brief),
                                          audio: self.readable.audio!)
            }, downloadAudio: { [unowned self] _ in
                self.downloads += 1
                guard self.computerOnline else { throw HubRequestError.noAnswer }
                return Data([4, 5, 6])
            }, makeEngine: { [unowned self] _ in self.engine }, managesSystemPlayback: false,
            authorizationReceipt: { [unowned self] in
                guard let paired = self.vault.current() else { return nil }
                return BriefAudioReceipt(deviceID: paired.deviceID, pairingID: paired.secretID,
                                         sequence: paired.counters.lastAcceptedBrief)
            }, resolveAuthorizationReceipt: { [weak self] brief in
                guard let self else { return nil }
                return await self.store.receiptForAudioAuthorization(for: brief)
            })
            store = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache,
                hub: HubChannel(vault: vault, relay: OfflineHubTransport()),
                relay: RelayClient(session: StubURLProtocol.session()))
            store.onBriefReceived = { [weak player] brief, hubID, fromCache, receipt in
                player?.receive(brief: brief, hubID: hubID, fromCache: fromCache, receipt: receipt)
            }
        }

        static func seal(_ brief: MobileBriefV1, sequence: UInt64, session: SessionContext) throws -> RelayBriefRecord {
            let ciphertext = try RelayCrypto.sealEnvelope(RelayCrypto.padBrief(JSONEncoder().encode(brief)),
                key: session.keys.h2d, hub: session.hubID, dev: session.deviceID,
                dir: "h2d", seq: sequence, kind: "brief")
            let envelope = RelayEnvelope(v: 1, hub: session.hubID, dev: session.deviceID, dir: "h2d", seq: sequence,
                                         kind: "brief", ct: RelayCrypto.base64URLEncode(ciphertext))
            return RelayBriefRecord(version: sequence, asOf: brief.asOf!, env: envelope)
        }

        func record(sequence: UInt64) throws -> RelayBriefRecord {
            try Self.seal(withdrawn, sequence: sequence, session: session)
        }

        func serve(_ record: RelayBriefRecord) throws {
            let envelope = try JSONSerialization.jsonObject(with: JSONEncoder().encode(record.env))
            let object: [String: Any] = ["version": record.version, "asOf": record.asOf, "env": envelope]
            let latest = try JSONSerialization.data(withJSONObject: ["ok": true, "data": object])
            let history = try JSONSerialization.data(withJSONObject: ["ok": true, "data": ["briefs": [object]]])
            let presence = Data("{\"ok\":true,\"data\":{\"hubOnline\":false}}".utf8)
            StubURLProtocol.install { request, _ in
                let data: Data
                switch request.url?.path {
                case "/v1/presence": data = presence
                case "/v1/brief/latest": data = latest
                case "/v1/briefs": data = history
                default: throw URLError(.unsupportedURL)
                }
                return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1",
                                         headerFields: nil)!, data)
            }
        }

        func receiveWithdrawalThenAuthorizeAudio() async throws {
            try serve(initialReceipt)
            await store.refresh()
            XCTAssertEqual(vault.lastAcceptedBrief(), 8)
            XCTAssertNil(store.current, "a withdrawn brief is never current")
            XCTAssertNil(audioCache.cached(brief: readable, hubID: session.hubID))
            await player.toggle(brief: withdrawn, hubID: session.hubID)
            XCTAssertEqual(metadataRequests, 1)
            XCTAssertEqual(downloads, 1)
            XCTAssertEqual(player.state, .playing)
            XCTAssertTrue(engine.playing)
            XCTAssertNotNil(audioCache.cached(brief: withdrawn, hubID: session.hubID))
            computerOnline = false
        }

        func pairAgainWithSameComputer() throws {
            player.stop()
            var paired = try XCTUnwrap(vault.current())
            paired.deviceID = UUID().uuidString.lowercased()
            paired.pairedAt = Date()
            paired.counters = SequenceCounters()
            let keys = RelayCrypto.makeDeviceKeyPair()
            try vault.store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(keys.privateKey),
                            bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
            session = try vault.session()
            engine = SilentEngine()
            makePlayerAndStore()
        }

        func reopen() {
            player.stop()
            vault = PairingVault(directory: directory.appendingPathComponent("pairing"), secrets: secrets)
            cache = BriefCache(directory: directory.appendingPathComponent("briefs"))
            audioCache = BriefAudioCache(directory: directory.appendingPathComponent("audio"))
            engine = SilentEngine()
            makePlayerAndStore()
            store.loadCached()
        }

        func assertAuthorizedAudioSurvives(file: StaticString = #filePath, line: UInt = #line) {
            XCTAssertEqual(player.state, .playing, file: file, line: line)
            XCTAssertTrue(engine.playing, file: file, line: line)
            XCTAssertEqual(metadataRequests, 1, file: file, line: line)
            XCTAssertEqual(downloads, 1, file: file, line: line)
            XCTAssertEqual(audioCache.cached(brief: withdrawn, hubID: session.hubID).flatMap { try? Data(contentsOf: $0.url) },
                           Data([4, 5, 6]), file: file, line: line)
        }

        func cleanup() {
            player.stop()
            try? FileManager.default.removeItem(at: directory)
        }
    }

    func testFirstEncryptedWithdrawalStopsAndDeletesExistingAudio() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        await fixture.player.toggle(brief: fixture.readable, hubID: fixture.session.hubID)
        XCTAssertTrue(fixture.engine.playing)
        try fixture.serve(fixture.initialReceipt)

        await fixture.store.refresh()

        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8)
        XCTAssertEqual(fixture.player.state, .unavailable(.notAvailable))
        XCTAssertFalse(fixture.engine.playing)
        XCTAssertNil(fixture.audioCache.cached(brief: fixture.readable, hubID: fixture.session.hubID))
    }

    func testDuplicateLatestWithdrawalPreservesSubsequentlyAuthorizedAudio() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try await fixture.receiveWithdrawalThenAuthorizeAudio()
        let recording = try XCTUnwrap(fixture.audioCache.cached(brief: fixture.withdrawn, hubID: fixture.session.hubID))
        fixture.player.seek(to: 17)

        await fixture.store.refresh()

        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.player.position, 17)
        XCTAssertTrue(FileManager.default.fileExists(atPath: recording.url.path))
        XCTAssertEqual(fixture.store.hubOnline, false)
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8)
    }

    func testDuplicateHistoryWithdrawalPreservesSubsequentlyAuthorizedAudio() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try await fixture.receiveWithdrawalThenAuthorizeAudio()

        await fixture.store.refreshHistory()

        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.store.cachedBriefs, [fixture.withdrawn])
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8, "History must not advance the latest-brief cursor")
    }

    func testDuplicateLatestAndHistoryWithdrawalsPreserveAuthorizedAudioAfterReopening() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try await fixture.receiveWithdrawalThenAuthorizeAudio()
        fixture.player.seek(to: 17)
        fixture.reopen()
        await fixture.player.toggle(brief: fixture.withdrawn, hubID: fixture.session.hubID)
        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.player.position, 17)

        await fixture.store.refresh()
        await fixture.store.refreshHistory()

        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.player.position, 17)
    }

    func testCachedHistoryWithdrawalBeyondLatestCursorSurvivesRecoveryAndDuplicateAfterReopening() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        XCTAssertTrue(fixture.audioCache.clear())
        try fixture.cache.save(fixture.withdrawn, relayVersion: 8)
        XCTAssertEqual(try fixture.vault.acceptBrief(seq: 7, deviceID: fixture.session.deviceID), .fresh)
        fixture.store.loadCached()
        XCTAssertNil(fixture.store.current, "a withdrawn brief is never current")
        try fixture.serve(fixture.initialReceipt)

        await fixture.player.toggle(brief: fixture.withdrawn, hubID: fixture.session.hubID)
        fixture.assertAuthorizedAudioSurvives()
        fixture.computerOnline = false
        fixture.reopen()
        await fixture.player.toggle(brief: fixture.withdrawn, hubID: fixture.session.hubID)
        fixture.assertAuthorizedAudioSurvives()

        await fixture.store.refreshHistory()

        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 7, "Resolving legacy history must not advance the latest cursor")
        await fixture.store.refresh()
        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8)
    }

    func testScopedCachedHistoryReceiptSurvivesAudioLedgerEvictionAndRecovery() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        XCTAssertTrue(fixture.audioCache.clear())
        let paired = try XCTUnwrap(fixture.vault.current())
        let receipt = BriefAudioReceipt(deviceID: paired.deviceID, pairingID: paired.secretID, sequence: 8)
        try fixture.cache.save(fixture.withdrawn, relayVersion: 8, audioReceipt: receipt)
        XCTAssertEqual(try fixture.vault.acceptBrief(seq: 7, deviceID: fixture.session.deviceID), .fresh)
        fixture.audioCache.recordWithdrawal(receipt, brief: fixture.withdrawn, hubID: fixture.session.hubID)
        for index in 0..<14 {
            let other = MobileBriefV1(briefId: "other-history-\(index)", asOf: fixture.withdrawn.asOf,
                                      narrativeStatus: "unavailable")
            let newerReceipt = BriefAudioReceipt(deviceID: paired.deviceID, pairingID: paired.secretID,
                                                 sequence: UInt64(9 + index))
            fixture.audioCache.recordWithdrawal(newerReceipt, brief: other, hubID: fixture.session.hubID)
        }
        XCTAssertNil(fixture.audioCache.latestWithdrawalReceipt(brief: fixture.withdrawn, hubID: fixture.session.hubID))
        XCTAssertNil(fixture.audioCache.cached(brief: fixture.withdrawn, hubID: fixture.session.hubID))
        try fixture.serve(fixture.initialReceipt)

        await fixture.player.toggle(brief: fixture.withdrawn, hubID: fixture.session.hubID)
        fixture.assertAuthorizedAudioSurvives()
        fixture.computerOnline = false
        fixture.store.loadCached()
        fixture.assertAuthorizedAudioSurvives()
        fixture.reopen()
        await fixture.player.toggle(brief: fixture.withdrawn, hubID: fixture.session.hubID)
        fixture.assertAuthorizedAudioSurvives()
        await fixture.store.refreshHistory()
        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 7)
        await fixture.store.refresh()
        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8)
    }

    func testLegacyCachedReceiptCannotAssignItsSequenceToANewPairing() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        XCTAssertTrue(fixture.audioCache.clear())
        try fixture.cache.save(fixture.withdrawn, relayVersion: 8)
        let previousDeviceID = fixture.session.deviceID
        let previousReceipt = BriefAudioReceipt(deviceID: previousDeviceID,
            pairingID: fixture.vault.current()?.secretID, sequence: 8)
        try fixture.pairAgainWithSameComputer()
        XCTAssertNotEqual(fixture.session.deviceID, previousDeviceID)
        XCTAssertEqual(try fixture.vault.acceptBrief(seq: 7, deviceID: fixture.session.deviceID), .fresh)
        fixture.store.loadCached()
        // An old envelope does not authenticate under the new pairing, even with the same brief and sequence.
        try fixture.serve(fixture.initialReceipt)
        await fixture.player.toggle(brief: fixture.withdrawn, hubID: fixture.session.hubID)
        fixture.assertAuthorizedAudioSurvives()
        XCTAssertNil(fixture.cache.load(identifier: fixture.withdrawn.identifier)?.audioReceipt)
        try fixture.cache.save(fixture.withdrawn, relayVersion: 8, audioReceipt: previousReceipt)
        let resolved = await fixture.store.receiptForAudioAuthorization(for: fixture.withdrawn)
        XCTAssertNil(resolved, "A scoped cache receipt from the previous pairing must also be rejected")
        fixture.computerOnline = false
        try fixture.serve(fixture.record(sequence: 8))

        await fixture.store.refresh()

        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8)
        XCTAssertEqual(fixture.player.state, .unavailable(.notAvailable))
        XCTAssertFalse(fixture.engine.playing)
        XCTAssertNil(fixture.audioCache.cached(brief: fixture.withdrawn, hubID: fixture.session.hubID),
                     "A previous pairing's cached sequence must not mask a new authenticated withdrawal")
    }

    func testNewerEncryptedWithdrawalStopsAndDeletesReauthorizedAudio() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try await fixture.receiveWithdrawalThenAuthorizeAudio()
        try fixture.serve(fixture.record(sequence: 9))

        await fixture.store.refresh()

        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 9)
        XCTAssertEqual(fixture.player.state, .unavailable(.notAvailable))
        XCTAssertFalse(fixture.engine.playing)
        XCTAssertNil(fixture.audioCache.cached(brief: fixture.withdrawn, hubID: fixture.session.hubID))
    }

    func testOlderEncryptedWithdrawalDoesNotUndoNewerAudioAuthorization() async throws {
        let fixture = try Fixture()
        defer { fixture.cleanup() }
        try await fixture.receiveWithdrawalThenAuthorizeAudio()
        try fixture.serve(fixture.record(sequence: 7))

        await fixture.store.refresh()

        fixture.assertAuthorizedAudioSurvives()
        XCTAssertEqual(fixture.vault.lastAcceptedBrief(), 8)
    }
}
