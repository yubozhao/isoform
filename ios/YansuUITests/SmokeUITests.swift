import XCTest

final class SmokeUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testLaunchShowsThreeTabsAndSettingsPairingEntry() throws {
        let app = launch(state: "unpaired")
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["tab.chat"].exists)
        XCTAssertFalse(app.buttons["tab.tasks"].exists, "the Tasks tab goes (phone-today rule 19)")
        XCTAssertTrue(app.buttons["tab.today"].isSelected)
        XCTAssertTrue(app.buttons["root.settings"].exists)

        openSettings(app)
        XCTAssertTrue(app.buttons["settings.pair"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["settings.group.you"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["settings.group.mac"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["settings.group.notifications"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["settings.group.brief"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["settings.group.privacy"].exists)
        attachScreenshot(app, named: "settings-unpaired")
    }

    func testPastBriefsRemainsReachableWithoutLibrary() throws {
        let app = launch(state: "feed")
        // Past briefs is in the brief card's menu (phone-today rule 2).
        let menu = app.buttons["feed.brief.menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        app.buttons["Past briefs"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["pastBriefs.screen"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons.matching(identifier: "pastBriefs.row").firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["pastBriefs.library"].exists)
    }

    private func launch(state: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        return app
    }
}
