import XCTest

final class DuoWideUITests: XCTestCase {
    /// The wide layout is laid out in a window as wide as an iPhone on its side, and the simulator
    /// is turned on its side, so both panes and the rail are on screen. Since the fold splits the
    /// page (phone-duo v1.0.0 rule 1) the right pane starts at the middle; in the 890-wide window
    /// used before, on an upright iPhone, it was off the screen.
    static let wideSize = "852x393"

    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .landscapeLeft
    }

    override func tearDownWithError() throws {
        XCUIDevice.shared.orientation = .portrait
        try super.tearDownWithError()
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

    /// Watch is a tab in the wide rail, between Today and Chat, and its page fills the main area.
    func testWideRailHasWatchAndItsPageOpensInTheMainArea() {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-audio"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_WATCH_RECORDINGS"] = "1"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
        app.launch()

        XCTAssertTrue(app.buttons["duo.tab.today"].waitForExistence(timeout: 15))
        attach(app, name: "duo-watch-rail")
        let watch = app.buttons["duo.tab.watch"]
        XCTAssertTrue(watch.waitForExistence(timeout: 5), "the rail has a Watch tab when the phone has a watch")
        let today = app.buttons["duo.tab.today"].frame
        let chat = app.buttons["duo.tab.chat"].frame
        XCTAssertGreaterThan(watch.frame.midY, today.midY)
        XCTAssertLessThan(watch.frame.midY, chat.midY)
        watch.tap()
        // Left pane: Now and where the hours are. Right pane: today's hours and conversations.
        XCTAssertTrue(app.descendants(matching: .any)["duo.watch.side"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["duo.watch.page"].firstMatch.exists)
        XCTAssertTrue(app.staticTexts["watch.now.word"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["Done"].exists, "the page is in the main area, not a sheet")
        // One title, in the left pane; the rail is the only chrome: one person button, no "…".
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "duo.pane.title").count, 1)
        XCTAssertEqual(app.descendants(matching: .any)["duo.pane.title"].firstMatch.label, "Watch")
        XCTAssertEqual(app.buttons.matching(identifier: "root.settings").count, 1)
        XCTAssertFalse(app.buttons["More"].exists)
        attach(app, name: "duo-watch-page")
        // Back to Today from the rail.
        app.buttons["duo.tab.today"].tap()
        XCTAssertFalse(app.descendants(matching: .any)["duo.watch.page"].firstMatch.waitForExistence(timeout: 2))
        app.terminate()
    }

    func testWideTodayYourWeekCardAndOpenChip() {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-your-week"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
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
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
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
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
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

    /// Bo's Duo, 2026-11-18: Today's watch line is pinned above the page, and the page scrolled under it and showed
    /// through. The line sits on the page's color; the screenshots show the top and a scrolled page.
    /// Today's watch line sits under the date and scrolls with the page; it is never pinned over it
    /// (phone-duo v1.0.0 rule 12). Before, it was pinned and covered what scrolled under it.
    func testWideTodayWatchLineSitsUnderTheDateAndScrollsWithThePage() {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
        app.launchEnvironment["YANSU_UITEST_WATCH_RECORDINGS"] = "1"
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 15))
        let line = app.buttons["today.watchLine"].firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 5), "the line is there whenever a watch is paired")
        let date = app.staticTexts["feed.summary"].firstMatch
        XCTAssertTrue(date.exists)
        XCTAssertGreaterThanOrEqual(line.frame.minY, date.frame.maxY - 0.5, "under the date")
        let before = line.frame.minY
        // The first scroll view of the right pane holds the date and the line.
        date.swipeUp()
        if line.exists {
            XCTAssertLessThan(line.frame.minY, before, "it moved with the page: nothing is pinned")
        }
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
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
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
        // The fold splits the page (phone-duo v1.0.0 rule 1): Today's right pane starts at the fold, the middle
        // of the window, and ends at the rail. Each item's Yes and No sit right of the fold, never across.
        let foldX = feed.frame.minX
        XCTAssertGreaterThanOrEqual(yes.frame.minX, foldX - 0.5)
        XCTAssertGreaterThanOrEqual(no.frame.minX, foldX - 0.5)
        XCTAssertGreaterThanOrEqual(app.descendants(matching: .any)["today.yourWork"].firstMatch.frame.minX, foldX - 0.5)
        XCTAssertLessThanOrEqual(yes.frame.maxX, feed.frame.maxX + 0.5)
        // One title and one person button on the whole screen, and no "…" (rules 3 and 4).
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "duo.pane.title").count, 1)
        XCTAssertEqual(app.buttons.matching(identifier: "root.settings").count, 1)
        XCTAssertFalse(app.buttons["More"].exists)
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
        app.launchEnvironment["YANSU_UITEST_DUO_SIZE"] = Self.wideSize
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
