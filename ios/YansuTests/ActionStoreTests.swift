import Foundation
import XCTest
@testable import Yansu

/// Records the memory rejections and one-time work ActionStore sends, under the keys it used, and can refuse them.
private final class StubActionHub: ActionHubAPI, ProactiveActionHubAPI, @unchecked Sendable {
    private let lock = NSLock()
    private var failures: [Error] = []
    private var removalAnswer = true
    private var rejections: [(memoryID: String, reason: String, key: String)] = []
    private var work: [StartOneTimeWorkRequest] = []
    private var starts: [(id: String, key: String)] = []
    private var startFailure: Error?
    private var proactive: [(chatID: String, momentID: String, actionID: String, key: String)] = []
    private var proactiveAnswer = "opened"

    private func locked<T>(_ body: () throws -> T) rethrows -> T {
        lock.lock()
        defer { lock.unlock() }
        return try body()
    }

    var memoryRejections: [(memoryID: String, reason: String, key: String)] { locked { rejections } }
    var workRequests: [StartOneTimeWorkRequest] { locked { work } }
    var startRequests: [(id: String, key: String)] { locked { starts } }
    func failStart(_ error: Error?) { locked { startFailure = error } }
    func runOneTimeWork(id: String, idempotencyKey: String) async throws {
        let failure = locked {
            starts.append((id, idempotencyKey))
            return startFailure
        }
        if let failure { throw failure }
    }
    var proactiveRequests: [(chatID: String, momentID: String, actionID: String, key: String)] { locked { proactive } }

    func failNext(_ errors: [Error]) {
        locked { failures = errors }
    }

    func answerRemoval(_ removed: Bool) {
        locked { removalAnswer = removed }
    }

    func answerProactive(_ state: String) {
        locked { proactiveAnswer = state }
    }

    private func nextFailure() -> Error? {
        failures.isEmpty ? nil : failures.removeFirst()
    }

    func rejectAttribution(memoryID: String, reason: String, idempotencyKey: String) async throws -> Bool {
        let outcome: (Error?, Bool) = locked {
            rejections.append((memoryID, reason, idempotencyKey))
            return (nextFailure(), removalAnswer)
        }
        if let failure = outcome.0 { throw failure }
        return outcome.1
    }

    func startOneTimeWork(_ request: StartOneTimeWorkRequest) async throws -> String {
        let failure: Error? = locked {
            work.append(request)
            return nextFailure()
        }
        if let failure { throw failure }
        return "hf_1"
    }

    func performProactiveAction(chatID: String, momentID: String, actionID: String,
                                idempotencyKey: String) async throws -> String {
        let failure: Error? = locked {
            proactive.append((chatID, momentID, actionID, idempotencyKey))
            return nextFailure()
        }
        if let failure { throw failure }
        return locked { proactiveAnswer }
    }
}

private final class RecordingGuard: DecisionGuarding, @unchecked Sendable {
    private let lock = NSLock()
    private var denial: Error?
    private var asked: [String] = []

    var reasons: [String] {
        lock.lock()
        defer { lock.unlock() }
        return asked
    }

    func deny() {
        lock.lock()
        denial = DecisionGuardError.notConfirmed
        lock.unlock()
    }

    func authorize(reason: String) async throws {
        if let failure = record(reason) { throw failure }
    }

    private func record(_ reason: String) -> Error? {
        lock.lock()
        defer { lock.unlock() }
        asked.append(reason)
        return denial
    }
}

private final class SequentialKeys: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0

    func next() -> String {
        lock.lock()
        defer { lock.unlock() }
        count += 1
        return "action-key-\(count)"
    }
}

