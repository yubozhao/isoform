import XCTest
@testable import Yansu

// Your data › Your answers: the answers ledger read from the computer (GET /answers).
final class YourDataTests: XCTestCase {
    func testAnAnswerEntryDecodesAndSaysWhatWasAnsweredWhereAndWhen() throws {
        let json = #"{"answers":[{"v":1,"id":"a1","item":"topic:t:p","kind":"work","title":"Write the test plan","action":"no","reason":"not_now","at":"2026-11-15T20:00:00Z","surface":"phone","source":"topic","undone":true}],"next":"c1"}"#
        let page = try JSONDecoder().decode(AnswersPage.self, from: Data(json.utf8))
        let answer = try XCTUnwrap(page.answers.first)
        XCTAssertEqual(page.next, "c1")
        XCTAssertEqual(answer.actionLine, "No · not now")
        let line = YourAnswersView.line(answer, locale: Locale(identifier: "en_US"))
        XCTAssertTrue(line.hasPrefix("No · not now · undone · on your phone · "), line)
    }

    func testTheAnswersRoutesAskTheLedgerAndForgetOneById() {
        XCTAssertEqual(HubRoute.answers(limit: 500, before: "c1").query, "limit=200&before=c1", "the limit is bounded")
        XCTAssertEqual(HubRoute.answers(limit: 50, before: nil).path, "/answers")
        XCTAssertEqual(HubRoute.forgetAnswer(id: "a 1").method, "DELETE")
        XCTAssertEqual(HubRoute.forgetAnswer(id: "a 1").path, "/answers/a%201")
    }
}
