import XCTest

// Large text and VoiceOver on the phone (pass-access v1.0.0 rules 7 to 9).
final class AccessUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    private func launch(largeText: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        if largeText { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launch()
        return app
    }

    func testAtTheLargestTextTheBriefFoldsToItsTitleAndTheAnswersStack() throws {
        let app = launch(largeText: true)
        XCTAssertTrue(app.otherElements["feed.brief"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertFalse(app.staticTexts["feed.brief.words"].exists, "the card folds to its title so Your work starts on the first screen")
        attachScreenshot(app, named: "access-1-large-text-today")
        let yes = app.buttons["work.yes"].firstMatch
        for _ in 0..<4 where !yes.isHittable { app.swipeUp() }
        let no = app.buttons["work.no"].firstMatch
        XCTAssertGreaterThan(no.frame.minY, yes.frame.minY, "No sits under Yes: the answers stack")
        XCTAssertGreaterThanOrEqual(yes.frame.height, 56)
        attachScreenshot(app, named: "access-2-large-text-answers")
    }

    func testVoiceOverReadsAnItemWholeWithItsAnswersAsActions() throws {
        let app = launch(largeText: false)
        let item = app.descendants(matching: .any)["work.item.noticed"].firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 15))
        XCTAssertTrue(item.label.contains("Lena will ask"), item.label)
        XCTAssertTrue(item.label.contains("Yes keeps it with the meeting"), "what Yes does is read with the item")
    }
}
