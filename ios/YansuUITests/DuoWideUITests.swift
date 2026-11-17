import XCTest

final class DuoWideUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    /// The Tasks tab went into Your work (phone-today v1.0.0 rule 19): on a wide phone, Today's pane holds it.
    func testWideTodayYourWorkInTodaysPaneLight() throws {
        try exerciseWideExperience(appearance: nil, suffix: "light")
    }

    func testWideTodayYourWorkInTodaysPaneDark() throws {
        try exerciseWideExperience(appearance: "Dark", suffix: "dark")
    }

    /// Today's card shows the brief's first lines; its chips and paragraphs are on the brief, one tap away.
    func testWideTodayBriefChipAndParagraphOpenChat() throws {
        let chipApp = launchWideBrief()
        openTheBrief(chipApp)
        let chip = chipApp.buttons["brief.chip.0"]
        XCTAssertTrue(chip.waitForExistence(timeout: 15))
        XCTAssertEqual(chip.label, "Review the project notes")
        chip.tap()
        // A chip asks its question in Ask about this, over the brief.
        XCTAssertTrue(chipApp.descendants(matching: .any)["brief.ask.sheet"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertEqual(chipApp.staticTexts["brief.ask.question"].firstMatch.label, "What changed in the project notes?")
        chipApp.terminate()

        let paragraphApp = launchWideBrief()
        openTheBrief(paragraphApp)
        let paragraph = paragraphApp.buttons["brief.paragraph.0"]
        XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
        paragraph.tap()
        // A tap shows the paragraph's actions; Reply about this opens Ask about this with the paragraph quoted and the
        // field empty.
        let reply = paragraphApp.buttons["brief.askAbout.0"].firstMatch
        XCTAssertTrue(reply.waitForExistence(timeout: 5))
        reply.tap()
        XCTAssertTrue(paragraphApp.descendants(matching: .any)["brief.ask.sheet"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertEqual(paragraphApp.staticTexts["brief.ask.quote"].firstMatch.label, "Start with the project notes today.")
        XCTAssertTrue(paragraphApp.textViews["brief.ask.field"].firstMatch.exists)
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
        XCTAssertTrue(app.descendants(matching: .any)["brief.ask.sheet"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertEqual(app.staticTexts["brief.ask.question"].firstMatch.label, "Review the launch plan from this week.")
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

    /// Past briefs is in the brief card's ··· menu (phone-today v1.0.0).
    func testWideTodayShowsPastBriefsEntry() {
        let app = launchWideToday(state: "feed")
        // The brief page's Past briefs link went (phone-brief rule 14); the card's ··· menu holds it. The emulated wide
        // window draws the menu past the simulator's screen, where it can't be tapped, so this checks the menu is
        // there; opening Past briefs from it is covered on a phone-width screen (BriefScreensUITests, SmokeUITests).
        let menu = app.buttons["feed.brief.menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 15))
        XCTAssertEqual(menu.label, "More options")
    }

    /// Read all on Today's card opens the brief. A relaunch may already be on it (the brief folds after a visit).
    private func openTheBrief(_ app: XCUIApplication) {
        let brief = app.scrollViews["brief.view"].firstMatch
        let readAll = app.buttons["feed.readAll"].firstMatch
        let card = app.descendants(matching: .any)["feed.brief"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 15))
        if brief.exists { return }
        // Folded to its title after the first visit: a tap unfolds it, then Read all opens it.
        if !readAll.waitForExistence(timeout: 3) { card.tap() }
        XCTAssertTrue(readAll.waitForExistence(timeout: 5))
        readAll.tap()
        XCTAssertTrue(brief.waitForExistence(timeout: 10))
    }

    func testWideTodayUnpairedLaunchShowsThePairPrompt() throws {
        throw XCTSkip("Tasks tab removed (phone-today v1.0.0 rule 19); its work is Your work on Today.")
        let app = launchWideToday(state: "unpaired")
        XCTAssertTrue(app.staticTexts["Read your computer's daily brief here."].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["Pair with your computer"].exists)
    }

    private func exerciseWideExperience(appearance: String?, suffix: String) throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_WORK"] = "1"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = "890x626"
        if let appearance {
            app.launchArguments += ["-AppleInterfaceStyle", appearance]
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        }
        app.launch()

        let feed = app.descendants(matching: .any)["duo.today.feed"].firstMatch
        XCTAssertTrue(feed.waitForExistence(timeout: 15))
        XCTAssertTrue(app.descendants(matching: .any)["today.yourWork"].firstMatch.waitForExistence(timeout: 10))
        let yes = app.buttons["work.yes"].firstMatch
        let no = app.buttons["work.no"].firstMatch
        XCTAssertTrue(yes.waitForExistence(timeout: 5))
        XCTAssertTrue(no.exists)
        XCTAssertGreaterThan(yes.frame.minX, feed.frame.minX, "Your work is in Today's pane")
        // The fold is 445 points into the 890-wide window, and Today's pane starts 280 in (250 + the 30 gutter).
        // Each item's Yes and No sit right of it, never across.
        let foldX = feed.frame.minX - 280 + 445
        XCTAssertGreaterThanOrEqual(yes.frame.minX, foldX + 16 - 0.5)
        XCTAssertGreaterThanOrEqual(no.frame.minX, foldX + 16 - 0.5)
        XCTAssertGreaterThanOrEqual(app.descendants(matching: .any)["today.yourWork"].firstMatch.frame.minX, foldX + 16 - 0.5)
        let profile = app.buttons["root.settings"]
        XCTAssertTrue(profile.waitForExistence(timeout: 5))
        XCTAssertFalse(yes.frame.intersects(profile.frame))
        XCTAssertFalse(app.buttons["duo.tab.tasks"].exists, "no Tasks tab")
        attach(app, name: "duo-today-your-work-\(suffix)")
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
