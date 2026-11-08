import XCTest

final class TasksUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testThreeTabsTasksBoardAndSettingsGroups() throws {
        let app = launchTasks()
        expandNeedsYou(app)
        XCTAssertTrue(app.buttons["tab.today"].exists)
        XCTAssertTrue(app.buttons["tab.chat"].exists)
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        XCTAssertTrue(app.buttons["root.settings"].exists)
        XCTAssertTrue(app.staticTexts["Review the onboarding flow"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.otherElements["tasks.needsYou"].exists)
        XCTAssertTrue(app.otherElements["tasks.running"].exists)
        XCTAssertTrue(app.otherElements["tasks.done"].exists)
        XCTAssertTrue(app.staticTexts["Up next"].exists)
        attachScreenshot(app, named: "tasks-light")

        app.buttons["root.settings"].tap()
        XCTAssertTrue(app.buttons["settings.done"].waitForExistence(timeout: 5))
        for group in ["you", "mac", "notifications", "privacy"] {
            XCTAssertTrue(app.descendants(matching: .any)["settings.group.\(group)"].exists, group)
        }
        XCTAssertTrue(app.switches["settings.notification.work_blocked"].exists)
        XCTAssertTrue(app.switches["settings.notification.brief_ready"].exists)
        XCTAssertTrue(app.switches["settings.notification.work_finished"].exists)
        attachScreenshot(app, named: "settings-light")
    }

    func testTasksAndSettingsInDarkAppearance() throws {
        let app = launchTasks(appearance: "Dark")
        expandNeedsYou(app)
        XCTAssertTrue(app.staticTexts["Review the onboarding flow"].waitForExistence(timeout: 10))
        attachScreenshot(app, named: "tasks-dark")
        app.buttons["root.settings"].tap()
        XCTAssertTrue(app.buttons["settings.done"].waitForExistence(timeout: 5))
        attachScreenshot(app, named: "settings-dark")
    }

