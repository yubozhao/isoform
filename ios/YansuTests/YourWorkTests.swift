import XCTest
@testable import Yansu

// Your work, the one list (phone-today v1.0.0 section 2): its order, its groups, and answers that wait for the
// computer, are sent once in order, and can be undone.
@MainActor
final class YourWorkTests: XCTestCase {
    private func item(_ id: String, origin: String = "topic", kind: String = "done", state: String = "open",
                      minutesAgo: Double = 0, answer: WorkAnswer? = nil, now: Date = Date()) -> WorkItem {
        WorkItem(id: id, kind: kind, state: state, source: origin, first: [WorkFirstPart(text: "from \(origin)")],
                 noticed: "Noticed \(id).", work: WorkBody(title: "Work \(id)"), answer: answer,
                 createdAt: RelayTime.format(now.addingTimeInterval(-minutesAgo * 60)))
    }

    func testOrderPutsTheBriefsItemsFirstInTheirOrderThenAResultThenPrepSoonestThenNewest() {
        let items = [
            item("old", minutesAgo: 90),
            item("brief-2", origin: "brief", minutesAgo: 5),
            item("new", minutesAgo: 10),
            item("prep-late", origin: "prep", minutesAgo: 1),
            item("result", origin: "agent", kind: "result", state: "back", minutesAgo: 60),
            item("brief-1", origin: "brief", minutesAgo: 1),
            item("prep-soon", origin: "prep", minutesAgo: 30),
        ]
        XCTAssertEqual(YourWork.ordered(items).map(\.id),
                       ["brief-2", "brief-1", "result", "prep-soon", "prep-late", "new", "old"])
    }

