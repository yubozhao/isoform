import Foundation
import XCTest
@testable import Yansu

/// Answers the notification routes the way the Mac does, and records every switch the phone sends.
private final class NotificationHub: NotificationSettingsAPI, @unchecked Sendable {
    private let lock = NSLock()
    private var answer: NotificationSettings
    private var failure: Error?
    private(set) var writes: [(kind: String, enabled: Bool, key: String)] = []
    private(set) var preferenceWrites: [SetNotificationPreferencesRequest] = []
    private(set) var reads = 0

    init(answer: NotificationSettings) {
        self.answer = answer
    }

    func fail(_ error: Error?) {
        lock.lock()
        failure = error
        lock.unlock()
    }

    func notificationSettings() async throws -> NotificationSettings {
        lock.lock()
        defer { lock.unlock() }
        reads += 1
        if let failure { throw failure }
        return answer
    }

    func setNotificationKind(_ kind: String, enabled: Bool, idempotencyKey: String) async throws -> NotificationSettings {
        lock.lock()
        defer { lock.unlock() }
        writes.append((kind, enabled, idempotencyKey))
        if let failure { throw failure }
        answer = NotificationSettings(
            available: answer.available,
            kinds: answer.kinds.map { item in
                item.kind == kind ? NotificationSettingsTests.kind(item.kind, enabled: enabled, sent: item.sent) : item
            },
            quietStart: answer.quietStart, quietEnd: answer.quietEnd,
            dailyInterruptBudget: answer.dailyInterruptBudget, digestTimes: answer.digestTimes, statsDays: answer.statsDays)
        return answer
    }

    func setNotificationPreferences(_ request: SetNotificationPreferencesRequest) async throws -> NotificationSettings {
        lock.lock()
        defer { lock.unlock() }
        preferenceWrites.append(request)
        if let failure { throw failure }
        answer = NotificationSettings(available: answer.available, kinds: answer.kinds,
                                      quietStart: request.quietStart, quietEnd: request.quietEnd,
                                      dailyInterruptBudget: answer.dailyInterruptBudget,
                                      digestTimes: answer.digestTimes, statsDays: answer.statsDays)
        return answer
    }
}

final class NotificationSettingsTests: XCTestCase {
    static func kind(_ kind: String, enabled: Bool, paused: Bool = false, sent: Int = 0) -> NotificationKind {
        NotificationKind(kind: kind, enabled: enabled, defaultOn: enabled, paused: paused, mutedUntil: nil,
                         sent: sent, opened: 0, acted: 1, dismissed: 2, muted: 1)
    }

    static func settings(_ kinds: [NotificationKind]) -> NotificationSettings {
        NotificationSettings(available: true, kinds: kinds, quietStart: "22:00", quietEnd: "08:00",
                             dailyInterruptBudget: 6, digestTimes: ["08:30", "13:00", "18:00"], statsDays: 30)
    }

