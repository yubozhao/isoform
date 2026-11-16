import XCTest

// The brief in full and Past briefs (phone-brief v1.0.0).
final class BriefPageUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launch()
        return app
    }

    func testTheBriefOpensWithItsTimePlayerAndItemsWhereMentioned() throws {
        let app = launch()
        let readAll = app.buttons["feed.readAll"].firstMatch
        XCTAssertTrue(readAll.waitForExistence(timeout: 15))
        readAll.tap()
        XCTAssertTrue(app.staticTexts["brief.title"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["brief.made"].firstMatch.exists)
        XCTAssertFalse(app.buttons["brief.previous"].exists, "the arrows went; swiping moves between briefs")
        XCTAssertTrue(app.buttons["brief.back15"].firstMatch.exists)
        attachScreenshot(app, named: "brief-1-top")
        let paragraph = app.buttons["brief.paragraph.0"].firstMatch
        if paragraph.exists { paragraph.tap() }
        attachScreenshot(app, named: "brief-2-paragraph-actions")
        app.swipeUp()
        attachScreenshot(app, named: "brief-3-items")
    }

    func testPastBriefsListsBriefsByDayWithWhereYouAre() throws {
        let app = launch()
        let menu = app.buttons["feed.brief.menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 15))
        menu.tap()
        app.buttons["Past briefs"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["pastBriefs.kept"].firstMatch.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "brief-4-past-briefs")
    }
}
