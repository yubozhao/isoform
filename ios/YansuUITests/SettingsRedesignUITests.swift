import XCTest

// Settings ranked as phone-settings v1.0.0: Your computer first, then Closed Eyes, Your data, Your agents.
final class SettingsRedesignUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testSettingsRanksTheComputerFirstAndHasNoWorkOnItOrPhoneCard() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launch()
        openSettings(app)
        XCTAssertTrue(app.descendants(matching: .any)["settings.group.mac"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["settings.computerState"].firstMatch.exists, "the computer's state in words")
        XCTAssertTrue(app.buttons["settings.yourAgents.open"].firstMatch.exists)
        XCTAssertFalse(app.switches["settings.workOnIt"].exists, "the Work on it switch goes")
        XCTAssertFalse(app.descendants(matching: .any)["settings.you"].exists, "the phone card goes")
        attachScreenshot(app, named: "settings-1-top")
        app.swipeUp()
        attachScreenshot(app, named: "settings-2-this-phone")
        app.swipeDown()
        app.buttons["settings.yourAgents.open"].firstMatch.tap()
        // The two jobs on top, then the other agents (phone-agent v1.2.0 rule 13).
        XCTAssertTrue(app.descendants(matching: .any)["agents.job.code"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["agents.job.else"].firstMatch.exists)
        XCTAssertTrue(app.descendants(matching: .any)["agents.row.claude"].firstMatch.exists)
        attachScreenshot(app, named: "settings-3-your-agents")
    }
}
