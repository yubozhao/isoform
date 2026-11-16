import XCTest

// Today redesigned (phone-today v1.0.0): the brief first, then Your work, the one list. No Tasks tab.
final class YourWorkUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    private func launch(state: String = "feed-audio", work: String = "1") -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_WORK"] = work
        app.launch()
        return app
    }

    func testTodayOpensOnTheBriefThenYourWorkAndHasNoTasksTab() throws {
        let app = launch()
        XCTAssertTrue(app.otherElements["feed.brief"].firstMatch.waitForExistence(timeout: 15), "the brief is on the first screen")
        XCTAssertTrue(app.staticTexts["feed.brief.title"].firstMatch.exists)
        XCTAssertTrue(app.otherElements["today.yourWork"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["4 to answer"].firstMatch.exists)
        XCTAssertTrue(app.buttons["work.yes"].firstMatch.exists)
        XCTAssertFalse(app.buttons["tab.tasks"].exists, "the Tasks tab goes (rule 19)")
        XCTAssertFalse(app.staticTexts["Your day"].exists)
        XCTAssertFalse(app.staticTexts["Your topics"].exists)
        attachScreenshot(app, named: "today-1-brief-and-your-work")

        app.swipeUp()
        XCTAssertTrue(app.otherElements["work.upNext"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.otherElements["work.withAgent"].firstMatch.exists)
        XCTAssertTrue(app.otherElements["work.doneToday"].firstMatch.exists)
        attachScreenshot(app, named: "today-2-up-next-agent-done")
    }

    func testNoAsksForAnOptionalReasonAndTheBriefFoldsButStays() throws {
        let app = launch()
        let no = app.buttons["work.no"].firstMatch
        XCTAssertTrue(no.waitForExistence(timeout: 15))
        no.tap()
        XCTAssertTrue(app.otherElements["work.reasons"].firstMatch.waitForExistence(timeout: 5), "No offers its reasons")
        XCTAssertTrue(app.buttons["work.reasons.skip"].firstMatch.exists, "a reason is never needed")
        XCTAssertTrue(app.otherElements["feed.brief"].firstMatch.exists, "the brief never leaves the first screen")
        XCTAssertFalse(app.buttons["feed.readAll"].exists, "folded to its title and two lines after the first answer")
        attachScreenshot(app, named: "today-3-no-reasons-brief-folded")
        app.buttons["work.reason.not_needed"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["3 to answer"].firstMatch.waitForExistence(timeout: 5))
    }

    func testEditOpensTheWorkFullScreen() throws {
        let app = launch()
        let edit = app.buttons["work.item.edit"].firstMatch
        XCTAssertTrue(edit.waitForExistence(timeout: 15))
        edit.tap()
        XCTAssertTrue(app.textViews["work.edit.text"].firstMatch.waitForExistence(timeout: 5))
        attachScreenshot(app, named: "today-4-edit")
    }

    func testEverythingAnsweredSaysSoAndKeepsTheBrief() throws {
        let app = launch(work: "done")
        XCTAssertTrue(app.descendants(matching: .any)["work.empty"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(app.otherElements["feed.brief"].firstMatch.exists)
        attachScreenshot(app, named: "today-5-all-answered")
    }

    func testTheComputerAsleepKeepsTheLastBriefUnderItsOwnTime() throws {
        let app = launch(state: "feed-offline")
        XCTAssertTrue(app.otherElements["feed.brief"].firstMatch.waitForExistence(timeout: 15))
        attachScreenshot(app, named: "today-6-computer-asleep")
    }
}
