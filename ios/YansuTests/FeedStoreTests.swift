import Foundation
import XCTest
@testable import Yansu

/// Records the feedback FeedStore sends, under the keys it used, and can refuse it.
private final class StubFeedHub: FeedHubAPI, @unchecked Sendable {
    struct Sent: Equatable {
        let briefID: String
        let itemID: String
        let action: FeedFeedbackAction
        let reason: String?
        let key: String
    }

    private let lock = NSLock()
    private var failures: [Error] = []
    private var sent: [Sent] = []
    private var effect = "recorded"

    var requests: [Sent] {
        lock.lock()
        defer { lock.unlock() }
        return sent
    }

    func failNext(_ errors: [Error]) {
        lock.lock()
        failures = errors
        lock.unlock()
    }

    func answer(effect: String) {
        lock.lock()
        self.effect = effect
        lock.unlock()
    }

    func sendFeedback(briefID: String, itemID: String, action: FeedFeedbackAction, reason: String?,
                      idempotencyKey: String) async throws -> BriefFeedbackPayload {
        let outcome: (Error?, String) = {
            lock.lock()
            defer { lock.unlock() }
            sent.append(Sent(briefID: briefID, itemID: itemID, action: action, reason: reason, key: idempotencyKey))
            return (failures.isEmpty ? nil : failures.removeFirst(), effect)
        }()
        if let failure = outcome.0 { throw failure }
        return BriefFeedbackPayload(recorded: true, effect: outcome.1)
    }
}

private final class FeedGuard: DecisionGuarding, @unchecked Sendable {
    private let lock = NSLock()
    private var asked = 0
    private var denied = false

    var count: Int {
        lock.lock()
        defer { lock.unlock() }
        return asked
    }

    func deny() {
        lock.lock()
        denied = true
        lock.unlock()
    }

    func authorize(reason: String) async throws {
        let refuse: Bool = {
            lock.lock()
            defer { lock.unlock() }
            asked += 1
            return denied
        }()
        if refuse { throw DecisionGuardError.notConfirmed }
    }
}

private final class FeedKeys: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0

    func next() -> String {
        lock.lock()
        defer { lock.unlock() }
        count += 1
        return "feed-key-\(count)"
    }
}

@MainActor
final class FeedStoreTests: XCTestCase {
    func testLeavingFlushesFeedbackOnceAndPersistsChoice() async throws {
        let h = try makeHarness()
        let site = card()
        let authorized = await h.store.authorizeFeedback(.notRelevant, for: site)
        let approval = try XCTUnwrap(authorized)
        let holds = DecisionHoldCoordinator(sleep: { _ in try await Task.sleep(for: .seconds(60)) },
                                            beginBackgroundTask: { _ in .invalid }, endBackgroundTask: { _ in })
        holds.begin(.init(id: site.key, label: "Fewer like this", detail: "github.com")) {
            _ = await h.store.commitFeedback(approval)
        }
        await holds.flushAll()
        await holds.flushAll()
        XCTAssertEqual(h.hub.requests.count, 1)
        XCTAssertEqual(h.hub.requests.first?.action, .notRelevant)
        let reopened = try makeHarness(directory: h.directory)
        XCTAssertEqual(reopened.store.choices[site.key]?.delivery, .sent)
        XCTAssertEqual(reopened.store.choices[site.key]?.action, .notRelevant)
    }

    func testNotRelevantDoesNotRequestMemoryCorrection() async throws {
        let h = try makeHarness()
        let memory = card(sourceKind: "memory", sourceID: "mem-1", section: "learned")
        let accepted = await h.store.send(.notRelevant, for: memory)
        XCTAssertTrue(accepted)
        XCTAssertEqual(h.guardian.count, 0)
        XCTAssertEqual(h.hub.requests.first?.action, .notRelevant)
        XCTAssertNil(h.hub.requests.first?.reason)
    }
    private struct Harness {
        let hub: StubFeedHub
        let guardian: FeedGuard
        let journal: PendingWriteJournal
        let directory: URL
        let store: FeedStore
    }

    private func makeHarness(hub: StubFeedHub = StubFeedHub(), directory: URL? = nil,
                             keys: FeedKeys = FeedKeys(), hubID: String? = nil) throws -> Harness {
        let folder = try directory ?? makeTemporaryDirectory()
        let guardian = FeedGuard()
        let journal = PendingWriteJournal(fileURL: folder.appendingPathComponent("writes.json"), hubID: { hubID })
        let store = FeedStore(hub: hub, journal: journal, guardian: guardian, isPaired: { true },
                              deviceID: { "phone" },
                              fileURL: folder.appendingPathComponent("feed.json"),
                              now: { Date(timeIntervalSince1970: 1_800_000_000) }, makeKey: { keys.next() })
        return Harness(hub: hub, guardian: guardian, journal: journal, directory: folder, store: store)
    }

