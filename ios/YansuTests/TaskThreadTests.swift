import Foundation
import XCTest
@testable import Yansu

final class TaskThreadTests: XCTestCase {
    private let english = Locale(identifier: "en_US")

    func testDetailFromAnOlderComputerReadsAsAnEmptyThread() throws {
        let json = #"{"id":"t","title":"T","status":"in_progress","group":"running","createdAt":"2026-10-30T16:00:00Z","updatedAt":"2026-10-30T16:00:00Z","steps":[]}"#
        let detail = try JSONDecoder().decode(MobileTaskDetail.self, from: Data(json.utf8))
        XCTAssertEqual(detail.events, [])
        XCTAssertNil(detail.actions)
        XCTAssertFalse(detail.hasThread)
        XCTAssertNil(detail.origin)
    }

    func testDetailDecodesTheThread() throws {
        let json = #"""
        {"id":"sow","title":"Send Priya the signed SOW","status":"needs_review","group":"needs_you",
         "createdAt":"2026-10-30T16:00:00Z","updatedAt":"2026-10-30T17:12:00Z","steps":[],"unread":true,
         "origin":{"kind":"up_next","at":"2026-10-30T16:02:00Z"},
         "events":[{"id":"q","kind":"isoform","at":"2026-10-30T16:32:00Z","text":"Send v3 now?",
                    "choices":[{"id":"now","label":"Send v3 now"}]},
                   {"id":"f","kind":"file","at":"2026-10-30T16:40:00Z","file":{"name":"headlines.md"}}],
         "actions":["mark_done","rename","not_now","made"]}
        """#
        let detail = try JSONDecoder().decode(MobileTaskDetail.self, from: Data(json.utf8))
        XCTAssertTrue(detail.hasThread)
        XCTAssertEqual(detail.origin?.kind, "up_next")
        XCTAssertEqual(detail.events.first?.openChoices.map(\.label), ["Send v3 now"])
        XCTAssertEqual(detail.events.last?.file?.name, "headlines.md")
        XCTAssertEqual(detail.task.unread, true)
        var answered = detail.events[0]
        answered.chosenId = "now"
        XCTAssertEqual(answered.openChoices, [], "The buttons go once one is tapped")
    }

    func testOutcomeSentencesAndNeverABareDone() {
        let at = "2026-10-30T17:12:00Z"
        func outcome(_ kind: String, _ params: [String: String]? = nil) -> String? {
            TaskThreadCopy.outcome(MobileTaskOutcome(kind: kind, by: "isoform", at: at, params: params), locale: english)
        }
        XCTAssertEqual(outcome("marked_done"), "You marked it done.")
        XCTAssertEqual(outcome("sent", ["person": "Priya", "thing": "the signed SOW"]), "Priya has the signed SOW.")
        XCTAssertEqual(outcome("hold_placed", ["when": "today, 2 to 4 PM"]), "On your calendar: today, 2 to 4 PM.")
        XCTAssertEqual(outcome("completed", ["summary": "Wrote three headlines."]), "Wrote three headlines.")
        XCTAssertNil(outcome("completed"), "No summary, no sentence")
        XCTAssertNil(outcome("sent", ["person": "Priya"]), "A sentence with a missing part is not shown")
        XCTAssertNil(outcome("something_new"))
        XCTAssertNil(TaskThreadCopy.outcome(nil, locale: english))
        XCTAssertTrue(outcome("you_did", ["app": "Mail"])?.hasPrefix("You replied from Mail at ") == true)
    }

    func testOriginLines() throws {
        let at = try XCTUnwrap(RelayTime.parse("2026-10-30T16:02:00Z"))
        let time = TaskThreadCopy.clock(at, locale: english)
        XCTAssertEqual(TaskThreadCopy.origin(MobileTaskOrigin(kind: "up_next", at: "2026-10-30T16:02:00Z"), now: at, locale: english),
                       "From Up next · you tapped Do it at \(time)")
        XCTAssertEqual(TaskThreadCopy.origin(MobileTaskOrigin(kind: "list", at: "2026-10-30T16:02:00Z"), now: at, locale: english),
                       "From your list · \(time)")
        XCTAssertEqual(TaskThreadCopy.origin(MobileTaskOrigin(kind: "brief_chip", at: "2026-10-30T16:02:00Z",
                                                              label: "Draft the reply to Dan"), now: at, locale: english),
                       "From your \(time) brief · Draft the reply to Dan")
        XCTAssertNil(TaskThreadCopy.origin(MobileTaskOrigin(kind: "brief_chip", at: "2026-10-30T16:02:00Z"), locale: english),
                     "A chip origin without the chip's words is not shown")
        XCTAssertNil(TaskThreadCopy.origin(MobileTaskOrigin(kind: "desktop", at: "2026-10-30T16:02:00Z"), locale: english))
        XCTAssertNil(TaskThreadCopy.origin(nil, locale: english))
    }

    func testStatusLineForEachKindOfTask() throws {
        let now = try XCTUnwrap(RelayTime.parse("2026-10-30T17:12:00Z"))
        let needs = MobileTask(id: "n", title: "N", status: "needs_review", group: "needs_you", blockedReason: nil,
                               runner: nil, progress: nil, createdAt: "", updatedAt: "2026-10-30T17:12:00Z")
        let status = TaskThreadCopy.status(task: needs, detail: nil, now: now, locale: english)
        XCTAssertEqual(status.label, "Needs you")
        XCTAssertEqual(status.detail, "updated " + TaskThreadCopy.clock(now, locale: english))

        let running = MobileTask(id: "r", title: "R", status: "in_progress", group: "running", blockedReason: nil,
                                 runner: nil, progress: nil, createdAt: "", updatedAt: "")
        XCTAssertEqual(TaskThreadCopy.status(task: running, detail: nil, locale: english).label, "Running · starting")
        let detail = MobileTaskDetail(id: "r", title: "R", status: "in_progress", group: "running", blockedReason: nil,
                                      runner: nil, progress: nil, createdAt: "", updatedAt: "", startedAt: nil,
                                      steps: [MobileTaskStep(text: "a", state: "done"), MobileTaskStep(text: "b", state: "done"),
                                              MobileTaskStep(text: "c", state: "current"), MobileTaskStep(text: "d", state: "pending"),
                                              MobileTaskStep(text: "e", state: "pending")])
        XCTAssertEqual(TaskThreadCopy.status(task: running, detail: detail, locale: english).label, "Running · step 3 of 5")
    }

    func testDoneRowsWithoutAnOutcomeHideOnlyWhenTheComputerServesThreads() {
        func done(_ id: String, outcome: MobileTaskOutcome?) -> MobileTask {
            MobileTask(id: id, title: id, status: "completed", group: "done", blockedReason: nil, runner: nil,
                       progress: nil, createdAt: "", updatedAt: "2026-10-30T17:00:00Z", outcome: outcome)
        }
        let rows = [done("with", outcome: MobileTaskOutcome(kind: "marked_done", by: "you", at: "2026-10-30T17:00:00Z")),
                    done("bare", outcome: nil)]
        XCTAssertEqual(TaskGroups.make(from: rows, threads: true).done.map(\.id), ["with"])
        XCTAssertEqual(Set(TaskGroups.make(from: rows, threads: false).done.map(\.id)), ["with", "bare"],
                       "An older computer keeps its Done rows")
    }

    func testThreadStringsHaveChineseAndTheChatPasteIsGone() throws {
        let path = try XCTUnwrap(Bundle.main.path(forResource: "zh-Hans", ofType: "lproj"))
        let bundle = try XCTUnwrap(Bundle(path: path))
        let expected = [
            "thread.status.needsYou": "需要你",
            "Reply about this task": "就这件事回复",
            "Mark as done": "标为完成",
            "What this task made": "这个任务做出的东西",
            "You marked it done.": "你标为完成了。",
            "Running · step %1$d of %2$d": "进行中 · 第 %1$d 步，共 %2$d 步",
            "section.needsYou": "需要你",
            "section.scheduled": "定时任务",
            "section.doneThisWeek": "本周完成",
            "upnext.notNow": "暂不",
            "Started": "已开始",
            "deck.kind.yourDay": "你的一天"
        ]
        for (key, value) in expected {
            XCTAssertEqual(bundle.localizedString(forKey: key, value: nil, table: nil), value, key)
        }
        let catalog = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Yansu/Resources/Localizable.xcstrings")
        let text = try String(contentsOf: catalog, encoding: .utf8)
        XCTAssertFalse(text.contains("Show me the result of"), "Review opens the thread; nothing pastes into Chat")
    }

    func testRowLinesNameWhoRunsItAndTheStep() throws {
        let now = try XCTUnwrap(RelayTime.parse("2026-10-30T17:12:00Z"))
        let running = MobileTask(id: "r", title: "Pricing page copy", status: "in_progress", group: "running",
                                 blockedReason: nil, runner: "claude_code", progress: MobileTaskProgress(done: 2, total: 5),
                                 createdAt: "", updatedAt: "")
        XCTAssertEqual(TaskThreadCopy.rowLine(running, now: now, locale: english).rest,
                       "Claude Code on your computer · step 3 of 5")
        let needs = MobileTask(id: "n", title: "N", status: "needs_review", group: "needs_you", blockedReason: nil,
                               runner: nil, progress: nil, createdAt: "", updatedAt: "2026-10-30T17:12:00Z")
        let line = TaskThreadCopy.rowLine(needs, now: now, locale: english)
        XCTAssertEqual(line.lead, "Ready for review")
        XCTAssertEqual(line.rest, " · " + TaskThreadCopy.clock(now, locale: english))
        XCTAssertFalse(TaskThreadCopy.rowLine(running, now: now, locale: english).rest.contains("Mac"),
                       "Phone copy says your computer, never your Mac")
    }

    func testUpNextSourceLineAndKinds() throws {
        let now = try XCTUnwrap(RelayTime.parse("2026-10-30T17:12:00Z"))
        let evidence = IdeaEvidence(text: "Maya introduced you to Lena on Tuesday.", source: "mail", at: "2026-10-27T22:40:00Z")
        let line = TaskThreadCopy.sourceLine(evidence, now: now, locale: english)
        XCTAssertTrue(line.hasPrefix("From Mail · "), line)
        XCTAssertEqual(TaskThreadCopy.deckKind("reply"), "Reply")
        XCTAssertEqual(TaskThreadCopy.deckKind("your_day"), "Your day")
        XCTAssertEqual(TaskThreadCopy.deckKind("claude_code"), "Claude Code")
        XCTAssertNil(TaskThreadCopy.deckKind(nil))
        let legacy = try JSONDecoder().decode(IdeaOffer.self, from: Data(#"{"id":"suggestion.a","kind":"suggestion","title":"T","why":"W","prompt":"P"}"#.utf8))
        XCTAssertNil(legacy.evidence, "An older computer's offer still reads")
    }

    func testDragPillFillsToTheLineAndScheduledSectionOrder() {
        XCTAssertEqual(TaskDeckMotion.presentation(translation: 17.5, width: 100).progress, 0.5, accuracy: 0.001)
        XCTAssertTrue(TaskDeckMotion.presentation(translation: 35, width: 100).atLine)
        XCTAssertFalse(TaskDeckMotion.presentation(translation: 34, width: 100).atLine)
        XCTAssertEqual(TaskDeckMotion.presentation(translation: -35, width: 100).notNowTint, 1, accuracy: 0.001)
        let groups = TaskGroups.make(from: [])
        XCTAssertEqual(TaskDeck.visibleSections(groups: groups, hasOffers: true, hasScheduled: true), [.upNext, .scheduled])
        XCTAssertEqual(TasksCopy.subtitle(MobileTaskCounts(needsYou: 1, running: 1, doneThisWeek: 3), showsDone: false),
                       "1 needs you · 1 running")
    }

    @MainActor
    func testReplyShowsAtOnceAndComesBackToTheBoxOnFailure() async throws {
        let detail = MobileTaskDetail(id: "t", title: "T", status: "needs_review", group: "needs_you", blockedReason: nil,
                                      runner: nil, progress: nil, createdAt: "", updatedAt: "", startedAt: nil, steps: [],
                                      actions: ["rename"])
        let hub = ThreadTestHub(detail: detail)
        let store = TasksStore(hub: hub, isPaired: { true }, hubID: { "hub" },
                               fileURL: FileManager.default.temporaryDirectory
                                .appendingPathComponent(UUID().uuidString).appendingPathComponent("tasks.json"))
        _ = await store.loadDetail(id: "t")
        let sent = await store.reply(id: "t", text: "  Keep it short ")
        XCTAssertTrue(sent)
        XCTAssertEqual(store.detail?.events.map(\.text), ["Keep it short"])
        let requests = await hub.replies
        XCTAssertEqual(requests.map(\.text), ["Keep it short"])

        await hub.failNext()
        let failed = await store.reply(id: "t", text: "Second")
        XCTAssertFalse(failed)
        XCTAssertEqual(store.detail?.events.map(\.text), ["Keep it short"], "A failed reply leaves the thread")
        XCTAssertEqual(store.threadNotice, "Update Isoform on your computer to reply here")
    }

    /// The computer was asleep: the relay took the reply but no answer came, so the words went back to the box.
    /// Sending them again uses the same key, so the computer posts the reply once when it wakes. Other words,
    /// or the same words after a refusal or a success, are a new reply.
    @MainActor
    func testARetypedReplyAfterNoAnswerKeepsItsKey() async throws {
        let detail = MobileTaskDetail(id: "t", title: "T", status: "needs_review", group: "needs_you", blockedReason: nil,
                                      runner: nil, progress: nil, createdAt: "", updatedAt: "", startedAt: nil, steps: [],
                                      actions: ["rename"])
        let hub = ThreadTestHub(detail: detail)
        let store = TasksStore(hub: hub, isPaired: { true }, hubID: { "hub" },
                               fileURL: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString))
        _ = await store.loadDetail(id: "t")
        await hub.goQuiet()
        let unanswered = await store.reply(id: "t", text: "Keep it short")
        XCTAssertFalse(unanswered)
        await hub.wake()
        let resent = await store.reply(id: "t", text: "Keep it short")
        XCTAssertTrue(resent)
        let other = await store.reply(id: "t", text: "And friendly")
        XCTAssertTrue(other)
        let again = await store.reply(id: "t", text: "Keep it short")
        XCTAssertTrue(again)
        let keys = await hub.attemptKeys
        XCTAssertEqual(keys.count, 4)
        XCTAssertEqual(keys[0], keys[1], "the retyped reply is the same request to the computer")
        XCTAssertNotEqual(keys[1], keys[2])
        XCTAssertNotEqual(keys[1], keys[3], "a reply the computer answered is not reused")
    }

    @MainActor
    func testStopFailureCanBeRetriedInThread() async {
        let detail = MobileTaskDetail(id: "t", title: "T", status: "in_progress", group: "running", blockedReason: nil,
                                      runner: nil, progress: nil, createdAt: "", updatedAt: "", startedAt: nil, steps: [])
        let hub = ThreadTestHub(detail: detail)
        let store = TasksStore(hub: hub, isPaired: { true }, guardian: LocalDecisionGuard(), hubID: { "hub" },
                               fileURL: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString))
        await hub.failNext()
        let failed = await store.stop(id: "t")
        XCTAssertFalse(failed)
        XCTAssertEqual(store.stopFailureID, "t")
        let stopped = await store.stop(id: "t")
        XCTAssertTrue(stopped)
        XCTAssertNil(store.stopFailureID)
    }

    @MainActor
    func testIconBadgeDefaultsOffAndUsesNeedsYouCount() {
        XCTAssertEqual(TasksStore.iconBadgeCount(enabled: false, needsYou: 3), 0)
        XCTAssertEqual(TasksStore.iconBadgeCount(enabled: true, needsYou: 3), 3)
        XCTAssertEqual(TasksStore.iconBadgeCount(enabled: true, needsYou: -1), 0)
    }

    @MainActor
    func testDelayedAnswerAppearsOnCompletionEvent() async throws {
        let detail = MobileTaskDetail(id: "t", title: "T", status: "needs_review", group: "needs_you", blockedReason: nil,
                                      runner: nil, progress: nil, createdAt: "", updatedAt: "", startedAt: nil, steps: [],
                                      actions: ["rename"])
        let hub = ThreadTestHub(detail: detail)
        let store = TasksStore(hub: hub, isPaired: { true }, hubID: { "hub" },
                               fileURL: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString))
        _ = await store.loadDetail(id: "t")
        let sent = await store.reply(id: "t", text: "Keep it short")
        XCTAssertTrue(sent)
        XCTAssertEqual(store.detail?.events.map(\.text), ["Keep it short"])
        await hub.answer("Here is the short version.")
        let event = MobileChatTurnEvent(event: "chat_turn", chatId: "chat-1", turnId: "turn-1",
                                        state: "done", seq: 2, at: "")
        await store.receiveTurn(event, for: "t")
        XCTAssertEqual(store.detail?.events.map(\.text), ["Keep it short", "Here is the short version."])
    }
}

private actor ThreadTestHub: TasksHubAPI {
    var detail: MobileTaskDetail
    var replies: [MobileTaskReplyRequest] = []
    private var failing = false
    private var quiet = false
    private(set) var attemptKeys: [String] = []

    init(detail: MobileTaskDetail) { self.detail = detail }

    func goQuiet() { quiet = true }
    func wake() { quiet = false }

    func failNext() { failing = true }

    func answer(_ text: String) {
        detail.events.append(MobileTaskEvent(id: "answer", kind: "isoform", at: "2026-10-30T17:12:01Z", text: text))
    }

    func tasks() async throws -> MobileTasksPage {
        MobileTasksPage(tasks: [detail.task], counts: MobileTaskCounts(needsYou: 1, running: 0, doneThisWeek: 0))
    }

    func task(id: String) async throws -> MobileTaskDetail { detail }

    func stopTask(id: String, request: MobileTaskStopRequest) async throws -> MobileTask {
        if failing { failing = false; throw HubRequestError.noAnswer }
        return detail.task
    }

    func replyToTask(id: String, request: MobileTaskReplyRequest) async throws -> MobileTaskDetail {
        attemptKeys.append(request.idempotencyKey)
        if quiet { throw HubRequestError.noAnswer }
        if failing {
            failing = false
            throw HubRequestError.rejected(status: 404, message: "not found")
        }
        replies.append(request)
        detail.events.append(MobileTaskEvent(id: "you-\(replies.count)", kind: "you", at: "2026-10-30T17:12:00Z",
                                             text: request.text))
        return detail
    }
}

/// PROTOCOL.md section 23: a send queued while the computer is away carries its queue time, and a late one
/// is held by the computer until the person says yes again.
final class TaskSendWaitTests: XCTestCase {
    func testAQueuedSendEncodesItsQueueTimeAndALiveSendDoesNot() throws {
        let queued = MobileTaskActionRequest(idempotencyKey: "k1", action: "send", text: "Yes",
                                             queuedAt: TaskSendWait.queuedAt(Date(timeIntervalSince1970: 1_800_000_000)))
        let json = try String(decoding: JSONEncoder().encode(queued), as: UTF8.self)
        XCTAssertTrue(json.contains("\"queuedAt\":\"2027-01-15T08:00:00Z\""), json)

        let live = MobileTaskActionRequest(idempotencyKey: "k2", action: "send", text: "Yes")
        let liveJSON = try String(decoding: JSONEncoder().encode(live), as: UTF8.self)
        XCTAssertFalse(liveJSON.contains("queuedAt"), "a live send must not carry a queue time: \(liveJSON)")
    }

    func testTheComputerHoldIsItsOwnRejectionAndIsNotDropped() {
        XCTAssertEqual(HubRequestError.rejection(status: 409, message: "confirm_send"), .confirmSend(person: nil))
        let named = HubResponse(status: 409, body: #"{"ok":false,"data":{"code":"confirm_send","person":"Sam"},"error":"confirm_send"}"#)
        XCTAssertThrowsError(try named.payload(EmptyPayload.self)) { error in
            XCTAssertEqual(error as? HubRequestError, .confirmSend(person: "Sam"))
        }
        let anonymous = HubResponse(status: 409, body: #"{"ok":false,"data":{"code":"confirm_send"},"error":"confirm_send"}"#)
        XCTAssertThrowsError(try anonymous.payload(EmptyPayload.self)) { error in
            XCTAssertEqual(error as? HubRequestError, .confirmSend(person: nil), "a send with no name says the reply")
        }
        XCTAssertEqual(HubRequestError.rejection(status: 409, message: "changed"), .conflict(message: "changed"))
        XCTAssertEqual(JournaledWriter.disposition(for: HubRequestError.confirmSend(person: "Sam")), .changed,
                       "a held send keeps its draft; it is not dropped")
    }

    func testTheAskWindowIsAnHourAndTheRelayKeepsADay() {
        XCTAssertEqual(TaskSendWait.askAfter, 3600)
        XCTAssertEqual(TaskSendWait.relayRetention, 24 * 3600)
        XCTAssertNil(TaskSendWait.queuedAt(nil))
    }
}

private struct EmptyPayload: Decodable {}

/// A drafted reply the relay is still holding stays on the phone while it is paired: the loader must not clear it.
@MainActor
final class PairedHeldDraftTests: XCTestCase {
    func testAPairedHeldDraftSurvivesTheLoaderAndIsExpiredAfterADay() async throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let journal = PendingWriteJournal(fileURL: url, hubID: { "hub" })
        let store = TasksStore(hub: SilentTasksHub(), isPaired: { true }, hubID: { "hub" },
                               journal: journal, deviceID: { "device" }, fileURL: url.appendingPathComponent("tasks.json"))
        _ = try await journal.reserveTodayDraft(taskID: "t", revision: "r", text: "Yes", deviceID: "device",
                                                makeKey: { "held-1" },
                                                createdAt: Date().addingTimeInterval(-25 * 3600))
        await store.loadPendingDraftSends()
        XCTAssertEqual(store.pendingDraftSends.map(\.key), ["held-1"], "a paired phone dropped its held draft")
        XCTAssertEqual(store.expiredDraftSends.map(\.key), ["held-1"], "a draft held a day is not expired")
    }
}

/// A hub that never answers: the loader test only reads the journal.
private actor SilentTasksHub: TasksHubAPI {
    func brainDumpAvailable() async throws -> Bool { throw HubRequestError.noAnswer }
    func parseBrainDump(text: String, idempotencyKey: String) async throws -> [BrainDumpItem] { throw HubRequestError.noAnswer }
    func addBrainDump(items: [BrainDumpItem], idempotencyKey: String) async throws { throw HubRequestError.noAnswer }
    func tasks() async throws -> MobileTasksPage { throw HubRequestError.noAnswer }
    func task(id: String) async throws -> MobileTaskDetail { throw HubRequestError.noAnswer }
    func stopTask(id: String, request: MobileTaskStopRequest) async throws -> MobileTask { throw HubRequestError.noAnswer }
    func replyToTask(id: String, request: MobileTaskReplyRequest) async throws -> MobileTaskDetail { throw HubRequestError.noAnswer }
    func actOnTask(id: String, request: MobileTaskActionRequest) async throws -> MobileTaskDetail { throw HubRequestError.noAnswer }
}
