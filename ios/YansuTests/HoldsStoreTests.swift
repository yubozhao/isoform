import XCTest
@testable import Yansu

private actor HoldsTestHub: HoldsHubAPI {
    private(set) var actions: [PhoneHoldAction] = []
    func holdProposal(kind: String, id: String) async throws -> PhoneHoldOffer { throw HubRequestError.invalidIdentifier }
    func holdStatus(id: String) async throws -> PhoneHoldStatus { PhoneHoldStatus(id: id, state: "pending_write") }
    func holdAction(_ action: PhoneHoldAction) async throws -> PhoneHoldStatus {
        actions.append(action)
        return PhoneHoldStatus(id: action.holdId ?? "hold-1", state: action.action == "hold" ? "pending_write" : "removed")
    }
    func recorded() -> [PhoneHoldAction] { actions }
}

/// The computer is away (no answer) until `answer` is set: then it takes the hold, or refuses it.
private actor AwayThenBackHub: HoldsHubAPI {
    enum Answer { case away, take, refuse }
    var answer: Answer = .away
    private(set) var keys: [String] = []
    func set(_ next: Answer) { answer = next }
    func holdProposal(kind: String, id: String) async throws -> PhoneHoldOffer { throw HubRequestError.invalidIdentifier }
    func holdStatus(id: String) async throws -> PhoneHoldStatus { PhoneHoldStatus(id: id, state: "written") }
    func holdAction(_ action: PhoneHoldAction) async throws -> PhoneHoldStatus {
        keys.append(action.idempotencyKey)
        switch answer {
        case .away: throw HubRequestError.noAnswer
        case .take: return PhoneHoldStatus(id: "hold-1", state: "written")
        case .refuse: throw HubRequestError.conflict(message: "HOLD_EDITED")
        }
    }
}

/// Every hold action fails with `error`, as when the computer is away.
private actor AwayHoldsHub: HoldsHubAPI {
    let error: Error
    init(error: Error) { self.error = error }
    func holdProposal(kind: String, id: String) async throws -> PhoneHoldOffer { throw HubRequestError.invalidIdentifier }
    func holdStatus(id: String) async throws -> PhoneHoldStatus { throw error }
    func holdAction(_ action: PhoneHoldAction) async throws -> PhoneHoldStatus { throw error }
}

