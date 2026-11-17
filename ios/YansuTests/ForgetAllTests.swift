import XCTest
@testable import Yansu

// Forget everything from the computer (forget_all): the phone deletes what it keeps for that computer, keeps the
// pairing, and confirms; a resent job deletes again and confirms again.
final class ForgetAllTests: XCTestCase {
    private actor RecordingAPI: ForgetAllAPI {
        var confirms: [(String, ForgetAllConfirm)] = []
        var fails = false
        func setFails(_ value: Bool) { fails = value }
        func confirmForgetAll(jobID: String, confirm: ForgetAllConfirm) async throws {
            if fails { throw HubRequestError.noAnswer }
            confirms.append((jobID, confirm))
        }
    }

    func testJobDecodesAndOnlyASafeVersionOneJobIsActedOn() throws {
        let job = try JSONDecoder().decode(ForgetAllJob.self,
                                           from: Data(#"{"v":1,"jobId":"7f1c2a8e-0000-4000-8000-000000000001","at":"2026-11-16T08:00:00Z"}"#.utf8))
        XCTAssertTrue(job.isValid)
        XCTAssertFalse(ForgetAllJob(v: 2, jobId: "abc").isValid)
        XCTAssertFalse(ForgetAllJob(v: 1, jobId: "../status").isValid)
    }

    /// The forget_all envelope's plaintext as vectors-v1.json carries it (PROTOCOL.md 28).
    func testTheVectorPlaintextIsAJobThePhoneActsOn() throws {
        let plaintext = #"{"v":1,"jobId":"c4b8f1e0-2d3a-4b5c-9e8f-7a6b5c4d3e2f","at":"2026-11-16T08:00:00Z"}"#
        let job = try JSONDecoder().decode(ForgetAllJob.self, from: Data(plaintext.utf8))
        XCTAssertTrue(job.isValid)
        XCTAssertEqual(ForgetAllRunner.key(for: job), "forget-all-c4b8f1e0-2d3a-4b5c-9e8f-7a6b5c4d3e2f")
    }

    @MainActor
    func testRunConfirmsAfterTheEraseAndAReplayConfirmsTheSameWay() async {
        let api = RecordingAPI()
        let job = ForgetAllJob(v: 1, jobId: "job-1")
        var erased = 0
        let first = await ForgetAllRunner.run(job, erase: { erased += 1; return ForgetAllPart.allCases }, api: api)
        let again = await ForgetAllRunner.run(job, erase: { erased += 1; return ForgetAllPart.allCases }, api: api)
        XCTAssertTrue(first)
        XCTAssertTrue(again)
        XCTAssertEqual(erased, 2, "a resent job deletes again")
        let confirms = await api.confirms
        XCTAssertEqual(confirms.map(\.0), ["job-1", "job-1"])
        XCTAssertEqual(confirms.first?.1.idempotencyKey, confirms.last?.1.idempotencyKey, "the same job is the same confirm")
        XCTAssertTrue(confirms.first?.1.forgot.contains("watch") == true)
    }

    @MainActor
    func testAFailedEraseOrConfirmLeavesTheJobForTheComputerToResend() async {
        let api = RecordingAPI()
        let job = ForgetAllJob(v: 1, jobId: "job-2")
        let failedErase = await ForgetAllRunner.run(job, erase: { nil }, api: api)
        XCTAssertFalse(failedErase)
        let confirmsAfterFailedErase = await api.confirms.count
        XCTAssertEqual(confirmsAfterFailedErase, 0, "nothing is confirmed that wasn't deleted")
        await api.setFails(true)
        let failedConfirm = await ForgetAllRunner.run(job, erase: { ForgetAllPart.allCases }, api: api)
        XCTAssertFalse(failedConfirm)
    }

    /// The caches are empty afterward: the saved briefs, Your work and its waiting answers, and chat.
    @MainActor
    func testEraseEmptiesThePhonesCachesAndKeepsThePairing() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let briefs = directory.appendingPathComponent("briefs")
        let chats = directory.appendingPathComponent("chats")
        let journal = directory.appendingPathComponent("writes.json")
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: briefs, journalURL: journal, chatDirectory: chats)
        let fileManager = FileManager.default
        try fileManager.createDirectory(at: briefs, withIntermediateDirectories: true)
        try fileManager.createDirectory(at: chats, withIntermediateDirectories: true)
        try Data(#"{"saved":"brief"}"#.utf8).write(to: briefs.appendingPathComponent("a.json"))
        try Data(#"{"saved":"chat"}"#.utf8).write(to: chats.appendingPathComponent("main.json"))
        let workFile = directory.appendingPathComponent("work.json")
        try Data(#"{"saved":"work"}"#.utf8).write(to: workFile)
        let pairingFiles = Set(try fileManager.contentsOfDirectory(atPath: directory.path))

        let forgot = await environment.eraseForComputer()

        XCTAssertEqual(forgot.map(Set.init), Set(ForgetAllPart.allCases))
        XCTAssertFalse(fileManager.fileExists(atPath: workFile.path), "Your work and its waiting answers are gone")
        XCTAssertEqual(Self.files(in: briefs), [], "no saved brief is left")
        XCTAssertEqual(Self.files(in: chats), [], "no chat is left")
        XCTAssertNil(environment.brief.current)
        XCTAssertNil(environment.work.page)
        XCTAssertTrue(environment.work.pending.isEmpty)
        let after = Set(try fileManager.contentsOfDirectory(atPath: directory.path))
        XCTAssertTrue(after.isSuperset(of: pairingFiles.subtracting(["work.json", "briefs", "chats"])),
                      "the pairing's own files stay")
    }

    private static func files(in directory: URL) -> [String] {
        let names = (try? FileManager.default.contentsOfDirectory(atPath: directory.path)) ?? []
        return names.filter { !$0.hasPrefix(".") }.filter { name in
            var isDirectory: ObjCBool = false
            FileManager.default.fileExists(atPath: directory.appendingPathComponent(name).path, isDirectory: &isDirectory)
            return !isDirectory.boolValue
        }
    }
}

// Read anywhere, cleared everywhere (notify A; PROTOCOL.md 9.4).
final class NoticesReadTests: XCTestCase {
    private actor FakeAPI: NoticesReadAPI {
        var available = true
        var pages: [NoticeReadList] = []
        var sinces: [String?] = []
        var marked: [[String]] = []
        func setPages(_ value: [NoticeReadList]) { pages = value }
        func noticesReadAvailable() async throws -> Bool { available }
        func noticesRead(since: String?) async throws -> NoticeReadList {
            sinces.append(since)
            return pages.isEmpty ? NoticeReadList(read: []) : pages.removeFirst()
        }
        func markNoticesRead(_ ids: [String]) async throws { marked.append(ids) }
    }

    private actor Withdrawn {
        var ids: [Set<String>] = []
        func add(_ value: Set<String>) { ids.append(value) }
    }

    func testWithdrawPushParsesAndKeepsOnlySafeIds() {
        let push: [AnyHashable: Any] = ["aps": ["content-available": 1],
                                        "yansu": ["kind": "withdraw", "hub": "h1", "ids": ["n-1", "brief:b2", "bad/id"]]]
        let parsed = NoticeWithdraw.parse(push)
        XCTAssertEqual(parsed?.hub, "h1")
        XCTAssertEqual(parsed?.ids, ["n-1", "brief:b2"])
        XCTAssertNil(NoticeWithdraw.parse(["yansu": ["kind": "brief_ready", "id": "x"]]))
    }

    func testANotificationStandsForItsNoticeIdAndItsBrief() {
        XCTAssertEqual(NoticeWithdraw.ids(of: ["yansu": ["kind": "meeting_prep", "id": "n-7"]]), ["n-7"])
        XCTAssertEqual(NoticeWithdraw.ids(of: ["yansu": ["kind": "brief"], "localBriefID": "b9"]), ["brief:b9"])
        XCTAssertEqual(HubRoute.noticesRead(since: "2026-11-16T08:00:00+01:00").query, "since=2026-11-16T08:00:00%2B01:00")
        XCTAssertEqual(HubRoute.noticesRead(since: nil).query, "")
        XCTAssertEqual(HubRoute.noticesRead(since: nil).path, "/notices/read")
    }

    @MainActor
    func testPollWithdrawsWhatWasReadAndStartsTheNextPollAfterIt() async {
        let api = FakeAPI()
        await api.setPages([NoticeReadList(read: [NoticeReadEntry(id: "n-1", at: "2026-11-16T08:00:00Z"),
                                                  NoticeReadEntry(id: "brief:b2", at: "2026-11-16T08:05:00Z")])])
        let withdrawn = Withdrawn()
        let defaults = UserDefaults(suiteName: "notices-read-\(UUID().uuidString)")!
        let store = NoticesReadStore(api: api, defaults: defaults, hubID: { "h1" }, withdraw: { await withdrawn.add($0) })
        await store.poll()
        await store.poll()
        let sinces = await api.sinces
        XCTAssertEqual(sinces, [nil, "2026-11-16T08:05:00Z"])
        let ids = await withdrawn.ids
        XCTAssertEqual(ids.first, ["n-1", "brief:b2"])
    }

    @MainActor
    func testTodayShowingTheBriefMarksItReadOnce() async {
        let api = FakeAPI()
        let withdrawn = Withdrawn()
        let store = NoticesReadStore(api: api, defaults: UserDefaults(suiteName: "notices-read-\(UUID().uuidString)")!,
                                     hubID: { "h1" }, withdraw: { await withdrawn.add($0) })
        await store.briefShown("b2")
        await store.briefShown("b2")
        let marked = await api.marked
        XCTAssertEqual(marked, [["brief:b2"]])
    }
}

// An agent joining from the phone (phone-agent v1.2.0 §4 and rule 14; PROTOCOL.md 27.4).
final class AgentJoinTests: XCTestCase {
    func testJoinDecodesEachState() throws {
        let start = try JSONDecoder().decode(AgentJoinStart.self, from: Data(#"{"joinId":"j1","message":"Connect: https://x","link":"https://x","expiresAt":1763280000000,"local":false}"#.utf8))
        XCTAssertEqual(start.joinId, "j1")
        let requested = try JSONDecoder().decode(AgentJoinState.self, from: Data(#"{"joinId":"j1","state":"requested","expiresAt":1763280000000,"agentName":"Claude Code","from":"Claude Code on this Mac","toOpen":"Only what each hand-off sends","code":"0417"}"#.utf8))
        XCTAssertFalse(requested.isOver)
        XCTAssertEqual(requested.code, "0417", "leading zeros stay")
        XCTAssertEqual(AgentJoinWords.spaced("0417"), "0 4 1 7")
        XCTAssertEqual(AgentJoinWords.fromLine(requested), "From Claude Code on this Mac · to open Only what each hand-off sends")
        XCTAssertTrue(AgentJoinState(joinId: "j1", state: "denied").isOver)
        XCTAssertEqual(AgentJoinPath.join("j1"), "/agents/join/j1")
        XCTAssertNil(AgentJoinPath.join("../x"))
        XCTAssertEqual(HubRoute.agentJoin(path: "/agents/join/j1").method, "GET")
        XCTAssertEqual(AgentJoinConflict.code(in: #"{"ok":false,"error":"conflict","data":{"code":"no_address"}}"#), "no_address")
        XCTAssertNil(AgentJoinConflict.code(in: "not json"))
    }

    func testADisconnectedAgentFadesWithConnectAgainWords() {
        let gone = PhoneAgent(id: "cursor", name: "Cursor", kind: "code", reach: "on_computer", disconnectedAt: 1_763_000_000_000)
        XCTAssertTrue(gone.isDisconnected)
        XCTAssertEqual(AgentRowWords.state(gone), "Disconnected")
        let back = PhoneAgent(id: "cursor", name: "Cursor", kind: "code", reach: "connected_computer", connected: true,
                              disconnectedAt: 1_763_000_000_000)
        XCTAssertFalse(back.isDisconnected, "connected again clears it")
        let joined = PhoneAgent(id: "ag_1", name: "Ana's Helper", kind: "else", reach: "disconnected", connected: false,
                                disconnectedAt: 1_763_000_000_000)
        XCTAssertEqual(WhoDoesYourWork.reachLine(joined), "Disconnected")
        XCTAssertEqual(AgentRowWords.state(joined), "Disconnected")
    }

    @MainActor
    func testTheJoinNoticeOpensItsCodeSheet() {
        let router = NotificationRouter()
        router.openTodayNotice(kind: "agent_join", id: "join-7")
        XCTAssertEqual(router.joinCheck?.joinId, "join-7")
    }
}

// Your work clear of the fold on a wide phone (phone-today v1.x).
final class DuoFoldClearTests: XCTestCase {
    func testAColumnAcrossTheFoldStartsRightOfIt() {
        // Today's pane on the 890-wide layout: 280 to 810, the fold at 445.
        let pane = CGRect(x: 280, y: 0, width: 530, height: 600)
        XCTAssertEqual(DuoLayout.foldClearInset(pane, foldX: 445), 181)
        XCTAssertEqual(DuoLayout.foldClearInset(CGRect(x: 470, y: 0, width: 340, height: 1), foldX: 445), 0, "already clear")
        XCTAssertEqual(DuoLayout.foldClearInset(pane, foldX: nil), 0, "not a wide phone")
        XCTAssertEqual(DuoLayout.foldClearInset(CGRect(x: 280, y: 0, width: 400, height: 1), foldX: 445), 0,
                       "too narrow right of the fold: it stays")
    }
}
