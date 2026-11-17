import XCTest

/// P156 frames: the phone while the computer sleeps (PROTOCOL.md section 23). Each case launches a paired fixture
/// with the sleeping or held-send seed, waits on the element the state shows, and attaches a screenshot, light and dark.
/// YANSU_UITEST_SCREENSHOT_DIRECTORY also writes the PNGs to disk.
final class P156QueueUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    /// Today under the away line: "You can still ask and act. Isoform does it when your computer wakes."
    func testTodaySleepingLine() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-review", appearance: appearance, extra: [
                "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
            ])
            XCTAssertTrue(app.descendants(matching: .any)["today.connection.canStillAct"].firstMatch
                .waitForExistence(timeout: 20), "the sleeping line is missing (\(appearance))")
            try capture(app, named: "today-sleeping-line-\(appearance)")
            app.terminate()
        }
    }

    /// Chat: a message waiting for the computer reads "Waits for your computer · Undo".
    func testWaitingChatMessage() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "chat-away", appearance: appearance, extra: [
                "YANSU_UITEST_AWAY_CAUSE": "asleep", "YANSU_UITEST_AWAY_PHASE": "waiting", "YANSU_UITEST_TAB": "chat",
            ])
            let waiting = app.descendants(matching: .any).matching(identifier: "chat.message.waiting").firstMatch
            XCTAssertTrue(waiting.waitForExistence(timeout: 20), "no waiting message (\(appearance))")
            try capture(app, named: "chat-waiting-\(appearance)")
            app.terminate()
        }
    }

    /// The 24-hour line in Chat: a message the computer did not take in a day. Under it, the line and Send again.
    func testTwentyFourHourLineInChat() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "chat-away", appearance: appearance, extra: [
                "YANSU_UITEST_AWAY_CAUSE": "asleep", "YANSU_UITEST_AWAY_PHASE": "long", "YANSU_UITEST_TAB": "chat",
            ])
            let again = app.buttons["chat.message.sendAgain"].firstMatch
            XCTAssertTrue(again.waitForExistence(timeout: 20), "the 24-hour line is missing in Chat (\(appearance))")
            XCTAssertTrue(app.staticTexts["This waited a day, so it wasn't sent."].firstMatch
                .waitForExistence(timeout: 5), "the 24-hour words are missing (\(appearance))")
            try capture(app, named: "day-line-chat-\(appearance)")
            app.terminate()
        }
    }

    /// Today's line for that send: one line under the away block, with Show, which opens Chat at it.
    func testTodayLineForAChatSendAfterADay() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-review", appearance: appearance, extra: [
                "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
                "YANSU_UITEST_CHAT_EXPIRED": "1",
            ])
            XCTAssertTrue(app.descendants(matching: .any)["today.expired.line"].firstMatch.waitForExistence(timeout: 20),
                          "the Today line is missing (\(appearance))")
            XCTAssertTrue(app.staticTexts["2 messages waited a day, so they weren't sent."].firstMatch
                .waitForExistence(timeout: 5), "the Today words are missing (\(appearance))")
            try capture(app, named: "day-line-today-\(appearance)")
            app.buttons["today.expired.show"].firstMatch.tap()
            XCTAssertTrue(app.buttons["tab.chat"].firstMatch.isSelected || app.buttons["chat.message.sendAgain"].firstMatch
                .waitForExistence(timeout: 10), "Show did not open Chat (\(appearance))")
            app.terminate()
        }
    }

    /// A reply the computer did not take in a day: Today lists it under Your work, saying so, with Send again
    /// (PROTOCOL.md section 23; it sat on its Tasks row until the Tasks tab went).
    func testHeldTasksReplyShowsItsLineAndSendAgain() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-review", appearance: appearance, extra: [
                "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
                "YANSU_UITEST_HELD_SEND": "expired",
            ])
            let line = app.staticTexts["This waited a day, so it wasn't sent."].firstMatch
            XCTAssertTrue(line.waitForExistence(timeout: 20), "the held reply has no line (\(appearance))")
            let again = app.buttons["Send again"].firstMatch
            XCTAssertTrue(again.waitForExistence(timeout: 5), "no Send again on the reply (\(appearance))")
            try capture(app, named: "tasks-held-reply-\(appearance)")
            again.tap()
            // Sent again, it is a new send: the line goes until the computer answers or a day passes.
            XCTAssertTrue(line.waitUntilGone(timeout: 15), "Send again did not clear the line (\(appearance))")
            app.terminate()
        }
    }

    /// Today's Show for a held reply scrolls Today to it.
    func testTodayShowOpensTasksAtTheHeldReply() throws {
        let app = launch(state: "feed-review", appearance: "light", extra: [
            "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
            "YANSU_UITEST_HELD_SEND": "expired",
        ])
        let show = app.buttons["today.expired.show"].firstMatch
        XCTAssertTrue(show.waitForExistence(timeout: 20), "the Today line is missing")
        show.tap()
        // The held reply's identifier carries its task id.
        let row = app.descendants(matching: .any).matching(NSPredicate(format: "identifier CONTAINS %@", "draft.ui-sow")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "Today does not list the held reply")
        let line = app.staticTexts["This waited a day, so it wasn't sent."].firstMatch
        XCTAssertTrue(line.waitForExistence(timeout: 5), "the reply's row lost its line")
        XCTAssertTrue(line.isHittable, "Show did not scroll Today to the reply")
        app.terminate()
    }

    /// Today's Show for a Chat send the computer did not take scrolls Chat to that message.
    func testTodayShowScrollsChatToTheMessage() throws {
        let app = launch(state: "feed-review", appearance: "light", extra: [
            "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
            "YANSU_UITEST_CHAT_EXPIRED": "1",
        ])
        let show = app.buttons["today.expired.show"].firstMatch
        XCTAssertTrue(show.waitForExistence(timeout: 20), "the Today line is missing")
        show.tap()
        let message = app.staticTexts["Can you move my 3 PM with Dana to Friday?"].firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 10), "Chat did not open")
        XCTAssertTrue(message.isHittable, "Chat did not scroll to the message")
        app.terminate()
    }

    /// Today names a single held reply in Tasks; Show opens Tasks at it (PROTOCOL.md section 23).
    func testTodayNamesTheHeldReply() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-review", appearance: appearance, extra: [
                "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
                "YANSU_UITEST_HELD_SEND": "expired", "YANSU_UITEST_HELD_PERSON": "Sam Lee",
            ])
            let line = app.staticTexts["Your reply to Sam\u{00A0}Lee waited a day, so it wasn't sent."].firstMatch
            XCTAssertTrue(line.waitForExistence(timeout: 20), "Today does not name the reply (\(appearance))")
            try capture(app, named: "today-named-reply-\(appearance)")
            app.buttons["today.expired.show"].firstMatch.tap()
            let row = app.descendants(matching: .any).matching(NSPredicate(format: "identifier CONTAINS %@", "draft.ui-sow")).firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 10), "Show did not open Tasks at the reply (\(appearance))")
            app.terminate()
        }
    }

    /// Chat's away line and Today's line name the same sleep time, because both read one goodbye time.
    func testSettledChatLineDropsItsTimeWhileTodayKeepsIt() throws {
        // One goodbye time for both lines: the app reads it from here, as Chat's stretch start and as Today's goodbye.
        let slept = ISO8601DateFormatter().string(from: Date().addingTimeInterval(-26 * 3600))
        let app = launch(state: "feed-review", appearance: "light", extra: [
            "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off",
            "YANSU_UITEST_CHAT_EXPIRED": "1", "YANSU_UITEST_GOODBYE_AT": slept,
        ])
        let todayLine = app.buttons["today.connection.line"].firstMatch
        XCTAssertTrue(todayLine.waitForExistence(timeout: 20))
        let today = todayLine.label
        app.buttons["today.expired.show"].firstMatch.tap()
        let chatLine = app.buttons["chat.away.line"].firstMatch
        XCTAssertTrue(chatLine.waitForExistence(timeout: 10), "Chat did not open")
        let chat = chatLine.label
        // The clock digits: the formatter's space before AM/PM is a narrow no-break space.
        // The day is "yesterday" or the date, depending on the hour: 26 hours ago is two days ago before 2 AM.
        let pattern = "(yesterday|[A-Z][a-z]{2} [0-9]{1,2}) at [0-9]+:[0-9]+"
        func when(_ text: String) -> String? {
            guard let range = text.range(of: pattern, options: .regularExpression) else { return nil }
            return String(text[range])
        }
        XCTAssertNotNil(when(today), "Today does not say since when: \(today)")
        // v1.0.4: every message under Chat's line was not sent, so the line drops its promise and its time.
        XCTAssertTrue(chat.hasPrefix("Your computer is asleep."), chat)
        XCTAssertNil(when(chat), "Chat should drop its time once nothing under the line waits: \(chat)")
        app.terminate()
    }

    /// Chat's away line without the goodbye time is the timeless line, never "since" with the phone's own time.
    func testChatDropsItsPromiseOnceEveryMessageWasNotSent() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "chat-away", appearance: appearance, extra: [
                "YANSU_UITEST_AWAY_CAUSE": "asleep", "YANSU_UITEST_AWAY_PHASE": "untimed", "YANSU_UITEST_TAB": "chat",
            ])
            let line = app.buttons["chat.away.line"].firstMatch
            XCTAssertTrue(line.waitForExistence(timeout: 20), "no away line (\(appearance))")
            XCTAssertTrue(line.label.hasPrefix("Your computer is asleep."), line.label)
            XCTAssertFalse(line.label.contains("answers"), line.label)
            XCTAssertFalse(line.label.contains("since"), line.label)
            try capture(app, named: "chat-settled-line-\(appearance)")
            app.terminate()
        }
    }

    /// Messages still waiting under the line: the full promise stays.
    func testChatKeepsTheFullLineWhileAMessageStillWaits() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "chat-away", appearance: appearance, extra: [
                "YANSU_UITEST_AWAY_CAUSE": "asleep", "YANSU_UITEST_AWAY_PHASE": "untimed-waiting", "YANSU_UITEST_TAB": "chat",
            ])
            let line = app.buttons["chat.away.line"].firstMatch
            XCTAssertTrue(line.waitForExistence(timeout: 20), "no away line (\(appearance))")
            XCTAssertTrue(line.label.hasPrefix("Your computer is asleep. Isoform answers when it wakes."), line.label)
            app.terminate()
        }
    }

    /// The ask-again prompt: "Your computer is back. Send the reply to Sam now?"
    func testAskAgainPrompt() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "tasks-seeded", appearance: appearance, extra: ["YANSU_UITEST_HELD_SEND": "confirm"])
            XCTAssertTrue(app.alerts.firstMatch.waitForExistence(timeout: 20), "the prompt is missing (\(appearance))")
            // v1.0.5: the prompt says the computer is back, true after sleep, after being off, or after Isoform opened.
            let back = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Your computer is back.")).firstMatch
            XCTAssertTrue(back.waitForExistence(timeout: 5), "the prompt does not say the computer is back (\(appearance))")
            try capture(app, named: "ask-again-prompt-\(appearance)")
            app.terminate()
        }
    }

    /// Needs you, under the Today sections: a proactive action queued while the computer sleeps says it waits.
    func testNeedsYouWaitsForComputer() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-review", appearance: appearance, extra: [
                "YANSU_UITEST_CONNECTION_CAUSE": "sleeping", "YANSU_UITEST_KEEP_AWAKE": "off", "YANSU_UITEST_TODAY_WAITS": "1",
            ])
            let waits = app.staticTexts.matching(NSPredicate(format: "label ==[c] %@", "Waits for your computer")).firstMatch
            XCTAssertTrue(waits.waitForExistence(timeout: 20), "the Needs-you wait line is missing (\(appearance))")
            for _ in 0..<8 where !waits.isHittable { app.swipeUp() }
            try capture(app, named: "needs-you-waits-\(appearance)")
            app.terminate()
        }
    }

    /// Talk about this brief while the computer sleeps: the ask sheet opens with the brief attached. Its answer waits for
    /// the computer, and the same message in Chat says "Waits for your computer".
    func testTalkAboutThisBriefWhileAsleep() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-conversation", appearance: appearance, extra: [
                "YANSU_UITEST_TAB": "feed", "YANSU_UITEST_BRIEF_TALK": "offline", "YANSU_UITEST_ASK_OFFLINE": "1",
            ])
            XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 20))
            app.buttons["feed.readAll"].tap()
            let talk = app.buttons["brief.talk"]
            for _ in 0..<12 where !talk.isHittable { app.swipeUp() }
            XCTAssertTrue(talk.waitForExistence(timeout: 10), "Talk about this brief is missing while asleep (\(appearance))")
            talk.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.opener"].waitForExistence(timeout: 10),
                          "Talk did not open the ask sheet (\(appearance))")
            XCTAssertFalse(app.descendants(matching: .any)["brief.talk.line"].firstMatch.exists,
                           "Talk showed the unreachable line instead of opening (\(appearance))")
            try capture(app, named: "talk-about-brief-asleep-\(appearance)")
            app.buttons["brief.ask.choice.opener:yes"].firstMatch.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.waiting"].firstMatch.waitForExistence(timeout: 10),
                          "the sheet does not say the question waits (\(appearance))")
            try capture(app, named: "ask-waits-for-computer-\(appearance)")
            // Open in Chat closes the sheet and opens the main chat with the brief attached.
            app.buttons["Open in Chat"].firstMatch.tap()
            let status = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Waits for your computer")).firstMatch
            XCTAssertTrue(status.waitForExistence(timeout: 10), "the Chat message does not say it waits (\(appearance))")
            try capture(app, named: "chat-waits-for-computer-\(appearance)")
            app.terminate()
        }
    }

    private func launch(state: String, appearance: String, extra: [String: String]) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
        for (key, value) in extra { app.launchEnvironment[key] = value }
        app.launch()
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

private extension XCUIElement {
    /// True once the element is gone, within the timeout.
    func waitUntilGone(timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while exists && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.2)) }
        return !exists
    }
}
