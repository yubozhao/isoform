import XCTest
@testable import Yansu

/// Nov 14: the phone checked each of a brief's five cards with its own /status and ask-answer, and every one came
/// back 409 because the computer's brief had changed. One pass now stops at the first 409.
final class BriefAnsweredCardsTests: XCTestCase {
    private actor Asked {
        var ids: [String] = []
        func add(_ id: String) { ids.append(id) }
    }

    func testThePassCollectsAnsweredCards() async {
        let asked = Asked()
        let answered = await BriefAnsweredCards.collect([("a", "A?"), ("b", "B?"), ("c", "C?")]) { id, _ in
            await asked.add(id)
            return id != "b"
        }
        XCTAssertEqual(answered, ["a", "c"])
        let ids = await asked.ids
        XCTAssertEqual(ids, ["a", "b", "c"])
    }

    func testThePassStopsAtTheFirstChangedBrief() async {
        let asked = Asked()
        let answered = await BriefAnsweredCards.collect([("a", "A?"), ("b", "B?"), ("c", "C?"), ("d", "D?")]) { id, _ in
            await asked.add(id)
            if id == "b" { throw HubRequestError.conflict(message: "the brief changed") }
            return true
        }
        XCTAssertEqual(answered, ["a"])
        let ids = await asked.ids
        XCTAssertEqual(ids, ["a", "b"], "every card after a 409 names the same stale revision")
    }

    func testOtherFailuresSkipOnlyThatCard() async {
        let answered = await BriefAnsweredCards.collect([("a", "A?"), ("b", "B?"), ("c", "C?")]) { id, _ in
            if id == "a" { throw HubRequestError.noAnswer }
            return true
        }
        XCTAssertEqual(answered, ["b", "c"])
    }
}
