import XCTest

// What Isoform sees (Phone Threads §5): the mark in Chat's title bar and the Settings row open one page that
// says, by job, what Isoform reads on the computer and what leaves it. The seeded computer has Mail off.
final class IsoformSeesUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testTheChatMarkOpensWhatIsoformSeesAndTurnOnAsksTheComputer() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = launch(language: language, appearance: appearance)
            let mark = app.buttons["chat.mark"]
            XCTAssertTrue(mark.waitForExistence(timeout: 15), "Chat's title bar has the Isoform mark")
            XCTAssertTrue(app.descendants(matching: .any)["chat.presence"].firstMatch.waitForExistence(timeout: 10),
                          "the presence line shows while the computer works")
            attach(app, named: "chat-presence-\(language)-\(appearance.lowercased())")
            mark.tap()
            let turnOn = app.buttons["sees.turnOn.mail"]
            XCTAssertTrue(turnOn.waitForExistence(timeout: 10), "Mail is off, so its row offers Turn on")
            XCTAssertFalse(app.buttons["sees.turnOn.calendar"].exists, "a source that is on has no Turn on")
            attach(app, named: "sees-\(language)-\(appearance.lowercased())")
            turnOn.tap()
            XCTAssertTrue(app.descendants(matching: .any)["sees.sent.mail"].firstMatch.waitForExistence(timeout: 5),
                          "Turn on asks the computer and says so")
            app.buttons["sees.done"].tap()
            XCTAssertTrue(mark.waitForExistence(timeout: 5))
            app.terminate()
        }
    }

    func testSettingsPrivacyOpensTheSamePage() throws {
        let app = launch(language: "en", appearance: "Light", presence: false)
        openSettings(app)
        // phone-settings rule 4: What Isoform sees is Your data's "Kept on your computer".
        let row = app.buttons["settings.yourData.open"].firstMatch
        for _ in 0..<6 where !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 5), "Settings has Your data")
        // A tap while the list still glides after the swipes only stops it, so tap until the page shows.
        tapUntil(row, shows: app.buttons["data.seen"].firstMatch)
        tapUntil(app.buttons["data.seen"].firstMatch, shows: app.buttons["sees.turnOn.mail"])
        XCTAssertTrue(app.buttons["sees.turnOn.mail"].exists, "the row opens What Isoform sees")
        attach(app, named: "sees-from-settings")
    }

    private func launch(language: String, appearance: String, presence: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language == "en" ? "en_US" : "zh_CN",
                                "-AppleInterfaceStyle", appearance]
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "chat-live"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launchEnvironment["YANSU_UITEST_SEES"] = "1"
        app.launchEnvironment["YANSU_UITEST_PRESENCE"] = presence ? "1" : "0"
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
