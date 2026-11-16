import XCTest
@testable import Yansu

// Your agents and the hand-off (phone-agent v1.1.0, PROTOCOL.md 27).
final class HandOffTests: XCTestCase {
    private let body = """
    {"choices":{"code":"codex","else":""},"agents":[
     {"id":"codex","name":"Codex","kind":"code","reach":"on_computer","found":true,"connected":false,"lastWorkedAt":1763229000000},
     {"id":"claude-code","name":"Claude Code","kind":"code","reach":"connected_computer","found":true,"connected":true},
     {"id":"claude","name":"Claude","kind":"else","reach":"its_app"},
     {"id":"instinct","name":"Instinct","kind":"else","reach":"by_text"},
     {"id":"muse","name":"Muse","kind":"else","reach":"connected","connected":true}]}
    """

    func testTheAgentsBodyDecodesWithTwoChoicesAndAnEmptyOneAsksOnce() throws {
        let agents = try JSONDecoder().decode(PhoneAgents.self, from: Data(body.utf8))
        XCTAssertEqual(agents.choice(for: "code")?.name, "Codex")
        XCTAssertNil(agents.choice(for: "else"), "no choice yet: the first Yes asks Who does your work?")
        XCTAssertEqual(agents.agents(of: "else").map(\.id), ["claude", "instinct", "muse"])
    }

    func testSendReachesEachAgentItsOwnWay() throws {
        let agents = try JSONDecoder().decode(PhoneAgents.self, from: Data(body.utf8))
        func route(_ id: String) -> HandOffRoute { HandOffRoute.route(for: agents.agents.first { $0.id == id }!) }
        XCTAssertEqual(route("muse"), .direct, "connected: straight to its inbox")
        XCTAssertEqual(route("claude-code"), .direct)
        XCTAssertEqual(route("instinct"), .messages, "by text: Messages with the task written")
        XCTAssertEqual(route("codex"), .computer, "a coding agent not connected is connected on the computer")
        XCTAssertEqual(route("claude"), .app(scheme: "claude"), "its app: copied, then the app opens")
    }

    func testMessagesOpensWithTheTaskAsItsBodyAndNoRecipient() throws {
        let url = try XCTUnwrap(HandOffRoute.messagesURL(text: "Write the test plan & send"))
        XCTAssertTrue(url.absoluteString.hasPrefix("sms:&body="))
        XCTAssertTrue(url.absoluteString.contains("%26"), "the text is encoded")
    }

    func testTheHandOffBodyIsTheContracts() throws {
        let request = PhoneHandOffRequest(idempotencyKey: "k", agentId: "muse", kind: "else", sourceKind: "work_item",
                                          sourceId: "topic:t1:p1", title: "Write the test plan")
        let json = try XCTUnwrap(String(data: JSONEncoder().encode(request), encoding: .utf8))
        for field in ["\"sourceKind\":\"work_item\"", "\"sourceId\":\"topic:t1:p1\"", "\"agentId\":\"muse\"", "\"kind\":\"else\""] {
            XCTAssertTrue(json.contains(field), field)
        }
    }

    func testAYourWorkItemHandsOffAsWorkItemToTheAgentForItsKind() throws {
        let json = #"{"id":"topic:t1:p1","kind":"offered","state":"open","source":"topic","first":[],"noticed":"n","work":{"title":"Fix the date parser","text":"Two tests fail."},"yes":{"does":"agent","line":"Yes hands it to Codex.","kind":"code"},"createdAt":"2026-11-15T20:00:00Z"}"#
        let item = try JSONDecoder().decode(WorkItem.self, from: Data(json.utf8))
        let source = HandOffSource.work(item)
        XCTAssertEqual(source.kind, "work_item", "the sourceKind /agents/handoffs accepts")
        XCTAssertEqual(source.workKind, "code", "code work goes to the code agent")
        var other = item
        other.yes = WorkYes(does: "agent", line: "Yes")
        XCTAssertEqual(HandOffSource.work(other).workKind, "else")
    }
}