    func testOfflineFeedbackCanBeUndoneAndReplayedOnce() async throws {
        let h = try makeHarness(hubID: "mac")
        let site = card()
        let firstApproval = await h.store.authorizeFeedback(.like, for: site)
        let approval = try XCTUnwrap(firstApproval)
        let queued = await h.store.queueToday(approval, bodyVersion: "v1")
        XCTAssertTrue(queued)
        XCTAssertTrue(h.hub.requests.isEmpty)
        let first = try XCTUnwrap(h.store.pendingToday.first)
        await h.store.undoToday(first.key)
        XCTAssertTrue(h.store.pendingToday.isEmpty)
        let nextApproval = await h.store.authorizeFeedback(.like, for: site)
        let secondApproval = try XCTUnwrap(nextApproval)
        let queuedAgain = await h.store.queueToday(secondApproval, bodyVersion: "v1")
        XCTAssertTrue(queuedAgain)
        h.hub.failNext([HubRequestError.noAnswer])
        await h.store.retryToday()
        await h.store.retryToday()
        XCTAssertEqual(h.hub.requests.count, 2)
        XCTAssertEqual(h.hub.requests[0].key, h.hub.requests[1].key)
        XCTAssertTrue(h.store.pendingToday.isEmpty)
        XCTAssertEqual(h.store.choices[site.key]?.delivery, .sent)
    }

    private func card(sourceKind: String = "web", sourceID: String = "site:github.com",
                      section: String = "watching") -> FeedCardModel {
        var brief = MobileBriefV1(version: 1, briefId: "2026-10-16-morning", slot: "morning", localDate: "2026-10-16")
        brief.evidence = [BriefEvidence(ref: "e1", sourceKind: sourceKind, sourceId: sourceID)]
        let item = BriefItem(id: "\(section)-1", key: FeedAssembly.itemKey(section: section, sourceKind: sourceKind,
                                                                            sourceID: sourceID),
                             title: "github.com", refs: ["e1"])
        if section == "learned" {
            brief.sections.learned = [item]
        } else {
            brief.sections.watching = [item]
        }
        return FeedAssembly.cards(in: brief)[0]
    }

    func testLikeIsSentOnceAndRemembered() async throws {
        let h = try makeHarness()
        let site = card()
        let sent = await h.store.send(.like, for: site)
        XCTAssertTrue(sent)
        XCTAssertEqual(h.hub.requests, [.init(briefID: "2026-10-16-morning", itemID: site.key, action: .like,
                                              reason: nil, key: "feed-key-1")])
        XCTAssertEqual(h.store.choices[site.key]?.delivery, .sent)
        let again = await h.store.send(.like, for: site)
        XCTAssertFalse(again, "a card takes one answer")
        XCTAssertEqual(h.hub.requests.count, 1)
        let journal = await h.journal.entries()
        XCTAssertTrue(journal.isEmpty)
        XCTAssertEqual(h.guardian.count, 0, "Like needs no owner check")

        let reopened = try makeHarness(directory: h.directory)
        XCTAssertEqual(reopened.store.choices[site.key]?.action, .like)
    }

    func testAnUnansweredLikeStaysAndIsSentAgainUnderTheSameKey() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])
        let site = card()
        let sent = await h.store.send(.like, for: site)
        XCTAssertFalse(sent)
        XCTAssertEqual(h.store.choices[site.key]?.delivery, .pending)
        XCTAssertEqual(h.store.notice, FeedCopy.waiting)

        // After a relaunch the pending Like is sent again with the key the journal kept.
        let reopened = try makeHarness(hub: h.hub, directory: h.directory)
        await reopened.store.retryPending()
        XCTAssertEqual(h.hub.requests.map(\.key), ["feed-key-1", "feed-key-1"])
        XCTAssertEqual(reopened.store.choices[site.key]?.delivery, .sent)
        let journal = await reopened.journal.entries()
        XCTAssertTrue(journal.isEmpty)
    }

    func testAPendingChoiceWithoutAJournalKeyIsForgotten() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.noAnswer])
        let site = card()
        _ = await h.store.send(.like, for: site)
        let keys = await h.journal.entries().map(\.key)
        await h.journal.remove(keys: keys)
        await h.store.retryPending()
        XCTAssertNil(h.store.choices[site.key])
        XCTAssertEqual(h.hub.requests.count, 1, "nothing is sent under a new key")
        XCTAssertEqual(h.store.notice, FeedCopy.expired(1), "the answer that never arrived is not dropped silently")
    }

    func testAReasonIsCappedToTheMacsByteLimit() async throws {
        let h = try makeHarness()
        let memory = card(sourceKind: "memory", sourceID: "mem-1", section: "learned")
        // 400 Chinese characters are 1200 UTF-8 bytes; the Mac refuses more than 1000.
        let sent = await h.store.send(.notRight, for: memory, reason: String(repeating: "错", count: 400))
        XCTAssertTrue(sent)
        let reason = try XCTUnwrap(h.hub.requests.first?.reason)
        XCTAssertLessThanOrEqual(reason.utf8.count, CorrectionReason.maxBytes)
        XCTAssertEqual(reason, String(repeating: "错", count: 333))
    }

    func testACardTheMacNoLongerHasIsForgotten() async throws {
        let h = try makeHarness()
        h.hub.failNext([HubRequestError.rejected(status: 404, message: "this item is no longer in the brief")])
        let site = card()
        let sent = await h.store.send(.notRight, for: site)
        XCTAssertFalse(sent)
        XCTAssertNil(h.store.choices[site.key])
        XCTAssertEqual(h.store.notice, FeedCopy.gone)
    }

    func testNotRightOnAMemoryNeedsAReasonAndTheOwner() async throws {
        let h = try makeHarness()
        h.hub.answer(effect: "memory_removed")
        let memory = card(sourceKind: "memory", sourceID: "mem-1", section: "learned")
        XCTAssertEqual(memory.memoryID, "mem-1")

        let blank = await h.store.send(.notRight, for: memory, reason: "  ")
        XCTAssertFalse(blank)
        XCTAssertEqual(h.store.notice, FeedCopy.reasonNeeded)
        XCTAssertTrue(h.hub.requests.isEmpty)

        h.guardian.deny()
        let denied = await h.store.send(.notRight, for: memory, reason: "Not me")
        XCTAssertFalse(denied)
        XCTAssertTrue(h.hub.requests.isEmpty)
        XCTAssertNil(h.store.choices[memory.key], "nothing shows before the Mac confirms")

        let confirmed = try makeHarness(hub: h.hub)
        let removed = await confirmed.store.send(.notRight, for: memory, reason: "Not me")
        XCTAssertTrue(removed)
        XCTAssertEqual(h.hub.requests.last?.reason, "Not me")
        XCTAssertEqual(confirmed.store.notice, FeedCopy.memoryRemoved)
        XCTAssertEqual(confirmed.store.choices[memory.key]?.action, .notRight)
    }

    func testTheInterestSentenceStaysOnThisIPhoneUntilUnpair() throws {
        let h = try makeHarness()
        h.store.setInterest("  AI agents for small teams  ")
        XCTAssertEqual(h.store.interest, "AI agents for small teams")
        XCTAssertEqual(try makeHarness(directory: h.directory).store.interest, "AI agents for small teams")
        h.store.setInterest(String(repeating: "a", count: 400))
        XCTAssertEqual(h.store.interest?.count, FeedStore.interestMaxLength)
        h.store.clearAfterUnpair()
        XCTAssertNil(h.store.interest)
        XCTAssertNil(try makeHarness(directory: h.directory).store.interest)
    }

    func testFeedbackWritesAreKeptByJournalPruning() {
        let write = PendingWrite(key: "k", operation: .feedback, target: "2026-10-16-morning/watching.abc",
                                 choice: "like", fingerprint: "f", createdAt: Date())
        XCTAssertFalse(ActionStore.isRetired(write))
    }
}

