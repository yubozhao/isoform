import XCTest

// Settings › Notifications on the phone (phone-rings v1.0.0, frame 17c): where it rings, three kinds, quiet hours.
final class RingsUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testNotificationsSaysWhereItRingsAndOffersThreeKinds() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
        app.launch()
        openSettings(app)
        let row = app.buttons["settings.notifications.open"].firstMatch
        for _ in 0..<6 where !(row.exists && row.isHittable) { app.swipeUp() }
        row.tap()
        XCTAssertTrue(app.staticTexts["notifications.where"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.switches["notifications.kind.brief_ready"].firstMatch.exists)
        XCTAssertTrue(app.switches["notifications.kind.meeting_prep"].firstMatch.exists)
        XCTAssertTrue(app.switches["notifications.kind.work_finished"].firstMatch.exists)
        XCTAssertFalse(app.switches["notifications.kind.work_blocked"].exists, "three kinds, and nothing else")
        attachScreenshot(app, named: "rings-1-notifications")
        app.swipeUp()
        attachScreenshot(app, named: "rings-2-quiet-hours")
    }
}