@MainActor
final class ActionStoreTests: XCTestCase {
    func testOfferWorkCreatesThenStartsUnderDistinctDurableKeys() async throws {
        let h = try makeHarness()
        let started = await h.store.runOneTimeWork(workDraft(), offerID: "offer-one")
        XCTAssertTrue(started)
        XCTAssertEqual(h.hub.workRequests.first?.idempotencyKey, "action-key-1-create")
        XCTAssertEqual(h.hub.startRequests.first?.id, "hf_1")
        XCTAssertEqual(h.hub.startRequests.first?.key, "action-key-1-start")
        XCTAssertEqual(h.store.notice, "Work started on your computer")
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testOfferWorkStartFailureKeepsCreationKeyForRetry() async throws {
        let h = try makeHarness()
        h.hub.failStart(HubRequestError.conflict(message: "Cannot start"))
        let failed = await h.store.runOneTimeWork(workDraft(), offerID: "offer-one")
        XCTAssertFalse(failed)
        let pending = await h.journal.entries()
        XCTAssertEqual(pending.count, 1)
        h.hub.failStart(nil)
        let retried = await h.store.runOneTimeWork(workDraft(), offerID: "offer-one")
        XCTAssertTrue(retried)
        XCTAssertEqual(h.hub.workRequests.map(\.idempotencyKey), ["action-key-1-create", "action-key-1-create"])
        XCTAssertEqual(h.hub.startRequests.map(\.key), ["action-key-1-start", "action-key-1-start"])
    }

    func testOfferWorkInvalidDraftDoesNotCreateOrStart() async throws {
        let h = try makeHarness()
        let started = await h.store.runOneTimeWork(OneTimeWorkDraft(), offerID: "offer-one")
        XCTAssertFalse(started)
        XCTAssertTrue(h.hub.workRequests.isEmpty)
        XCTAssertTrue(h.hub.startRequests.isEmpty)
    }
    private let instant = Date(timeIntervalSince1970: 1_800_000_000)

    private struct Harness {
        let hub: StubActionHub
        let guardian: RecordingGuard
        let journal: PendingWriteJournal
        let journalURL: URL
        let store: ActionStore
    }

    private func makeHarness(hub: StubActionHub = StubActionHub(), journalURL: URL? = nil,
                             paired: Bool = true, keys: SequentialKeys = SequentialKeys(),
                             hubID: String? = nil) throws -> Harness {
        let guardian = RecordingGuard()
        let url = try journalURL ?? makeTemporaryDirectory().appendingPathComponent("desk-writes.json")
        let journal = PendingWriteJournal(fileURL: url, hubID: { hubID })
        let now = instant
        let store = ActionStore(hub: hub, journal: journal, guardian: guardian, isPaired: { paired },
                                deviceID: { "phone" },
                                now: { now }, makeKey: { keys.next() })
        return Harness(hub: hub, guardian: guardian, journal: journal, journalURL: url, store: store)
    }

    func testTodayActionWaitsOnDiskCanBeUndoneAndReplaysWithTheSameKey() async throws {
        let h = try makeHarness(hubID: "mac")
        let item = try JSONDecoder().decode(ChatProactiveView.self, from: Data(#"{"momentId":"m1","kind":"reminder","state":"pending","actionIds":["done"],"actionLabels":{"done":"Done"}}"#.utf8))
        let outcome = await h.store.queueTodayAction(chatID: "chat", item: item, actionID: "done")
        guard case .pending = outcome else { return XCTFail("Expected queued action: \(outcome)") }
        XCTAssertTrue(h.hub.proactiveRequests.isEmpty)
        let firstKey = try XCTUnwrap(h.store.pendingToday.first?.key)
        await h.store.undoToday(firstKey)
        XCTAssertTrue(h.store.pendingToday.isEmpty)
        XCTAssertTrue(h.hub.proactiveRequests.isEmpty)

        _ = await h.store.queueTodayAction(chatID: "chat", item: item, actionID: "done")
        h.hub.failNext([HubRequestError.noAnswer])
        await h.store.retryToday()
        XCTAssertEqual(h.hub.proactiveRequests.count, 1)
        XCTAssertEqual(h.store.pendingToday.first?.todayReplay?.dispatched, true)
        await h.store.retryToday()
        XCTAssertEqual(h.hub.proactiveRequests.count, 2)
        XCTAssertEqual(h.hub.proactiveRequests[0].key, h.hub.proactiveRequests[1].key)
        XCTAssertTrue(h.store.pendingToday.isEmpty)
    }

    func testExpiredTodayActionCannotBeReplayedThroughChat() async throws {
        let h = try makeHarness(hubID: "mac")
        let item = try JSONDecoder().decode(ChatProactiveView.self, from: Data(#"{"momentId":"m1","kind":"reminder","state":"pending","actionIds":["done"],"actionLabels":{"done":"Done"}}"#.utf8))
        let sent = instant.addingTimeInterval(-ActionStore.journalRetention)
        let write = try await h.journal.reserveToday(chatID: "chat", item: item, actionID: "done", deviceID: "phone",
                                                     makeKey: { "old" }, createdAt: sent)
        _ = try await h.journal.markTodayDispatched(key: write.key, at: sent)
        await h.store.loadPendingToday()
        XCTAssertEqual(h.store.pendingToday.first?.todayState, .outcomeUnknown)
        await h.store.retryToday()
        _ = await h.store.performProactiveAction(chatID: "chat", momentID: "m1", actionID: "done")
        XCTAssertTrue(h.hub.proactiveRequests.isEmpty)
        let remaining = await h.journal.entries()
        XCTAssertEqual(remaining.first?.key, write.key)
    }

    func testTodayMemoryCorrectionWaitsForReconnectAndCanBeUndone() async throws {
        let h = try makeHarness(hubID: "mac")
        let first = await h.store.queueTodayCorrection(memoryID: "memory-1", reason: "Incorrect", briefID: "brief", bodyVersion: "v1")
        XCTAssertTrue(first)
        XCTAssertTrue(h.hub.memoryRejections.isEmpty)
        let key = try XCTUnwrap(h.store.pendingToday.first?.key)
        await h.store.undoToday(key)
        XCTAssertTrue(h.store.pendingToday.isEmpty)
        let second = await h.store.queueTodayCorrection(memoryID: "memory-1", reason: "Incorrect", briefID: "brief", bodyVersion: "v1")
        XCTAssertTrue(second)
        h.hub.failNext([HubRequestError.noAnswer])
        await h.store.retryToday()
        await h.store.retryToday()
        XCTAssertEqual(h.hub.memoryRejections.count, 2)
        XCTAssertEqual(h.hub.memoryRejections[0].key, h.hub.memoryRejections[1].key)
        XCTAssertTrue(h.store.pendingToday.isEmpty)
    }

    private func workDraft(title: String = "Draft the Q3 outline", brief: String = "Use the notes in the review folder",
                           folder: String = "") -> OneTimeWorkDraft {
        var draft = OneTimeWorkDraft()
        draft.title = title
        draft.brief = brief
        draft.workDir = folder
        return draft
    }

    // MARK: Not right on a memory

    func testRejectingAMemorySendsTheReasonUnderAKeyWithoutOwnerAuthentication() async throws {
        let h = try makeHarness()

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "  Sam is not the owner  ")

        XCTAssertTrue(removed)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        XCTAssertEqual(h.hub.memoryRejections.map(\.memoryID), ["memory-7"])
        XCTAssertEqual(h.hub.memoryRejections.map(\.reason), ["Sam is not the owner"])
        XCTAssertEqual(h.hub.memoryRejections.map(\.key), ["action-key-1"])
        XCTAssertEqual(h.store.notice, "Recorded on your computer. Isoform will stop using that memory.")
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testMemoryCorrectionDoesNotAskForOwnerAuthentication() async throws {
        let h = try makeHarness()
        h.guardian.deny()

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")

        XCTAssertTrue(removed)
        XCTAssertEqual(h.hub.memoryRejections.count, 1)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testARejectionWithoutAReasonIsNotSent() async throws {
        let h = try makeHarness()

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "   ")

        XCTAssertFalse(removed)
        XCTAssertTrue(h.hub.memoryRejections.isEmpty)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        XCTAssertEqual(h.store.notice, "Write a short reason before you send this to your computer.")
    }

    func testALongReasonIsCutToTheLimit() async throws {
        let h = try makeHarness()

        _ = await h.store.rejectAttribution(memoryID: "memory-7", reason: String(repeating: "a", count: 900))

        XCTAssertEqual(h.hub.memoryRejections.first?.reason.count, CorrectionReason.maxLength)
    }

    func testAReasonInChineseIsCutToTheMacsByteLimit() async throws {
        let h = try makeHarness()

        _ = await h.store.rejectAttribution(memoryID: "memory-7", reason: String(repeating: "错", count: 400))

        let reason = try XCTUnwrap(h.hub.memoryRejections.first?.reason)
        XCTAssertLessThanOrEqual(reason.utf8.count, CorrectionReason.maxBytes)
        XCTAssertEqual(reason.count, 333)
    }

    func testAMemoryIdThatIsUnsafeInAPathIsNeverSent() async throws {
        let h = try makeHarness()

        let removed = await h.store.rejectAttribution(memoryID: "memory/7", reason: "Wrong")

        XCTAssertFalse(removed)
        XCTAssertTrue(h.hub.memoryRejections.isEmpty)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
    }

    func testAMemoryThatIsGoneOnTheMacSaysNothingChanged() async throws {
        let h = try makeHarness()
        h.hub.answerRemoval(false)

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")

        XCTAssertFalse(removed)
        XCTAssertEqual(h.store.notice, "Your computer no longer has that memory. Nothing changed.")
    }

    func testAConflictOnARejectionDropsTheKeyAndSaysItChanged() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.conflict(message: "Changed.")])

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")

        XCTAssertFalse(removed)
        XCTAssertEqual(h.store.notice, "This changed on your computer. Review it again.")
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAForbiddenAnswerDropsTheKeyAndNamesThePairingFix() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.forbidden])

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")

        XCTAssertFalse(removed)
        XCTAssertEqual(h.store.notice, HubRequestError.forbidden.errorDescription)
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAnUnpairedStoreSendsNothing() async throws {
        let h = try makeHarness(paired: false)

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")
        let started = await h.store.startOneTimeWork(workDraft())

        XCTAssertFalse(removed)
        XCTAssertFalse(started)
        XCTAssertTrue(h.hub.memoryRejections.isEmpty)
        XCTAssertTrue(h.hub.workRequests.isEmpty)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
    }

    // MARK: Retries

    func testRetryAfterNoAnswerReusesTheSameKey() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])

        let first = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")
        XCTAssertFalse(first)
        XCTAssertEqual(h.store.notice, HubRequestError.noAnswer.errorDescription)
        let kept = await h.journal.entries()
        XCTAssertEqual(kept.map(\.key), ["action-key-1"])
        XCTAssertEqual(kept.first?.operation, .correction)
        XCTAssertEqual(kept.first?.target, "memory-7")
        XCTAssertEqual(kept.first?.choice, "reject_attribution")

        let second = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")
        XCTAssertTrue(second)
        XCTAssertEqual(h.hub.memoryRejections.map(\.key), ["action-key-1", "action-key-1"])
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testRetryAfterARelaunchStillReusesTheSavedKey() async throws {
        let first = try makeHarness()
        first.hub.failNext([HubRequestError.noAnswer])
        await first.store.startOneTimeWork(workDraft())

        // A new launch: a new store and journal on the same file, with a key source that would issue a new key.
        let second = try makeHarness(hub: first.hub, journalURL: first.journalURL, keys: SequentialKeys())
        let started = await second.store.startOneTimeWork(workDraft())

        XCTAssertTrue(started)
        XCTAssertEqual(first.hub.workRequests.map(\.idempotencyKey), ["action-key-1", "action-key-1"])
        let remaining = await second.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAnUnavailableMacKeepsTheKeyForAReplay() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.rejected(status: 503, message: "the Mac's store is not ready")])

        let started = await h.store.startOneTimeWork(workDraft())

        XCTAssertFalse(started)
        XCTAssertEqual(h.store.notice, "the Mac's store is not ready")
        let kept = await h.journal.entries()
        XCTAssertEqual(kept.map(\.key), ["action-key-1"])
        XCTAssertEqual(kept.first?.operation, .handoff)
    }

    func testChangedWorkGetsItsOwnKey() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])
        await h.store.startOneTimeWork(workDraft(title: "First title"))

        await h.store.startOneTimeWork(workDraft(title: "Second title"))

        XCTAssertEqual(h.hub.workRequests.map(\.idempotencyKey), ["action-key-1", "action-key-2"])
        let remaining = await h.journal.entries()
        XCTAssertEqual(remaining.map(\.key), ["action-key-1"])
    }

    // MARK: One-time work

    func testSavingOneTimeWorkSendsTheDraftUnderAKeyWithoutOwnerAuthentication() async throws {
        let h = try makeHarness()

        let started = await h.store.startOneTimeWork(workDraft(folder: "  /Users/me/src/notes  "))

        XCTAssertTrue(started)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        let request = try XCTUnwrap(h.hub.workRequests.first)
        XCTAssertEqual(request.title, "Draft the Q3 outline")
        XCTAssertEqual(request.brief, "Use the notes in the review folder")
        XCTAssertEqual(request.workDir, "/Users/me/src/notes")
        XCTAssertEqual(request.idempotencyKey, "action-key-1")
        XCTAssertEqual(h.store.notice, "Saved on your computer as one-time work. It starts when you start it from One-time work on your computer.")
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testOneTimeWorkFolderPlaceholderPassesValidation() async throws {
        let h = try makeHarness()

        let started = await h.store.startOneTimeWork(workDraft(folder: OneTimeWorkComposer.folderPlaceholder))

        XCTAssertTrue(started)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        XCTAssertEqual(h.hub.workRequests.count, 1)
        XCTAssertEqual(h.hub.workRequests.first?.workDir, "/Users/you/src/notes")
    }

    func testOneTimeWorkNeedsTheFullFolderPathOrNoFolder() async throws {
        let h = try makeHarness()

        let started = await h.store.startOneTimeWork(workDraft(folder: "~/src/notes"))

        XCTAssertFalse(started)
        XCTAssertTrue(h.hub.workRequests.isEmpty)
        XCTAssertTrue(h.guardian.reasons.isEmpty, "a folder the Mac would refuse is caught before the owner check")
        XCTAssertEqual(h.store.notice, "Use the full folder path on your computer, starting with /, or leave the folder empty.")
    }

    func testOneTimeWorkNeedsATitleAndWhatToDo() async throws {
        let h = try makeHarness()

        let started = await h.store.startOneTimeWork(workDraft(title: "Only a title", brief: "  "))

        XCTAssertFalse(started)
        XCTAssertTrue(h.hub.workRequests.isEmpty)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        XCTAssertEqual(h.store.notice, "Add a title and what to do first.")
    }

    func testSavingOneTimeWorkDoesNotAskForOwnerAuthentication() async throws {
        let h = try makeHarness()
        h.guardian.deny()

        let started = await h.store.startOneTimeWork(workDraft())

        XCTAssertTrue(started)
        XCTAssertEqual(h.hub.workRequests.count, 1)
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAnEmptyFolderIsSentAsEmpty() {
        XCTAssertEqual(workDraft().request(idempotencyKey: "k").workDir, "")
    }

    // MARK: Proactive cards

    func testLocalProactiveChoicesDoNotAskForOwnerAuthentication() async throws {
        for action in ["open", "answer", "review", "open_settings", "see", "open_person", "not_now", "dismiss", "send"] {
            let h = try makeHarness()
            h.guardian.deny()
            let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1",
                                                               actionID: action, permissionRequest: false)
            XCTAssertEqual(outcome, .applied(state: "opened"), action)
            XCTAssertTrue(h.guardian.reasons.isEmpty, action)
            XCTAssertEqual(h.hub.proactiveRequests.map(\.actionID), [action])
        }
    }

    func testExecutionAndUnknownActionsStillRequireOwnerAuthentication() async throws {
        for action in ["approve", "do_once", "add_to_upcoming", "stop", "pay", "delete", "future_action"] {
            let h = try makeHarness()
            h.guardian.deny()
            let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1",
                                                               actionID: action, permissionRequest: false)
            XCTAssertEqual(outcome, .refused(message: DecisionGuardError.notConfirmed.errorDescription!), action)
            XCTAssertEqual(h.guardian.reasons.count, 1, action)
            XCTAssertTrue(h.hub.proactiveRequests.isEmpty, action)
            let remaining = await h.journal.entries()
            XCTAssertTrue(remaining.isEmpty, action)
        }
    }

    func testPermissionCardCannotBypassAuthenticationWithADismissAction() async throws {
        let h = try makeHarness()
        h.guardian.deny()
        let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: "permission-1",
                                                           actionID: "dismiss", permissionRequest: true)
        XCTAssertEqual(outcome, .refused(message: DecisionGuardError.notConfirmed.errorDescription!))
        XCTAssertTrue(h.hub.proactiveRequests.isEmpty)
    }

    func testProactiveActionUsesOwnerConfirmationAndAJournaledKey() async throws {
        let h = try makeHarness()
        let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")

        XCTAssertEqual(outcome, .applied(state: "opened"))
        XCTAssertEqual(h.guardian.reasons.count, 1)
        XCTAssertEqual(h.hub.proactiveRequests.map(\.chatID), ["chat-1"])
        XCTAssertEqual(h.hub.proactiveRequests.map(\.momentID), ["moment-1"])
        XCTAssertEqual(h.hub.proactiveRequests.map(\.actionID), ["open"])
        XCTAssertEqual(h.hub.proactiveRequests.map(\.key), ["action-key-1"])
        let entries = await h.journal.entries()
        XCTAssertTrue(entries.isEmpty)
        XCTAssertNil(h.store.notice, "the card presents the outcome inline")
    }

    func testDecliningProactiveOwnerCheckSendsNothingAndKeepsTheCardAvailable() async throws {
        let h = try makeHarness()
        h.guardian.deny()

        let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")

        XCTAssertEqual(outcome, .refused(message: DecisionGuardError.notConfirmed.errorDescription!))
        XCTAssertTrue(h.hub.proactiveRequests.isEmpty)
        let entries = await h.journal.entries()
        XCTAssertTrue(entries.isEmpty)
    }

    func testProactiveActionPreservesAStoredWorkIdentityContainingAPath() async throws {
        let h = try makeHarness()
        let momentID = "handoff:work:blocked:Review /Users/you/报告 first:100"

        let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: momentID, actionID: "review")

        XCTAssertEqual(outcome, .applied(state: "opened"))
        XCTAssertEqual(h.guardian.reasons.count, 1)
        XCTAssertEqual(h.hub.proactiveRequests.map(\.momentID), [momentID])
    }

    func testPendingProactiveJournalDoesNotKeepDisplayTextEmbeddedInTheIdentity() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])
        let momentID = "handoff:work:blocked:Review /Users/you/报告 first:100"

        _ = await h.store.performProactiveAction(chatID: "chat-1", momentID: momentID, actionID: "review")

        let entries = await h.journal.entries()
        XCTAssertEqual(entries.map(\.target), ["proactive:" + WriteFingerprint.make(["chat-1", momentID])])
        let saved = try String(contentsOf: h.journalURL, encoding: .utf8)
        XCTAssertFalse(saved.contains(momentID))
        XCTAssertFalse(saved.contains("Review"))
        XCTAssertFalse(saved.contains("/Users/you"))
    }

    func testUnansweredProactiveActionReusesItsKeyAfterRelaunch() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])
        let first = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")
        guard case .pending = first else { return XCTFail("an unanswered action must stay pending") }
        let entries = await h.journal.entries()
        XCTAssertEqual(entries.map(\.operation), [.chatProactiveAction])

        let relaunched = try makeHarness(hub: h.hub, journalURL: h.journalURL)
        let retry = await relaunched.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")

        XCTAssertEqual(retry, .applied(state: "opened"))
        XCTAssertEqual(h.hub.proactiveRequests.map(\.key), ["action-key-1", "action-key-1"])
        let remaining = await relaunched.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAcceptedPermissionAwaitingReceiptStorageKeepsItsKeyAcrossRelaunch() async throws {
        let h = try makeHarness()
        h.hub.answerProactive("acted")
        h.hub.failNext([HubRequestError.rejected(status: 503, message: "The accepted answer is waiting to be saved.")])
        let momentID = "work-permission:request-1"

        let first = await h.store.performProactiveAction(chatID: "chat-1", momentID: momentID, actionID: "approve")
        guard case .pending = first else { return XCTFail("an accepted answer awaiting storage is not a refusal") }
        let saved = await h.journal.entries()
        XCTAssertEqual(saved.map(\.key), ["action-key-1"])
        XCTAssertEqual(saved.map(\.choice), ["approve"])

        let relaunched = try makeHarness(hub: h.hub, journalURL: h.journalURL)
        let opposite = await relaunched.store.performProactiveAction(chatID: "chat-1", momentID: momentID, actionID: "decline")
        guard case .pending = opposite else { return XCTFail("the unresolved approval must block a contradictory choice") }
        XCTAssertEqual(h.hub.proactiveRequests.count, 1)
        let retry = await relaunched.store.performProactiveAction(chatID: "chat-1", momentID: momentID, actionID: "approve")

        XCTAssertEqual(retry, .applied(state: "acted"))
        XCTAssertEqual(h.hub.proactiveRequests.map(\.key), ["action-key-1", "action-key-1"])
        XCTAssertEqual(h.hub.proactiveRequests.map(\.actionID), ["approve", "approve"])
        let remaining = await relaunched.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testDifferentProactiveChoiceWaitsForTheUnansweredChoice() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])
        _ = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")

        let outcome = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "dismiss")

        guard case .pending = outcome else { return XCTFail("a different choice must wait for the first answer") }
        XCTAssertEqual(h.hub.proactiveRequests.map(\.actionID), ["open"])
        XCTAssertEqual(h.guardian.reasons.count, 1)
        let entries = await h.journal.entries()
        XCTAssertEqual(entries.map(\.key), ["action-key-1"])
    }

    func testRefusedProactiveActionRestoresTheCardAndAllowsAnotherAttempt() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.rejected(status: 400, message: "This action is no longer offered.")])

        let refused = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")
        XCTAssertEqual(refused, .refused(message: "This action is no longer offered."))
        let entries = await h.journal.entries()
        XCTAssertTrue(entries.isEmpty)

        let retried = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")
        XCTAssertEqual(retried, .applied(state: "opened"))
        XCTAssertEqual(h.hub.proactiveRequests.map(\.key), ["action-key-1", "action-key-2"])
    }

    func testProactiveActionSendsNothingWhenTheJournalCannotSave() async throws {
        let hub = StubActionHub()
        let guardian = RecordingGuard()
        let url = try makeTemporaryDirectory().appendingPathComponent("writes.json")
        let journal = PendingWriteJournal(fileURL: url, writeData: { _, _ in throw CocoaError(.fileWriteNoPermission) })
        let store = ActionStore(hub: hub, journal: journal, guardian: guardian, isPaired: { true })

        let outcome = await store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")

        guard case .refused(let message) = outcome else { return XCTFail("an unsaved write must be refused") }
        XCTAssertTrue(message.contains("nothing was sent"))
        XCTAssertTrue(hub.proactiveRequests.isEmpty)
        XCTAssertEqual(guardian.reasons.count, 1)
    }

    func testInvalidOrUnpairedProactiveActionsNeverAskTheOwnerOrSend() async throws {
        let h = try makeHarness()
        _ = await h.store.performProactiveAction(chatID: "chat/1", momentID: "moment-1", actionID: "open")
        _ = await h.store.performProactiveAction(chatID: "chat-1", momentID: " ", actionID: "open")
        _ = await h.store.performProactiveAction(chatID: "chat-1", momentID: String(repeating: "a", count: 48 * 1024 + 1), actionID: "open")
        _ = await h.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "")
        XCTAssertTrue(h.guardian.reasons.isEmpty)
        XCTAssertTrue(h.hub.proactiveRequests.isEmpty)

        let unpaired = try makeHarness(paired: false)
        _ = await unpaired.store.performProactiveAction(chatID: "chat-1", momentID: "moment-1", actionID: "open")
        XCTAssertTrue(unpaired.guardian.reasons.isEmpty)
        XCTAssertTrue(unpaired.hub.proactiveRequests.isEmpty)
    }

    // MARK: Journal

    func testPruningDropsRetiredDeskWritesAndOldWritesButKeepsCurrentOnes() async throws {
        let h = try makeHarness()
        let recent = instant.addingTimeInterval(-60)
        let old = instant.addingTimeInterval(-ActionStore.journalRetention - 60)
        let writes = [
            PendingWrite(key: "decision", operation: .decision, target: "req-1", choice: "approve",
                         fingerprint: "f1", createdAt: recent),
            PendingWrite(key: "command", operation: .initiativeCommand, target: "init-1", choice: "stop",
                         fingerprint: "f2", createdAt: recent),
            PendingWrite(key: "feedback", operation: .initiativeFeedback, target: "init-1", choice: "useful",
                         fingerprint: "f3", createdAt: recent),
            PendingWrite(key: "review", operation: .selfUpdateReview, target: "op-1", choice: "accept",
                         fingerprint: "f4", createdAt: recent),
            PendingWrite(key: "priority", operation: .purpose, target: "new-priority", choice: "create",
                         fingerprint: "f5", createdAt: recent),
            PendingWrite(key: "revise", operation: .correction, target: "purpose-1", choice: "revise",
                         fingerprint: "f6", createdAt: recent),
            PendingWrite(key: "memory", operation: .correction, target: "memory-7", choice: "reject_attribution",
                         fingerprint: "f7", createdAt: recent),
            PendingWrite(key: "work", operation: .handoff, target: "new-one-time-work", choice: "create",
                         fingerprint: "f8", createdAt: recent),
            PendingWrite(key: "message", operation: .chatMessage, target: "chat-1", choice: "send",
                         fingerprint: "f9", createdAt: recent),
            PendingWrite(key: "offer", operation: .offerDecision, target: "suggestion:notes", choice: "dismiss",
                         fingerprint: "f-offer", createdAt: recent),
            PendingWrite(key: "old-message", operation: .chatMessage, target: "chat-1", choice: "send",
                         fingerprint: "f10", createdAt: old),
            PendingWrite(key: "proactive", operation: .chatProactiveAction, target: "chat-1/moment-1", choice: "open",
                         fingerprint: "f11", createdAt: recent),
        ]
        for write in writes {
            try await h.journal.insert(write)
        }

        await h.store.pruneJournal()

        let remaining = await h.journal.entries()
        XCTAssertEqual(remaining.map(\.key), ["memory", "work", "message", "offer", "proactive"])
    }

    func testAJournalAnOlderBuildWroteStillDecodesSoItsLiveWritesReplay() async throws {
        // The file an older build left behind, with a Desk decision next to an unanswered memory rejection.
        let url = try makeTemporaryDirectory().appendingPathComponent("desk-writes.json")
        let fingerprint = WriteFingerprint.make(["memory-7", "Wrong"])
        let legacy = """
        [{"choice":"approve","createdAt":"2027-01-15T08:00:00Z","fingerprint":"digest","key":"old-decision","operation":"decision","target":"req-1"},
         {"choice":"reject_attribution","createdAt":"2027-01-15T08:00:00Z","fingerprint":"\(fingerprint)","key":"old-memory","operation":"correction","target":"memory-7"}]
        """
        try Data(legacy.utf8).write(to: url)
        let h = try makeHarness(journalURL: url)

        await h.store.pruneJournal()
        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")

        XCTAssertTrue(removed)
        XCTAssertEqual(h.hub.memoryRejections.map(\.key), ["old-memory"])
        let remaining = await h.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testAnUnknownOrRetiredGoalWriteSkipsOnlyItsOwnEntry() async throws {
        // A round-3 goal_create next to an operation from a newer build and an unanswered memory rejection.
        let url = try makeTemporaryDirectory().appendingPathComponent("goal-writes.json")
        let fingerprint = WriteFingerprint.make(["memory-7", "Wrong"])
        let mixed = """
        [{"choice":"create","createdAt":"2027-01-15T08:00:00Z","fingerprint":"digest","key":"old-goal","operation":"goal_create","target":"new-goal"},
         {"choice":"send","createdAt":"2027-01-15T08:00:00Z","fingerprint":"digest","key":"future","operation":"from_a_newer_build","target":"x"},
         {"choice":"reject_attribution","createdAt":"2027-01-15T08:00:00Z","fingerprint":"\(fingerprint)","key":"old-memory","operation":"correction","target":"memory-7"}]
        """
        try Data(mixed.utf8).write(to: url)
        let h = try makeHarness(journalURL: url)

        let decoded = await h.journal.allEntries()
        XCTAssertEqual(decoded.map(\.key), ["old-goal", "old-memory"], "only the unknown entry is skipped")
        await h.store.pruneJournal()
        let afterPrune = await h.journal.entries()
        XCTAssertEqual(afterPrune.map(\.key), ["old-memory"], "a goal_create is retired")

        let removed = await h.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")
        XCTAssertTrue(removed)
        XCTAssertEqual(h.hub.memoryRejections.map(\.key), ["old-memory"], "the live write replays under its key")
    }

    func testARoundThreeGoalCreateKeepsOtherPendingKeysAcrossPruneAndRelaunch() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("desk-writes.json")
        let draft = workDraft()
        let memoryFingerprint = WriteFingerprint.make(["memory-7", "Wrong"])
        let workFingerprint = WriteFingerprint.make([draft.trimmedTitle, draft.trimmedBrief, draft.trimmedWorkDir])
        // Keep the literal retired operation between two live writes, as a round-3 build wrote it.
        let legacy = """
        [{"choice":"reject_attribution","createdAt":"2027-01-15T08:00:00Z","fingerprint":"\(memoryFingerprint)","key":"old-memory","operation":"correction","target":"memory-7"},
         {"choice":"create","createdAt":"2027-01-15T08:00:00Z","fingerprint":"digest","key":"old-goal","operation":"goal_create","target":"new-goal"},
         {"choice":"create","createdAt":"2027-01-15T08:00:00Z","fingerprint":"\(workFingerprint)","key":"old-work","operation":"handoff","target":"new-one-time-work"}]
        """
        try Data(legacy.utf8).write(to: url)
        let first = try makeHarness(journalURL: url)

        await first.store.pruneJournal()
        let afterPrune = await first.journal.entries()
        XCTAssertEqual(afterPrune.map(\.key), ["old-memory", "old-work"])
        first.hub.failNext([HubRequestError.noAnswer])
        let unanswered = await first.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")
        XCTAssertFalse(unanswered)

        let reopened = try makeHarness(hub: first.hub, journalURL: url)
        let restored = await reopened.journal.entries()
        XCTAssertEqual(restored.map(\.key), ["old-memory", "old-work"], "pruning and an unanswered retry preserve both saved keys")
        let removed = await reopened.store.rejectAttribution(memoryID: "memory-7", reason: "Wrong")
        let started = await reopened.store.startOneTimeWork(draft)

        XCTAssertTrue(removed)
        XCTAssertTrue(started)
        XCTAssertEqual(reopened.hub.memoryRejections.map(\.key), ["old-memory", "old-memory"])
        XCTAssertEqual(reopened.hub.workRequests.map(\.idempotencyKey), ["old-work"])
        let remaining = await reopened.journal.entries()
        XCTAssertTrue(remaining.isEmpty)
        XCTAssertFalse(FileManager.default.fileExists(atPath: url.path))
    }

    func testPruningIncludesPreviousHubsAndUnscopedLegacyWrites() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("writes.json")
        let journal = PendingWriteJournal(fileURL: url, hubID: { "hub-b" })
        let recent = instant.addingTimeInterval(-60)
        let old = instant.addingTimeInterval(-ActionStore.journalRetention - 60)
        let writes = [
            PendingWrite(key: "old-a", operation: .chatMessage, target: "chat", choice: "send",
                         fingerprint: "f1", createdAt: old, hubID: "hub-a"),
            PendingWrite(key: "retired-a", operation: .decision, target: "request", choice: "approve",
                         fingerprint: "f2", createdAt: recent, hubID: "hub-a"),
            PendingWrite(key: "old-unscoped", operation: .chatMessage, target: "chat", choice: "send",
                         fingerprint: "f3", createdAt: old),
            PendingWrite(key: "retired-unscoped", operation: .purpose, target: "priority", choice: "create",
                         fingerprint: "f4", createdAt: recent),
            PendingWrite(key: "old-current", operation: .chatMessage, target: "chat", choice: "send",
                         fingerprint: "f5", createdAt: old, hubID: "hub-b"),
            PendingWrite(key: "keep-a", operation: .correction, target: "memory", choice: "reject_attribution",
                         fingerprint: "f6", createdAt: recent, hubID: "hub-a"),
            PendingWrite(key: "keep-current", operation: .chatMessage, target: "chat", choice: "send",
                         fingerprint: "f7", createdAt: recent, hubID: "hub-b"),
            PendingWrite(key: "keep-unscoped", operation: .handoff, target: "work", choice: "create",
                         fingerprint: "f8", createdAt: recent),
        ]
        for write in writes { try await journal.insert(write) }
        let now = instant
        let store = ActionStore(hub: StubActionHub(), journal: journal, guardian: RecordingGuard(),
                                isPaired: { true }, now: { now })

        await store.pruneJournal()

        let visible = await journal.entries()
        XCTAssertEqual(visible.map(\.key), ["keep-current"])
        let reopened = PendingWriteJournal(fileURL: url, hubID: { "hub-b" })
        let remaining = await reopened.allEntries()
        XCTAssertEqual(remaining.map(\.key), ["keep-a", "keep-current", "keep-unscoped"])
    }

    func testTheJournalKeepsWritesOnDiskUntilTheyAreRemoved() async throws {
        let url = try makeTemporaryDirectory().appendingPathComponent("desk-writes.json")
        let write = PendingWrite(key: "key-9", operation: .handoff, target: "new-one-time-work", choice: "create",
                                 fingerprint: "digest", createdAt: instant)
        let first = PendingWriteJournal(fileURL: url)
        try await first.insert(write)

        let reopened = PendingWriteJournal(fileURL: url)
        let loaded = await reopened.entries()
        XCTAssertEqual(loaded, [write])

        await reopened.remove(keys: ["key-9"])
        let afterRemoval = await PendingWriteJournal(fileURL: url).entries()
        XCTAssertTrue(afterRemoval.isEmpty)
        XCTAssertFalse(FileManager.default.fileExists(atPath: url.path))
    }

    func testTheJournalKeepsItsFileName() {
        XCTAssertEqual(PendingWriteJournal.defaultFileURL.lastPathComponent, "desk-writes.json")
        XCTAssertEqual(PendingWriteJournal.defaultFileURL.deletingLastPathComponent().lastPathComponent, "Isoform")
    }

    func testFingerprintsSeparateRequestsWithSimilarText() {
        XCTAssertNotEqual(WriteFingerprint.make(["ab", "c"]), WriteFingerprint.make(["a", "bc"]))
        XCTAssertEqual(WriteFingerprint.make(["same", "parts"]), WriteFingerprint.make(["same", "parts"]))
    }
}