@MainActor
final class HoldsStoreTests: XCTestCase {
    /// The relay took the hold but the asleep computer did not answer: it will be written when the computer wakes,
    /// so Undo is gone (hub decision: a Sent state, no cancel route). A hold the relay never got still undoes.
    func testAQueuedHoldTheRelayHoldsCannotBeUndone() async throws {
        let slot = PhoneHoldSlot(start: "2030-01-02T14:00:00Z", end: "2030-01-02T16:00:00Z")
        let proposal = PhoneHoldProposal(id: "hold-1", thing: PhoneHoldThing(title: "Q4 plan", sourceKind: "up_next", sourceId: "offer"), calendarId: "work", slots: [slot])
        let offer = PhoneHoldOffer(proposal: proposal, calendars: [PhoneHoldCalendar(id: "work", title: "Work")], token: "signed")
        for (error, held) in [(HubRequestError.noAnswer as Error, true), (RelayClientError.offline as Error, false)] {
            let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: root) }
            let url = root.appendingPathComponent("writes.json")
            let journal = PendingWriteJournal(fileURL: url, hubID: { "hub" })
            let store = HoldsStore(hub: AwayHoldsHub(error: error), journal: journal, pairingID: { "hub/phone" })
            let placed = await store.place(offer, slot: slot, calendarID: "work")
            let status = try XCTUnwrap(placed)
            XCTAssertEqual(status.state, HoldsStore.queuedState)
            XCTAssertEqual(status.canUndoHold, !held, "\(error)")
            let undone = await store.undo(id: "hold-1")
            XCTAssertEqual(undone, !held, "\(error)")
            let remaining = await journal.entries()
            XCTAssertEqual(remaining.isEmpty, !held, "\(error): a held hold keeps its key, so a retry reuses it")
            if held {
                XCTAssertEqual(remaining.first?.calendarHold?.relayHolds, true, "it stays held after a relaunch")
            }
        }
    }

    private func offer() -> (PhoneHoldOffer, PhoneHoldSlot) {
        let slot = PhoneHoldSlot(start: "2030-01-02T14:00:00Z", end: "2030-01-02T16:00:00Z")
        let proposal = PhoneHoldProposal(id: "hold-1", thing: PhoneHoldThing(title: "Q4 plan", sourceKind: "up_next", sourceId: "offer"), calendarId: "work", slots: [slot])
        return (PhoneHoldOffer(proposal: proposal, calendars: [PhoneHoldCalendar(id: "work", title: "Work")], token: "signed"), slot)
    }

    private func journalURL() throws -> URL {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: root) }
        return root.appendingPathComponent("writes.json")
    }

    /// A hold queued while the computer slept, then the app quit: the next launch shows it queued and sends it,
    /// with the same key, the first time the computer answers. It used to wait in the journal and never go.
    func testAHoldQueuedBeforeTheAppQuitGoesWhenTheComputerIsBack() async throws {
        let url = try journalURL()
        let hub = AwayThenBackHub()
        let (offer, slot) = offer()
        let first = HoldsStore(hub: hub, journal: PendingWriteJournal(fileURL: url, hubID: { "hub" }), pairingID: { "hub/phone" })
        let queued = await first.place(offer, slot: slot, calendarID: "work")
        XCTAssertEqual(queued?.state, HoldsStore.queuedState)

        let journal = PendingWriteJournal(fileURL: url, hubID: { "hub" })
        let relaunched = HoldsStore(hub: hub, journal: journal, pairingID: { "hub/phone" })
        await relaunched.restoreQueued()
        XCTAssertEqual(relaunched.states["hold-1"]?.state, HoldsStore.queuedState, "the row still says it is queued")
        await hub.set(.take)
        await relaunched.resendQueued()
        XCTAssertEqual(relaunched.states["hold-1"]?.state, "written")
        let keys = await hub.keys
        XCTAssertEqual(Set(keys).count, 1, "one hold, one key, however often it was sent")
        let remaining = await journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    /// The computer refuses a queued hold when it is back: the row stops saying queued, and the store has nothing
    /// left to resend (it used to stay queued and refresh Tasks every five seconds).
    func testAQueuedHoldTheComputerRefusesIsNoLongerQueued() async throws {
        let url = try journalURL()
        let hub = AwayThenBackHub()
        let (offer, slot) = offer()
        let journal = PendingWriteJournal(fileURL: url, hubID: { "hub" })
        let store = HoldsStore(hub: hub, journal: journal, pairingID: { "hub/phone" })
        _ = await store.place(offer, slot: slot, calendarID: "work")
        await hub.set(.refuse)
        await store.resendQueued()
        XCTAssertNotEqual(store.states["hold-1"]?.state, HoldsStore.queuedState)
        XCTAssertNotNil(store.failures["hold-1"], "the row offers to try again")
        let remaining = await journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }

    func testHoldUsesJournalAndUndoCallsTheSameHold() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let journal = PendingWriteJournal(fileURL: root.appendingPathComponent("writes.json"), hubID: { "hub" })
        let hub = HoldsTestHub()
        let store = HoldsStore(hub: hub, journal: journal, pairingID: { "hub/phone" })
        let slot = PhoneHoldSlot(start: "2030-01-02T14:00:00Z", end: "2030-01-02T16:00:00Z")
        let proposal = PhoneHoldProposal(id: "hold-1", thing: PhoneHoldThing(title: "Q4 plan", sourceKind: "up_next", sourceId: "offer"), calendarId: "work", slots: [slot])
        let offer = PhoneHoldOffer(proposal: proposal, calendars: [PhoneHoldCalendar(id: "work", title: "Work")], token: "signed")
        let result = await store.place(offer, slot: slot, calendarID: "work")
        XCTAssertEqual(result?.state, "pending_write")
        let undone = await store.undo(id: "hold-1")
        XCTAssertTrue(undone)
        let actions = await hub.recorded()
        XCTAssertEqual(actions.map(\.action), ["hold", "undo_hold"])
        XCTAssertEqual(actions.first?.slot, slot)
        XCTAssertEqual(actions.last?.holdId, "hold-1")
        XCTAssertNotEqual(actions.first?.idempotencyKey, actions.last?.idempotencyKey)
        let remaining = await journal.entries()
        XCTAssertTrue(remaining.isEmpty)
    }
}
