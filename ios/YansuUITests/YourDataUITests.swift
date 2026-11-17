import XCTest

// Your data, by where the data is (phone-settings v1.0.0 rule 12).
final class YourDataUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testYourDataShowsThreePlacesAndYourAnswers() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launch()
        openSettings(app)
        let row = app.buttons["settings.yourData.open"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()
        XCTAssertTrue(app.buttons["data.answers"].firstMatch.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "data-1-your-data")
        app.buttons["data.answers"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["Write the test plan"].firstMatch.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "data-2-your-answers")
    }

    /// Sent to your agents lists each hand-off with exactly what went (PROTOCOL.md 27.2).
    func testSentToYourAgentsShowsExactlyWhatWent() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launch()
        openSettings(app)
        let row = app.buttons["settings.yourData.open"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()
        let sent = app.buttons["data.sent"].firstMatch
        XCTAssertTrue(sent.waitForExistence(timeout: 10))
        for _ in 0..<4 where !sent.isHittable { app.swipeUp() }
        sent.tap()
        XCTAssertTrue(app.descendants(matching: .any)["data.sent.h1"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["data.sent.h2"].firstMatch.exists)
        attachScreenshot(app, named: "data-3-sent-to-your-agents")
    }
}
