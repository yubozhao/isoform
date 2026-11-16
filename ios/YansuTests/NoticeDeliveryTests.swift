import Foundation
import UserNotifications
import XCTest
@testable import Yansu

// Opening a push's sealed notice, applying it to the content, and where the app routes it.
final class NoticeDeliveryTests: XCTestCase {
    func testDiagnosticRequiresVerifiedMarkerForTheCurrentPairing() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        XCTAssertTrue(SharedNoticeDedup.record(hub: "hub", device: "phone", id: "notice", directory: directory))
        XCTAssertFalse(SharedNoticeDedup.containsVerifiedTest(hub: "hub", device: "phone", id: "notice", directory: directory))
        XCTAssertTrue(SharedNoticeDedup.recordVerifiedTest(hub: "hub", device: "phone", id: "notice", directory: directory))
        XCTAssertTrue(SharedNoticeDedup.containsVerifiedTest(hub: "hub", device: "phone", id: "notice", directory: directory))
        XCTAssertFalse(SharedNoticeDedup.containsVerifiedTest(hub: "hub", device: "other", id: "notice", directory: directory))
    }
    func testSealedDestinationIsValidated() throws {
        let raw = #"{"v":1,"id":"n1","kind":"work_finished","title":"Done","category":"YANSU_MOMENT","destinationKind":"task","destinationId":"task-42"}"#
        let notice = try JSONDecoder().decode(NoticePlaintext.self, from: Data(raw.utf8))
        XCTAssertEqual(notice.destinationKind, "task")
        XCTAssertEqual(notice.destinationID, "task-42")
        let unsafe = raw.replacingOccurrences(of: "task-42", with: "bad/id")
        let rejected = try JSONDecoder().decode(NoticePlaintext.self, from: Data(unsafe.utf8))
        XCTAssertNil(rejected.destinationID)
    }
    private let hub = "hub-delivery"
    private let dev = "dev-delivery"
    private let noticeID = "notice-delivery-1"
    private let key = Data(repeating: 0x33, count: 32)

    private func store(hub: String? = nil, dev: String? = nil, key: Data? = nil) throws -> InMemorySecretStore {
        let secrets = InMemorySecretStore()
        try SharedNoticeStore.save(key: key ?? self.key,
                                   context: NoticeRelayContext(origin: "https://relay.example", hubID: hub ?? self.hub,
                                                               deviceID: dev ?? self.dev),
                                   secrets: secrets)
        try secrets.save("bearer-value", account: PairingSecretAccount.deviceBearer)
        return secrets
    }

    private func sealedPush(kind: String = "work_blocked", category: String = "YANSU_MOMENT", ct: String? = nil,
                            id: String? = nil) throws -> NoticePush {
        let plaintext = #"{"v":1,"id":"\#(noticeID)","kind":"\#(kind)","title":"Q3 outline is waiting on you.","body":"It needs to be picked up again.","category":"\#(category)","actions":[{"id":"open","label":"Open","auth":false}],"subjectId":"hf_42","createdAt":"2026-10-10T09:00:00Z"}"#
        let sealed = try ct ?? NoticeCrypto.seal(Data(plaintext.utf8), key: key, hub: hub, dev: dev, noticeID: noticeID,
                                                  nonce: RelayCrypto.randomBytes(12))
        return NoticePush(kind: kind, id: id ?? noticeID, hub: hub, dev: dev, ct: sealed)
    }

    func testOpenReturnsThePlaintextWhenEveryCheckPasses() throws {
        let opened = try XCTUnwrap(NoticeDelivery.open(try sealedPush(), secrets: try store()))
        XCTAssertEqual(opened.plaintext.subjectID, "hf_42")
        XCTAssertTrue(opened.json.contains("\"title\":\"Q3 outline is waiting on you.\""))
    }

    func testANoticeInTheRetiredDecisionCategoryKeepsTheFallback() throws {
        let push = try sealedPush(kind: "decision_due", category: "YANSU_DECISION")
        XCTAssertNil(NoticeDelivery.open(push, secrets: try store()))
    }

    func testOpenFallsBackWhenTheStoredPairingIsAnotherOne() throws {
        let push = try sealedPush()
        XCTAssertNil(NoticeDelivery.open(push, secrets: try store(hub: "hub-other")))
        XCTAssertNil(NoticeDelivery.open(push, secrets: try store(dev: "dev-other")))
    }

    func testOpenFallsBackWithoutAKeyOrCiphertext() throws {
        XCTAssertNil(NoticeDelivery.open(try sealedPush(), secrets: InMemorySecretStore()))
        let unsealed = NoticePush(kind: "work_blocked", id: noticeID, hub: hub, dev: dev, ct: nil)
        XCTAssertNil(NoticeDelivery.open(unsealed, secrets: try store()))
    }

    func testOpenFallsBackWhenTheKeyIsWrong() throws {
        let push = try sealedPush()
        XCTAssertNil(NoticeDelivery.open(push, secrets: try store(key: Data(repeating: 0x44, count: 32))))
    }

    func testOpenFallsBackWhenTheKindDoesNotMatchThePush() throws {
        let push = try sealedPush(kind: "work_blocked")
        let mismatched = NoticePush(kind: "work_finished", id: push.id, hub: push.hub, dev: push.dev, ct: push.ct)
        XCTAssertNil(NoticeDelivery.open(mismatched, secrets: try store()))
    }

    func testApplySetsTheSealedTextAndAttachesTheJSONForTheApp() throws {
        let opened = try XCTUnwrap(NoticeDelivery.open(try sealedPush(), secrets: try store()))
        let content = UNMutableNotificationContent()
        content.title = "Isoform"
        content.body = "Work is waiting on you."
        content.threadIdentifier = hub
        content.userInfo = ["yansu": ["kind": "work_blocked", "id": noticeID]]
        NoticeDelivery.apply(opened, to: content)
        XCTAssertEqual(content.title, "Q3 outline is waiting on you.")
        XCTAssertEqual(content.body, "It needs to be picked up again.")
        XCTAssertEqual(content.categoryIdentifier, NoticeCategory.moment.rawValue)
        XCTAssertEqual(content.threadIdentifier, "isoform.notice.\(opened.plaintext.kind)",
                       "notices of one kind stack in one Notification Center group (P145)")
        XCTAssertNotEqual(content.threadIdentifier, hub)
        let attached = NoticePush.plaintext(in: content.userInfo)
        XCTAssertEqual(attached?.subjectID, "hf_42")
        XCTAssertEqual(attached?.id, noticeID)
    }

    func testPushParsingRecognisesNoticesAndIgnoresLegacyPushes() {
        let notice: [AnyHashable: Any] = ["aps": ["alert": ["title": "Isoform"]],
                                          "yansu": ["kind": "meeting_ended", "id": "n-7", "hub": "h", "dev": "d", "ct": "AAAA"]]
        let parsed = NoticePush.parse(notice)
        XCTAssertEqual(parsed, NoticePush(kind: "meeting_ended", id: "n-7", hub: "h", dev: "d", ct: "AAAA"))
        XCTAssertNil(NoticePush.parse(["yansu": ["kind": "needs_you"]]))
        XCTAssertNil(NoticePush.parse(["yansu": ["kind": "brief", "eid": "7"]]))
        XCTAssertNil(NoticePush.parse(["yansu": ["kind": "meeting_ended", "id": "bad/id"]]))
        XCTAssertNil(NoticePush.parse(["aps": [:]]))
    }

    func testRoutingFollowsTheKindTable() {
        XCTAssertEqual(NoticeRouting.route(kind: "work_blocked"), .chat(momentID: nil))
        XCTAssertEqual(NoticeRouting.route(kind: "work_finished"), .chat(momentID: nil))
        XCTAssertEqual(NoticeRouting.route(kind: "meeting_prep", momentID: "moment-123"),
                       .chat(momentID: "moment-123"))
        XCTAssertEqual(NoticeRouting.route(kind: "meeting_prep", momentID: "bad/id"), .brief)
        XCTAssertEqual(NoticeRouting.route(kind: "weekly_recap", momentID: "moment-123"), .brief)
        for kind in ["meeting_ended", "brief_ready", "commitment_slipping", "person_reappeared", "goal_check_in",
                     "learned_to_confirm", "capture_paused", "decision_due", "something_new"] {
            XCTAssertEqual(NoticeRouting.route(kind: kind), .brief, kind)
        }
    }

    func testReceiptStatesAndCategoriesUseTheProtocolSpelling() {
        XCTAssertEqual(NoticeReceiptState.allCases.map(\.rawValue), ["delivered", "opened", "acted", "dismissed"])
        XCTAssertEqual(NoticeCategory.allCases.map(\.rawValue), ["YANSU_MOMENT", "YANSU_BRIEF", "YANSU_PREP", "YANSU_RESULT"])
    }

    func testNoticeIdentifiersMustBeSafeInAPath() {
        XCTAssertTrue(NoticeIdentifier.isSafe("notice-7f3a"))
        XCTAssertFalse(NoticeIdentifier.isSafe(""))
        XCTAssertFalse(NoticeIdentifier.isSafe("a/b"))
        XCTAssertFalse(NoticeIdentifier.isSafe("a?b"))
        XCTAssertFalse(NoticeIdentifier.isSafe("a%20b"))
        XCTAssertFalse(NoticeIdentifier.isSafe("a b"))
        XCTAssertFalse(NoticeIdentifier.isSafe(String(repeating: "a", count: 129)))
    }

    func testPlaintextCarriesAnOptionalSafeMomentID() throws {
        let base = #"""
        {"v":1,"id":"notice-1","kind":"meeting_prep","title":"Prep","category":"YANSU_MOMENT",
         "actions":[],"createdAt":"2026-10-16T10:00:00Z"}
        """#
        let withMoment = base.replacingOccurrences(of: "\"actions\":[]",
                                                    with: "\"actions\":[],\"momentId\":\"moment-123\"")
        XCTAssertEqual(try JSONDecoder().decode(NoticePlaintext.self, from: Data(withMoment.utf8)).momentID,
                       "moment-123")
        XCTAssertNil(try JSONDecoder().decode(NoticePlaintext.self, from: Data(base.utf8)).momentID)
        let unsafe = withMoment.replacingOccurrences(of: "moment-123", with: "bad/id")
        XCTAssertNil(try JSONDecoder().decode(NoticePlaintext.self, from: Data(unsafe.utf8)).momentID)
    }
}
