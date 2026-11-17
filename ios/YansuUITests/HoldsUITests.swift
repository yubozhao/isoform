import XCTest

final class HoldsUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
        throw XCTSkip("Holds were offered on the Tasks tab, which phone-today v1.0.0 rule 19 removed; offered work is Your work on Today.")
    }

    func testHoldOfferAndUndoInEnglishLight() { exerciseHold(language: "en", appearance: "Light") }
    func testHoldOfferAndUndoInChineseDark() { exerciseHold(language: "zh-Hans", appearance: "Dark") }

    func testComputerAwayQueuesThenHoldsInEnglishLight() { exerciseAway(language: "en", appearance: "Light") }
    func testComputerAwayQueuesThenHoldsInChineseDark() { exerciseAway(language: "zh-Hans", appearance: "Dark") }
    func testTimePassedOpensFindTimeSheetInEnglishDark() { exercisePassed(language: "en", appearance: "Dark") }
    func testTimePassedOpensFindTimeSheetInChineseLight() { exercisePassed(language: "zh-Hans", appearance: "Light") }
    func testCleanupAskKeepInEnglishLight() { exerciseCleanup(language: "en", appearance: "Light", remove: false) }
    func testCleanupAskRemoveInChineseDark() { exerciseCleanup(language: "zh-Hans", appearance: "Dark", remove: true) }
    func testFailureInlineInEnglishDark() { exerciseFailure(language: "en", appearance: "Dark") }
    func testFailureInlineInChineseLight() { exerciseFailure(language: "zh-Hans", appearance: "Light") }

    func testFilledUpOffersNewTimeInEnglishLight() { exerciseAsk(language: "en", appearance: "Light", mode: "filled") }
    func testFilledUpOffersNewTimeInChineseDark() { exerciseAsk(language: "zh-Hans", appearance: "Dark", mode: "filled") }
    func testReadOnlyOffersOtherCalendarInEnglishDark() { exerciseAsk(language: "en", appearance: "Dark", mode: "readonly") }
    func testReadOnlyOffersOtherCalendarInChineseLight() { exerciseAsk(language: "zh-Hans", appearance: "Light", mode: "readonly") }

    /// The computer finds the time filled up or the calendar read-only: the card opens with the question and one press holds.
    private func exerciseAsk(language: String, appearance: String, mode: String) {
        let app = launch(language: language, appearance: appearance, mode: mode)
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(doIt.waitForExistence(timeout: 20))
        doIt.tap()
        let words: [String: [String]] = [
            "filled": ["That time just filled up: Design review at", "这段时间刚被占用："],
            "readonly": ["Your Work calendar can't be changed from this computer. Hold it on Home instead?", "“Work”日历在这台电脑上不能修改。改放到“Home”日历上？"],
        ]
        let line = words[mode]![language == "en" ? 0 : 1]
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", line)).firstMatch.waitForExistence(timeout: 15), app.debugDescription)
        attach(app, name: "hold-\(mode)-\(language)-\(appearance)")
        app.buttons["holds.sheet.hold"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["holds.sheet.heldUndo"].waitForExistence(timeout: 10), app.debugDescription)
    }

    /// Hold while the computer is away: it queues with Undo and goes once the computer is back.
    private func exerciseAway(language: String, appearance: String) {
        let app = launch(language: language, appearance: appearance, mode: "away")
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(doIt.waitForExistence(timeout: 20))
        doIt.tap()
        let away = app.staticTexts[language == "en" ? "Holding when your computer is back" : "电脑恢复后留出"]
        XCTAssertTrue(away.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.buttons["tasks.starting.undo"].isEnabled)
        attach(app, name: "hold-away-\(language)-\(appearance)")
        let row = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "tasks.row.open.hold.")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 30), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["holds.thread.placed"].waitForExistence(timeout: 30))
    }

    /// The computer comes back after the time passed: "Didn't go" with Other times, which opens the card.
    private func exercisePassed(language: String, appearance: String) {
        let app = launch(language: language, appearance: appearance, mode: "away-passed")
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(doIt.waitForExistence(timeout: 20))
        doIt.tap()
        let passed = app.staticTexts[language == "en" ? "Didn't go: the time passed." : "没有完成：时间已经过了。"]
        XCTAssertTrue(passed.waitForExistence(timeout: 30), app.debugDescription)
        attach(app, name: "hold-passed-\(language)-\(appearance)")
        app.buttons["tasks.offer.otherTimes"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["holds.sheet.block"].waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts["holds.sheet.why"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["holds.sheet.where"].exists)
        app.buttons["holds.sheet.otherTimes"].tap()
        XCTAssertTrue(app.buttons["holds.sheet.slot"].firstMatch.waitForExistence(timeout: 5))
        attach(app, name: "hold-sheet-\(language)-\(appearance)")
        app.buttons["holds.sheet.slot"].firstMatch.tap()
        app.buttons["holds.sheet.hold"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["holds.sheet.heldUndo"].waitForExistence(timeout: 10), app.debugDescription)
        attach(app, name: "hold-sheet-held-\(language)-\(appearance)")
        XCTAssertTrue(app.descendants(matching: .any)["holds.sheet.remove"].waitForExistence(timeout: 15), app.debugDescription)
    }

    /// Done before the hold starts: the thread asks once; Keep it ends the asking, Remove deletes the event.
    private func exerciseCleanup(language: String, appearance: String, remove: Bool) {
        let app = launch(language: language, appearance: appearance, mode: "")
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(doIt.waitForExistence(timeout: 20))
        doIt.tap()
        let row = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "tasks.row.open.hold.")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["holds.thread.placed"].waitForExistence(timeout: 20))
        app.buttons["tasks.thread.more"].firstMatch.tap()
        app.buttons["tasks.thread.menu.mark_done"].tap()
        let ask = app.descendants(matching: .any)["holds.thread.cleanup"]
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        attach(app, name: "hold-cleanup-\(language)-\(appearance)")
        if remove {
            app.buttons["holds.thread.cleanup.remove"].tap()
            XCTAssertTrue(app.descendants(matching: .any)["holds.thread.removed"].waitForExistence(timeout: 10), app.debugDescription)
            attach(app, name: "hold-removed-\(language)-\(appearance)")
        } else {
            app.buttons["holds.thread.cleanup.keep"].tap()
            XCTAssertTrue(ask.waitForNonExistence(timeout: 10), app.debugDescription)
            XCTAssertTrue(app.descendants(matching: .any)["holds.thread.placed"].exists)
        }
    }

    /// A failed Hold says so in place, then "Still didn't work." after the second try. No toast.
    private func exerciseFailure(language: String, appearance: String) {
        let app = launch(language: language, appearance: appearance, mode: "fail")
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(doIt.waitForExistence(timeout: 20))
        doIt.tap()
        XCTAssertTrue(app.staticTexts[language == "en" ? "Couldn't hold it." : "没能留出时间。"].waitForExistence(timeout: 10), app.debugDescription)
        app.descendants(matching: .any)["tasks.offer.startFailed"].buttons.firstMatch.tap()
        XCTAssertTrue(app.staticTexts[language == "en" ? "Still didn't work." : "还是没成功。"].waitForExistence(timeout: 10), app.debugDescription)
        attach(app, name: "hold-failed-\(language)-\(appearance)")
    }

    private func launch(language: String, appearance: String, mode: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "holds-seeded"
        app.launchEnvironment["YANSU_UITEST_HOLDS"] = mode
        app.launchEnvironment["YANSU_UITEST_TAB"] = "tasks"
        app.launchEnvironment["YANSU_UITEST_OWNER_CHECK"] = "allow"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launchArguments += ["-AppleInterfaceStyle", appearance, "-AppleLanguages", "(\(language))", "-AppleLocale", language == "zh-Hans" ? "zh_CN" : "en_US"]
        app.launch()
        return app
    }

    private func exerciseHold(language: String, appearance: String) {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "holds-seeded"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "tasks"
        app.launchEnvironment["YANSU_UITEST_OWNER_CHECK"] = "allow"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launchArguments += ["-AppleInterfaceStyle", appearance, "-AppleLanguages", "(\(language))", "-AppleLocale", language == "zh-Hans" ? "zh_CN" : "en_US"]
        app.launch()
        let doIt = app.buttons["tasks.offer.doIt"]
        XCTAssertTrue(doIt.waitForExistence(timeout: 20))
        attach(app, name: "hold-offer-\(language)-\(appearance)")
        doIt.tap()
        let undo = app.buttons["tasks.starting.undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 10))
        XCTAssertTrue(undo.isEnabled)
        undo.tap()
        XCTAssertTrue(doIt.waitForExistence(timeout: 10))
        doIt.tap()
        XCTAssertTrue(undo.waitForExistence(timeout: 10))
        let row = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "tasks.row.open.hold.")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["holds.thread.placed"].waitForExistence(timeout: 20))
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.outcome"].waitForExistence(timeout: 5))
        attach(app, name: "hold-thread-\(language)-\(appearance)")
    }

    private func attach(_ app: XCUIApplication, name: String) {
        let image = XCTAttachment(screenshot: app.screenshot())
        image.name = name
        image.lifetime = .keepAlways
        add(image)
    }
}
