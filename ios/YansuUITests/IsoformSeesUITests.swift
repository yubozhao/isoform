import XCTest

// What Isoform sees (phone-duo v1.1.0): it opens from one place, Settings › Your data, first row under "Kept on your
// computer", as a page with "Your data" as its back button. Chat has no mark; while the computer works, its line sits
// at the end of the conversation. The seeded computer has Mail off.
final class IsoformSeesUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testChatHasNoMarkAndTheWorkingLineSitsAtTheEndOfTheConversation() throws {
        let app = launch(language: "en", appearance: "Light")
        let presence = app.descendants(matching: .any)["chat.presence"].firstMatch
        XCTAssertTrue(presence.waitForExistence(timeout: 15), "the working line shows while the computer works")
        XCTAssertFalse(app.buttons["chat.mark"].exists, "Chat's title is the word alone")
        let composer = app.descendants(matching: .any)["chat.composerBar"].firstMatch
        XCTAssertTrue(composer.exists)
        XCTAssertGreaterThan(presence.frame.minY, app.navigationBars.firstMatch.frame.maxY + 40,
                             "the line is in the conversation, not under the title")
        XCTAssertLessThanOrEqual(presence.frame.maxY, composer.frame.minY, "it sits where the reply will appear")
        attach(app, named: "chat-working-line")
    }

    func testWhatIsoformSeesOpensFromYourDataAsAPage() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = launch(language: language, appearance: appearance, presence: false)
            openSeesPage(app)
            let turnOn = app.buttons["sees.turnOn.mail"]
            XCTAssertTrue(turnOn.waitForExistence(timeout: 10), "Mail is off, so its row offers Turn on")
            XCTAssertFalse(app.buttons["sees.turnOn.calendar"].exists, "a source that is on has no Turn on")
            XCTAssertTrue(app.navigationBars.buttons[language == "en" ? "Your data" : "你的数据"].exists,
                          "a page inside Settings, with Your data as its back button")
            XCTAssertFalse(app.buttons["sees.done"].exists, "not a sheet")
            if language == "en" {
                XCTAssertTrue(label("Screenshots and audio are kept on your computer;", in: app).exists)
                XCTAssertTrue(label("Change these on your computer, in Settings › What Isoform sees.", in: app).exists)
                XCTAssertFalse(label("The one exception is Agent of You", in: app).exists,
                               "the exception shows only while Agent of You's review is off")
            }
            attach(app, named: "sees-\(language)-\(appearance.lowercased())")
            turnOn.tap()
            XCTAssertTrue(app.descendants(matching: .any)["sees.sent.mail"].firstMatch.waitForExistence(timeout: 5),
                          "Turn on asks the computer and says so")
            app.terminate()
        }
    }

    func testTheTapLineNamesAgentOfYouWhileItsReviewIsOff() throws {
        let app = launch(language: "en", appearance: "Light", presence: false, reviewOff: true)
        openSeesPage(app)
        let exception = label("The one exception is Agent of You with its review off.", in: app)
        for _ in 0..<4 where !(exception.exists && exception.isHittable) { app.swipeUp() }
        XCTAssertTrue(exception.waitForExistence(timeout: 10))
        attach(app, named: "sees-review-off")
    }

    private func openSeesPage(_ app: XCUIApplication) {
        openSettings(app)
        let row = app.buttons["settings.yourData.open"].firstMatch
        for _ in 0..<6 where !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 5), "Settings has Your data")
        // A tap while the list still glides after the swipes only stops it, so tap until the page shows.
        tapUntil(row, shows: app.buttons["data.seen"].firstMatch)
        tapUntil(app.buttons["data.seen"].firstMatch, shows: app.descendants(matching: .any)["sees.page"].firstMatch)
    }

    private func label(_ text: String, in app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    private func launch(language: String, appearance: String, presence: Bool = true,
                        reviewOff: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language == "en" ? "en_US" : "zh_CN",
                                "-AppleInterfaceStyle", appearance]
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "chat-live"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launchEnvironment["YANSU_UITEST_SEES"] = "1"
        app.launchEnvironment["YANSU_UITEST_PRESENCE"] = presence ? "1" : "0"
        if reviewOff { app.launchEnvironment["YANSU_UITEST_SEES_REVIEW_OFF"] = "1" }
        app.launch()
        XCTAssertTrue(app.buttons["tab.chat"].waitForExistence(timeout: 15))
        return app
    }

    private func attach(_ app: XCUIApplication, named name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
