import XCTest
@testable import Yansu

// The brief in full (phone-brief v1.0.0): its items sit where it mentions them, and Past briefs says where you are.
final class BriefPageTests: XCTestCase {
    private func brief(_ paragraphs: [String]) -> MobileBriefV1 {
        MobileBriefV1(briefId: "b1", slot: "evening", localDate: "2026-11-15", asOf: "2026-11-15T18:00:00Z", kind: "evening",
                      narrativeStatus: "ok", narrative: paragraphs.map { BriefParagraph(text: $0) })
    }

    private func item(_ id: String, brief: String? = "b1", paragraph: Int? = nil, state: String = "open") -> WorkItem {
        WorkItem(id: id, kind: "done", state: state, source: "brief", ref: WorkRef(briefId: brief, paragraph: paragraph),
                 first: [WorkFirstPart(text: "from the brief")], noticed: "n", work: WorkBody(title: id),
                 createdAt: "2026-11-15T18:00:00Z")
    }

    func testItemsSitUnderTheirParagraphOrAfterTheLastAndOnlyThisBriefs() {
        let value = brief(["One.", "Two.", "Three."])
        let placed = BriefItemPlacement.place([item("a", paragraph: 1), item("b"), item("c", brief: "b2"),
                                               item("brief:b1:p0"), item("d", paragraph: 9)], brief: value)
        XCTAssertEqual(placed[1]?.map(\.id), ["a"])
        XCTAssertEqual(placed[2]?.map(\.id), ["b", "d"], "no paragraph, or one that isn't there: after the last")
        XCTAssertNil(placed[0], "a sentence answered Not right is not an item")
    }

    func testNotRightOnASentenceIsANoOnItsOwnId() {
        let sentence = BriefItemPlacement.sentence(brief: "b1", paragraph: 2, text: "Two tests still fail.")
        XCTAssertEqual(sentence.id, "brief:b1:p2")
        XCTAssertEqual(sentence.kind, "claim")
        XCTAssertEqual(WorkReason.choices(for: sentence), [.wasTrueOnce, .onlySometimes, .notAtAll])
        XCTAssertTrue(BriefItemPlacement.isSentence("brief:b1:p2", brief: "b1"))
        XCTAssertFalse(BriefItemPlacement.isSentence("brief:b1:prep", brief: "b1"))
    }

    func testPastBriefLineSaysWhereYouAreWithIt() {
        let part = BriefPlaybackMemory(seconds: 40, duration: 120, played: false)
        let played = BriefPlaybackMemory(seconds: 120, duration: 120, played: true)
        XCTAssertEqual(PastBriefStatus(memory: part, open: 3, total: 4).line, "Part heard · 3 to answer")
        XCTAssertEqual(PastBriefStatus(memory: played, open: 0, total: 2).line, "Played · all answered")
        XCTAssertEqual(PastBriefStatus(memory: nil, open: 2, total: 2).line, "Not opened")
        XCTAssertEqual(PastBriefStatus(memory: played, open: 1, total: 3).line, "Played · 1 left unanswered")
        XCTAssertEqual(PastBriefStatus(memory: played, open: nil, total: nil).line, "Played")
        XCTAssertTrue(PastBriefStatus(memory: played, open: nil, total: nil).playedThrough, "an open ring once played")
        XCTAssertFalse(PastBriefStatus(memory: part, open: nil, total: nil).playedThrough, "a dot until then")
    }
}
