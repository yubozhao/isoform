import XCTest
@testable import Yansu

// B9 on the phone (PROTOCOL.md 9.1 and 9.2): one brief notification, and Not needed / Useful answered from the
// notification through POST /work/{id}/answer.
final class NoticeWorkAnswerTests: XCTestCase {
    private func plaintext(kind: String, workItemId: String?, actions: [String]) throws -> NoticePlaintext {
        var object: [String: Any] = ["v": 1, "id": "n1", "kind": kind, "title": "t", "category": "YANSU_MOMENT",
                                     "actions": actions.map { ["id": $0, "label": $0, "auth": false] },
                                     "createdAt": "2026-11-15T20:00:00Z"]
        if let workItemId { object["workItemId"] = workItemId }
        return try JSONDecoder().decode(NoticePlaintext.self, from: JSONSerialization.data(withJSONObject: object))
    }

    func testPrepAndFinishedWorkGetTheirAnswerCategoryOnlyWithAWorkItem() throws {
        XCTAssertEqual(NoticeWorkAnswer.category(for: try plaintext(kind: "meeting_prep", workItemId: "prep:e1",
                                                                    actions: ["open", "not_needed"])), .prep)
        XCTAssertEqual(NoticeWorkAnswer.category(for: try plaintext(kind: "work_finished", workItemId: "result:h1",
                                                                    actions: ["open", "useful"])), .result)
        XCTAssertNil(NoticeWorkAnswer.category(for: try plaintext(kind: "meeting_prep", workItemId: nil,
                                                                  actions: ["not_needed"])), "an older computer keeps YANSU_MOMENT")
        XCTAssertNil(NoticeWorkAnswer.category(for: try plaintext(kind: "work_finished", workItemId: "result:h1",
                                                                  actions: ["open"])))
        XCTAssertNil(try plaintext(kind: "meeting_prep", workItemId: "a/b", actions: []).workItemID, "never a path")
    }

    func testTheAnswerBodyIsTheContractsAndDecodesAsAPhoneAnswer() throws {
        let notNeeded = NoticeWorkAnswer.notNeeded.body(noticeID: "n1", answeredAt: "2026-11-15T20:00:00Z")
        XCTAssertEqual(notNeeded, ["idempotencyKey": "notice:n1:not_needed", "action": "no", "reason": "not_needed",
                                   "surface": "notification", "answeredAt": "2026-11-15T20:00:00Z"])
        let useful = NoticeWorkAnswer.useful.body(noticeID: "n1", answeredAt: "2026-11-15T20:00:00Z")
        let request = try JSONDecoder().decode(WorkAnswerRequest.self, from: JSONSerialization.data(withJSONObject: useful))
        XCTAssertEqual(request.action, "useful")
        XCTAssertEqual(request.surface, "notification")
        XCTAssertEqual(request.idempotencyKey, "notice:n1:useful", "a second tap is the same answer")
    }

    func testTheLocalBriefNoticeYieldsOnceTheComputersCameForThatBrief() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("dedup-\(UUID().uuidString)")
        let pairing = TodayPairingIdentity(hubID: "hub", deviceID: "dev")
        XCTAssertFalse(LocalBriefNotices.remoteNoticeCame(for: "b1", pairing: pairing, directory: directory))
        SharedNoticeDedup.recordRemoteBrief(hub: "hub", device: "dev", briefID: "b1", directory: directory)
        XCTAssertTrue(LocalBriefNotices.remoteNoticeCame(for: "b1", pairing: pairing, directory: directory))
        XCTAssertFalse(LocalBriefNotices.remoteNoticeCame(for: "b2", pairing: pairing, directory: directory),
                       "another brief still gets its local notice")
        XCTAssertFalse(LocalBriefNotices.remoteNoticeCame(for: "b1", pairing: TodayPairingIdentity(hubID: "hub", deviceID: "other"),
                                                          directory: directory), "scoped to the pairing")
    }

    func testThePrepAndResultCategoriesAreRegisteredWithTheirButtons() {
        let categories = Dictionary(uniqueKeysWithValues: NotificationCategories.all.map { ($0.identifier, $0) })
        XCTAssertEqual(categories["YANSU_PREP"]?.actions.map(\.identifier), ["read", "not_needed"])
        XCTAssertEqual(categories["YANSU_RESULT"]?.actions.map(\.identifier), ["read", "useful"])
        XCTAssertEqual(categories["YANSU_PREP"]?.actions.last?.options.contains(.foreground), false, "answered without opening the app")
    }
}