/// Suspends a single stub response so tests can change pairing or permissions while it is in flight.
private actor IdeasReplyGate {
    private var entered = false
    private var waitingForEntry: [CheckedContinuation<Void, Never>] = []
    private var response: CheckedContinuation<Void, Never>?

    func wait() async {
        entered = true
        waitingForEntry.forEach { $0.resume() }
        waitingForEntry = []
        await withCheckedContinuation { response = $0 }
    }

    func waitUntilEntered() async {
        guard !entered else { return }
        await withCheckedContinuation { waitingForEntry.append($0) }
    }

    func release() {
        response?.resume()
        response = nil
    }
}

private final class StubIdeasHub: IdeasHubAPI, @unchecked Sendable {
    struct Decision: Equatable {
        let id: String
        let request: IdeaDeclineRequest
    }

    private let lock = NSLock()
    private var pages: [IdeasPage] = []
    private var readFailures: [Error] = []
    private var decisionFailures: [Error] = []
    private var sent: [Decision] = []
    private var reads = 0
    private var finds = 0
    private var readGate: IdeasReplyGate?
    private var decisionGate: IdeasReplyGate?
    private var confirms = true

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }

    var decisions: [Decision] { locked { sent } }
    var readCount: Int { locked { reads } }
    var findCount: Int { locked { finds } }
    func findMoreOffers() async throws -> IdeasPage {
        locked { finds += 1 }
        return try await offers()
    }
    func serve(_ offers: [IdeaOffer]) { locked { pages.append(IdeasPage(offers: offers)) } }
    func failReads(_ errors: [Error]) { locked { readFailures = errors } }
    func failDecisions(_ errors: [Error]) { locked { decisionFailures = errors } }
    func holdRead(_ gate: IdeasReplyGate) { locked { readGate = gate } }
    func holdDecision(_ gate: IdeasReplyGate) { locked { decisionGate = gate } }
    func confirmDecline(_ value: Bool) { locked { confirms = value } }

    func offers() async throws -> IdeasPage {
        let outcome: (IdeasPage, Error?, IdeasReplyGate?) = locked {
            reads += 1
            let page = pages.isEmpty ? IdeasPage(offers: []) : pages.removeFirst()
            let failure = readFailures.isEmpty ? nil : readFailures.removeFirst()
            let gate = readGate
            readGate = nil
            return (page, failure, gate)
        }
        await outcome.2?.wait()
        if let error = outcome.1 { throw error }
        return outcome.0
    }

    func declineOffer(id: String, _ request: IdeaDeclineRequest) async throws -> IdeaDeclinePayload {
        let outcome: (Bool, Error?, IdeasReplyGate?) = locked {
            sent.append(Decision(id: id, request: request))
            let failure = decisionFailures.isEmpty ? nil : decisionFailures.removeFirst()
            let gate = decisionGate
            decisionGate = nil
            return (confirms, failure, gate)
        }
        await outcome.2?.wait()
        if let error = outcome.1 { throw error }
        return IdeaDeclinePayload(declined: outcome.0)
    }
}

