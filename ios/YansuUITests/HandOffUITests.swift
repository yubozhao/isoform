import XCTest

// Yes on offered work hands it to your agent (phone-agent v1.0.0 and v1.1.0).
final class HandOffUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testYesOnOfferedWorkAsksWhoDoesYourWorkOnceThenShowsExactlyWhatGoes() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launch()
        XCTAssertTrue(app.otherElements["today.yourWork"].firstMatch.waitForExistence(timeout: 15))
        app.swipeUp()
        let offered = app.buttons["work.upNext.work-test-plan"].firstMatch
        XCTAssertTrue(offered.waitForExistence(timeout: 5))
        offered.tap()
        app.swipeDown()
        let yes = app.buttons["work.yes"].firstMatch
        XCTAssertTrue(yes.waitForExistence(timeout: 5))
        yes.tap()
        XCTAssertTrue(app.staticTexts["handoff.pick.title"].firstMatch.waitForExistence(timeout: 10), "asked once")
        attachScreenshot(app, named: "handoff-1-who-does-your-work")
        app.buttons["handoff.agent.claude"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["handoff.title"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["handoff.text"].firstMatch.exists, "exactly what goes")
        // Claude runs in its own app: the button says it opens there (phone-agent v1.2.0 rule 6).
        XCTAssertEqual(app.buttons["handoff.send"].firstMatch.label, "Open in Claude")
        XCTAssertTrue(app.staticTexts["handoff.foot"].firstMatch.exists)
        attachScreenshot(app, named: "handoff-2-hand-it-to")
    }
}