    func testDecodesTheMacsView() throws {
        let body = #"""
        {"ok":true,"data":{"available":true,"kinds":[
          {"kind":"work_blocked","enabled":true,"defaultOn":true,"paused":false,"sent":2,"opened":1,"acted":1,"dismissed":0,"muted":0},
          {"kind":"goal_check_in","enabled":false,"defaultOn":false,"paused":true,"mutedUntil":"2026-10-11T00:00:00Z","sent":20,"opened":0,"acted":0,"dismissed":13,"muted":0},
          {"kind":"something_new","enabled":true,"defaultOn":true,"paused":false,"sent":0,"opened":0,"acted":0,"dismissed":0,"muted":0}
        ],"quietStart":"22:00","quietEnd":"08:00","dailyInterruptBudget":6,"digestTimes":["08:30","13:00","18:00"],"statsDays":30}}
        """#
        let settings = try HubResponse(status: 200, body: body).payload(NotificationSettings.self)
        XCTAssertTrue(settings.available)
        XCTAssertEqual(settings.kinds.count, 3)
        XCTAssertEqual(settings.kinds[0].title, "Work blocked")
        XCTAssertTrue(settings.kinds[1].paused)
        XCTAssertEqual(settings.kinds[1].mutedUntil, "2026-10-11T00:00:00Z")
        XCTAssertEqual(settings.kinds[2].title, "something_new", "an unknown kind shows its key")
        XCTAssertEqual(settings.kinds[2].detail, "")
        XCTAssertEqual(settings.rules, "Quiet hours run from 22:00 to 08:00. At most 6 interruptions a day; quieter kinds wait for a digest at 08:30, 13:00, 18:00.")
    }

    func testEveryKindTheMacKnowsHasALabel() {
        // Mirrors engagement.AllKinds() in wails-gui/internal/engagement/moment.go.
        let kinds = ["work_blocked", "work_finished", "commitment_slipping", "capture_paused",
                     "meeting_ended", "brief_ready", "person_reappeared", "goal_check_in", "learned_to_confirm"]
        for kind in kinds {
            let item = Self.kind(kind, enabled: true)
            XCTAssertNotEqual(item.title, kind, "\(kind) needs a label")
            XCTAssertFalse(item.detail.isEmpty, "\(kind) needs a description")
        }
    }

    func testTheRetiredDecisionKindShowsItsKeyLikeAnyUnknownKind() {
        // decision_due left with the Desk. An older Mac may still list it; the phone does not pretend to know it.
        let retired = Self.kind("decision_due", enabled: true)
        XCTAssertEqual(retired.title, "decision_due")
        XCTAssertEqual(retired.detail, "")
    }

    func testRouteAndRequestShape() throws {
        XCTAssertEqual(HubRoute.notifications.method, "GET")
        XCTAssertEqual(HubRoute.notifications.path, "/notifications")
        XCTAssertEqual(HubRoute.notifications.query, "")
        XCTAssertEqual(HubRoute.notifications.body, "")
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let data = try encoder.encode(SetNotificationKindRequest(idempotencyKey: "k-1", enabled: false))
        XCTAssertEqual(String(data: data, encoding: .utf8), #"{"enabled":false,"idempotencyKey":"k-1"}"#)
    }

    @MainActor
    func testStoreRefreshesAndWritesOneSwitchWithAFreshKey() async {
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true), Self.kind("goal_check_in", enabled: false)]))
        var keys = ["key-a", "key-b"]
        let store = NotificationSettingsStore(hub: hub, isPaired: { true }, makeKey: { keys.removeFirst() })

        await store.refresh()
        XCTAssertEqual(store.loadState, .loaded)
        XCTAssertEqual(store.settings?.kinds.map(\.kind), ["brief_ready", "goal_check_in"])

        await store.setEnabled("goal_check_in", true)
        XCTAssertEqual(hub.writes.count, 1)
        XCTAssertEqual(hub.writes[0].kind, "goal_check_in")
        XCTAssertTrue(hub.writes[0].enabled)
        XCTAssertEqual(hub.writes[0].key, "key-a")
        XCTAssertEqual(store.settings?.kinds[1].enabled, true, "the Mac's answer replaces the view")
        XCTAssertNil(store.failure)
        XCTAssertFalse(store.isBusy("goal_check_in"))
    }

    @MainActor
    func testTodayNotificationChangeCanBeUndoneBeforeReconnect() async throws {
        let hub = NotificationHub(answer: Self.settings([Self.kind("weekly_recap", enabled: true)]))
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = NotificationSettingsStore(hub: hub, isPaired: { true },
            fileURL: directory.appendingPathComponent("notifications.json"), pairingID: { "mac/phone" }, hubID: { "mac" })
        await store.refresh()
        await store.setEnabled("weekly_recap", false, deferForToday: true)
        XCTAssertTrue(hub.writes.isEmpty)
        let key = try XCTUnwrap(store.pendingToday.first?.idempotencyKey)
        store.undoToday(key)
        XCTAssertTrue(store.pendingToday.isEmpty)
        XCTAssertEqual(store.settings?.kinds.first?.enabled, true)
        await store.setEnabled("weekly_recap", false, deferForToday: true)
        await store.replayPending(includeDeferred: true)
        XCTAssertEqual(hub.writes.count, 1)
        XCTAssertTrue(store.pendingToday.isEmpty)
        XCTAssertEqual(store.settings?.kinds.first?.enabled, false)
    }

    @MainActor
    func testStoreKeepsTheLastViewAndSaysWhyWhenAWriteFails() async {
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        let store = NotificationSettingsStore(hub: hub, isPaired: { true })
        await store.refresh()

        hub.fail(HubRequestError.forbidden)
        await store.setEnabled("brief_ready", false)
        XCTAssertEqual(store.settings?.kinds[0].enabled, true)
        XCTAssertEqual(store.failure, String(localized: "notifications.error.forbidden"))

        hub.fail(HubRequestError.noAnswer)
        await store.refresh()
        XCTAssertEqual(store.loadState, .failed(String(localized: "notifications.error.offline")))
        XCTAssertNotNil(store.settings, "a failed refresh keeps the last view")
    }

    /// A saved copy that can't be written never hides the page: the computer answered, so its choices show.
    @MainActor
    func testAnAnswerThePhoneCannotSaveStillLoads() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let blocked = directory.appendingPathComponent("blocked-file")
        try Data().write(to: blocked)
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        let store = NotificationSettingsStore(hub: hub, isPaired: { true },
            fileURL: blocked.appendingPathComponent("notifications.json"))
        await store.refresh()
        XCTAssertEqual(store.loadState, .loaded)
        XCTAssertTrue(NotificationSettingsView.showsChoices(store.loadState))
        XCTAssertEqual(store.settings?.kinds.map(\.kind), ["brief_ready"])
    }

    @MainActor
    func testStoreDoesNothingWhileUnpaired() async {
        let hub = NotificationHub(answer: Self.settings([]))
        let store = NotificationSettingsStore(hub: hub, isPaired: { false })
        await store.refresh()
        XCTAssertEqual(store.loadState, .idle)
        XCTAssertNil(store.settings)
        XCTAssertEqual(hub.reads, 0)
        store.clearAfterUnpair()
        XCTAssertNil(store.failure)
    }

    func testFirstRunKindsUseActualChoicesRatherThanDefaults() {
        let settings = Self.settings([
            Self.kind("capture_paused", enabled: true),
            Self.kind("brief_ready", enabled: false),
            Self.kind("work_finished", enabled: true, paused: true),
            Self.kind("work_blocked", enabled: true),
            Self.kind("meeting_prep", enabled: true),
            Self.kind("suggestion", enabled: true)
        ])
        // Three kinds, and nothing else: the brief, meeting prep, finished work (phone-rings v1.0.0).
        XCTAssertEqual(settings.phoneKinds.map(\.kind), ["brief_ready", "meeting_prep", "work_finished"])
        XCTAssertEqual(settings.enabledPhoneKinds.map(\.kind), ["meeting_prep"])
    }

    func testQuietHoursRequestAndClockValidation() throws {
        let request = SetNotificationPreferencesRequest(idempotencyKey: "quiet-1", quietStart: "23:00", quietEnd: "07:30")
        let data = try JSONEncoder().encode(request)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: String])
        XCTAssertEqual(body, ["idempotencyKey": "quiet-1", "quietStart": "23:00", "quietEnd": "07:30"])
    }

    func testQuietHoursRangeUsesTheLocaleWithoutShiftingTheWallClock() throws {
        let en = try XCTUnwrap(NotificationSettings.quietHoursRange(start: "23:00", end: "08:00",
                                                                       locale: Locale(identifier: "en_US")))
        XCTAssertEqual(en.replacingOccurrences(of: "\u{202F}", with: " "), "11:00 PM – 8:00 AM")
        let chinese = try XCTUnwrap(NotificationSettings.quietHoursRange(start: "23:00", end: "08:00",
                                                                            locale: Locale(identifier: "zh_Hans")))
        XCTAssertEqual(chinese, "23:00 – 8:00")
        let chinese12 = try XCTUnwrap(NotificationSettings.quietHoursRange(start: "23:00", end: "08:00",
                                                                              locale: Locale(identifier: "zh_Hans-u-hc-h12")))
        // The day period for 23:00 is the OS's: iOS 27 says 晚上, iOS 17 says 下午. The clock itself must not move.
        XCTAssertTrue(["晚上11:00 – 上午8:00", "下午11:00 – 上午8:00"].contains(chinese12), chinese12)
        XCTAssertNil(NotificationSettings.quietHoursRange(start: "00:00", end: "00:00", locale: Locale(identifier: "en_US")))
        XCTAssertNil(NotificationSettings.quietHoursRange(start: "24:00", end: "08:00", locale: Locale(identifier: "en_US")))
    }

    @MainActor
    func testQuietHoursReportsSavedQueuedAndRejectedWrites() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let hub = NotificationHub(answer: Self.settings([]))
        let store = NotificationSettingsStore(hub: hub, isPaired: { true },
            fileURL: directory.appendingPathComponent("settings.json"))
        await store.refresh()
        let saved = await store.setQuietHours(start: "23:00", end: "07:00")
        XCTAssertTrue(saved)
        hub.fail(HubRequestError.noAnswer)
        let queued = await store.setQuietHours(start: "22:00", end: "08:00")
        XCTAssertTrue(queued)
        XCTAssertEqual(store.pendingCount, 1)
        hub.fail(HubRequestError.forbidden)
        await store.replayPending()
        let rejected = await store.setQuietHours(start: "21:00", end: "06:00")
        XCTAssertFalse(rejected)
        XCTAssertNotNil(store.failure)
    }

    @MainActor
    func testOfflineKindsAndQuietHoursSurviveRelaunchAndReplayWithOriginalKeys() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("notifications.json")
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        var keys = ["kind-queued", "quiet-queued"]
        let store = NotificationSettingsStore(hub: hub, isPaired: { true }, makeKey: { keys.removeFirst() },
                                               fileURL: file, pairingID: { "pairing-a" })
        await store.refresh()
        hub.fail(HubRequestError.noAnswer)
        await store.setEnabled("brief_ready", false)
        await store.setQuietHours(start: "23:15", end: "07:45")
        XCTAssertEqual(store.pendingCount, 2)
        XCTAssertEqual(store.settings?.kinds.first?.enabled, false)
        XCTAssertEqual(store.settings?.quietStart, "23:15")
        XCTAssertNil(store.failure, "offline changes are queued rather than reported lost")

        let restored = NotificationSettingsStore(hub: hub, isPaired: { true }, fileURL: file, pairingID: { "pairing-a" })
        XCTAssertEqual(restored.pendingCount, 2)
        XCTAssertEqual(restored.settings?.quietEnd, "07:45")
        let fetched = Self.settings([Self.kind("brief_ready", enabled: true)])
        let merged = restored.applyingPending(to: fetched)
        XCTAssertEqual(merged.kinds.first?.enabled, false)
        XCTAssertEqual(merged.quietStart, "23:15")
        XCTAssertEqual(merged.quietEnd, "07:45")
        let staleRevision = restored.localNoticeRevision
        hub.fail(nil)
        await restored.refresh()
        XCTAssertNil(restored.applyingPending(to: fetched, revision: staleRevision))
        XCTAssertEqual(restored.pendingCount, 0)
        XCTAssertEqual(hub.writes.last?.key, "kind-queued")
        XCTAssertEqual(hub.preferenceWrites.last?.idempotencyKey, "quiet-queued")
        XCTAssertEqual(restored.settings?.kinds.first?.enabled, false)
        XCTAssertEqual(restored.settings?.quietStart, "23:15")
        XCTAssertEqual(restored.settings?.dailyInterruptBudget, 6)
        let afterReplay = NotificationSettingsStore(hub: hub, isPaired: { true }, fileURL: file, pairingID: { "pairing-a" })
        XCTAssertFalse(afterReplay.hasPendingChanges)
        XCTAssertEqual(afterReplay.settings, restored.settings)
    }

    @MainActor
    func testQueuedPreferencesNeverCrossPairingsAndClearOnUnpair() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("notifications.json")
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        let store = NotificationSettingsStore(hub: hub, isPaired: { true }, fileURL: file, pairingID: { "old" })
        await store.refresh()
        hub.fail(HubRequestError.noAnswer)
        await store.setQuietHours(start: "23:00", end: "06:00")
        let replacement = NotificationSettingsStore(hub: hub, isPaired: { true }, fileURL: file, pairingID: { "new" })
        XCTAssertFalse(replacement.hasPendingChanges)
        XCTAssertNil(replacement.settings)
        XCTAssertTrue(store.clearAfterUnpair())
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path))
        XCTAssertFalse(store.hasPendingChanges)
    }

    @MainActor
    func testInvalidTimesAndUnpairedWritesDoNotReachComputer() async {
        let hub = NotificationHub(answer: Self.settings([]))
        let store = NotificationSettingsStore(hub: hub, isPaired: { false })
        for raw in ["2:00", "24:00", "23:60", " 22:00", "22:0", "aa:bb"] {
            XCTAssertFalse(NotificationSettingsStore.validClock(raw))
            await store.setQuietHours(start: raw, end: "08:00")
        }
        XCTAssertTrue(NotificationSettingsStore.validClock("00:00"))
        await store.setEnabled("brief_ready", false)
        XCTAssertTrue(hub.writes.isEmpty)
        XCTAssertTrue(hub.preferenceWrites.isEmpty)
        XCTAssertEqual(store.failure, String(localized: "notifications.error.notPaired"))
    }

    @MainActor
    func testRevokedAndExpiredPairingsRejectQueuedChanges() async {
        for error in [RelayClientError.unauthorized, .forbidden, .gone] {
            let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
            let store = NotificationSettingsStore(hub: hub, isPaired: { true })
            await store.refresh()
            hub.fail(error)
            await store.setEnabled("brief_ready", false)
            XCTAssertFalse(store.hasPendingChanges)
            XCTAssertEqual(store.settings?.kinds.first?.enabled, true)
            XCTAssertEqual(store.failure, String(localized: "notifications.error.forbidden"))
        }
    }

    @MainActor
    func testUnreadableQueueIsPreservedUntilItCanBeRestored() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("notifications.json")
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        let original = NotificationSettingsStore(hub: hub, isPaired: { true }, makeKey: { "keep-this-key" },
                                                  fileURL: file, pairingID: { "pairing" })
        await original.refresh()
        hub.fail(HubRequestError.noAnswer)
        await original.setEnabled("brief_ready", false)
        let recoverable = try Data(contentsOf: file)
        let unreadable = Data("unreadable queue".utf8)
        try unreadable.write(to: file)

        let restored = NotificationSettingsStore(hub: hub, isPaired: { true }, fileURL: file, pairingID: { "pairing" })
        let writesBefore = hub.writes.count
        let readsBefore = hub.reads
        await restored.refresh()
        await restored.setEnabled("brief_ready", true)
        XCTAssertEqual(hub.writes.count, writesBefore)
        XCTAssertEqual(hub.reads, readsBefore)
        XCTAssertEqual(try Data(contentsOf: file), unreadable)
        XCTAssertEqual(restored.failure, String(localized: "notifications.error.restore"))

        try recoverable.write(to: file)
        hub.fail(nil)
        await restored.refresh()
        XCTAssertEqual(hub.writes.last?.key, "keep-this-key")
        XCTAssertEqual(restored.settings?.kinds.first?.enabled, false)
        XCTAssertFalse(restored.hasPendingChanges)
        XCTAssertNil(restored.failure)
    }

    @MainActor
    func testColdLaunchRestoresQueueAfterSavedPairingBecomesReadable() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("notifications.json")
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        let original = NotificationSettingsStore(hub: hub, isPaired: { true }, makeKey: { "saved-before-lock" },
                                                  fileURL: file, pairingID: { "pairing-a" })
        await original.refresh()
        hub.fail(HubRequestError.noAnswer)
        await original.setEnabled("brief_ready", false)
        let saved = try Data(contentsOf: file)

        var readable = false
        let restored = NotificationSettingsStore(hub: hub, isPaired: { readable }, fileURL: file,
                                                  pairingID: { readable ? "pairing-a" : nil })
        await restored.refresh()
        XCTAssertEqual(try Data(contentsOf: file), saved)
        readable = true
        hub.fail(nil)
        await restored.refresh()
        XCTAssertEqual(hub.writes.last?.key, "saved-before-lock")
        XCTAssertEqual(restored.settings?.kinds.first?.enabled, false)
        XCTAssertFalse(restored.hasPendingChanges)
    }

    @MainActor
    func testColdLaunchDoesNotReplayOldQueueToReplacementPairing() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("notifications.json")
        let hub = NotificationHub(answer: Self.settings([Self.kind("brief_ready", enabled: true)]))
        let original = NotificationSettingsStore(hub: hub, isPaired: { true }, fileURL: file, pairingID: { "old" })
        await original.refresh()
        hub.fail(HubRequestError.noAnswer)
        await original.setEnabled("brief_ready", false)
        let writesBefore = hub.writes.count
        var readable = false
        let restored = NotificationSettingsStore(hub: hub, isPaired: { readable }, fileURL: file,
                                                  pairingID: { readable ? "replacement" : nil })
        readable = true
        hub.fail(nil)
        await restored.refresh()
        XCTAssertEqual(hub.writes.count, writesBefore)
        XCTAssertEqual(restored.settings?.kinds.first?.enabled, true)
        XCTAssertFalse(restored.hasPendingChanges)
    }
}
