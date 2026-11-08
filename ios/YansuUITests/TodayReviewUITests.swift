import XCTest

final class TodayReviewUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    // P036: both a question chip and an answer button post in the Today sheet, never in Chat's composer.
    func testBriefCardsAskInSheetWithoutPrefillingChat() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            for (identifier, words, readAll) in [("brief.chip.0", "What changed in the project notes?", false),
                                                 ("brief.card.card-plan", "Draft a plan for the project notes from this brief.", true)] {
                let app = XCUIApplication()
                app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
                app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
                app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
                app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
                app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
                app.launch()
                XCTAssertTrue(app.buttons["brief.chip.0"].waitForExistence(timeout: 15))
                if readAll { app.buttons["feed.readAll"].tap() }
                let card = app.buttons[identifier]
                revealInTapBand(card, in: app, swipes: 12)
                XCTAssertTrue(card.waitForExistence(timeout: 10))
                card.tap()
                let question = app.staticTexts["brief.ask.question"]
                XCTAssertTrue(question.waitForExistence(timeout: 15))
                XCTAssertEqual(question.label, words)
                let answer = app.staticTexts["brief.ask.answer"]
                XCTAssertTrue(answer.waitForExistence(timeout: 15))
                let complete = NSPredicate(format: "label == %@", "The project notes are ready for your review.")
                XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: complete, object: answer)], timeout: 10), .completed)
                attachScreenshot(app, named: "today-card-ask-\(readAll ? "answer" : "chip")-\(language)-\(appearance)")
                app.buttons["brief.ask.done"].tap()
                XCTAssertTrue(card.waitForExistence(timeout: 5))
                if !readAll {
                    let answered = NSPredicate(format: "value == %@", language == "en" ? "Answered" : "已回复")
                    XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: answered, object: card)], timeout: 10), .completed)
                    attachScreenshot(app, named: "today-answered-chip-\(language)-\(appearance)")
                }
                card.tap()
                XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 10))
                XCTAssertEqual(app.staticTexts["brief.ask.answer"].label, "The project notes are ready for your review.")
                app.buttons["brief.ask.done"].tap()
                if readAll { app.navigationBars.buttons.firstMatch.tap() }
                app.buttons["tab.chat"].tap()
                let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
                XCTAssertTrue(composer.waitForExistence(timeout: 10))
                let composerValue = composer.value as? String ?? ""
                XCTAssertTrue(composerValue.isEmpty || composerValue == composer.placeholderValue || composerValue == "Message Isoform",
                              "an Ask must leave the Chat composer empty")
                XCTAssertEqual(app.staticTexts.matching(NSPredicate(format: "label == %@", words)).count, 1)
                app.terminate()
            }
        }
    }

    func testTalkOpensOnQuestionWithAnswerChips() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 15))
            app.buttons["feed.readAll"].tap()
            let talk = app.buttons["brief.talk"]
            revealInTapBand(talk, in: app, swipes: 12)
            XCTAssertTrue(talk.waitForExistence(timeout: 10))
            talk.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.opener"].waitForExistence(timeout: 10))
            let first = app.buttons["brief.ask.choice.opener:yes"]
            let other = app.buttons["brief.ask.choice.opener:other"]
            XCTAssertTrue(first.waitForExistence(timeout: 5))
            XCTAssertTrue(other.exists)
            attachScreenshot(app, named: "today-talk-opener-\(language)-\(appearance)")
            first.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.question"].waitForExistence(timeout: 10))
            XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 15))
            app.terminate()
        }
    }

    func testSavedCardAnswerCanBeRefreshed() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
        app.launch()
        let chip = app.buttons["brief.chip.0"]
        XCTAssertTrue(chip.waitForExistence(timeout: 15))
        chip.tap()
        XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 15))
        app.buttons["brief.ask.done"].tap()
        chip.tap()
        let refresh = app.buttons["brief.ask.refresh"]
        XCTAssertTrue(refresh.waitForExistence(timeout: 10))
        refresh.tap()
        XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 15))
        app.buttons["brief.ask.done"].tap()
        app.buttons["tab.chat"].tap()
        let questions = app.staticTexts.matching(NSPredicate(format: "label == %@", "What changed in the project notes?"))
        let twoQuestions = NSPredicate(format: "count == 2")
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: twoQuestions, object: questions)], timeout: 15), .completed)
    }

    func testYourWeekAsksAndStoresNotNowWithoutOfferingHold() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-your-week"
            app.launchEnvironment["YANSU_UITEST_SETTINGS_ID"] = UUID().uuidString
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let chip = app.buttons["brief.yourWeek.chip.open-launch"]
            revealInTapBand(chip, in: app, swipes: 12)
            XCTAssertTrue(chip.waitForExistence(timeout: 15))
            chip.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.question"].waitForExistence(timeout: 15))
            XCTAssertEqual(app.staticTexts["brief.ask.question"].label, "Review the launch plan from this week.")
            XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 15))
            attachScreenshot(app, named: "today-week-ask-\(language)-\(appearance)")
            app.buttons["brief.ask.done"].tap()
            let notNow = app.buttons["brief.yourWeek.notNow"]
            revealInTapBand(notNow, in: app, swipes: 12)
            XCTAssertTrue(notNow.waitForExistence(timeout: 10))
            XCTAssertFalse(app.buttons["brief.yourWeek.ask"].exists, "calendar holds remain read-only")
            XCTAssertTrue(app.staticTexts["brief.yourWeek.holdReadOnly"].exists)
            attachScreenshot(app, named: "today-week-readonly-\(language)-\(appearance)")
            notNow.tap()
            let gone = NSPredicate(format: "exists == false")
            XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: gone, object: notNow)], timeout: 10), .completed)
            app.terminate()
            app.launch()
            XCTAssertTrue(app.staticTexts["brief.yourWeek.title"].waitForExistence(timeout: 15))
            XCTAssertFalse(app.buttons["brief.yourWeek.notNow"].exists)
            app.terminate()
        }
    }

    func testYourWeekTopicRecommendationAsksInSheet() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-your-week"
            app.launchEnvironment["YANSU_UITEST_SETTINGS_ID"] = UUID().uuidString
            app.launchEnvironment["YANSU_UITEST_WEEK_TOPIC"] = "1"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let ask = app.buttons["brief.yourWeek.ask"]
            revealInTapBand(ask, in: app, swipes: 12)
            XCTAssertTrue(ask.waitForExistence(timeout: 15))
            ask.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.question"].waitForExistence(timeout: 15))
            XCTAssertEqual(app.staticTexts["brief.ask.question"].label, "Help me plan next week for the launch.")
            XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 15))
            attachScreenshot(app, named: "today-week-topic-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testParagraphAskStreamsInSheetAndQueuesOffline() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                    "-AppleInterfaceStyle", appearance]
            app.launch()
            let paragraph = app.buttons["brief.paragraph.0"]
            XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
            paragraph.tap()
            let field = app.descendants(matching: .any)["brief.ask.field"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 10))
            XCTAssertTrue(app.staticTexts["brief.ask.quote"].exists)
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
            attachScreenshot(app, named: "today-ask-field-\(language)-\(appearance)")
            field.typeText("What happened?")
            app.buttons["brief.ask.send"].tap()
            let answer = app.staticTexts["brief.ask.answer"]
            XCTAssertTrue(answer.waitForExistence(timeout: 15))
            XCTAssertEqual(answer.label, "The project notes are ready")
            let complete = NSPredicate(format: "label == %@", "The project notes are ready for your review.")
            XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: complete, object: answer)], timeout: 10), .completed)
            XCTAssertTrue(app.buttons["tab.today"].isSelected || !app.buttons["tab.chat"].isSelected)
            attachScreenshot(app, named: "today-ask-answer-\(language)-\(appearance)")
            app.buttons["brief.ask.done"].tap()
            XCTAssertTrue(paragraph.waitForExistence(timeout: 5))
            app.buttons["tab.chat"].tap()
            XCTAssertTrue(app.staticTexts["The project notes are ready for your review."].waitForExistence(timeout: 10))
            let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
            XCTAssertTrue(composer.waitForExistence(timeout: 5))
            XCTAssertNotEqual(composer.value as? String, "What happened?")
            app.buttons["tab.today"].tap()
            XCTAssertTrue(paragraph.waitForExistence(timeout: 5))
            paragraph.tap()
            XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 10))
            XCTAssertEqual(app.staticTexts["brief.ask.answer"].label, "The project notes are ready for your review.")
            app.terminate()
        }
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_ASK_OFFLINE"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let paragraph = app.buttons["brief.paragraph.0"]
            XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
            paragraph.tap()
            let field = app.descendants(matching: .any)["brief.ask.field"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 5))
            field.typeText("What happened?")
            app.buttons["brief.ask.send"].tap()
            XCTAssertTrue(app.staticTexts["brief.ask.waiting"].waitForExistence(timeout: 10))
            attachScreenshot(app, named: "today-ask-offline-\(language)-\(appearance)")
            app.buttons["brief.ask.undo"].tap()
            XCTAssertEqual(field.value as? String, "What happened?")
            app.terminate()
        }
    }

    func testParagraphAskStopKeepsThePartialAnswer() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let paragraph = app.buttons["brief.paragraph.0"]
            XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
            paragraph.tap()
            let field = app.descendants(matching: .any)["brief.ask.field"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 5))
            field.typeText("What happened?")
            app.buttons["brief.ask.send"].tap()
            let stop = app.buttons["brief.ask.stop"]
            XCTAssertTrue(stop.waitForExistence(timeout: 10))
            stop.tap()
            XCTAssertTrue(app.buttons["brief.ask.startOver"].waitForExistence(timeout: 5))
            XCTAssertEqual(app.staticTexts["brief.ask.answer"].label, "The project notes are ready")
            attachScreenshot(app, named: "today-ask-stopped-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testParagraphAskFailureKeepsWordsAndCanBeEdited() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_ASK_FAIL_ONCE"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let paragraph = app.buttons["brief.paragraph.0"]
            XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
            paragraph.tap()
            let field = app.descendants(matching: .any)["brief.ask.field"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 5))
            field.typeText("What happened?")
            app.buttons["brief.ask.send"].tap()
            let edit = app.buttons["brief.ask.edit"]
            XCTAssertTrue(edit.waitForExistence(timeout: 10))
            XCTAssertTrue(app.buttons["brief.ask.retry"].exists)
            XCTAssertEqual(app.staticTexts["brief.ask.question"].label, "What happened?")
            attachScreenshot(app, named: "today-ask-failed-\(language)-\(appearance)")
            edit.tap()
            XCTAssertEqual(field.value as? String, "What happened?")
            app.buttons["brief.ask.send"].tap()
            XCTAssertTrue(app.staticTexts["brief.ask.answer"].waitForExistence(timeout: 15))
            app.terminate()
        }
    }

    func testParagraphAskStreamFailureKeepsThePartialAnswer() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_ASK_STREAM_FAIL"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let paragraph = app.buttons["brief.paragraph.0"]
            XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
            paragraph.tap()
            let field = app.descendants(matching: .any)["brief.ask.field"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 5))
            field.typeText("What happened?")
            app.buttons["brief.ask.send"].tap()
            XCTAssertTrue(app.staticTexts["brief.ask.streamFailed"].waitForExistence(timeout: 15))
            XCTAssertEqual(app.staticTexts["brief.ask.answer"].label, "The project notes are ready")
            XCTAssertTrue(app.buttons["brief.ask.streamRetry"].exists)
            attachScreenshot(app, named: "today-ask-stream-failed-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testBriefFeedbackWordsAndUndo() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                    "-AppleInterfaceStyle", appearance]
            app.launch()
            let read = app.buttons["feed.readAll"]
            XCTAssertTrue(read.waitForExistence(timeout: 15))
            read.tap()
            XCTAssertFalse(app.buttons["tab.today"].exists, "the floating tab bar must not cover the brief")
            // Each brief item is one row with its own actions (brief.item, BriefContentView.itemRow).
            let card = app.descendants(matching: .any).matching(identifier: "brief.item")
                .containing(.button, identifier: "feed.notRight").firstMatch
            revealInTapBand(card, in: app, swipes: 12)
            XCTAssertTrue(card.isHittable)
            let useful = card.buttons["feed.like"]
            let notForMe = card.buttons["feed.notRight"]
            XCTAssertEqual(useful.label, language == "en" ? "Useful" : "有用")
            XCTAssertEqual(notForMe.label, language == "en" ? "Not for me" : "不适合我")
            attachScreenshot(app, named: "today-brief-feedback-\(language)-\(appearance)")
            notForMe.tap()
            let undo = app.buttons["brief.feedback.undo"].firstMatch
            XCTAssertTrue(undo.waitForExistence(timeout: 5))
            XCTAssertTrue(app.descendants(matching: .any)["brief.feedback.folded"].firstMatch.exists)
            attachScreenshot(app, named: "today-brief-feedback-undo-\(language)-\(appearance)")
            undo.tap()
            XCTAssertTrue(notForMe.waitForExistence(timeout: 5))
            let learned = app.descendants(matching: .any).matching(identifier: "brief.item")
                .containing(.staticText, identifier: "You prefer short replies in the morning").firstMatch
            revealInTapBand(learned, in: app, swipes: 8)
            XCTAssertTrue(learned.isHittable)
            XCTAssertTrue(learned.buttons.matching(NSPredicate(format: "label ENDSWITH 'sources' OR label ENDSWITH 'source'")).firstMatch.exists)
            let notQuite = learned.buttons["brief.notRight"]
            revealInTapBand(notQuite, in: app, swipes: 5)
            XCTAssertEqual(notQuite.label, language == "en" ? "Not quite" : "不太对")
            XCTAssertFalse(learned.buttons["feed.like"].exists)
            notQuite.tap()
            XCTAssertTrue(app.descendants(matching: .any)["brief.correction.field"].firstMatch.waitForExistence(timeout: 5))
            let cancel = app.buttons["brief.correction.cancel"].firstMatch
            revealInTapBand(cancel, in: app, swipes: 5)
            attachScreenshot(app, named: "today-feedback-correction-\(language)-\(appearance)")
            cancel.tap()
            app.terminate()
        }
    }

    func testFeedbackFlushesWhenLeavingBrief() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launch()
        let read = app.buttons["feed.readAll"]
        XCTAssertTrue(read.waitForExistence(timeout: 15))
        read.tap()
        let notForMe = app.buttons["feed.notRight"].firstMatch
        revealInTapBand(notForMe, in: app, swipes: 12)
        XCTAssertTrue(notForMe.isHittable)
        notForMe.tap()
        XCTAssertTrue(app.buttons["brief.feedback.undo"].firstMatch.waitForExistence(timeout: 5))
        app.navigationBars.buttons.firstMatch.tap()
        XCTAssertTrue(read.waitForExistence(timeout: 10))
        read.tap()
        let folded = app.descendants(matching: .any)["brief.feedback.folded"].firstMatch
        revealInTapBand(folded, in: app, swipes: 12)
        XCTAssertTrue(folded.waitForExistence(timeout: 10))
        XCTAssertFalse(folded.buttons["brief.feedback.undo"].exists)
    }

    func testMemoryCorrectionIsInline() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            for week in [false, true] {
                let app = XCUIApplication()
                app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = week ? "feed-your-week" : "feed"
                app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
                app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
                app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                        "-AppleInterfaceStyle", appearance]
                app.launch()
                if !week {
                    XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 15))
                    app.buttons["feed.readAll"].tap()
                }
                let correction = app.buttons[week ? "brief.yourWeek.notRight.learned-focus" : "brief.notRight"].firstMatch
                for _ in 0..<15 where !correction.isHittable { app.swipeUp() }
                XCTAssertTrue(correction.isHittable)
                XCTAssertEqual(correction.label, language == "en" ? "Not quite" : "不太对")
                correction.tap()
                let field = app.textFields["brief.correction.field"].firstMatch
                XCTAssertTrue(field.waitForExistence(timeout: 5))
                field.tap()
                field.typeText("Not accurate")
                XCTAssertFalse(app.buttons["tab.today"].exists, "the tab bar must step aside for the correction")
                let send = app.buttons["brief.correction.send"]
                for _ in 0..<5 where !send.isHittable { app.swipeUp() }
                if week { app.swipeUp() }
                attachScreenshot(app, named: "today-correction-\(week ? "week" : "brief")-\(language)-\(appearance)")
                send.tap()
                let undo = app.buttons["brief.correction.undo"].firstMatch
                XCTAssertTrue(undo.waitForExistence(timeout: 5))
                undo.tap()
                XCTAssertTrue(correction.exists)
                app.terminate()
            }
        }
    }

    func testFailedCorrectionKeepsItsWordsInPlace() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launchEnvironment["YANSU_UITEST_FAIL_CORRECTION"] = "1"
        app.launch()
        XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 15))
        app.buttons["feed.readAll"].tap()
        let learned = app.descendants(matching: .any).matching(identifier: "brief.item")
            .containing(.staticText, identifier: "You prefer short replies in the morning").firstMatch
        revealInTapBand(learned, in: app, swipes: 15)
        XCTAssertTrue(learned.isHittable)
        let notQuite = learned.buttons["brief.notRight"]
        revealInTapBand(notQuite, in: app, swipes: 5)
        XCTAssertTrue(notQuite.isHittable)
        notQuite.tap()
        let field = app.descendants(matching: .any)["brief.correction.field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("This was a one-time exception")
        let send = app.buttons["brief.correction.send"].firstMatch
        revealInTapBand(send, in: app, swipes: 5)
        send.tap()
        XCTAssertTrue(app.buttons["brief.correction.undo"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(field.waitForExistence(timeout: 15))
        XCTAssertEqual(field.value as? String, "This was a one-time exception")
        XCTAssertTrue(app.staticTexts["Didn't save. Try again."].exists)
    }

    func testBriefSettingsAreRemoved() {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                    "-AppleInterfaceStyle", appearance]
            app.launch()
            openSettings(app)
            XCTAssertTrue(app.descendants(matching: .any)["settings.group.notifications"].waitForExistence(timeout: 10))
            XCTAssertFalse(app.descendants(matching: .any)["settings.group.brief"].exists)
            attachScreenshot(app, named: "b10-settings-\(language)-\(appearance)")
            app.terminate()
        }
    }

    /// Notifications: until the computer answers, the page shows only its line and Try again. The choices, including
    /// the one delivery test, appear once it answers (1ccd20f2a). This fixture has no computer answer, so it guards that rule.
    func testNotificationDiagnosticHasOneDeliveryTest() {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "paired"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                    "-AppleInterfaceStyle", appearance]
            app.launch()
            openSettings(app)
            let preferences = app.buttons["settings.notifications.preferences"].firstMatch
            revealInTapBand(preferences, in: app, swipes: 6)
            XCTAssertTrue(preferences.waitForExistence(timeout: 10))
            preferences.tap()
            XCTAssertTrue(app.buttons["notifications.refresh"].waitForExistence(timeout: 10))
            XCTAssertFalse(app.buttons["notifications.deliveryTest"].exists, "the delivery test waits for the computer")
            XCTAssertFalse(app.buttons["settings.testNotification"].exists)
            attachScreenshot(app, named: "notification-diagnostic-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testConnectionCausesAndComputerSheet() {
        let cases = [
            ("sleeping", "Computer asleep", "电脑睡眠中"),
            ("shutting_down", "Computer shut down", "电脑已关机"),
            ("closing", "Isoform closed", "Isoform 已关闭"),
            ("not_answering", "Computer not answering", "电脑没有响应"),
            ("phone_offline", "iPhone offline", "iPhone 离线"),
            ("cant_connect", "Can't connect", "无法连接"),
        ]
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            for (cause, english, chinese) in cases {
                let app = XCUIApplication()
                app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
                app.launchEnvironment["YANSU_UITEST_CONNECTION_CAUSE"] = cause
                app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
                app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                        "-AppleInterfaceStyle", appearance]
                app.launch()
                let line = app.buttons["today.connection.line"]
                XCTAssertTrue(line.waitForExistence(timeout: 15), cause)
                XCTAssertTrue(line.label.contains(language == "en" ? english : chinese), line.label)
                attachScreenshot(app, named: "connection-\(cause)-\(language)-\(appearance)")
                line.tap()
                XCTAssertTrue(app.descendants(matching: .any)["today.connection.sheet"].firstMatch.waitForExistence(timeout: 5))
                attachScreenshot(app, named: "connection-sheet-\(cause)-\(language)-\(appearance)")
                app.terminate()
            }
        }
    }

    // P023: Review stays on Today and shows the actual draft and its actions.
    func testDraftReviewAndPastBriefs() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language,
                                    "-AppleInterfaceStyle", appearance]
            app.launch()
            XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 15))
            attachScreenshot(app, named: "today-review-\(language)-\(appearance)")
            let review = app.buttons["today.review.draft.ui-sow"]
            revealInTapBand(review, in: app, swipes: 5)
            XCTAssertTrue(review.isHittable)
            review.tap()
            XCTAssertTrue(app.descendants(matching: .any)["tasks.thread.draft"].firstMatch.waitForExistence(timeout: 10))
            XCTAssertTrue(app.buttons["tasks.draft.send"].exists)
            XCTAssertTrue(app.buttons["tasks.draft.edit"].exists)
            XCTAssertTrue(app.buttons["tab.today"].isSelected || !app.buttons["tab.chat"].isSelected)
            attachScreenshot(app, named: "today-draft-\(language)-\(appearance)")
            app.buttons["today.review.done"].tap()
            let row = app.descendants(matching: .any)["tasks.row.review-ui"].firstMatch
            revealInTapBand(row, in: app, swipes: 5)
            XCTAssertTrue(row.isHittable)
            attachScreenshot(app, named: "today-sections-\(language)-\(appearance)")
            row.tap()
            XCTAssertTrue(app.descendants(matching: .any)["tasks.thread"].firstMatch.waitForExistence(timeout: 10))
            attachScreenshot(app, named: "today-task-thread-\(language)-\(appearance)")
            app.navigationBars.buttons.element(boundBy: 0).tap()
            let done = app.descendants(matching: .any)["today.doneForYou"].firstMatch
            revealInTapBand(done, in: app, swipes: 5)
            XCTAssertTrue(done.waitForExistence(timeout: 10))
            let next = app.buttons["today.day.next"]
            let chatTab = app.buttons["tab.chat"]
            for _ in 0..<8 {
                if next.isHittable && (!chatTab.exists || next.frame.maxY < chatTab.frame.minY) { break }
                app.swipeUp()
            }
            XCTAssertTrue(next.isHittable)
            XCTAssertTrue(!chatTab.exists || next.frame.maxY < chatTab.frame.minY)
            next.tap()
            XCTAssertTrue(app.descendants(matching: .any)["today.meeting.sheet"].firstMatch.waitForExistence(timeout: 10))
            XCTAssertTrue(app.staticTexts["Send the launch outline"].waitForExistence(timeout: 10))
            attachScreenshot(app, named: "today-meeting-\(language)-\(appearance)")
            app.buttons["today.meeting.done"].tap()
            let topics = app.descendants(matching: .any)["feed.topics"].firstMatch
            revealInTapBand(topics, in: app, swipes: 5)
            XCTAssertTrue(topics.waitForExistence(timeout: 10))
            attachScreenshot(app, named: "today-day-topics-\(language)-\(appearance)")
            let past = app.buttons["feed.pastBriefs"]
            for _ in 0..<8 where !past.isHittable { app.swipeDown() }
            XCTAssertTrue(past.isHittable)
            past.tap()
            XCTAssertTrue(app.descendants(matching: .any)["pastBriefs.screen"].firstMatch.waitForExistence(timeout: 10))
            XCTAssertFalse(app.buttons["pastBriefs.library"].exists)
            attachScreenshot(app, named: "today-past-briefs-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testTaskRowOpensThreadAndPastBriefsHasNoLibrary() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launch()
        let row = app.descendants(matching: .any)["tasks.row.review-ui"].firstMatch
        revealInTapBand(row, in: app, swipes: 5)
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["tasks.thread"].firstMatch.waitForExistence(timeout: 10))
    }

    func testDraftSendCanBeUndoneAndQueuesOffline() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = launchDraft(offline: true, language: language, appearance: appearance)
            app.buttons["tasks.draft.send"].tap()
            let undo = app.buttons["today.draft.undo"]
            XCTAssertTrue(undo.waitForExistence(timeout: 5))
            undo.tap()
            XCTAssertTrue(app.buttons["tasks.draft.send"].waitForExistence(timeout: 5))
            app.buttons["tasks.draft.send"].tap()
            let queued = app.descendants(matching: .any)["today.draft.pending"].firstMatch
            XCTAssertTrue(queued.waitForExistence(timeout: 12))
            XCTAssertTrue(app.buttons["today.draft.undo"].exists)
            attachScreenshot(app, named: "today-draft-offline-\(language)-\(appearance)")
            app.buttons["today.draft.undo"].tap()
            XCTAssertTrue(app.buttons["tasks.draft.send"].waitForExistence(timeout: 5))
            app.terminate()
        }
    }

    func testDraftEditAndFailureKeepTheWords() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = launchDraft(failure: true, language: language, appearance: appearance)
            app.buttons["tasks.draft.edit"].tap()
            let editor = app.textViews["tasks.draft.editor"]
            XCTAssertTrue(editor.waitForExistence(timeout: 5))
            editor.tap()
            editor.typeText(" More context.")
            app.swipeUp()
            app.buttons["tasks.draft.send"].tap()
            XCTAssertTrue(app.staticTexts["tasks.draft.failed"].waitForExistence(timeout: 12))
            XCTAssertTrue((editor.value as? String ?? "").contains("More context."))
            attachScreenshot(app, named: "today-draft-failure-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testDraftSendCompletesAfterUndoWindow() throws {
        let app = launchDraft()
        app.buttons["tasks.draft.send"].tap()
        let undo = app.buttons["tasks.draft.undo"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        undo.tap()
        XCTAssertTrue(app.buttons["tasks.draft.send"].waitForExistence(timeout: 5))
        app.buttons["tasks.draft.send"].tap()
        XCTAssertTrue(undo.waitForExistence(timeout: 5))
        let gone = NSPredicate(format: "exists == false")
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: gone, object: undo)], timeout: 12), .completed)
        let sent = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Sent to Priya")).firstMatch
        XCTAssertTrue(sent.waitForExistence(timeout: 10))
    }

    func testWaitingCardOpensSheetWithItsSource() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let row = app.buttons["today.waiting.ui-waiting-on-reply"]
            revealInTapBand(row, in: app, swipes: 5)
            XCTAssertTrue(row.waitForExistence(timeout: 15))
            row.tap()
            XCTAssertTrue(app.descendants(matching: .any)["chat.proactive.item"].firstMatch.waitForExistence(timeout: 10))
            XCTAssertTrue(app.buttons["today.source.open"].exists)
            XCTAssertFalse(app.buttons["tab.chat"].isSelected)
            attachScreenshot(app, named: "today-waiting-source-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testMeetingFailureIsInline() throws {
        for (language, appearance) in [("en", "Light"), ("zh-Hans", "Dark")] {
            let app = XCUIApplication()
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launchEnvironment["YANSU_UITEST_MEETING_FAILURE"] = "1"
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
            app.launch()
            let next = app.buttons["today.day.next"]
            revealInTapBand(next, in: app, swipes: 8)
            XCTAssertTrue(next.waitForExistence(timeout: 15))
            next.tap()
            XCTAssertTrue(app.descendants(matching: .any)["today.meeting.failed"].firstMatch.waitForExistence(timeout: 10))
            attachScreenshot(app, named: "today-meeting-failure-\(language)-\(appearance)")
            app.terminate()
        }
    }

    func testMoreUsesTheSameCountAndOpensTasks() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launchEnvironment["YANSU_UITEST_EXTRA_WAITING"] = "1"
        app.launch()
        let more = app.buttons["today.moreInTasks"]
        revealInTapBand(more, in: app, swipes: 6)
        XCTAssertTrue(more.waitForExistence(timeout: 15))
        XCTAssertEqual(more.label, "1 more in Tasks")
        XCTAssertEqual(app.buttons["tab.tasks"].value as? String, "4 need you")
        more.tap()
        XCTAssertTrue(app.buttons["tab.tasks"].isSelected)
        XCTAssertTrue(app.staticTexts["tasks.subtitle"].label.contains("4 need you"))
    }

    private func launchDraft(offline: Bool = false, failure: Bool = false,
                             language: String = "en", appearance: String = "Light") -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-review"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "today"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-AppleInterfaceStyle", appearance]
        if offline { app.launchEnvironment["YANSU_UITEST_DRAFT_OFFLINE"] = "1" }
        if failure { app.launchEnvironment["YANSU_UITEST_DRAFT_FAILURE"] = "1" }
        app.launch()
        let review = app.buttons["today.review.draft.ui-sow"]
        revealInTapBand(review, in: app, swipes: 6)
        XCTAssertTrue(review.waitForExistence(timeout: 15))
        review.tap()
        XCTAssertTrue(app.buttons["tasks.draft.send"].waitForExistence(timeout: 10))
        return app
    }

    /// On iPhone 15 the bottom ~60 pt of the screen is a system zone where taps never reach an element. A tap at an
    /// element that is only hittable down there does nothing, so scroll until its middle is above that zone.
    private func revealInTapBand(_ element: XCUIElement, in app: XCUIApplication, swipes: Int) {
        for _ in 0..<swipes {
            if element.exists && element.isHittable && element.frame.midY < 760 { break }
            if element.exists && element.frame.midY < 100 { app.swipeDown(velocity: .slow) }
            else { app.swipeUp(velocity: .slow) }
        }
        // Let the scroll stop: a tap during the scroll's momentum is swallowed.
        var settled = element.frame.minY
        for _ in 0..<10 {
            Thread.sleep(forTimeInterval: 0.3)
            if element.frame.minY == settled { break }
            settled = element.frame.minY
        }
    }
}