private final class IdeasIdentity: @unchecked Sendable {
    private let lock = NSLock()
    private var stored = "computer-a"
    var value: String {
        get {
            lock.lock()
            defer { lock.unlock() }
            return stored
        }
        set {
            lock.lock()
            stored = newValue
            lock.unlock()
        }
    }
}

private final class IdeasCacheReader: @unchecked Sendable {
    private let lock = NSLock()
    private var available = false

    func unlock() {
        lock.lock()
        available = true
        lock.unlock()
    }

    func read(_ url: URL) throws -> Data {
        lock.lock()
        let canRead = available
        lock.unlock()
        guard canRead else { throw CocoaError(.fileReadNoPermission) }
        return try Data(contentsOf: url)
    }
}

@MainActor
private final class IdeasAccess {
    var paired = true
    var reads = true
    var decisions = true
}

@MainActor
final class IdeasStoreTests: XCTestCase {
    func testFindMoreUsesFreshRouteAndHandlesEmptyAndFailure() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        let fresh = await h.store.findMore()
        XCTAssertEqual(fresh?.offers.count, 1)
        XCTAssertEqual(h.hub.findCount, 1)
        XCTAssertEqual(h.store.ideas.count, 1)
        h.hub.serve([])
        let empty = await h.store.findMore()
        XCTAssertEqual(empty?.offers, [])
        XCTAssertTrue(h.store.ideas.isEmpty)
        h.hub.failReads([HubRequestError.noAnswer])
        let failed = await h.store.findMore()
        XCTAssertNil(failed)
        XCTAssertEqual(h.store.loadState, .failed)
        XCTAssertEqual(h.hub.findCount, 3)
    }
    func testOfferReferencesRequireTheServerVersionAndKeepLocalizedCopy() throws {
        let legacy = try JSONDecoder().decode(IdeaOffer.self, from: Data("""
        {"id":"suggestion.notes","kind":"suggestion","title":"Notes","why":"Observed pattern","prompt":"Tidy my notes"}
        """.utf8))
        XCTAssertNil(legacy.chatReference, "An old cached card must not turn source text into an ordinary chat draft")
        var offer = legacy
        offer.version = String(repeating: "a", count: 64)
        offer.whyLocalized = ["en": "Observed pattern", "zh-CN": "根据观察到的习惯"]
        XCTAssertEqual(offer.chatReference, ChatOfferReference(id: "suggestion.notes", version: String(repeating: "a", count: 64)))
        XCTAssertEqual(offer.explanation(locale: Locale(identifier: "zh-Hans-CN")), "根据观察到的习惯")
        XCTAssertEqual(offer.explanation(locale: Locale(identifier: "en-US")), "Observed pattern")
        XCTAssertEqual(offer.explanation(locale: Locale(identifier: "fr-FR")), "Observed pattern")
        let roundTrip = try JSONDecoder().decode(IdeaOffer.self, from: JSONEncoder().encode(offer))
        XCTAssertEqual(roundTrip, offer)
        for version in ["", "old", String(repeating: "G", count: 64), String(repeating: "a", count: 65)] {
            offer.version = version
            XCTAssertNil(offer.chatReference)
        }
    }

    func testIdeasCopyUsesTheNativeEnglishAndChineseLanguageChoice() {
        XCTAssertEqual(IdeasCopy.text(en: "Ideas", zhCN: "想法", locale: Locale(identifier: "en-GB")), "Ideas")
        XCTAssertEqual(IdeasCopy.text(en: "Ideas", zhCN: "想法", locale: Locale(identifier: "zh-Hans-CN")), "想法")
        XCTAssertEqual(IdeasCopy.text(en: "Ideas", zhCN: "想法", locale: Locale(identifier: "fr-FR")), "Ideas")
        XCTAssertEqual(IdeasCopy.handoffFailed(locale: Locale(identifier: "en-US")),
                       "Isoform could not prepare this draft on this phone. Refresh Ideas and try again.")
        XCTAssertEqual(IdeasCopy.handoffFailed(locale: Locale(identifier: "zh-Hans-CN")),
                       "Isoform 无法在手机上准备这份草稿。请刷新想法后重试。")
    }

    private struct Harness {
        let hub: StubIdeasHub
        let journal: PendingWriteJournal
        let identity: IdeasIdentity
        let access: IdeasAccess
        let directory: URL
        let store: IdeasStore
        var cacheURL: URL { directory.appendingPathComponent("ideas.json") }
    }

    private func makeHarness(hub: StubIdeasHub = StubIdeasHub(), directory: URL? = nil,
                             identity: IdeasIdentity = IdeasIdentity(), journal: PendingWriteJournal? = nil,
                             keys: FeedKeys = FeedKeys(),
                             readData: @escaping @Sendable (URL) throws -> Data = { try Data(contentsOf: $0) }) throws -> Harness {
        let folder = try directory ?? makeTemporaryDirectory()
        let access = IdeasAccess()
        let journal = journal ?? PendingWriteJournal(fileURL: folder.appendingPathComponent("writes.json"),
                                                     hubID: { identity.value })
        let store = IdeasStore(hub: hub, journal: journal, isPaired: { access.paired },
                               fileURL: folder.appendingPathComponent("ideas.json"), canRead: { access.reads },
                               canDecide: { access.decisions }, hubID: { identity.value },
                               readData: readData,
                               now: { Date(timeIntervalSince1970: 1_800_000_000) }, makeKey: { keys.next() })
        return Harness(hub: hub, journal: journal, identity: identity, access: access,
                       directory: folder, store: store)
    }

    private func idea(_ id: String = "suggestion-a", title: String = "Prepare a reading list",
                      prompt: String = "Help me prepare a reading list.") -> IdeaOffer {
        IdeaOffer(id: id, kind: "suggestion", title: title,
                  why: "You have been researching this topic.", prompt: prompt)
    }

    func testRefreshDeduplicatesAndKeepsTheLastListOffline() async throws {
        let h = try makeHarness()
        let first = idea()
        h.hub.serve([first, first, idea("blank-title", title: ""), idea("blank-prompt", prompt: "")])
        await h.store.refresh()
        XCTAssertEqual(h.store.ideas, [first])
        XCTAssertEqual(h.store.readAt, Date(timeIntervalSince1970: 1_800_000_000))
        XCTAssertEqual(h.store.loadState, .loaded)
        let reopened = try makeHarness(directory: h.directory)
        XCTAssertEqual(reopened.store.ideas, [first])
        reopened.hub.failReads([HubRequestError.noAnswer])
        await reopened.store.refresh()
        XCTAssertEqual(reopened.store.ideas, [first])
        XCTAssertEqual(reopened.store.loadState, .failed)
    }

    func testDeclineSendsTheExactDecisionOnceWithoutOwnerAuthentication() async throws {
        let h = try makeHarness()
        let offered = idea()
        h.hub.serve([offered])
        await h.store.refresh()
        let result = await h.store.decline(offered)
        XCTAssertTrue(result)
        XCTAssertEqual(h.hub.decisions, [.init(id: offered.id,
                                             request: IdeaDeclineRequest(idempotencyKey: "feed-key-1", decision: "dismiss"))])
        XCTAssertTrue(h.store.ideas.isEmpty)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertTrue(h.store.busy.isEmpty)
        let writes = await h.journal.entries()
        XCTAssertTrue(writes.isEmpty)
        let repeated = await h.store.decline(offered)
        XCTAssertFalse(repeated)
        XCTAssertEqual(h.hub.decisions.count, 1)
        XCTAssertTrue(try makeHarness(directory: h.directory).store.ideas.isEmpty)
    }

    func testDismissalDoesNotAskForOwnerAuthentication() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let result = await h.store.decline(idea())
        XCTAssertTrue(result)
        XCTAssertTrue(h.store.ideas.isEmpty)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertTrue(h.store.busy.isEmpty)
        XCTAssertEqual(h.hub.decisions.count, 1)
        let writes = await h.journal.entries()
        XCTAssertTrue(writes.isEmpty)
    }

    func testMissingScopesAndUnpairedStateCannotSendAChoice() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        h.access.decisions = false
        let withoutDecision = await h.store.decline(idea())
        h.access.decisions = true
        h.access.reads = false
        let withoutRead = await h.store.decline(idea())
        h.access.reads = true
        h.access.paired = false
        let unpaired = await h.store.decline(idea())
        XCTAssertFalse(withoutDecision)
        XCTAssertFalse(withoutRead)
        XCTAssertFalse(unpaired)
        XCTAssertTrue(h.hub.decisions.isEmpty)
    }

    func testDecisionScopeIsCheckedAgainAfterTheComputerReply() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let gate = IdeasReplyGate()
        h.hub.holdDecision(gate)
        let request = Task { await h.store.decline(idea()) }
        await gate.waitUntilEntered()
        h.access.decisions = false
        await gate.release()
        let result = await request.value
        XCTAssertFalse(result)
        XCTAssertEqual(h.hub.decisions.count, 1)
        XCTAssertEqual(h.store.pending, [idea().id])
        XCTAssertTrue(h.store.busy.isEmpty)
    }

    func testAnUnansweredDeclineRetriesItsOriginalKeyAfterRelaunch() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        h.hub.failDecisions([HubRequestError.noAnswer])
        await h.store.refresh()
        let result = await h.store.decline(idea())
        XCTAssertFalse(result)
        XCTAssertEqual(h.store.pending, [idea().id])
        XCTAssertEqual(h.store.ideas, [idea()])
        XCTAssertEqual(h.store.notice, IdeasCopy.waiting)
        let writes = await h.journal.entries()
        XCTAssertEqual(writes.first?.operation, .offerDecision)
        XCTAssertEqual(writes.first?.hubID, "computer-a")
        XCTAssertFalse(ActionStore.isRetired(try XCTUnwrap(writes.first)))
        let reopened = try makeHarness(hub: h.hub, directory: h.directory)
        h.hub.serve([idea()])
        await reopened.store.refresh()
        XCTAssertEqual(h.hub.decisions.map(\.request.idempotencyKey), ["feed-key-1", "feed-key-1"])
        XCTAssertTrue(reopened.store.ideas.isEmpty)
        XCTAssertTrue(reopened.store.pending.isEmpty)
    }

    func testAPendingDecisionWithoutItsKeyIsNeverRetriedUnderANewKey() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        h.hub.failDecisions([HubRequestError.noAnswer])
        await h.store.refresh()
        _ = await h.store.decline(idea())
        let keys = await h.journal.entries().map(\.key)
        await h.journal.remove(keys: keys)
        await h.store.retryPending()
        XCTAssertEqual(h.hub.decisions.count, 1)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertTrue(h.store.busy.isEmpty)
        XCTAssertEqual(h.store.ideas, [idea()])
        XCTAssertEqual(h.store.notice, IdeasCopy.expired)
    }

    func testUnreadableCacheCannotBeOverwrittenToSendADecision() async throws {
        let folder = try makeTemporaryDirectory()
        let file = folder.appendingPathComponent("ideas.json")
        let original = Data("damaged cache".utf8)
        try original.write(to: file)
        let h = try makeHarness(directory: folder)
        h.hub.serve([idea()])
        await h.store.refresh()
        let result = await h.store.decline(idea())
        XCTAssertFalse(result)
        XCTAssertTrue(h.hub.decisions.isEmpty)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertEqual(h.store.notice, IdeasCopy.saveFailed)
        XCTAssertEqual(try Data(contentsOf: file), original)
    }

    func testUnlockRecoversSavedDecisionsBeforeRefreshCanOverwriteTheCache() async throws {
        let keys = FeedKeys()
        let h = try makeHarness(keys: keys)
        h.hub.serve([idea(), idea("another-idea")])
        h.hub.failDecisions([HubRequestError.noAnswer])
        await h.store.refresh()
        _ = await h.store.decline(idea())
        let original = try Data(contentsOf: h.cacheURL)
        let reader = IdeasCacheReader()
        let reopened = try makeHarness(hub: h.hub, directory: h.directory, keys: keys,
                                       readData: { try reader.read($0) })
        XCTAssertTrue(reopened.store.ideas.isEmpty)
        XCTAssertEqual(reopened.store.notice, IdeasCopy.saveFailed)
        h.hub.serve([idea(), idea("another-idea")])
        await reopened.store.refresh()
        XCTAssertEqual(try Data(contentsOf: h.cacheURL), original,
                       "A remote list must not replace unreadable saved owner decisions")
        XCTAssertEqual(h.hub.decisions.count, 1)

        reader.unlock()
        h.hub.serve([idea(), idea("another-idea")])
        await reopened.store.refresh()
        XCTAssertEqual(h.hub.decisions.map(\.request.idempotencyKey), ["feed-key-1", "feed-key-1"])
        XCTAssertEqual(reopened.store.ideas, [idea("another-idea")])
        XCTAssertTrue(reopened.store.pending.isEmpty)
        XCTAssertNil(reopened.store.notice)
        XCTAssertEqual(try makeHarness(directory: h.directory).store.ideas, [idea("another-idea")])
        let result = await reopened.store.decline(idea("another-idea"))
        XCTAssertTrue(result, "Dismissals recover within the same process after unlocking")
        XCTAssertEqual(h.hub.decisions.last?.request.idempotencyKey, "feed-key-2")
    }

    func testDeclineCanRecoverTheCacheAfterUnlockWithoutAnotherRefresh() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let reader = IdeasCacheReader()
        let reopened = try makeHarness(hub: h.hub, directory: h.directory, readData: { try reader.read($0) })
        h.hub.serve([idea()])
        await reopened.store.refresh()
        reader.unlock()
        let result = await reopened.store.decline(idea())
        XCTAssertTrue(result)
        XCTAssertTrue(reopened.store.ideas.isEmpty)
        XCTAssertNil(reopened.store.notice)
        XCTAssertTrue(try makeHarness(directory: h.directory).store.ideas.isEmpty)
    }

    func testJournalSaveFailurePreventsSending() async throws {
        let folder = try makeTemporaryDirectory()
        let journal = PendingWriteJournal(fileURL: folder.appendingPathComponent("writes.json"),
                                          hubID: { "computer-a" }, writeData: { _, _ in
            throw CocoaError(.fileWriteNoPermission)
        })
        let h = try makeHarness(directory: folder, journal: journal)
        h.hub.serve([idea()])
        await h.store.refresh()
        let result = await h.store.decline(idea())
        XCTAssertFalse(result)
        XCTAssertTrue(h.hub.decisions.isEmpty)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertEqual(h.store.ideas, [idea()])
        XCTAssertEqual(h.store.notice, IdeasCopy.saveFailed)
    }

    func testAResponseWithoutConfirmationKeepsTheOriginalDecisionForRetry() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        h.hub.confirmDecline(false)
        await h.store.refresh()
        let result = await h.store.decline(idea())
        XCTAssertFalse(result)
        XCTAssertEqual(h.store.ideas, [idea()])
        XCTAssertEqual(h.store.pending, [idea().id])
        h.hub.confirmDecline(true)
        await h.store.retryPending()
        XCTAssertEqual(h.hub.decisions.map(\.request.idempotencyKey), ["feed-key-1", "feed-key-1"])
        XCTAssertTrue(h.store.ideas.isEmpty)
    }

    func testAnIdeaThatNoLongerExistsIsRemovedAndItsKeyForgotten() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        h.hub.failDecisions([HubRequestError.rejected(status: 410, message: "Gone")])
        await h.store.refresh()
        _ = await h.store.decline(idea())
        XCTAssertTrue(h.store.ideas.isEmpty)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertEqual(h.store.notice, IdeasCopy.gone)
        let writes = await h.journal.entries()
        XCTAssertTrue(writes.isEmpty)
    }

    func testReadRefusalClearsPrivateCacheAndRequiresRepair() async throws {
        for error: Error in [HubRequestError.forbidden, HubRequestError.rejected(status: 401, message: "Expired"),
                             RelayClientError.unauthorized] {
            let h = try makeHarness()
            h.hub.serve([idea()])
            await h.store.refresh()
            h.hub.failReads([error])
            await h.store.refresh()
            XCTAssertTrue(h.store.ideas.isEmpty)
            XCTAssertNil(h.store.readAt)
            XCTAssertTrue(h.store.repairNeeded)
            XCTAssertEqual(h.store.loadState, .failed)
            XCTAssertFalse(FileManager.default.fileExists(atPath: h.cacheURL.path))
        }
    }

    func testAForbiddenDecisionClearsPrivateCacheAndRequiresRepair() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        h.hub.failDecisions([HubRequestError.forbidden])
        await h.store.refresh()
        _ = await h.store.decline(idea())
        XCTAssertTrue(h.store.ideas.isEmpty)
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertTrue(h.store.repairNeeded)
        XCTAssertFalse(FileManager.default.fileExists(atPath: h.cacheURL.path))
    }

    func testPairingWithAnotherComputerCannotReadThePreviousCache() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let other = IdeasIdentity()
        other.value = "computer-b"
        let reopened = try makeHarness(directory: h.directory, identity: other)
        XCTAssertTrue(reopened.store.ideas.isEmpty)
        XCTAssertNil(reopened.store.readAt)
        XCTAssertFalse(FileManager.default.fileExists(atPath: h.cacheURL.path))
    }

    func testUnpairIgnoresAHeldReadAndDeletesItsCache() async throws {
        let h = try makeHarness()
        let gate = IdeasReplyGate()
        h.hub.serve([idea()])
        h.hub.holdRead(gate)
        let request = Task { await h.store.refresh() }
        await gate.waitUntilEntered()
        h.access.paired = false
        XCTAssertTrue(h.store.clearAfterUnpair())
        await gate.release()
        await request.value
        XCTAssertTrue(h.store.ideas.isEmpty)
        XCTAssertNil(h.store.readAt)
        XCTAssertEqual(h.store.loadState, .idle)
        XCTAssertFalse(FileManager.default.fileExists(atPath: h.cacheURL.path))
    }

    func testSameComputerRepairInvalidatesAHeldReadButKeepsTheCurrentCache() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let gate = IdeasReplyGate()
        h.hub.serve([idea("stale-reply")])
        h.hub.holdRead(gate)
        let request = Task { await h.store.refresh() }
        await gate.waitUntilEntered()
        h.store.pairingCommitted()
        await gate.release()
        await request.value
        XCTAssertEqual(h.store.ideas, [idea()])
        XCTAssertEqual(h.store.loadState, .idle)
        XCTAssertEqual(try makeHarness(directory: h.directory).store.ideas, [idea()])
    }

    func testRepairWithoutReadScopeClearsTheCache() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        h.access.reads = false
        h.store.pairingCommitted()
        XCTAssertTrue(h.store.ideas.isEmpty)
        XCTAssertNil(h.store.readAt)
        XCTAssertFalse(FileManager.default.fileExists(atPath: h.cacheURL.path))
    }

    func testRepairInvalidatesAPendingDecisionReply() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let gate = IdeasReplyGate()
        h.hub.holdDecision(gate)
        let request = Task { await h.store.decline(idea()) }
        await gate.waitUntilEntered()
        h.store.pairingCommitted()
        await gate.release()
        let result = await request.value
        XCTAssertFalse(result)
        XCTAssertEqual(h.hub.decisions.count, 1)
        XCTAssertEqual(h.store.pending, [idea().id], "The saved decision stays available for a receipt retry")
        XCTAssertTrue(h.store.busy.isEmpty)
    }

    func testARefreshCannotRaceWithAnInFlightDecline() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let gate = IdeasReplyGate()
        h.hub.holdDecision(gate)
        let decline = Task { await h.store.decline(idea()) }
        await gate.waitUntilEntered()
        h.hub.serve([idea()])
        await h.store.refresh()
        XCTAssertEqual(h.hub.readCount, 1, "A stale GET must not resurrect a confirmed dismissal")
        let duplicate = await h.store.decline(idea())
        XCTAssertFalse(duplicate)
        await gate.release()
        let result = await decline.value
        XCTAssertTrue(result)
        XCTAssertTrue(h.store.ideas.isEmpty)
    }

    func testADeclineCannotStartDuringAHeldRefresh() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let gate = IdeasReplyGate()
        h.hub.serve([idea()])
        h.hub.holdRead(gate)
        let refresh = Task { await h.store.refresh() }
        await gate.waitUntilEntered()
        let result = await h.store.decline(idea())
        await h.store.refresh()
        XCTAssertFalse(result)
        XCTAssertTrue(h.hub.decisions.isEmpty)
        XCTAssertEqual(h.hub.readCount, 2, "Concurrent refreshes coalesce")
        await gate.release()
        await refresh.value
        XCTAssertEqual(h.store.ideas, [idea()])
    }

    func testAHeldDecisionCannotMutateAReplacementPairing() async throws {
        let h = try makeHarness()
        h.hub.serve([idea()])
        await h.store.refresh()
        let gate = IdeasReplyGate()
        h.hub.holdDecision(gate)
        let request = Task { await h.store.decline(idea()) }
        await gate.waitUntilEntered()
        h.identity.value = "computer-b"
        h.store.pairingCommitted()
        h.hub.serve([idea("new-computer")])
        await h.store.refresh()
        await gate.release()
        let result = await request.value
        XCTAssertFalse(result)
        XCTAssertEqual(h.store.ideas, [idea("new-computer")])
        XCTAssertTrue(h.store.pending.isEmpty)
        XCTAssertEqual(try makeHarness(directory: h.directory, identity: h.identity).store.ideas,
                       [idea("new-computer")])
    }

    func testOfferPayloadAndDecisionEncodingMatchTheProtocol() throws {
        let response = HubResponse(status: 200, body: #"{"ok":true,"data":{"offers":[{"id":"suggestion-a","kind":"suggestion","title":"Prepare a reading list","why":"You have been researching this topic.","prompt":"Help me prepare a reading list."}]}}"#)
        XCTAssertEqual(try response.payload(IdeasPage.self).offers, [idea()])
        let data = try JSONEncoder().encode(IdeaDeclineRequest(idempotencyKey: "same-key", decision: "dismiss"))
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: String])
        XCTAssertEqual(body, ["idempotencyKey": "same-key", "decision": "dismiss"])
        let declined = HubResponse(status: 200, body: #"{"ok":true,"data":{"declined":true}}"#)
        XCTAssertTrue(try declined.payload(IdeaDeclinePayload.self).declined)
    }
}


