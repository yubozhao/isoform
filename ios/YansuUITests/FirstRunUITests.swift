import XCTest

// First run (phone-setup v1.1.0): four screens on what Isoform does, then the pair screen. Nothing is asked before
// Scan the code.
final class FirstRunUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testFourScreensThenPairAndNothingAskedBefore() {
        let app = fixture()
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["welcome.page.0"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["tab.today"].exists)
        for index in 0..<4 {
            XCTAssertTrue(app.descendants(matching: .any)["welcome.page.\(index)"].firstMatch.waitForExistence(timeout: 5))
            attachScreenshot(app, named: "setup-9\(["a", "b", "c", "d"][index])")
            app.buttons["welcome.continue"].firstMatch.tap()
        }
        XCTAssertTrue(app.descendants(matching: .any)["welcome.title"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any)["welcome.download"].firstMatch.exists, "people without Isoform learn where to get it")
        XCTAssertFalse(app.alerts.firstMatch.exists, "no system prompt before Scan the code")
        attachScreenshot(app, named: "setup-9e-pair")
        app.buttons["welcome.pair"].tap()
        XCTAssertTrue(app.buttons["pairing.close"].waitForExistence(timeout: 10))
        attachScreenshot(app, named: "setup-9f-scan")
        app.buttons["pairing.close"].tap()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 10))
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 10), "first run shows once")
    }

    func testSkipGoesStraightToPairing() {
        let app = fixture()
        app.launchArguments += ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launch()
        let skip = app.buttons["welcome.skip"].firstMatch
        XCTAssertTrue(skip.waitForExistence(timeout: 10))
        skip.tap()
        XCTAssertTrue(app.descendants(matching: .any)["welcome.title"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertEqual(app.descendants(matching: .any)["welcome.title"].firstMatch.label, "与你的电脑配对")
    }

    private func fixture() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "first-run"
        app.launchEnvironment["YANSU_UITEST_SETTINGS_ID"] = UUID().uuidString
        return app
    }
}
