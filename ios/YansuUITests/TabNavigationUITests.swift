import XCTest

// From Chat, every tab opens and Chat opens again (TestFlight 2026-10-31: "chat works, but can't navigate to
// other screens"). Runs against the seeded live Mac, a cached-only Mac, and the first-read day.
final class TabNavigationUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testChatReachesEveryTabWhileTheMacAnswers() { checkTabs(state: "chat-live") }

    func testChatReachesEveryTabWithACachedChat() { checkTabs(state: "chat") }

    func testChatReachesEveryTabOnTheFirstDay() { checkTabs(state: "day-one") }

    /// With the keyboard up in Chat the tab bar stays, so every tab is one tap away, and Done puts the keyboard
    /// away. Run with the Simulator's hardware keyboard off, so the on-screen keyboard shows as on a phone.
    func testKeyboardInChatKeepsTheTabBarAndGoesAwayByDone() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "chat-live"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launch()
        let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 15))
        let keyboard = app.keyboards.firstMatch

        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "the composer opens the keyboard")
        XCTAssertTrue(app.buttons["tab.today"].isHittable, "the tab bar stays above the keyboard")
        attachScreenshot(app, named: "keyboard-up")
        app.buttons["tab.today"].tap()
        XCTAssertTrue(app.navigationBars["Today"].waitForExistence(timeout: 10), "Today opens with the keyboard up")

        app.buttons["tab.chat"].tap()
        composer.tap()
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5))
        app.buttons["composer.keyboard.done"].tap()
        XCTAssertFalse(keyboard.waitForExistence(timeout: 2), "Done puts the keyboard away")

        for (tab, title) in [("tab.today", "Today"), ("tab.chat", "Chat")] {
            let button = app.buttons[tab]
            XCTAssertTrue(button.waitForExistence(timeout: 5) && button.isHittable, "\(tab) can be tapped")
            button.tap()
            XCTAssertTrue(app.navigationBars[title].waitForExistence(timeout: 10)
                          || app.staticTexts[title].waitForExistence(timeout: 2), "\(tab) opens \(title)")
        }
        attachScreenshot(app, named: "keyboard-then-tabs")
    }

    private func checkTabs(state: String) {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launch()
        let chat = app.buttons["tab.chat"]
        XCTAssertTrue(chat.waitForExistence(timeout: 15), "the tab bar shows on Chat")
        attachScreenshot(app, named: "chat-\(state)")
        for (tab, title) in [("tab.today", "Today"), ("tab.chat", "Chat")] {
            let button = app.buttons[tab]
            XCTAssertTrue(button.waitForExistence(timeout: 5), "\(tab) is on screen")
            XCTAssertTrue(button.isHittable, "\(tab) can be tapped")
            button.tap()
            XCTAssertTrue(app.navigationBars[title].waitForExistence(timeout: 10)
                          || app.staticTexts[title].waitForExistence(timeout: 2), "\(tab) opens \(title)")
            XCTAssertTrue(button.isSelected, "\(tab) is the selected tab")
            attachScreenshot(app, named: "\(state)-\(tab)")
        }
        app.terminate()
    }
}
