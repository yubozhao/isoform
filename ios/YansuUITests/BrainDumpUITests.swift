import XCTest

final class BrainDumpUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
        throw XCTSkip("The Tasks tab and its task field went (phone-today v1.0.0 rule 19); ask in Chat instead.")
    }

    func testBrainDumpEnglishLight() throws { try exercise(language: "en", appearance: "Light") }
    func testBrainDumpChineseDark() throws { try exercise(language: "zh-Hans", appearance: "Dark") }

    private func exercise(language: String, appearance: String) throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "brain-dump"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "tasks"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launchArguments += ["-AppleInterfaceStyle", appearance, "-AppleLanguages", "(\(language))",
                                "-AppleLocale", language == "zh-Hans" ? "zh_CN" : "en_US"]
        app.launch()
        XCTAssertTrue(app.buttons["tasks.addTask"].waitForExistence(timeout: 15))
        app.buttons["tasks.addTask"].tap()
        let input = app.textViews["brainDump.text"]
        XCTAssertTrue(input.waitForExistence(timeout: 10))
        input.tap()
        input.typeText("Reply to Lena about the appendix\nBook the dentist\nWrite the pricing FAQ")
        let done = app.toolbars.buttons[language == "en" ? "Done" : "完成"]
        if done.exists { done.tap() }
        attach(app, named: "25-write-\(language)-\(appearance.lowercased())")
        app.buttons["brainDump.make"].tap()
        XCTAssertTrue(app.buttons["brainDump.item.draft"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["brainDump.item.reminder"].exists)
        XCTAssertTrue(app.buttons["brainDump.item.code"].exists)
        XCTAssertTrue(app.buttons["brainDump.item.draft"].label.contains(language == "en" ? "Isoform drafts it. You send it." : "Isoform 起草，你来发送。"))
        XCTAssertTrue(app.buttons["brainDump.item.reminder"].label.contains(language == "en" ? "A reminder for you" : "给你的提醒"))
        XCTAssertTrue(app.buttons["brainDump.item.code"].label.contains(language == "en" ? "Claude Code on your computer does it. You review it." : "你电脑上的 Claude Code 来做，你来检查。"))
        attach(app, named: "\(language == "en" ? "26" : "35")-check-\(language)-\(appearance.lowercased())")
        app.buttons["brainDump.item.code"].tap()
        XCTAssertTrue(app.buttons["brainDump.add"].label.contains("2"))
        app.buttons["brainDump.add"].tap()
        XCTAssertTrue(app.staticTexts["Reply to Lena about the appendix"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Book the dentist"].exists)
        XCTAssertTrue(app.otherElements["tasks.running"].exists)
        XCTAssertTrue(app.otherElements["tasks.scheduled"].exists)
        attach(app, named: "27-added-\(language)-\(appearance.lowercased())")
        app.staticTexts["Reply to Lena about the appendix"].tap()
        let origin = app.staticTexts["tasks.thread.origin"]
        XCTAssertTrue(origin.waitForExistence(timeout: 10))
        XCTAssertTrue(origin.label.contains(language == "en" ? "From your list" : "来自你的清单"))
        attach(app, named: "thread-list-\(language)-\(appearance.lowercased())")
    }

    private func attach(_ app: XCUIApplication, named: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = named
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