#if DEBUG && targetEnvironment(simulator)
@MainActor
final class SeededIdeasChatTests: XCTestCase {
    func testSeededIdeaSendAcceptsSourceReferencesAndLeavesTheComposerEmpty() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let cache = ChatCache(directory: directory.appendingPathComponent("chats", isDirectory: true))
        try ChatUITestSeed.seed(cache, answered: true, now: now)
        let hub = ChatUITestHub(now: now)
        let store = ChatStore(hub: hub, journal: PendingWriteJournal(fileURL: directory.appendingPathComponent("writes.json")),
                              guardian: FeedGuard(), presence: { true }, isPaired: { true }, cache: cache,
                              makeKey: { "seeded-idea-send" })
        let chatID = ChatUITestSeed.mainChatID
        await store.open(chatID: chatID)
        let idea = try XCTUnwrap(IdeasUITestSeed.offers.first)
        let reference = try XCTUnwrap(idea.chatReference)
        XCTAssertTrue(IdeaHandoff.open(idea, chat: store, router: NotificationRouter()))
        _ = store.takeReturnedDrafts(in: chatID, appendingTo: "")
        let sent = await store.sendComposer(in: chatID)
        XCTAssertTrue(sent)
        XCTAssertTrue(store.composerDraft(in: chatID).text.isEmpty)
        XCTAssertTrue(store.composerDraft(in: chatID).offerReferences.isEmpty)
        XCTAssertTrue(store.returnedDrafts.isEmpty, "The seeded Send path must not restore a default-409 refusal")
        let outgoing = try XCTUnwrap(store.outgoingMessages(in: chatID).last)
        XCTAssertNotNil(outgoing.turnID)
        XCTAssertEqual(outgoing.offerReferences, [reference])

        let repeated = try await hub.sendMessage(chatID: chatID, text: idea.prompt, offerReferences: [reference],
                                                  idempotencyKey: "seeded-idea-send")
        XCTAssertEqual(repeated.turnId, outgoing.turnID)
        XCTAssertEqual(repeated.seq, outgoing.seq)
        for invalid in [ChatOfferReference(id: reference.id, version: String(repeating: "f", count: 64)),
                        ChatOfferReference(id: "suggestion.missing", version: reference.version)] {
            do {
                _ = try await hub.sendMessage(chatID: chatID, text: idea.prompt, offerReferences: [invalid],
                                               idempotencyKey: "invalid-idea")
                XCTFail("The seeded computer must reject stale or unknown idea references")
            } catch {
                guard let requestError = error as? HubRequestError, case .conflict = requestError else {
                    return XCTFail("Unexpected error: \(error)")
                }
            }
        }
        let detail = try await hub.chat(id: chatID, after: nil, limit: 50)
        XCTAssertEqual(detail.items.filter { $0.text == idea.prompt }.count, 1, "Retries must not append another user row")
    }
}
#endif
