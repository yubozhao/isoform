import XCTest

final class DuoWideUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testWidePanesTaskDetailAndFullScreenLight() throws {
        try exerciseWideExperience(appearance: nil, suffix: "light")
    }

    func testWidePanesTaskDetailAndFullScreenDark() throws {
        try exerciseWideExperience(appearance: "Dark", suffix: "dark")
    }

    func testWideTodayBriefChipAndParagraphOpenChat() throws {
        let chipApp = launchWideBrief()
        let chip = chipApp.buttons["brief.chip.0"]
        XCTAssertTrue(chip.waitForExistence(timeout: 15))
        XCTAssertEqual(chip.label, "Review the project notes")
        chip.tap()
        let filledComposer = chipApp.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(filledComposer.waitForExistence(timeout: 10))
        XCTAssertEqual(filledComposer.value as? String, "What changed in the project notes?")
        chipApp.terminate()

        let paragraphApp = launchWideBrief()
        let paragraph = paragraphApp.buttons["brief.paragraph.0"]
        XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
        paragraph.tap()
        XCTAssertTrue(paragraphApp.buttons["duo.tab.chat"].waitForExistence(timeout: 10))
        let emptyComposer = paragraphApp.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(emptyComposer.waitForExistence(timeout: 10))
        let value = emptyComposer.value as? String ?? ""
        XCTAssertTrue(value.isEmpty || value == "Message Isoform")
        attach(paragraphApp, name: "duo-today-brief-paragraph-chat")
        paragraphApp.terminate()
    }

    func testWideTodayYourWeekCardAndOpenChip() {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-your-week"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = "890x626"
        app.launch()

        XCTAssertTrue(app.staticTexts["brief.yourWeek.title"].waitForExistence(timeout: 15))
        let chip = app.buttons["brief.yourWeek.chip.open-launch"]
        let today = app.scrollViews.firstMatch
        for _ in 0..<8 {
            if chip.exists && chip.isHittable { break }
            today.swipeUp()
        }
        XCTAssertTrue(chip.waitForExistence(timeout: 10))
        chip.tap()
        let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        XCTAssertEqual(composer.value as? String, "Review the launch plan from this week.")
        attach(app, name: "duo-your-week-open-chip")
        app.terminate()
    }

    func testWideChatCardOpensInRightPaneWithComposerVisible() {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "chat-cards"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = "890x626"
        app.launch()

        let prep = app.buttons["chat.proactive.button.open_prep"].firstMatch
        XCTAssertTrue(prep.waitForExistence(timeout: 15))
        prep.tap()
        XCTAssertTrue(app.descendants(matching: .any)["duo.chat.proactive.item"].waitForExistence(timeout: 10))
        let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        XCTAssertTrue(composer.isHittable)
        XCTAssertTrue(app.buttons["duo.tab.chat"].exists)
        attach(app, name: "duo-chat-card-item-pane")
    }

    private func launchWideBrief() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = "890x626"
        app.launch()
        return app
    }

    func testWideTodayShowsPastBriefsEntry() {
        let app = launchWideToday(state: "feed")
        XCTAssertTrue(app.buttons["feed.pastBriefs"].waitForExistence(timeout: 15))
    }

    func testWideTodayUnpairedLaunchShowsThePairPrompt() throws {
        throw XCTSkip("Tasks tab removed (phone-today v1.0.0 rule 19); its work is Your work on Today.")
        let app = launchWideToday(state: "unpaired")
        XCTAssertTrue(app.staticTexts["Read your computer's daily brief here."].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["Pair with your computer"].exists)
    }

    private func exerciseWideExperience(appearance: String?, suffix: String) throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "tasks"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = "890x626"
        if let appearance {
            app.launchArguments += ["-AppleInterfaceStyle", appearance]
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        }
        app.launch()

        XCTAssertTrue(app.staticTexts["Run iOS compatibility tests"].waitForExistence(timeout: 15))
        let tasksBoard = app.descendants(matching: .any)["duo.tasks.board"]
        XCTAssertTrue(tasksBoard.waitForExistence(timeout: 5))
        let foldX = tasksBoard.frame.minX + 445
        let notNow = app.buttons["tasks.offer.notNow"]
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(notNow.waitForExistence(timeout: 5))
        XCTAssertTrue(doIt.waitForExistence(timeout: 5))
        XCTAssertGreaterThanOrEqual(notNow.frame.minX, foldX + 16)
        XCTAssertGreaterThanOrEqual(doIt.frame.minX, foldX + 16)
        let findMore = app.buttons["tasks.findMore"]
        let addTask = app.buttons["duo.tasks.add"]
        let profile = app.buttons["root.settings"]
        XCTAssertTrue(findMore.waitForExistence(timeout: 5))
        XCTAssertTrue(addTask.waitForExistence(timeout: 5))
        XCTAssertTrue(profile.waitForExistence(timeout: 5))
        XCTAssertFalse(findMore.frame.intersects(addTask.frame))
        XCTAssertFalse(findMore.frame.intersects(profile.frame))
        attach(app, name: "duo-tasks-\(suffix)")
        let runningTask = app.staticTexts["Run iOS compatibility tests"]
        XCTAssertTrue(runningTask.waitForExistence(timeout: 10))
        runningTask.tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.detail.steps"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["When it’s done"].exists)
        XCTAssertTrue(app.buttons["tasks.thread.more"].exists)
        attach(app, name: "duo-task-detail-\(suffix)")

        let fullScreen = app.buttons["duo.fullScreen.toggle"]
        XCTAssertTrue(fullScreen.waitForExistence(timeout: 5))
        fullScreen.tap()
        XCTAssertTrue(app.descendants(matching: .any)["duo.fullScreen"].waitForExistence(timeout: 5))
        attach(app, name: "duo-full-screen-\(suffix)")
        app.buttons["duo.fullScreen.toggle"].tap()

        app.buttons["duo.tab.today"].tap()
        XCTAssertTrue(app.staticTexts["Your day"].waitForExistence(timeout: 5))
        attach(app, name: "duo-today-\(suffix)")
        let todayCard = app.staticTexts["3pm with Maya moved"]
        XCTAssertTrue(todayCard.waitForExistence(timeout: 5))
        todayCard.tap()
        let closeTodayItem = app.buttons["duo.today.item.close"]
        XCTAssertTrue(closeTodayItem.waitForExistence(timeout: 5))
        XCTAssertFalse(closeTodayItem.frame.intersects(profile.frame))
        let selectedItemTitles = app.staticTexts.matching(
            NSPredicate(format: "label == %@", "3pm with Maya moved")
        )
        XCTAssertTrue(selectedItemTitles.element(boundBy: 1).waitForExistence(timeout: 5))
        attach(app, name: "duo-today-item-\(suffix)")
        closeTodayItem.tap()
        XCTAssertFalse(closeTodayItem.exists)

        app.buttons["duo.tab.chat"].tap()
        XCTAssertTrue(app.staticTexts["Topics"].waitForExistence(timeout: 5))
        let topic = app.buttons["Halcyon ERP sync"]
        XCTAssertTrue(topic.waitForExistence(timeout: 10))
        attach(app, name: "duo-chat-\(suffix)")
        topic.tap()
        XCTAssertTrue(app.buttons["duo.chat.thread.close"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["duo.chat.thread.close"].frame.intersects(profile.frame))
        XCTAssertTrue(app.staticTexts["Halcyon ERP sync"].exists)
        attach(app, name: "duo-chat-thread-\(suffix)")
        app.buttons["duo.chat.thread.close"].tap()

        app.buttons["duo.tab.tasks"].tap()
        app.buttons["root.settings"].tap()
        XCTAssertTrue(app.buttons["settings.done"].waitForExistence(timeout: 5))
        attach(app, name: "duo-settings-\(suffix)")
        app.terminate()
    }

    private func launchWideToday(state: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = "890x626"
        app.launch()
        return app
    }

    private func scrollIntoView(_ element: XCUIElement, in app: XCUIApplication) {
        let scrollView = app.scrollViews.firstMatch
        for _ in 0..<6 where !element.isHittable {
            scrollView.swipeUp()
        }
    }

    private func attach(_ app: XCUIApplication, name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