private final class JournalHubIdentity: @unchecked Sendable {
    private let lock = NSLock()
    private var value = "hub-a"
    func current() -> String {
        lock.lock()
        defer { lock.unlock() }
        return value
    }
    func set(_ value: String) {
        lock.lock()
        defer { lock.unlock() }
        self.value = value
    }
}

private final class JournalWriteGate: @unchecked Sendable {
    private let lock = NSLock()
    private var shouldFail = true

    func allowWrites() {
        lock.lock()
        shouldFail = false
        lock.unlock()
    }

    func write(_ data: Data, to url: URL) throws {
        lock.lock()
        let fails = shouldFail
        lock.unlock()
        if fails { throw CocoaError(.fileWriteUnknown) }
        try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
}

final class PendingWriteHubScopeTests: XCTestCase {
    func testIdenticalWritesOnDifferentHubsNeverReuseEachOthersKey() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let identity = JournalHubIdentity()
        let file = directory.appendingPathComponent("writes.json")
        let journal = PendingWriteJournal(fileURL: file, hubID: { identity.current() })
        let createdAt = Date(timeIntervalSince1970: 1_800_000_000)
        let first = try await journal.reserve(operation: .handoff, target: "new", choice: "create",
                                               fingerprint: "same", makeKey: { "key-a" }, createdAt: createdAt)
        identity.set("hub-b")
        let second = try await journal.reserve(operation: .handoff, target: "new", choice: "create",
                                                fingerprint: "same", makeKey: { "key-b" }, createdAt: createdAt)
        XCTAssertEqual(first.hubID, "hub-a")
        XCTAssertEqual(second.hubID, "hub-b")
        XCTAssertNotEqual(first.key, second.key)
        let visible = await journal.entries()
        XCTAssertEqual(visible, [second])
        let reopened = PendingWriteJournal(fileURL: file, hubID: { identity.current() })
        let retriedB = await reopened.existing(operation: .handoff, target: "new", fingerprint: "same")
        XCTAssertEqual(retriedB, second)
        identity.set("hub-a")
        let retriedA = await reopened.existing(operation: .handoff, target: "new", fingerprint: "same")
        XCTAssertEqual(retriedA, first)
    }

