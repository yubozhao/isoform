import XCTest
@testable import Yansu

/// P163: what the first read shows after Not quite and Send, decided without the simulator.
final class FirstReadCorrectionTests: XCTestCase {
    private struct Failure: Error {}

    func testTheWordsKeepEveryLineAndTrimOnlyTheOuterSpace() {
        XCTAssertEqual(FirstReadCorrectionRules.words(from: "  I work best later.\nAnd nights.  "),
                       "I work best later.\nAnd nights.")
        XCTAssertEqual(FirstReadCorrectionRules.words(from: "   \n "), "")
    }

    func testTheComputerAnswerEndsInAnsweredWithTheWords() {
        XCTAssertEqual(FirstReadCorrectionRules.result(JournaledOutcome.sent(())), .answered)
        XCTAssertEqual(FirstReadCorrectionRules.phase(after: .answered, words: "Later."), .answered(words: "Later."))
    }

    func testAHeldSendKeepsTheWordsWaitingForTheComputer() {
        // A write the phone kept for a retry (no answer, server busy) waits; it is not a failure.
        XCTAssertEqual(FirstReadCorrectionRules.result(JournaledOutcome<Void>.failed(Failure(), .keep)), .held)
        XCTAssertEqual(FirstReadCorrectionRules.phase(after: .held, words: "Later."), .waiting(words: "Later."))
    }

    func testARefusedOrUnsavedSendReturnsTheWordsToTheField() {
        XCTAssertEqual(FirstReadCorrectionRules.result(JournaledOutcome<Void>.failed(Failure(), .drop)), .failed)
        XCTAssertEqual(FirstReadCorrectionRules.result(JournaledOutcome<Void>.failed(Failure(), .changed)), .failed)
        XCTAssertEqual(FirstReadCorrectionRules.result(JournaledOutcome<Void>.notSaved), .failed)
        XCTAssertEqual(FirstReadCorrectionRules.phase(after: .failed, words: "Later."), .failed(words: "Later."))
    }

    func testACancelledSendChangesNothing() {
        XCTAssertNil(FirstReadCorrectionRules.result(JournaledOutcome<Void>.cancelled))
    }

    func testUndoStaysFiveSecondsAfterTheComputerAnswers() {
        let answered = Date(timeIntervalSince1970: 1_000)
        XCTAssertTrue(FirstReadCorrectionRules.undoOpen(answeredAt: answered, now: answered.addingTimeInterval(4.9)))
        XCTAssertFalse(FirstReadCorrectionRules.undoOpen(answeredAt: answered, now: answered.addingTimeInterval(5)))
        XCTAssertFalse(FirstReadCorrectionRules.undoOpen(answeredAt: nil, now: answered))
    }

    func testAWaitThatPassesADayExpires() {
        let sent = Date(timeIntervalSince1970: 1_000)
        XCTAssertFalse(FirstReadCorrectionRules.expired(sentAt: sent, now: sent.addingTimeInterval(60)))
        XCTAssertTrue(FirstReadCorrectionRules.expired(sentAt: sent, now: sent.addingTimeInterval(TaskSendWait.relayRetention)))
    }

    func testOnlyTheAnswerStatesShowTheWordsInPlaceOfTheButtons() {
        XCTAssertFalse(FirstReadCorrectionPhase.none.showsAnswer)
        XCTAssertFalse(FirstReadCorrectionPhase.failed(words: "Later.").showsAnswer)
        XCTAssertTrue(FirstReadCorrectionPhase.waiting(words: "Later.").showsAnswer)
        XCTAssertTrue(FirstReadCorrectionPhase.answered(words: nil).showsAnswer)
        XCTAssertTrue(FirstReadCorrectionPhase.expired(words: "Later.").showsAnswer)
    }

    private func held(_ moment: String, choice: String = "correct_portrait", words: String? = "I work later.",
                      createdAt: Date) -> PendingWrite {
        PendingWrite(key: UUID().uuidString, operation: .chatProactiveAction, target: "first-read/\(moment)",
                     choice: choice, fingerprint: UUID().uuidString, createdAt: createdAt, hubID: "hub-1",
                     correctionWords: words)
    }

    /// P163: a held correction keeps its words in the journal, so a relaunched card still says what was sent.
    func testAHeldCorrectionComesBackWithItsWordsAfterARelaunch() async throws {
        let now = Date(timeIntervalSince1970: 2_000_000)
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("first-read-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: url) }
        let before = PendingWriteJournal(fileURL: url, hubID: { "hub-1" })
        _ = try await before.reserve(operation: .chatProactiveAction, target: "first-read/m1", choice: "correct_portrait",
                               fingerprint: "f1", makeKey: { "k1" }, createdAt: now, correctionWords: "I work best later.")
        let after = PendingWriteJournal(fileURL: url, hubID: { "hub-1" })
        let restored = FirstReadCorrectionRules.restoredWaiting(from: await after.entries(), target: "first-read/m1",
                                                                now: now.addingTimeInterval(60))
        XCTAssertEqual(restored?.phase, .waiting(words: "I work best later."))
        XCTAssertEqual(restored?.sentAt, now)
    }

    func testAHeldCorrectionPastADayComesBackAsExpiredWithItsWords() {
        let sent = Date(timeIntervalSince1970: 2_000_000)
        let restored = FirstReadCorrectionRules.restoredWaiting(
            from: [held("m1", createdAt: sent)], target: "first-read/m1",
            now: sent.addingTimeInterval(TaskSendWait.relayRetention))
        XCTAssertEqual(restored?.phase, .expired(words: "I work later."))
    }

    func testOnlyACorrectionWithWordsForThisCardIsRestored() {
        let now = Date(timeIntervalSince1970: 2_000_000)
        let entries = [held("m1", choice: "confirm_portrait", createdAt: now),
                       held("m2", createdAt: now),
                       held("m1", words: nil, createdAt: now)]
        XCTAssertNil(FirstReadCorrectionRules.restoredWaiting(from: entries, target: "first-read/m1", now: now))
    }

    /// P163 v1.0.1: a blank Not quite is a real answer, so a held blank comes back as a blank waiting card.
    func testAHeldBlankAnswerComesBackAsABlankWaitingCard() {
        let sent = Date(timeIntervalSince1970: 2_000_000)
        let restored = FirstReadCorrectionRules.restoredWaiting(
            from: [held("m1", words: "", createdAt: sent)], target: "first-read/m1", now: sent)
        XCTAssertEqual(restored?.phase, .waiting(words: ""))
    }
}