    func testTimesInTheFirstLineAreWordedForTheReadersDay() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        let now = try XCTUnwrap(RelayTime.parse("2026-11-15T20:00:00Z"))
        var value = item("prep:e1", origin: "prep")
        value = WorkItem(id: value.id, kind: "done", state: "open", source: "prep",
                         first: [WorkFirstPart(text: "For your meeting with Lena"),
                                 WorkFirstPart(text: "", key: "when", at: "2026-11-16T17:00:00Z"),
                                 WorkFirstPart(text: "", key: "when"),
                                 WorkFirstPart(text: "from the brief")],
                         noticed: "n", work: WorkBody(title: "t"), createdAt: "2026-11-15T19:00:00Z")
        XCTAssertEqual(value.firstLine(now: now, calendar: calendar, locale: Locale(identifier: "en_US")),
                       "For your meeting with Lena · tomorrow · from the brief", "a time part with no at is left out")
    }

    func testAResultBackAsksWhetherItWasUsefulInsteadOfYesNo() {
        XCTAssertTrue(YourWork.asksUseful(item("result:h1", origin: "agent", kind: "result", state: "back")))
        XCTAssertFalse(YourWork.asksUseful(item("prep:e1", origin: "prep", kind: "done")))
    }

    func testTheAnswerPathPercentEncodesTheComputersId() {
        XCTAssertEqual(WorkPath.answer("brief:b1:offer:o 1"), "/work/brief:b1:offer:o%201/answer")
        XCTAssertEqual(WorkPath.answer("task:a/b"), "/work/task:a%2Fb/answer")
        XCTAssertNil(WorkPath.answer(""))
    }

    func testTheComputersOrderWinsWhenItSendsOne() {
        var a = item("a", origin: "brief"); a.order = 1
        var b = item("b"); b.order = 0
        XCTAssertEqual(YourWork.ordered([a, b]).map(\.id), ["b", "a"])
    }

    func testAnAnswerThatCouldNotBeSentSaysItWasQueued() async throws {
        let hub = FakeHub(page: WorkPage(toAnswer: 1, items: [item("a")]))
        let work = store(hub)
        await work.refresh()
        await hub.setReachable(false)
        await work.answer(work.items[0], action: .yes)
        XCTAssertNotNil(work.pending.first?.request.queuedAt)
        XCTAssertNotNil(work.pending.first?.request.seq)
    }

    func testGroupsCapOpenItemsAtTenAndKeepOnlyTodaysAnswers() {
        let now = Date()
        var items = (0..<14).map { item("o\($0)", minutesAgo: Double($0), now: now) }
        let today = WorkAnswer(action: "yes", at: RelayTime.format(now), line: "Prep: kept",
                               undoUntil: RelayTime.format(now.addingTimeInterval(3600)))
        let yesterday = WorkAnswer(action: "no", at: RelayTime.format(now.addingTimeInterval(-2 * 86_400)), line: "Old: no")
        items.append(item("done", state: "answered", answer: today, now: now))
        items.append(item("older", state: "answered", answer: yesterday, now: now))
        var agent = item("agent", kind: "offered", state: "with_agent", now: now)
        agent.agent = WorkAgent(name: "Claude", since: RelayTime.format(now))
        items.append(agent)
        let groups = YourWork.groups(items, now: now)
        XCTAssertEqual(groups.open.count, 10)
        XCTAssertEqual(groups.toAnswer, 10)
        XCTAssertEqual(groups.upNext.count, 3)
        XCTAssertEqual(groups.doneToday.map(\.id), ["done"])
        XCTAssertEqual(groups.withAgent.map(\.id), ["agent"])
    }

    func testUndoShowsUntilTomorrowsBuild() {
        let now = Date()
        let open = WorkAnswer(action: "yes", at: RelayTime.format(now), line: "x", undoUntil: RelayTime.format(now.addingTimeInterval(60)))
        let closed = WorkAnswer(action: "yes", at: RelayTime.format(now), line: "x", undoUntil: RelayTime.format(now.addingTimeInterval(-60)))
        XCTAssertTrue(YourWork.canUndo(open, now: now))
        XCTAssertFalse(YourWork.canUndo(closed, now: now))
        XCTAssertFalse(YourWork.canUndo(WorkAnswer(action: "yes", at: RelayTime.format(now), line: "x"), now: now))
    }

    func testNoOffersTheReasonsForItsKindAndNotNowIsAReason() {
        XCTAssertEqual(WorkReason.choices(for: "offered"), [.alreadyDone, .notNow, .notUseful, .myself])
        XCTAssertEqual(WorkReason.choices(for: "done"), [.notNeeded, .wrongFacts, .notHowIdSayIt, .myself])
        XCTAssertEqual(WorkReason.choices(for: "claim"), [.wasTrueOnce, .onlySometimes, .notAtAll])
    }

    func testTheWireShapeDecodesAsAgreedForB2() throws {
        let json = """
        {"v":1,"version":"v1","generatedAt":"2026-11-15T20:00:00Z","toAnswer":1,"backFromAgents":0,
        "undoUntil":"2026-11-16T15:00:00Z","items":[{"id":"brief:b1:k1","kind":"done","row":"prep","state":"open",
        "source":"brief","ref":{"briefId":"b1","itemKey":"k1"},
        "first":[{"text":"For your 10:00 with Lena"},{"text":"tomorrow"},{"text":"from the brief","ref":"brief:b1"}],
        "noticed":"Lena will ask.","work":{"title":"Prep for Lena","text":"Where it stands.","minutes":10},
        "yes":{"does":"keep","line":"Yes keeps it with the meeting."},"why":[{"label":"Her email"}],
        "gets":[{"id":"g1","label":"Lena's email","removable":true}],"reasons":["not_needed","myself"],
        "createdAt":"2026-11-15T20:00:00Z","order":0}]}
        """
        let page = try JSONDecoder().decode(WorkPage.self, from: Data(json.utf8))
        let item = try XCTUnwrap(page.items.first)
        XCTAssertEqual(item.briefID, "b1")
        XCTAssertEqual(item.isOpen, true)
        XCTAssertEqual(item.firstLine, "For your 10:00 with Lena · tomorrow · from the brief")
        XCTAssertEqual(item.yes?.line, "Yes keeps it with the meeting.")
        XCTAssertEqual(WorkReason.choices(for: item), [.notNeeded, .myself], "the computer's reasons win")
        let request = WorkAnswerRequest(idempotencyKey: "k", action: "no", reason: "not_now", seq: 3)
        let body = try XCTUnwrap(String(data: JSONEncoder().encode(request), encoding: .utf8))
        XCTAssertTrue(body.contains("\"surface\":\"phone\""))
        XCTAssertTrue(body.contains("\"reason\":\"not_now\""))
        XCTAssertTrue(body.contains("\"seq\":3"))
    }

    // MARK: The store

    private actor FakeHub: WorkHubAPI {
        var available = true
        var reachable = true
        var page: WorkPage
        private(set) var sent: [(String, WorkAnswerRequest)] = []

        init(page: WorkPage) { self.page = page }
        func setReachable(_ value: Bool) { reachable = value }
        func setAvailable(_ value: Bool) { available = value }
        func workAvailable() async throws -> Bool {
            guard reachable else { throw HubRequestError.noAnswer }
            return available
        }
        func work() async throws -> WorkPage {
            guard reachable else { throw HubRequestError.noAnswer }
            return page
        }
        func answerWork(id: String, request: WorkAnswerRequest) async throws -> WorkItem {
            guard reachable else { throw HubRequestError.noAnswer }
            sent.append((id, request))
            let item = page.items.first { $0.id == id }!
            return YourWork.answered(item, action: WorkAction(rawValue: request.action)!, reason: nil, at: Date(), waits: false)
        }
        func sentIDs() -> [String] { sent.map(\.0) }
        func sentActions() -> [String] { sent.map(\.1.action) }
        func sentKeys() -> [String] { sent.map(\.1.idempotencyKey) }
    }

    private func store(_ hub: FakeHub) -> WorkStore {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("work-\(UUID().uuidString).json")
        return WorkStore(hub: hub, isPaired: { true }, canRead: { true }, hubID: { "hub" }, fileURL: url)
    }

    func testAnAnswerShowsAtOnceWaitsWhileTheComputerIsAwayAndIsSentOnceInOrder() async throws {
        let page = WorkPage(toAnswer: 2, items: [item("a"), item("b")])
        let hub = FakeHub(page: page)
        let work = store(hub)
        await work.refresh()
        XCTAssertEqual(work.supported, true)
        await hub.setReachable(false)
        await work.answer(work.items[0], action: .yes)
        await work.answer(work.items[1], action: .no, reason: .notNow)
        XCTAssertEqual(work.pending.count, 2, "kept on the phone while the computer is away")
        XCTAssertEqual(work.groups(now: Date()).open.count, 0, "both show answered at once")
        XCTAssertEqual(work.items.first?.answer?.waits, true, "the line says it waits for your computer")
        await hub.setReachable(true)
        await work.sendPending()
        let ids = await hub.sentIDs()
        XCTAssertEqual(ids, ["a", "b"], "in order")
        XCTAssertEqual(work.pending.count, 0)
        await work.sendPending()
        let again = await hub.sentIDs()
        XCTAssertEqual(again.count, 2, "never sent twice")
    }

    func testUndoBeforeTheComputerTookItDropsTheAnswerWithoutSendingIt() async throws {
        let hub = FakeHub(page: WorkPage(toAnswer: 1, items: [item("a")]))
        let work = store(hub)
        await work.refresh()
        await hub.setReachable(false)
        await work.answer(work.items[0], action: .notMine)
        await work.undo(work.items[0])
        XCTAssertTrue(work.pending.isEmpty)
        XCTAssertEqual(work.groups(now: Date()).open.map(\.id), ["a"])
        await hub.setReachable(true)
        await work.sendPending()
        let sent = await hub.sentIDs()
        XCTAssertTrue(sent.isEmpty)
    }

    func testAReasonPickedAfterNoRidesOnTheSameAnswer() async throws {
        let hub = FakeHub(page: WorkPage(toAnswer: 1, items: [item("a", kind: "offered")]))
        let work = store(hub)
        await work.refresh()
        await hub.setReachable(false)
        await work.answer(work.items[0], action: .no)
        await work.addReason(.alreadyDone, to: work.items[0])
        XCTAssertEqual(work.pending.count, 1)
        XCTAssertEqual(work.pending.first?.request.reason, "already_done")
    }

    func testAComputerWithoutYourWorkLeavesTheListToTheRowsItHas() async throws {
        let hub = FakeHub(page: WorkPage(toAnswer: 0, items: []))
        await hub.setAvailable(false)
        let work = store(hub)
        await work.refresh()
        XCTAssertEqual(work.supported, false)
        XCTAssertNil(work.page)
    }

    func testTheSavedListDrawsAtOnceAfterARelaunch() async throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("work-\(UUID().uuidString).json")
        let hub = FakeHub(page: WorkPage(toAnswer: 1, items: [item("a")]))
        let first = WorkStore(hub: hub, isPaired: { true }, canRead: { true }, hubID: { "hub" }, fileURL: url)
        await first.refresh()
        let second = WorkStore(hub: hub, isPaired: { true }, canRead: { true }, hubID: { "hub" }, fileURL: url)
        second.loadCached()
        XCTAssertEqual(second.items.map(\.id), ["a"])
        let other = WorkStore(hub: hub, isPaired: { true }, canRead: { true }, hubID: { "other" }, fileURL: url)
        other.loadCached()
        XCTAssertTrue(other.items.isEmpty, "never another computer's list")
    }
}
