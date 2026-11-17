import XCTest

// Connect an agent from the phone, with the code checked here (phone-agent v1.2.0 §4 and rule 14).
final class AgentJoinUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testConnectAnAgentChecksTheCodeAndOnlyYesConnects() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launch()
        openSettings(app)
        let agents = app.buttons["settings.yourAgents.open"].firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 10))
        agents.tap()
        XCTAssertTrue(app.buttons["agents.connectAgain.cursor"].firstMatch.waitForExistence(timeout: 10),
                      "a disconnected agent offers Connect again")
        let connect = app.buttons["agents.connect"].firstMatch
        for _ in 0..<4 where !connect.isHittable { app.swipeUp() }
        attachScreenshot(app, named: "join-1-your-agents")
        connect.tap()
        XCTAssertTrue(app.staticTexts["join.message"].firstMatch.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "join-2-give-this")
        let code = app.staticTexts["join.code"].firstMatch
        XCTAssertTrue(code.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["join.answer.yes"].firstMatch.label.contains("4817"))
        attachScreenshot(app, named: "join-3-check-the-code")
        app.buttons["join.answer.yes"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["join.allowed"].firstMatch.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "join-4-connected")
    }
}
