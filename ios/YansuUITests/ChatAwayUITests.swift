import XCTest

/// Chat while the computer is away (phone-connection v1.1.0 section 2): waiting bubbles under one thread line per
/// cause, Undo and long-press, the line a day later, and the green line with send times and "You asked at".
final class ChatAwayUITests: XCTestCase {
    private let questions = ["Can you move my 3 PM with Dana to Friday?", "And tell Sam the deck is coming Monday."]

    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testEachCauseShowsWaitingBubblesUnderOneThreadLine() throws {
        let lines = [
            "asleep": "Your computer is asleep. Isoform answers when it wakes. More",
            "asleep_battery": "Your computer went to sleep to save its battery. Isoform answers when it wakes. More",
            "lid_closed": "Your computer's lid is closed. Isoform answers when you open it. More",
            "off": "Your computer is off. Isoform answers when it's back on. More",
            "closed": "Isoform is closed on your computer. It answers when you open it. More",
            "not_answering": "Your computer isn't answering. Isoform answers when it's back. More",
            "phone_offline": "This iPhone is offline. Your message goes out when it's back online. More",
            "cant_connect": "Isoform can't connect right now. Your message goes out when it does. More",
        ]
        for (cause, line) in lines.sorted(by: { $0.key < $1.key }) {
            let app = launch(cause: cause)
            let thread = app.buttons["chat.away.line"].firstMatch
            XCTAssertTrue(thread.waitForExistence(timeout: 15), cause)
            XCTAssertEqual(thread.label, line)
            XCTAssertEqual(app.buttons.matching(identifier: "chat.away.line").count, 1, "one line per away period")
            let waiting = app.staticTexts.matching(identifier: "chat.message.status")
                .matching(NSPredicate(format: "label == %@", "Waits for your computer"))
            XCTAssertEqual(waiting.count, 2)
            XCTAssertEqual(app.buttons.matching(identifier: "chat.message.undo").count, 2)
            try capture(app, named: "away-\(cause)-en-light")
            app.terminate()
        }
    }

    func testMoreOpensTheSheet() throws {
        let app = launch(cause: "asleep")
        let thread = app.buttons["chat.away.line"].firstMatch
        XCTAssertTrue(thread.waitForExistence(timeout: 15))
        thread.tap()
        XCTAssertTrue(app.descendants(matching: .any)["chat.away.sheet"].waitForExistence(timeout: 5))
        try capture(app, named: "away-more-en-light")
    }

