import UserNotifications
import XCTest
@testable import Yansu

// The categories the app registers at launch: identifiers, button order and the options that matter for safety.
final class NotificationCategoryTests: XCTestCase {
    private func category(_ identifier: String) throws -> UNNotificationCategory {
        try XCTUnwrap(NotificationCategories.all.first { $0.identifier == identifier },
                      "missing category \(identifier)")
    }

    private func action(_ category: UNNotificationCategory, _ identifier: String) throws -> UNNotificationAction {
        try XCTUnwrap(category.actions.first { $0.identifier == identifier }, "missing action \(identifier)")
    }

    func testTheCategoriesAreRegistered() {
        // The brief-ready notice (LocalBriefNotices) has its own category with Read it and Play; prep and finished
        // work can be answered from the notification (PROTOCOL.md 9.2).
        XCTAssertEqual(Set(NotificationCategories.all.map(\.identifier)),
                       ["YANSU_MOMENT", "YANSU_BRIEF", "YANSU_PREP", "YANSU_RESULT", NotificationCategories.localBriefCategory])
    }

    func testTheRetiredDecisionCategoryAndItsButtonsAreGone() {
        XCTAssertFalse(NotificationCategories.all.contains { $0.identifier == "YANSU_DECISION" })
        let actions = NotificationCategories.all.flatMap(\.actions).map(\.identifier)
        XCTAssertFalse(actions.contains("approve"))
        XCTAssertFalse(actions.contains("decline"))
        XCTAssertFalse(actions.contains("snooze"))
    }

    func testMomentHasOpenAndNotNow() throws {
        let moment = try category("YANSU_MOMENT")
        XCTAssertEqual(moment.actions.map(\.identifier), ["open", "later"])
        XCTAssertEqual(moment.actions.map(\.title), ["Open", "Not now"])
        XCTAssertTrue(try action(moment, "open").options.contains(.foreground))
        XCTAssertFalse(try action(moment, "later").options.contains(.foreground))
        XCTAssertFalse(try action(moment, "later").options.contains(.authenticationRequired))
    }

    func testBriefHasReadAndPlay() throws {
        let brief = try category("YANSU_BRIEF")
        XCTAssertEqual(brief.actions.map(\.identifier), ["read", "play"])
        XCTAssertTrue(try action(brief, "read").options.contains(.foreground))
        XCTAssertTrue(try action(brief, "play").options.contains(.foreground))
    }

    func testEveryCategoryReportsDismissalsToTheApp() {
        for category in NotificationCategories.all {
            XCTAssertTrue(category.options.contains(.customDismissAction), category.identifier)
        }
    }

    func testLaterIdentifiersAreStableForANotice() {
        XCTAssertEqual(NotificationCategories.Action.later, "later")
        XCTAssertEqual(NotificationCategories.Action.open, "open")
        XCTAssertEqual(LocalNotices.snoozeInterval, 3600)
    }

    func testFailedReminderDoesNotRecordAnActedReceipt() async {
        enum ScheduleError: Error { case rejected }
        var receipts = 0
        do {
            try await LocalNotices.recordLater(schedule: { throw ScheduleError.rejected }, receipt: { receipts += 1 })
            XCTFail("A rejected reminder must fail")
        } catch { }
        XCTAssertEqual(receipts, 0)
        try? await LocalNotices.recordLater(schedule: {}, receipt: { receipts += 1 })
        XCTAssertEqual(receipts, 1)
    }
}
