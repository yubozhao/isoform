import XCTest

// A lost pairing never blanks Today (phone-setup rule 11).
final class SetupUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testALostPairingKeepsTodayWithOneLine() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "unreadable"
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["today.pairingLost"].firstMatch.waitForExistence(timeout: 15))
        attachScreenshot(app, named: "setup-9h-pairing-lost")
    }
}
