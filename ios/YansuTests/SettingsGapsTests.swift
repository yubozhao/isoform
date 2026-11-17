import XCTest
@testable import Yansu

// The Settings gaps the computer closed (PROTOCOL.md 15.9.1, 24.10 untilEnded, 27.2).
final class SettingsGapsTests: XCTestCase {
    func testClosedEyesOffersUntilIEndItWhenTheComputerTakesIt() {
        XCTAssertEqual(ClosedEyesRow.lengths(untilEnded: true), [.minutes(15), .minutes(60), .untilEnded])
        XCTAssertEqual(ClosedEyesRow.lengths(untilEnded: false), [.minutes(15), .minutes(60), .minutes(240)])
        XCTAssertEqual(ClosedEyesRow.Length.untilEnded.body, "{\"untilEnded\":true}")
        XCTAssertEqual(ClosedEyesRow.Length.minutes(15).body, "{\"minutes\":15}")
        XCTAssertEqual(ClosedEyesRow.Length.untilEnded.key, "set.closed.untilEnded")
    }

    func testStatusDecodesTheNewFlags() throws {
        let json = #"{"hubName":"Mac","observingPauseUntilEnded":true,"topicsDailySwitch":true,"agentHandoffs":true}"#
        let status = try JSONDecoder().decode(HubStatus.self, from: Data(json.utf8))
        XCTAssertEqual(status.observingPauseUntilEnded, true)
        XCTAssertEqual(status.topicsDailySwitch, true)
        XCTAssertEqual(status.agentHandoffs, true)
        let old = try JSONDecoder().decode(HubStatus.self, from: Data(#"{"hubName":"Mac"}"#.utf8))
        XCTAssertNil(old.agentHandoffs)
    }

    func testHandoffsRouteAndPage() throws {
        let first = HubRoute.agentHandoffs(limit: 50, before: nil)
        XCTAssertEqual(first.method, "GET")
        XCTAssertEqual(first.path, "/agents/handoffs")
        XCTAssertEqual(first.query, "limit=50")
        XCTAssertEqual(HubRoute.agentHandoffs(limit: 999, before: "h 2").query, "limit=200&before=h%202")
        XCTAssertEqual(HubRoute.topicsDaily.path, "/topics-daily")
        XCTAssertEqual(HubRoute.topicsDaily.method, "GET")

        let json = #"""
        {"handoffs":[{"id":"h1","agentId":"claude-code","agentName":"Claude Code","kind":"code","method":"task_link",
          "text":"Fix the build.","sent":"Fix the build","sentAt":"2026-11-15T18:20:00Z","surface":"phone",
          "source":{"kind":"work_item","id":"w1"},"future":1}],"next":"h1"}
        """#
        let page = try JSONDecoder().decode(AgentHandoffsPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.next, "h1")
        XCTAssertEqual(page.handoffs.first?.sent, "Fix the build")
        XCTAssertEqual(page.handoffs.first?.source?.kind, "work_item")
        XCTAssertNotNil(page.handoffs.first?.date)
    }

    func testHandoffLineNamesTheAgentWhereAndWhen() throws {
        let entry = AgentHandoffEntry(id: "h1", agentName: "Claude Code", text: "Fix the build.",
                                      sentAt: "2026-11-15T18:20:00Z", surface: "phone")
        let line = SentToAgentsView.line(entry, locale: Locale(identifier: "en_US"))
        XCTAssertTrue(line.hasPrefix("Claude Code · on your phone · "), line)
        let bare = AgentHandoffEntry(id: "h2", agentId: "chatgpt", text: "x", sentAt: "not a time")
        XCTAssertEqual(SentToAgentsView.line(bare, locale: Locale(identifier: "en_US")), "chatgpt")
    }
}

// The hand-off's button by kind, and Your agents' row words (phone-agent v1.2.0 §3 and rule 14).
final class HandOffByKindTests: XCTestCase {
    func testTheButtonSaysWhatWillReallyHappen() {
        let codex = PhoneAgent(id: "codex", name: "Codex", kind: "code", reach: "connected_computer", connected: true)
        XCTAssertEqual(HandOffButton.button(for: codex), .onComputer)
        XCTAssertEqual(HandOffButton.onComputer.title("Codex"), "Send to Codex on your computer")
        XCTAssertEqual(HandOffButton.onComputer.foot(codex), "Codex works on a new branch. The result comes back here.")

        let notConnected = PhoneAgent(id: "cursor", name: "Cursor", kind: "code", reach: "on_computer")
        XCTAssertEqual(HandOffButton.button(for: notConnected), .send, "never claims a send it can't make")

        let claude = PhoneAgent(id: "claude", name: "Claude", kind: "else", reach: "its_app")
        XCTAssertEqual(HandOffButton.button(for: claude), .openIn(scheme: "claude"))
        XCTAssertEqual(HandOffButton.openIn(scheme: "claude").title("Claude"), "Open in Claude")

        let text = PhoneAgent(id: "instinct", name: "Instinct", kind: "else", reach: "by_text")
        XCTAssertEqual(HandOffButton.button(for: text), .messages)
        XCTAssertEqual(HandOffButton.messages.title("Instinct"), "Write it in Messages")

        let cloud = PhoneAgent(id: "muse", name: "Muse", kind: "else", reach: "connected", connected: true)
        XCTAssertEqual(HandOffButton.button(for: cloud), .send)
        XCTAssertEqual(HandOffButton.send.title("Muse"), "Send to Muse")
        XCTAssertNil(HandOffButton.send.foot(cloud))
    }

    func testRowWordsSayConnectedWhereAndLastWorked() {
        let codex = PhoneAgent(id: "codex", name: "Codex", kind: "code", reach: "connected_computer", connected: true,
                               lastWorkedAt: 1_763_229_000_000)
        XCTAssertEqual(AgentRowWords.state(codex), "Connected · on your computer")
        let claude = PhoneAgent(id: "claude", name: "Claude", kind: "else", reach: "its_app")
        XCTAssertEqual(AgentRowWords.state(claude), "Not connected · in its app")
        let text = PhoneAgent(id: "instinct", name: "Instinct", kind: "else", reach: "by_text")
        XCTAssertEqual(AgentRowWords.state(text), "Not connected · by text message")
        let now = Date(timeIntervalSince1970: 1_763_229_000 + 7200)
        let line = AgentRowWords.lastWorked(codex, now: now, locale: Locale(identifier: "en_US"))
        XCTAssertEqual(line, "Last worked 2 hours ago")
        XCTAssertNil(AgentRowWords.lastWorked(claude, locale: Locale(identifier: "en_US")))
    }
}

// With your agents (phone-agent v1.2.1, PROTOCOL.md 27.2).
final class WithYourAgentsTests: XCTestCase {
    private let en = Locale(identifier: "en_US")

    func testStatusDecodesAndUnknownStatesReadAsSent() throws {
        let json = #"{"handoffs":[{"id":"wo_1","agentId":"codex","agentName":"Codex","title":"Draft","sentAt":1763229000000,"state":"working","line":"Drafting.","since":0},{"id":"wo_2","state":"thinking_hard"}]}"#
        let list = try JSONDecoder().decode(HandoffStatusList.self, from: Data(json.utf8))
        XCTAssertEqual(list.handoffs.count, 2)
        XCTAssertEqual(list.handoffs[0].line, "Drafting.")
        XCTAssertNil(HandoffStatus.date(list.handoffs[0].since), "0 is unknown")
        XCTAssertEqual(list.handoffs[1].known, "sent")
        XCTAssertEqual(HubRoute.handoffStatuses.path, "/agents/handoffs/status")
        XCTAssertEqual(HubRoute.handoffStatuses.method, "GET")
    }

    func testWordsAndToneByState() {
        let working = HandoffStatus(id: "a", agentName: "Codex", state: "working")
        XCTAssertEqual(WithAgentWords.line(working, computerAsleep: false, locale: en).text, "Codex · working, on your computer")
        XCTAssertEqual(WithAgentWords.line(working, computerAsleep: false, locale: en).tone, .working)
        let noWord = HandoffStatus(id: "b", agentName: "ChatGPT", state: "no_word", since: 1_763_229_000_000)
        let words = WithAgentWords.line(noWord, computerAsleep: false, locale: en)
        XCTAssertTrue(words.text.hasPrefix("No word from ChatGPT since "), words.text)
        XCTAssertEqual(words.tone, .amber)
        let sent = HandoffStatus(id: "c", agentName: "Codex", sentAt: 1_763_229_000_000, state: "sent")
        XCTAssertEqual(WithAgentWords.line(sent, computerAsleep: true, locale: en).text, "Waiting for your computer · it's asleep")
        XCTAssertTrue(WithAgentWords.line(sent, computerAsleep: false, locale: en).text.hasPrefix("Sent to Codex · "))
        let opened = HandoffStatus(id: "d", agentName: "Claude", sentAt: 1_763_229_000_000, state: "opened")
        XCTAssertTrue(WithAgentWords.line(opened, computerAsleep: false, locale: en).text.hasPrefix("Opened in Claude · "))
        XCTAssertNotEqual(WithAgentWords.line(opened, computerAsleep: false, locale: en).tone, .working, "never Working for an app")
    }

    func testOrderPutsNoWordFirstThenWaitingThenNewestAndDropsBack() {
        let rows = [
            HandoffStatus(id: "old", sentAt: 1, state: "has_it"),
            HandoffStatus(id: "new", sentAt: 3, state: "working"),
            HandoffStatus(id: "waiting", sentAt: 2, state: "sent"),
            HandoffStatus(id: "silent", sentAt: 0, state: "no_word"),
            HandoffStatus(id: "back", sentAt: 9, state: "back"),
            HandoffStatus(id: "taken", sentAt: 9, state: "taken_back"),
        ]
        XCTAssertEqual(WithAgentWords.ordered(rows, computerAsleep: true).map(\.id), ["silent", "waiting", "new", "old"])
        XCTAssertEqual(WithAgentWords.ordered(rows, computerAsleep: false).map(\.id), ["silent", "new", "waiting", "old"])
    }
}

// Agents' requests, result files, taken back (phone-agent v1.2.0 and v1.2.1; the /work shapes from phone-connection).
final class WorkRequestsFilesTests: XCTestCase {
    private let en = Locale(identifier: "en_US")

    func testRequestFilesAndTakenBackDecodeAsOptionalFields() throws {
        let json = #"""
        {"id":"req:1","kind":"request","row":"request","state":"open","source":"agent","ref":{"handoffId":"h1"},
         "first":[],"noticed":"","work":{"title":"Codex asks Muse to collect the formats."},"reasons":[],
         "request":{"from":{"agentId":"codex","name":"Codex"},"to":{"agentId":"muse","name":"Muse"},"moves":"The last test."},
         "expiresAt":"2026-11-16T16:10:00Z","createdAt":"2026-11-15T16:10:00Z",
         "files":[{"id":"f1","name":"plan.md","where":"branch halcyon/test-plan","readable":true,"bytes":2048}],
         "takenBack":{"agentId":"claude","agentName":"Claude","at":"2026-11-15T16:30:00Z"}}
        """#
        let item = try JSONDecoder().decode(WorkItem.self, from: Data(json.utf8))
        XCTAssertEqual(item.request?.to.name, "Muse")
        XCTAssertEqual(item.files?.first?.readable, true)
        XCTAssertEqual(item.takenBack?.agentName, "Claude")
        XCTAssertEqual(item.ref?.handoffId, "h1")
        XCTAssertEqual(WorkReason.choices(for: item), [], "a request's No takes no reason")
        let old = #"{"id":"a","kind":"done","state":"open","source":"brief","first":[],"noticed":"n","work":{"title":"t"},"createdAt":"2026-11-15T16:10:00Z"}"#
        XCTAssertNil(try JSONDecoder().decode(WorkItem.self, from: Data(old.utf8)).request)
    }

    func testRequestWords() throws {
        let created = try XCTUnwrap(RelayTime.parse("2026-11-15T16:10:00Z"))
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        let item = WorkItem(id: "r", kind: "request", state: "open", source: "agent", first: [], noticed: "",
                            work: WorkBody(title: "x"), createdAt: "2026-11-15T16:10:00Z",
                            request: WorkRequest(from: WorkAgentName(name: "Codex"), to: WorkAgentName(name: "Muse")),
                            expiresAt: "2026-11-16T16:10:00Z")
        XCTAssertTrue(WorkRequestWords.firstLine(item, locale: en).hasPrefix("Codex asks Muse · "))
        let line = try XCTUnwrap(WorkRequestWords.yesLine(item, now: created, calendar: calendar, locale: en))
        XCTAssertTrue(line.hasPrefix("Yes lets Muse do it. It waits until "), line)
        XCTAssertTrue(line.hasSuffix(" tomorrow, then ends."), line)
    }

    func testFileWordsAndRoute() {
        XCTAssertEqual(WorkRequestWords.fileWhere(WorkFile(id: "f", name: "a.md", where: "branch halcyon/test-plan")),
                       "branch halcyon/test-plan, on your computer")
        XCTAssertEqual(WorkRequestWords.fileWhere(WorkFile(id: "f", name: "a.md")), "on your computer")
        XCTAssertEqual(WorkPath.file("result:a b"), "/work/result:a%20b/file")
        let route = HubRoute.workFile(path: "/work/x/file", file: "f 1")
        XCTAssertEqual(route.method, "GET")
        XCTAssertEqual(route.path, "/work/x/file")
        XCTAssertEqual(route.query, "file=f%201")
        XCTAssertEqual(WorkAction.openFile.rawValue, "open_file")
    }
}
