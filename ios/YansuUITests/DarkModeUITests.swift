import XCTest

// The phone pass in dark mode (phone-theme v1.0.0): every page by Theme.swift, text on the accent in onSolid.
final class DarkModeUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testTodayAndYourWorkInDark() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = "dark"
        app.launch()
        XCTAssertTrue(app.otherElements["today.yourWork"].firstMatch.waitForExistence(timeout: 15))
        attachScreenshot(app, named: "dark-1-today")
        app.swipeUp()
        attachScreenshot(app, named: "dark-2-your-work")
        app.buttons["work.no"].firstMatch.tap()
        attachScreenshot(app, named: "dark-3-reasons")
    }
}