    func testLegacyUnscopedWriteIsAdoptedAndPersistedBeforeItReplays() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("writes.json")
        let legacy = PendingWriteJournal(fileURL: file)
        try await legacy.insert(PendingWrite(key: "legacy-key", operation: .chatMessage, target: "chat-1",
                                             choice: "send", fingerprint: "same", createdAt: Date()))
        let scoped = PendingWriteJournal(fileURL: file, hubID: { "hub-b" })
        let writer = JournaledWriter(journal: scoped, makeKey: { "fresh-key" }, now: { Date() })

        let outcome: JournaledOutcome<Void> = await writer.send(.chatMessage, target: "chat-1", choice: "send",
                                                                 fingerprint: "same") { key in
            XCTAssertEqual(key, "legacy-key")
            XCTAssertEqual(HubRequestScope.hubID, "hub-b")
            let persisted = await PendingWriteJournal(fileURL: file).allEntries()
            XCTAssertEqual(persisted.map(\.key), ["legacy-key"])
            XCTAssertEqual(persisted.map(\.hubID), ["hub-b"])
            throw HubRequestError.noAnswer
        }

        guard case .failed(_, .keep) = outcome else { return XCTFail("expected the unanswered write to stay saved") }
        let remaining = await scoped.entries()
        XCTAssertEqual(remaining.map(\.key), ["legacy-key"])
    }

    func testLegacyAdoptionKeepsAllOldKeysOnTheirFirstHubAcrossSwitchesAndRestart() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("writes.json")
        let legacy = PendingWriteJournal(fileURL: file)
        let createdAt = Date(timeIntervalSince1970: 1_800_000_000)
        for target in ["chat-1", "chat-2"] {
            try await legacy.insert(PendingWrite(key: "legacy-\(target)", operation: .chatMessage, target: target,
                                                 choice: "send", fingerprint: "same", createdAt: createdAt))
        }
        try await legacy.insert(PendingWrite(key: "other-hub-key", operation: .chatMessage, target: "chat-3",
                                             choice: "send", fingerprint: "same", createdAt: createdAt, hubID: "hub-c"))
        let identity = JournalHubIdentity()
        let scoped = PendingWriteJournal(fileURL: file, hubID: { identity.current() })
        let first = try await scoped.reserve(operation: .chatMessage, target: "chat-1", choice: "send",
                                             fingerprint: "same", makeKey: { "unused-a" }, createdAt: createdAt)
        XCTAssertEqual(first.key, "legacy-chat-1")
        let adopted = await scoped.allEntries()
        XCTAssertEqual(adopted.map(\.hubID), ["hub-a", "hub-a", "hub-c"])

        identity.set("hub-b")
        let second = try await scoped.reserve(operation: .chatMessage, target: "chat-2", choice: "send",
                                              fingerprint: "same", makeKey: { "new-b" }, createdAt: createdAt)
        XCTAssertEqual(second.key, "new-b", "a later hub cannot adopt even an old write that was not retried yet")
        let reopened = PendingWriteJournal(fileURL: file, hubID: { identity.current() })
        let afterRestart = try await reopened.reserve(operation: .chatMessage, target: "chat-1", choice: "send",
                                                      fingerprint: "same", makeKey: { "new-b-after-restart" }, createdAt: createdAt)
        XCTAssertEqual(afterRestart.key, "new-b-after-restart")

        identity.set("hub-a")
        let original = try await reopened.reserve(operation: .chatMessage, target: "chat-2", choice: "send",
                                                  fingerprint: "same", makeKey: { "unused-a" }, createdAt: createdAt)
        XCTAssertEqual(original.key, "legacy-chat-2")
        let persisted = await PendingWriteJournal(fileURL: file).allEntries()
        XCTAssertEqual(persisted.prefix(3).map(\.hubID), ["hub-a", "hub-a", "hub-c"])
    }

    func testLegacyAdoptionUsesTheHubAtInitializationBeforeAnyReserve() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("writes.json")
        let original = PendingWrite(key: "legacy-key", operation: .chatMessage, target: "chat-1",
                                    choice: "send", fingerprint: "same", createdAt: Date(timeIntervalSince1970: 1_800_000_000))
        try await PendingWriteJournal(fileURL: file).insert(original)
        let identity = JournalHubIdentity()
        let scoped = PendingWriteJournal(fileURL: file, hubID: { identity.current() })
        identity.set("hub-b")
        // Even a restart before the first reserve must not assign the old write to the new Mac.
        let restarted = PendingWriteJournal(fileURL: file, hubID: { identity.current() })
        let adoptedBeforeReserve = await restarted.allEntries()
        XCTAssertEqual(adoptedBeforeReserve.map(\.hubID), ["hub-a"])

        let onNewHub = try await scoped.reserve(operation: .chatMessage, target: "chat-1", choice: "send",
                                                fingerprint: "same", makeKey: { "new-key" }, createdAt: Date())
        XCTAssertEqual(onNewHub.key, "new-key")
        XCTAssertEqual(onNewHub.hubID, "hub-b")
        identity.set("hub-a")
        let onOriginalHub = try await restarted.reserve(operation: .chatMessage, target: "chat-1", choice: "send",
                                                        fingerprint: "same", makeKey: { "unused" }, createdAt: Date())
        XCTAssertEqual(onOriginalHub.key, "legacy-key")
        XCTAssertEqual(onOriginalHub.hubID, "hub-a")
    }

    func testFailedLegacyAdoptionSendsNothingAndKeepsTheOriginalKeyForRetry() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("writes.json")
        let original = PendingWrite(key: "legacy-key", operation: .chatMessage, target: "chat-1",
                                    choice: "send", fingerprint: "same", createdAt: Date(timeIntervalSince1970: 1_800_000_000))
        try await PendingWriteJournal(fileURL: file).insert(original)
        let gate = JournalWriteGate()
        let scoped = PendingWriteJournal(fileURL: file, hubID: { "hub-a" }, writeData: { try gate.write($0, to: $1) })
        let loaded = await scoped.allEntries()
        XCTAssertEqual(loaded, [original])
        let writer = JournaledWriter(journal: scoped, makeKey: { "fresh-key" }, now: { Date() })

        let outcome: JournaledOutcome<Void> = await writer.send(.chatMessage, target: "chat-1", choice: "send",
                                                                 fingerprint: "same") { _ in
            XCTFail("a legacy key must not be sent until its hub assignment is saved")
        }

        guard case .notSaved = outcome else { return XCTFail("expected failed adoption to stop the send") }
        let inMemory = await scoped.allEntries()
        XCTAssertEqual(inMemory, [original])
        let onDisk = await PendingWriteJournal(fileURL: file).allEntries()
        XCTAssertEqual(onDisk, [original])

        gate.allowWrites()
        let retried = try await scoped.reserve(operation: .chatMessage, target: "chat-1", choice: "send",
                                               fingerprint: "same", makeKey: { "fresh-key" }, createdAt: Date())
        XCTAssertEqual(retried.key, "legacy-key")
        XCTAssertEqual(retried.hubID, "hub-a")
        let persisted = await PendingWriteJournal(fileURL: file, hubID: { "hub-a" }).entries()
        XCTAssertEqual(persisted, [retried])
    }

    func testJournaledWriterCarriesItsReservedHubIntoTheRequestTask() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let journal = PendingWriteJournal(fileURL: directory.appendingPathComponent("writes.json"), hubID: { "hub-a" })
        let writer = JournaledWriter(journal: journal, makeKey: { "key" }, now: { Date() })
        let outcome: JournaledOutcome<String?> = await writer.send(.handoff, target: "new", choice: "create",
                                                                   fingerprint: "same") { _ in
            HubRequestScope.hubID
        }
        guard case .sent(let scopedHub) = outcome else { return XCTFail("expected scoped send") }
        XCTAssertEqual(scopedHub, "hub-a")
        XCTAssertNil(HubRequestScope.hubID)
    }
}

/// Send again for a draft the relay took and never answered: undoable only once the relay has let it go (PROTOCOL.md section 23).
final class TodayDraftUndoTests: XCTestCase {
    func testADispatchedDraftCanBeTakenBackOnlyAfterTheRelayLetsItGo() {
        let sent = Date(timeIntervalSince1970: 1_800_000_000)
        let unsent = PendingWrite.TodayDraftSend(taskID: "draft.ui-sow", revision: "r1", text: "Yes", dispatchedAt: nil)
        let dispatched = PendingWrite.TodayDraftSend(taskID: "draft.ui-sow", revision: "r1", text: "Yes", dispatchedAt: sent)
        XCTAssertTrue(PendingWriteJournal.draftSendCanBeUndone(unsent, now: sent))
        XCTAssertFalse(PendingWriteJournal.draftSendCanBeUndone(dispatched, now: sent.addingTimeInterval(3600)),
                       "an answer may still come within the day")
        XCTAssertTrue(PendingWriteJournal.draftSendCanBeUndone(dispatched, now: sent.addingTimeInterval(TaskSendWait.relayRetention)))
        XCTAssertFalse(PendingWriteJournal.draftSendCanBeUndone(nil, now: sent))
    }
}