    func testUndoPutsTheWordsBackAfterTheBoxText() throws {
        let app = launch(cause: "asleep")
        let composer = app.textViews["chat.composer"].firstMatch.exists
            ? app.textViews["chat.composer"].firstMatch : app.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 15))
        composer.tap()
        composer.typeText("Also")
        let undo = app.buttons.matching(identifier: "chat.message.undo").element(boundBy: 0)
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        undo.tap()
        let expected = "Also\n" + questions[0]
        let deadline = Date().addingTimeInterval(5)
        while (composer.value as? String) != expected && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.2)) }
        XCTAssertEqual(composer.value as? String, expected)
        XCTAssertEqual(app.buttons.matching(identifier: "chat.message.undo").count, 1, "the other message keeps waiting")
        XCTAssertTrue(app.buttons["chat.away.line"].exists, "the line moves to the next waiting message")
        try capture(app, named: "away-undo-en-light")
    }

    func testLongPressOffersCopyAndUndo() throws {
        let app = launch(cause: "asleep")
        let bubble = app.descendants(matching: .any).matching(identifier: "chat.message.waiting").element(boundBy: 1)
        XCTAssertTrue(bubble.waitForExistence(timeout: 15))
        bubble.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["Copy"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["Undo"].firstMatch.exists)
        try capture(app, named: "away-longpress-en-light")
    }

    func testADayLaterTheLineSaysSinceAndTheMessagesSayTheyWereNotSent() throws {
        let app = launch(cause: "asleep", phase: "long")
        let thread = app.buttons["chat.away.line"].firstMatch
        XCTAssertTrue(thread.waitForExistence(timeout: 15))
        // Every message ran out (a day with no answer), so the line drops its promise and its "since" (chat v1.0.4).
        XCTAssertEqual(thread.label, "Your computer is asleep. More")
        // A day with no answer: the relay let the messages go. They say so and offer Send again, not Undo.
        XCTAssertEqual(app.buttons.matching(identifier: "chat.message.undo").count, 0)
        XCTAssertEqual(app.buttons.matching(identifier: "chat.message.sendAgain").count, 2)
        try capture(app, named: "away-long-en-light")
    }

    func testBackShowsSendTimesTheGreenLineAndYouAskedAt() throws {
        let app = launch(cause: "asleep", phase: "back")
        let back = app.staticTexts["chat.away.back"].firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 15))
        XCTAssertTrue(back.label.hasPrefix("Your computer is back. These went out at "), back.label)
        let asked = app.staticTexts.matching(identifier: "chat.answer.askedAt")
        XCTAssertEqual(asked.count, 2)
        XCTAssertTrue(asked.element(boundBy: 0).label.hasPrefix("You asked at "), asked.element(boundBy: 0).label)
        XCTAssertFalse(app.buttons["chat.message.undo"].exists)
        try capture(app, named: "away-back-en-light")
    }

    /// The same states in Simplified Chinese, and in dark appearance.
    func testScreenshotsInChineseAndDark() throws {
        for cause in ["asleep", "asleep_battery", "lid_closed", "off", "closed", "not_answering", "phone_offline", "cant_connect"] {
            let app = launch(cause: cause, chinese: true)
            XCTAssertTrue(app.buttons["chat.away.line"].firstMatch.waitForExistence(timeout: 15), cause)
            try capture(app, named: "away-\(cause)-zh-light")
            app.terminate()
        }
        for phase in ["long", "back"] {
            for chinese in [false, true] {
                for appearance in ["light", "dark"] where !(phase != "back" && appearance == "light" && !chinese) {
                    let app = launch(cause: "asleep", phase: phase, chinese: chinese, appearance: appearance)
                    let id = phase == "back" ? "chat.away.back" : "chat.away.line"
                    XCTAssertTrue(app.descendants(matching: .any)[id].firstMatch.waitForExistence(timeout: 15))
                    try capture(app, named: "away-\(phase)-\(chinese ? "zh" : "en")-\(appearance)")
                    app.terminate()
                }
            }
        }
        for chinese in [false, true] {
            let app = launch(cause: "asleep", chinese: chinese, appearance: "dark")
            XCTAssertTrue(app.buttons["chat.away.line"].firstMatch.waitForExistence(timeout: 15))
            try capture(app, named: "away-asleep-\(chinese ? "zh" : "en")-dark")
            if chinese {
                let bubble = app.descendants(matching: .any).matching(identifier: "chat.message.waiting").element(boundBy: 1)
                bubble.press(forDuration: 1.2)
                XCTAssertTrue(app.buttons["拷贝"].waitForExistence(timeout: 5))
                try capture(app, named: "away-longpress-zh-dark")
            }
            app.terminate()
        }
        let app = launch(cause: "asleep", chinese: true)
        let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 15))
        app.buttons.matching(identifier: "chat.message.undo").element(boundBy: 0).tap()
        let deadline = Date().addingTimeInterval(5)
        while (composer.value as? String) != questions[0] && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.2)) }
        try capture(app, named: "away-undo-zh-light")
    }

    /// The "Your computer" sheet reads the goodbye reason; user_sleep and unknown keep today's text. The Keep awake
    /// offer shows because the seeded setting is off.
    func testTheComputerSheetSaysWhyItSlept() throws {
        let statuses = [
            "battery_floor": ("It went to sleep to save its battery (below 20%).", "为了省电（电量低于 20%）进入了睡眠。"),
            "low_power": ("Low Power Mode is on.", "低电量模式已开启。"),
            "lid_closed": ("Its lid is closed.", "盖子合上了。"),
        ]
        for (language, appearance) in [("en", "light"), ("zh", "light"), ("en", "dark"), ("zh", "dark")] {
            for reason in ["battery_floor", "low_power", "lid_closed", "user_sleep", "unknown"] {
                if appearance == "dark" && !["battery_floor", "user_sleep"].contains(reason) { continue }
                let app = XCUIApplication()
                if language == "zh" { app.launchArguments += ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"] }
                app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
                app.launchEnvironment["YANSU_UITEST_CONNECTION_CAUSE"] = "sleeping"
                app.launchEnvironment["YANSU_UITEST_GOODBYE_REASON"] = reason
                // The sheet says "Asleep since" only when the sleep time is known (TodayStore.ConnectionCause.firstLine).
                if statuses[reason] != nil { app.launchEnvironment["YANSU_UITEST_GOODBYE_AT"] = "2026-11-07T10:46:00Z" }
                app.launchEnvironment["YANSU_UITEST_KEEP_AWAKE"] = "off"
                app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
                app.launch()
                let line = app.buttons["today.connection.line"]
                XCTAssertTrue(line.waitForExistence(timeout: 15), reason)
                let todayLine = line.label
                line.tap()
                XCTAssertTrue(app.descendants(matching: .any)["today.connection.sheet"].firstMatch.waitForExistence(timeout: 5))
                let status = app.staticTexts["today.connection.status"]
                if let (english, chinese) = statuses[reason] {
                    XCTAssertTrue(status.waitForExistence(timeout: 5), reason)
                    XCTAssertTrue(status.label.hasSuffix(language == "zh" ? chinese : english), status.label)
                    XCTAssertTrue(status.label.hasPrefix(language == "zh" ? "从" : "Asleep since "), status.label)
                } else {
                    // No reason copy: the sheet keeps Today's own line, the cause and the time it was last heard.
                    XCTAssertTrue(status.waitForExistence(timeout: 5), reason)
                    XCTAssertEqual(status.label, todayLine, reason)
                }
                XCTAssertTrue(app.descendants(matching: .any)["today.connection.keepAwakeOffer"].firstMatch.exists)
                try capture(app, named: "sheet-\(reason)-\(language)-\(appearance)")
                app.terminate()
            }
        }
    }

    private func launch(cause: String, phase: String = "waiting", chinese: Bool = false,
                        appearance: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        if chinese { app.launchArguments += ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"] }
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "chat-away"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "chat"
        app.launchEnvironment["YANSU_UITEST_AWAY_CAUSE"] = cause
        app.launchEnvironment["YANSU_UITEST_AWAY_PHASE"] = phase
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
        app.launch()
        XCTAssertTrue(app.buttons["tab.chat"].waitForExistence(timeout: 15))
        return app
    }

    /// Attachments stay in the result bundle; YANSU_UITEST_SCREENSHOT_DIRECTORY also writes them to disk.
    private func capture(_ app: XCUIApplication, named name: String) throws {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty else { return }
        let directory = URL(fileURLWithPath: path, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"), options: .atomic)
    }
}