    func testNeedsYouRowOpensItsThreadAndNothingGoesToChat() throws {
        let app = launchTasks()
        expandNeedsYou(app)
        XCTAssertTrue(app.descendants(matching: .any)["tasks.row.unread.review-ui"].waitForExistence(timeout: 10))
        app.staticTexts["Review the onboarding flow"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["tasks.thread.title"].exists)
        XCTAssertTrue(app.navigationBars.buttons["Tasks"].exists, "Back names where the thread came from")
        XCTAssertFalse(app.buttons["tab.tasks"].exists, "The tab bar steps away while a thread is open")
        let origin = app.staticTexts["tasks.thread.origin"]
        XCTAssertTrue(origin.waitForExistence(timeout: 5))
        XCTAssertTrue(origin.label.hasPrefix("From Up next · you tapped Do it at "))
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.status"].label.hasPrefix("Needs you"))
        attachScreenshot(app, named: "thread-needs-you")

        let choice = app.buttons["tasks.thread.choice.ship"]
        XCTAssertTrue(choice.waitForExistence(timeout: 5))
        choice.tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.you"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Ship it now"].exists, "The tap becomes your message")
        XCTAssertFalse(app.buttons["tasks.thread.choice.fix"].exists, "The buttons go once one is tapped")

        let reply = app.descendants(matching: .any)["tasks.thread.reply"].firstMatch
        XCTAssertTrue(reply.exists)
        XCTAssertEqual(reply.placeholderValue, "Reply about this task")
        reply.tap()
        reply.typeText("Keep the old frame for now")
        app.buttons["tasks.thread.send"].tap()
        XCTAssertTrue(app.staticTexts["Keep the old frame for now"].waitForExistence(timeout: 5))
        attachScreenshot(app, named: "thread-replied")

        app.buttons["tasks.thread.more"].tap()
        app.buttons["Mark as done"].tap()
        let outcome = app.descendants(matching: .any)["tasks.thread.outcome"]
        XCTAssertTrue(outcome.waitForExistence(timeout: 5))
        XCTAssertTrue(outcome.label.contains("You marked it done."))
        attachScreenshot(app, named: "thread-marked-done")

        app.navigationBars.buttons["Tasks"].tap()
        XCTAssertTrue(app.buttons["tab.tasks"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected, "Nothing pasted a prompt into Chat")
        XCTAssertFalse(app.buttons["tab.chat"].isSelected)
    }

    func testRunningThreadShowsStepsFilesAndItsMenu() throws {
        let app = launchTasks()
        let row = app.staticTexts["Run iOS compatibility tests"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.detail.steps"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Running · step 3 of 5"].exists)
        XCTAssertTrue(app.staticTexts["When it’s done"].exists)
        XCTAssertTrue(app.staticTexts["ios17-results.md"].exists)
        XCTAssertTrue(app.staticTexts["tasks.thread.origin"].label.hasPrefix("You asked in Chat · "))
        attachScreenshot(app, named: "thread-running")
        app.buttons["tasks.thread.more"].tap()
        XCTAssertTrue(app.buttons["What this task made"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["Rename"].exists)
        XCTAssertTrue(app.buttons["Stop"].exists)
        XCTAssertFalse(app.buttons["Mark as done"].exists)
        attachScreenshot(app, named: "thread-running-menu")
    }

    func testStopFailureStaysInTheThreadAndCanRetry() throws {
        let app = launchTasks(ownerCheck: "allow", failStop: true)
        let row = app.staticTexts["Run iOS compatibility tests"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()
        app.buttons["tasks.thread.more"].tap()
        app.buttons["Stop"].tap()
        // The confirmation is a dialog, not a sheet: wait for its text, then tap the Stop inside it.
        XCTAssertTrue(app.staticTexts["Stop this task? Isoform stops working on it on your computer."].waitForExistence(timeout: 5))
        app.buttons["Stop"].firstMatch.tap()
        let failure = app.descendants(matching: .any)["tasks.thread.stopFailure"]
        XCTAssertTrue(failure.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "thread-stop-failure")
        failure.buttons["Try again"].tap()
        XCTAssertTrue(app.buttons["tab.tasks"].waitForExistence(timeout: 10))
    }

    func testDoneRowsShowTheirOutcomeNeverABareDone() throws {
        let app = launchTasks()
        let done = app.otherElements["tasks.done"]
        XCTAssertTrue(done.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["The Today tab builds and its tests pass."].exists)
        XCTAssertFalse(app.staticTexts["An older task with no outcome"].exists)
        app.staticTexts["Build the Today tab"].tap()
        let outcome = app.descendants(matching: .any)["tasks.thread.outcome"]
        XCTAssertTrue(outcome.waitForExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.status"].label.hasPrefix("Done · "))
        attachScreenshot(app, named: "thread-done")
    }

    func testThreadInDarkAppearance() throws {
        let app = launchTasks(appearance: "Dark")
        let row = app.staticTexts["Review the onboarding flow"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()
        XCTAssertTrue(app.buttons["tasks.thread.choice.ship"].waitForExistence(timeout: 5))
        attachScreenshot(app, named: "thread-dark")
    }

    func testUpNextShowsWhatIsoformSawWithItsSourceAndTime() throws {
        let app = launchTasks()
        XCTAssertTrue(app.staticTexts["I can tidy the project notes"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["tasks.offer.evidence"].exists)
        XCTAssertEqual(app.staticTexts["tasks.offer.evidence"].label,
                       "You opened the project notes three times this week and left them unsorted.")
        let source = app.descendants(matching: .any)["tasks.offer.source"].firstMatch
        XCTAssertTrue(source.label.hasPrefix("From Claude Code · "), source.label)
        XCTAssertFalse(source.label.contains("Why"))
        XCTAssertEqual(app.staticTexts["tasks.offer.kind"].label.uppercased(), "CLAUDE CODE")
        attachScreenshot(app, named: "up-next-evidence")
    }

    func testDoItStartsAtOnceWithUndoThatPutsTheCardBack() throws {
        let app = launchTasks(ownerCheck: "allow")
        let title = "I can tidy the project notes"
        XCTAssertTrue(app.staticTexts[title].waitForExistence(timeout: 10))
        app.buttons["tasks.offer.doIt"].tap()
        // Undo is offered for 5 seconds before the start commits (TasksModels, DecisionHoldCoordinator), so tap it
        // straight away: the screenshot and the checks come after the undo.
        let undo = app.buttons["tasks.starting.undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        undo.tap()
        attachScreenshot(app, named: "do-it-undone")
        XCTAssertTrue(app.buttons["tasks.offer.doIt"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts[title].waitForExistence(timeout: 5), "Undo puts the card back")
        XCTAssertTrue(undo.waitForNonExistence(timeout: 5), "the started row lets go of Undo")
    }

    func testScheduledAndDoneRowsFollowTheDesign() throws {
        let app = launchTasks()
        let scheduled = app.otherElements["tasks.scheduled"]
        XCTAssertTrue(scheduled.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Evening brief"].exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Today, ' OR label BEGINSWITH 'Tomorrow, '")).firstMatch.exists)
        XCTAssertEqual(app.staticTexts["tasks.subtitle"].label, "3 need you · 1 running")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Claude Code on your computer · step 3 of 5'")).firstMatch.exists)
        app.swipeUp()
        attachScreenshot(app, named: "tasks-scheduled-done")
        app.staticTexts["Evening brief"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.status"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.status"].label.hasPrefix("Scheduled · "))
        XCTAssertFalse(app.descendants(matching: .any)["tasks.thread.reply"].firstMatch.exists)
    }

    func testDraftCardSaysWhyWhoAndVoiceAndSendHasUndo() throws {
        let app = launchTasks()
        let row = app.staticTexts["Send Priya the signed SOW"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Draft ready"].exists || app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Draft ready'")).firstMatch.exists)
        row.tap()
        let draft = app.descendants(matching: .any)["tasks.thread.draft"].firstMatch
        XCTAssertTrue(draft.waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Send v3 now"].exists, "The earlier choice shows as your message")
        app.swipeUp()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.draft.why"].firstMatch.exists)
        XCTAssertEqual(app.staticTexts["tasks.draft.who"].label, "Only to Priya. Dan is on the thread and isn't copied.")
        XCTAssertEqual(app.staticTexts["tasks.draft.voice"].label, "Written like your last 6 replies to Priya: short, no greeting.")
        XCTAssertTrue(app.staticTexts["Reply · Mail on your computer"].exists)
        attachScreenshot(app, named: "draft-card")

        app.buttons["tasks.draft.send"].tap()
        let undo = app.buttons["tasks.draft.undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Sent to Priya'")).firstMatch.exists)
        attachScreenshot(app, named: "draft-sent-undo")
        undo.tap()
        XCTAssertTrue(app.buttons["tasks.draft.send"].waitForExistence(timeout: 3), "Undo brings the draft back, unsent")

        app.buttons["tasks.draft.send"].tap()
        let outcome = app.descendants(matching: .any)["tasks.thread.outcome"]
        XCTAssertTrue(outcome.waitForExistence(timeout: 10))
        XCTAssertTrue(outcome.label.contains("Priya has the signed SOW."))
        XCTAssertFalse(app.buttons["tasks.draft.undo"].exists)
        attachScreenshot(app, named: "draft-sent-done")
    }

    func testNotNowShowsUndoThenAdvancesTheOfferDeck() throws {
        let app = launchTasks(ownerCheck: "decline")
        let title = app.staticTexts["I can tidy the project notes"]
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        let notNow = app.buttons["tasks.offer.notNow"]
        let todayTab = app.buttons["tab.today"]
        XCTAssertTrue(notNow.isHittable)
        XCTAssertLessThan(notNow.frame.maxY, todayTab.frame.minY - 8)
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        notNow.tap()
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        let undo = app.buttons["Undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        undo.tap()
        XCTAssertTrue(title.waitForExistence(timeout: 5))
        app.buttons["tasks.offer.notNow"].tap()
        XCTAssertTrue(app.staticTexts["Explore local AI tools"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["tasks.findMore"].isHittable)
        app.buttons["tasks.findMore"].tap()
        XCTAssertTrue(app.staticTexts["Explore local AI tools"].waitForExistence(timeout: 10))
    }

    func testOfferIsVisibleOnTheFirstScreenAndRightSwipeStartsRunningWithoutChat() throws {
        let app = launchTasks()
        let offer = app.otherElements["tasks.offer"]
        XCTAssertTrue(offer.waitForExistence(timeout: 10))
        assertOfferFits(app)
        attachScreenshot(app, named: "tasks-first-screen")
        offer.swipeRight()
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        let running = app.otherElements["tasks.running"]
        XCTAssertTrue(running.staticTexts["I can tidy the project notes"].waitForExistence(timeout: 10))
    }

    func testMotionCaptureSequence() throws {
        let app = launchTasks(ownerCheck: "allow", motionCapture: true)
        let offer = app.otherElements["tasks.offer"]
        XCTAssertTrue(offer.waitForExistence(timeout: 10))
        assertOfferFits(app)
        Thread.sleep(forTimeInterval: 1.5)
        offer.swipeRight()
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        XCTAssertTrue(app.otherElements["tasks.running"].staticTexts["I can tidy the project notes"].waitForExistence(timeout: 10))
        Thread.sleep(forTimeInterval: 1)
        attachScreenshot(app, named: "motion-running")
        // Do it holds for five seconds with Undo before the work goes to the computer.
        let startedUndo = app.buttons["tasks.starting.undo"]
        let started = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: startedUndo)
        XCTAssertEqual(XCTWaiter.wait(for: [started], timeout: 8), .completed)
        offer.swipeLeft()
        let undo = app.buttons["Undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Organize the interview notes"].exists)
        undo.tap()
        XCTAssertTrue(app.staticTexts["Explore local AI tools"].waitForExistence(timeout: 5))
        Thread.sleep(forTimeInterval: 1)
        attachScreenshot(app, named: "motion-restored")
        offer.swipeLeft()
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        let committed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: undo)
        XCTAssertEqual(XCTWaiter.wait(for: [committed], timeout: 7), .completed)
        Thread.sleep(forTimeInterval: 1)
        app.buttons["tasks.findMore"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.findMore.placeholder"].waitForExistence(timeout: 3))
        attachScreenshot(app, named: "motion-finding-more")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'More arrive after '")).firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        Thread.sleep(forTimeInterval: 1.5)
        attachScreenshot(app, named: "motion-cooldown")
    }

    func testTasksInSimplifiedChinese() throws {
        let app = launchTasks(language: "zh-Hans")
        assertOfferFits(app)
        for label in ["3 项需要你 · 1 项进行中", "需要你", "接下来", "进行中", "定时任务", "本周完成", "晚间简报", "暂不", "就做这个"] {
            XCTAssertTrue(app.staticTexts[label].exists, label)
        }
        attachScreenshot(app, named: "tasks-zh-Hans")
        app.staticTexts["Review the onboarding flow"].tap()
        XCTAssertTrue(app.buttons["tasks.thread.choice.ship"].waitForExistence(timeout: 5))
        XCTAssertEqual(app.descendants(matching: .any)["tasks.thread.reply"].firstMatch.placeholderValue, "就这件事回复")
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.status"].label.hasPrefix("需要你"))
        XCTAssertTrue(app.staticTexts["tasks.thread.origin"].label.hasPrefix("来自“接下来” · 你在"))
        XCTAssertTrue(app.navigationBars.buttons["任务"].exists)
        attachScreenshot(app, named: "thread-zh-Hans")
    }

    func testTasksPlusOpensOneTimeWorkComposer() throws {
        let app = launchTasks()
        let add = app.buttons["tasks.addTask"]
        XCTAssertTrue(add.waitForExistence(timeout: 10))
        add.tap()
        XCTAssertTrue(app.navigationBars["Give Isoform a task"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["oneTimeWork.start"].exists)
    }

    private func launchTasks(ownerCheck: String? = nil, appearance: String = "Light", language: String = "en", failStop: Bool = false,
                             motionCapture: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "tasks"
        app.launchArguments += ["-AppleInterfaceStyle", appearance, "-AppleLanguages", "(\(language))",
                                "-AppleLocale", language == "zh-Hans" ? "zh_CN" : "en_US"]
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        if failStop { app.launchEnvironment["YANSU_UITEST_FAIL_STOP"] = "1" }
        if motionCapture { app.launchEnvironment["YANSU_UITEST_MOTION_CAPTURE"] = "1" }
        if let ownerCheck { app.launchEnvironment["YANSU_UITEST_OWNER_CHECK"] = ownerCheck }
        app.launch()
        XCTAssertTrue(app.buttons["tab.tasks"].waitForExistence(timeout: 15))
        return app
    }

    private func expandNeedsYou(_ app: XCUIApplication) {
        XCTAssertTrue(app.otherElements["tasks.needsYou"].waitForExistence(timeout: 10))
    }

    private func assertOfferFits(_ app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        let window = app.windows.firstMatch.frame
        let tabTop = app.buttons["tab.tasks"].frame.minY
        for id in ["tasks.offer.doIt", "tasks.offer.notNow"] {
            let button = app.buttons[id]
            XCTAssertTrue(button.waitForExistence(timeout: 10), file: file, line: line)
            XCTAssertTrue(button.isHittable, file: file, line: line)
            XCTAssertTrue(window.contains(button.frame), "\(id): \(button.frame) outside \(window)", file: file, line: line)
            XCTAssertLessThan(button.frame.maxY, tabTop, "\(id) overlaps tabs", file: file, line: line)
        }
        let card = app.otherElements["tasks.offer"].frame
        XCTAssertTrue(window.contains(card), "Card outside window: \(card)", file: file, line: line)
        XCTAssertLessThan(card.maxY, tabTop, "Card overlaps tabs", file: file, line: line)
        // Not now and Do it sit together at the card's foot, Do it last (Phone Threads §2).
        let notNow = app.buttons["tasks.offer.notNow"].frame
        let doIt = app.buttons["tasks.offer.doIt"].frame
        XCTAssertLessThan(notNow.maxX, doIt.minX, file: file, line: line)
        XCTAssertEqual(doIt.maxX, card.maxX - 18, accuracy: 2, file: file, line: line)
        XCTAssertEqual(notNow.midY, doIt.midY, accuracy: 1, file: file, line: line)
    }

    private func waitForSelected(_ element: XCUIElement, timeout: TimeInterval = 5) -> Bool {
        let expectation = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "isSelected == true"),
            object: element
        )
        return XCTWaiter.wait(for: [expectation], timeout: timeout) == .completed
    }
}
