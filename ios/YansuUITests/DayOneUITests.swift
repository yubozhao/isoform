import XCTest

final class DayOneUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testFirstCopyShowsShapesUntilTheComputerResponds() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale",
                                    language == "en" ? "en_US" : "zh_CN", "-AppleInterfaceStyle", appearance]
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
            app.launchEnvironment["YANSU_UITEST_FIRST_COPY"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launch()
            let todayShapes = app.descendants(matching: .any)["feed.skeleton"].firstMatch
            XCTAssertTrue(todayShapes.waitForExistence(timeout: 10))
            attachScreenshot(app, named: "first-copy-today-\(language)-\(appearance.lowercased())")
            app.buttons["tab.tasks"].tap()
            let shapes = app.descendants(matching: .any)["firstCopy.shapes"].firstMatch
            XCTAssertTrue(shapes.waitForExistence(timeout: 5))
            XCTAssertFalse(app.staticTexts["tasks.subtitle"].exists)
            attachScreenshot(app, named: "first-copy-tasks-\(language)-\(appearance.lowercased())")
            XCTAssertTrue(app.staticTexts["tasks.subtitle"].waitForExistence(timeout: 25))
            XCTAssertFalse(shapes.exists)
            app.buttons["tab.today"].tap()
            XCTAssertTrue(app.buttons["feed.summary"].waitForExistence(timeout: 10))
            XCTAssertFalse(todayShapes.exists, "Today reveals its sections when the first copy arrives")
            app.terminate()
        }
    }

    func testTodayPlaceholderIsShownOnceForThePairing() {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-no-history"
        app.launchEnvironment["YANSU_UITEST_SETTINGS_ID"] = UUID().uuidString
        app.launch()
        let skeleton = app.descendants(matching: .any)["feed.skeleton"].firstMatch
        XCTAssertTrue(skeleton.waitForExistence(timeout: 15))
        app.terminate()
        app.launchEnvironment["YANSU_UITEST_KEEP_PLACEHOLDER"] = "1"
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        XCTAssertFalse(skeleton.exists, "The same pairing does not show the placeholder again")
    }

    func testFirstReadEnglishLight() throws {
        try exerciseFirstRead(language: "en", appearance: "Light")
        try exerciseWaitingAndFailed(language: "en", appearance: "Light")
        try exerciseBlank(language: "en", appearance: "Light")
        try exerciseOffers(language: "en", appearance: "Light")
    }

    /// P163 frames for the other two pairs: English dark and Chinese light.
    func testFirstReadEnglishDarkStates() throws {
        try exerciseFirstRead(language: "en", appearance: "Dark")
        try exerciseWaitingAndFailed(language: "en", appearance: "Dark")
        try exerciseBlank(language: "en", appearance: "Dark")
    }

    func testFirstReadChineseLightStates() throws {
        try exerciseFirstRead(language: "zh-Hans", appearance: "Light")
        try exerciseWaitingAndFailed(language: "zh-Hans", appearance: "Light")
        try exerciseBlank(language: "zh-Hans", appearance: "Light")
    }

    func testFirstReadChineseDark() throws {
        try exerciseFirstRead(language: "zh-Hans", appearance: "Dark")
        try exerciseWaitingAndFailed(language: "zh-Hans", appearance: "Dark")
        try exerciseBlank(language: "zh-Hans", appearance: "Dark")
        try exerciseOffers(language: "zh-Hans", appearance: "Dark")
    }

    private func launch(language: String, appearance: String, firstReadAnswer: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "day-one"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        // The seeded computer holds the correction ("held") or refuses it ("failed") instead of answering.
        if let firstReadAnswer { app.launchEnvironment["YANSU_UITEST_FIRST_READ_ANSWER"] = firstReadAnswer }
        app.launchArguments += ["-AppleInterfaceStyle", appearance, "-AppleLanguages", "(\(language))",
                                "-AppleLocale", language == "zh-Hans" ? "zh_CN" : "en_US"]
        app.launch()
        XCTAssertTrue(app.buttons["firstRead.confirm"].waitForExistence(timeout: 20))
        return app
    }

    private func exerciseFirstRead(language: String, appearance: String) throws {
        let app = launch(language: language, appearance: appearance)
        let confirm = app.buttons["firstRead.confirm"]
        reveal(confirm, in: app, direction: .down)
        // Show the beginning of the portrait as well as its buttons in the
        // capture, even when Chat initially follows the bottom of the item.
        let label = app.staticTexts["firstRead.label"]
        let headerBottom = app.navigationBars.firstMatch.frame.maxY
        for _ in 0..<5 where label.frame.minY < headerBottom {
            app.scrollViews.firstMatch.swipeDown()
        }
        XCTAssertGreaterThanOrEqual(label.frame.minY, headerBottom)
        capture(app, "first-read-\(language)-\(appearance)")
        confirm.tap()
        XCTAssertTrue(app.staticTexts["firstRead.confirmed"].waitForExistence(timeout: 5))
        XCTAssertGreaterThanOrEqual(label.frame.minY, headerBottom)
        capture(app, "first-read-confirmed-\(language)-\(appearance)")
        let undo = app.buttons["firstRead.undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        undo.tap()
        XCTAssertTrue(confirm.waitForExistence(timeout: 5))
        let notQuite = app.buttons["firstRead.notQuite"]
        XCTAssertTrue(notQuite.waitForExistence(timeout: 5))
        notQuite.tap()
        let correction = app.descendants(matching: .any)["firstRead.correction"]
        XCTAssertTrue(correction.waitForExistence(timeout: 5))
        XCTAssertGreaterThanOrEqual(label.frame.minY, headerBottom)
        capture(app, "first-read-correction-\(language)-\(appearance)")
        correction.tap()
        correction.typeText("I work best later in the day.")
        app.buttons["firstRead.send"].tap()
        XCTAssertTrue(app.staticTexts["I work best later in the day."].waitForExistence(timeout: 10))
        capture(app, "first-read-corrected-\(language)-\(appearance)")
        app.terminate()
    }

    /// P163: the words stay on the card while the computer has not answered, and come back to the field on failure.
    private func exerciseWaitingAndFailed(language: String, appearance: String) throws {
        let held = launch(language: language, appearance: appearance, firstReadAnswer: "held")
        sendCorrection(in: held, words: "I work best later in the day.")
        let waiting = held.staticTexts["firstRead.waiting"]
        XCTAssertTrue(waiting.waitForExistence(timeout: 10))
        XCTAssertTrue(held.staticTexts["I work best later in the day."].exists)
        capture(held, "first-read-waiting-\(language)-\(appearance)")
        held.buttons["firstRead.undo"].tap()
        XCTAssertTrue(held.descendants(matching: .any)["firstRead.correction"].waitForExistence(timeout: 5))
        held.terminate()

        let failed = launch(language: language, appearance: appearance, firstReadAnswer: "failed")
        sendCorrection(in: failed, words: "I work best later in the day.")
        XCTAssertTrue(failed.staticTexts["firstRead.error"].waitForExistence(timeout: 10))
        let field = failed.descendants(matching: .any)["firstRead.correction"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        XCTAssertEqual(field.value as? String, "I work best later in the day.")
        // With the field focused, Chat keeps the card, its Send and the failure line above the composer.
        field.tap()
        Thread.sleep(forTimeInterval: 1)
        capture(failed, "first-read-failed-\(language)-\(appearance)")
        failed.terminate()
    }

    /// P163 v1.0.1: Not quite left blank goes through the real Send and shows the blank state.
    private func exerciseBlank(language: String, appearance: String) throws {
        let app = launch(language: language, appearance: appearance)
        let notQuite = app.buttons["firstRead.notQuite"]
        reveal(notQuite, in: app, direction: .down)
        notQuite.tap()
        let send = app.buttons["firstRead.send"]
        XCTAssertTrue(send.waitForExistence(timeout: 5))
        XCTAssertTrue(send.isEnabled)
        send.tap()
        let said = app.staticTexts["firstRead.said"]
        XCTAssertTrue(said.waitForExistence(timeout: 10))
        let portrait = app.staticTexts["firstRead.portraitLine"]
        XCTAssertTrue(portrait.waitForExistence(timeout: 10))
        reveal(portrait, in: app, direction: .up)
        capture(app, "first-read-blank-\(language)-\(appearance)")
        app.terminate()
    }

    private func sendCorrection(in app: XCUIApplication, words: String) {
        let notQuite = app.buttons["firstRead.notQuite"]
        reveal(notQuite, in: app, direction: .down)
        notQuite.tap()
        let correction = app.descendants(matching: .any)["firstRead.correction"]
        XCTAssertTrue(correction.waitForExistence(timeout: 5))
        correction.tap()
        correction.typeText(words)
        app.buttons["firstRead.send"].tap()
    }

    private func exerciseOffers(language: String, appearance: String) throws {
        let app = launch(language: language, appearance: appearance)
        let see = app.buttons["firstRead.seeThem"]
        reveal(see, in: app, direction: .up)
        XCTAssertTrue(see.isHittable)
        capture(app, "first-read-offers-\(language)-\(appearance)")
        see.tap()
        let upNext = app.descendants(matching: .any)["tasks.upNext"]
        XCTAssertTrue(upNext.waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        XCTAssertTrue(upNext.isHittable)
        capture(app, "first-read-up-next-\(language)-\(appearance)")
        app.terminate()
    }

    private enum Direction { case up, down }
    private func reveal(_ element: XCUIElement, in app: XCUIApplication, direction: Direction) {
        for _ in 0..<4 where !element.isHittable {
            switch direction {
            case .up: app.swipeUp()
            case .down: app.swipeDown()
            }
        }
    }

    private func capture(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
