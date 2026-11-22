import XCTest
@testable import Yansu

// What Isoform sees says what the AI reads (phone-duo v1.1.0, data-leaves v1.1.0 rule 6): nothing a model reads
// "stays on" or "never leaves" the computer, the assistant's row is in computer-data's words, the tap line has its
// one exception only while it is so, and the foot names the computer's section.
final class IsoformSeesCopyTests: XCTestCase {
    func testThePageSaysWhatTheAIReads() {
        XCTAssertEqual(String(localized: "sees.screen.on"),
                       "Sees the app and window you're in. It keeps what it sees on your computer, and your AI reads it to write your briefs.")
        XCTAssertEqual(String(localized: "sees.leaves.kept"),
                       "Screenshots and audio are kept on your computer; your AI reads what's in them to write your briefs and answers.")
        XCTAssertEqual(IsoformSeesCopy.aiLine("claude_code"), "What each brief or answer is written from, to your AI, Claude Code.")
        XCTAssertEqual(IsoformSeesCopy.aiLine("codex"), "What each brief or answer is written from, to your AI, Codex.")
        XCTAssertEqual(IsoformSeesCopy.aiLine(nil), "What each brief or answer is written from, to the AI Isoform uses.")
        XCTAssertEqual(String(localized: "sees.foot"), "Change these on your computer, in Settings › What Isoform sees.")
        XCTAssertEqual(String(localized: "data.seen"), "What Isoform sees")
        XCTAssertEqual(String(localized: "data.seenSub"), "Each source on your computer, on or off")
    }

    func testNoLineAboutWhatTheAIReadsSaysItStaysOrNeverLeaves() {
        let lines = [String(localized: "sees.screen.on"), String(localized: "sees.leaves.kept"),
                     IsoformSeesCopy.aiLine("claude_code"), IsoformSeesCopy.aiLine("codex"), IsoformSeesCopy.aiLine(nil),
                     IsoformSeesCopy.tapLine(agentOfYouReviewOff: true), String(localized: "sees.foot")]
        for line in lines {
            let lower = line.lowercased()
            XCTAssertFalse(lower.contains("stay"), line)
            XCTAssertFalse(lower.contains("never leave"), line)
        }
        // The old key and its words are gone.
        XCTAssertEqual(String(localized: "sees.leaves.never"), "sees.leaves.never")
    }

    func testTheTapLineNamesItsOneExceptionOnlyWhileItIsSo() {
        let line = "Isoform doesn't send, delete or accept anything until you tap."
        XCTAssertEqual(IsoformSeesCopy.tapLine(agentOfYouReviewOff: nil), line)
        XCTAssertEqual(IsoformSeesCopy.tapLine(agentOfYouReviewOff: false), line)
        XCTAssertEqual(IsoformSeesCopy.tapLine(agentOfYouReviewOff: true),
                       line + " The one exception is Agent of You with its review off.")
    }

    func testTheComputerSaysWhenAgentOfYouActsWithoutReview() throws {
        let older = try JSONDecoder().decode(MobileSees.self, from: Data(#"{"sources":[{"job":"mail","on":true}]}"#.utf8))
        XCTAssertNil(older.agentOfYouReviewOff)
        let off = try JSONDecoder().decode(MobileSees.self,
                                           from: Data(#"{"sources":[],"agentOfYouReviewOff":true}"#.utf8))
        XCTAssertEqual(off.agentOfYouReviewOff, true)
    }
}
