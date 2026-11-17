import XCTest

// The Feed, Ideas and Goals tabs over seeded cached briefs (AppEnvironment.settingsUITestEnvironment and
// FeedUITestSeed). No relay or hub is contacted.
final class FeedUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testTodayShowsCatchUpBriefFirst() throws {
        let app = launch(state: "feed-catchup")
        let brief = app.descendants(matching: .any)["feed.brief"].firstMatch
        XCTAssertTrue(brief.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["feed.brief.title"].exists)
        // Your work sits under the brief (phone-today v1.0.0 rule 8).
        let needs = app.descendants(matching: .any)["today.yourWork"].firstMatch
        XCTAssertTrue(needs.waitForExistence(timeout: 10))
        XCTAssertLessThan(brief.frame.minY, needs.frame.minY)
        XCTAssertFalse(app.staticTexts["Nothing new yet."].exists)
    }

    func testCachedBriefHasNoEmptyState() throws {
        let app = launch(state: "feed-empty")
        XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["Nothing new yet."].exists)
        XCTAssertFalse(app.descendants(matching: .any)["feed.skeleton"].exists)
    }

    func testOfflineAsOfAndNote() throws {
        let app = launch(state: "feed-offline", cause: "sleeping")
        // Out of reach: the date stays and the away line sits under it (phone-today v1.0.0 rule 7).
        XCTAssertTrue(app.buttons["today.connection.line"].waitForExistence(timeout: 15))
        // The line names the cause; the directions under it say what to do (P147).
        XCTAssertTrue(app.buttons["today.connection.line"].label.hasPrefix("Computer asleep"), app.buttons["today.connection.line"].label)
        XCTAssertTrue(app.staticTexts["Press any key on your computer. Isoform picks up where it left off."].exists, app.debugDescription)
    }

    func testPastBriefsShowLeads() throws {
        let app = launch(state: "feed")
        openPastBriefs(in: app)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Kept on this phone for 30 days")).firstMatch.waitForExistence(timeout: 10))
        // Each row carries its brief's lead in its label.
        let lead = app.buttons.matching(identifier: "pastBriefs.row").firstMatch
        XCTAssertTrue(lead.waitForExistence(timeout: 10))
        XCTAssertFalse(lead.label.isEmpty)
        XCTAssertFalse(app.staticTexts["Summary unavailable"].exists)
        app.buttons.matching(identifier: "pastBriefs.row").firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["brief.view"].firstMatch.waitForExistence(timeout: 10))
    }

    func testTopicCardOpensChatWithClearableFilter() throws {
        throw XCTSkip("The topic cards that opened Chat with a filter left Today (phone-today v1.0.0 rule 20); a topic opens from the brief, covered by testAParagraphTopicOpensItsTopicInDarkAndChinese.")
        let app = launchTopics()
        openTopicFromBrief(in: app)
        XCTAssertTrue(app.buttons["tab.chat"].isSelected)
        let chip = app.buttons["chat.topicFilter.clear"]
        XCTAssertTrue(chip.waitForExistence(timeout: 10))
        XCTAssertTrue(chip.label.contains("Halcyon ERP sync"))
        let composer = app.descendants(matching: .any)["topic.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        XCTAssertEqual(composer.label, "Ask about Halcyon ERP sync")
        XCTAssertTrue(composer.isHittable)
        XCTAssertLessThan(composer.frame.maxY, app.buttons["tab.chat"].frame.minY)
        chip.tap()
        XCTAssertFalse(chip.exists)
    }

    func testTopicComposerDoneDismissesKeyboardAndKeepsTabBar() throws {
        let app = launchTopics()
        openTopicFromBrief(in: app)
        let composer = app.descendants(matching: .any)["topic.composer"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        composer.tap()
        composer.typeText("Keep Nov 1 in the reply.")
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "the topic composer opens the keyboard")
        let done = app.buttons["composer.keyboard.done"]
        XCTAssertTrue(done.waitForExistence(timeout: 5), "the keyboard has Done, as Chat does")
        done.tap()
        XCTAssertFalse(keyboard.waitForExistence(timeout: 2), "Done puts the keyboard away")
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 5) && app.buttons["tab.today"].isHittable,
                      "the tab bar stays after Done")
        XCTAssertTrue(app.buttons["tab.chat"].isHittable, "the tab bar stays after Done")
        XCTAssertEqual(composer.value as? String, "Keep Nov 1 in the reply.", "Done keeps the typed words")
    }

    func testTodayScreenshots() throws {
        for appearance in ["Light", "Dark"] {
            let suffix = appearance.lowercased()
            let app = launchTopics(appearance: appearance)
            XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 10))
            app.swipeDown()
            app.swipeDown()
            attach(app, named: "today-reachable-\(suffix)")
            app.swipeUp()
            attach(app, named: "today-scrolled-\(suffix)")
            openTopicFromBrief(in: app)
            XCTAssertTrue(app.buttons["topic.readDraft"].waitForExistence(timeout: 10))
            attach(app, named: "chat-topic-\(suffix)")
            app.terminate()
            app.launch()
            openPastBriefs(in: app)
            XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Kept on this phone for 30 days")).firstMatch.waitForExistence(timeout: 10))
            attach(app, named: "past-briefs-\(suffix)")
            app.terminate()

            let offline = launch(state: "feed-offline", appearance: appearance)
            // Out of reach: the date stays and the away line sits under it (phone-today v1.0.0 rule 7).
            XCTAssertTrue(offline.buttons["today.connection.line"].waitForExistence(timeout: 15))
            attach(offline, named: "today-offline-\(suffix)")
            offline.terminate()

            let skeleton = launch(state: "feed-no-history", appearance: appearance)
            XCTAssertTrue(skeleton.descendants(matching: .any)["feed.skeleton"].firstMatch.waitForExistence(timeout: 10))
            skeleton.swipeDown()
            skeleton.swipeDown()
            attach(skeleton, named: "today-skeleton-\(suffix)")
            skeleton.terminate()
        }
        let chinese = launchTopics(language: "zh-Hans")
        XCTAssertTrue(chinese.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 10))
        chinese.swipeDown()
        chinese.swipeDown()
        attach(chinese, named: "today-zh-hans")
    }

    func testFeedShowsTodaysCardsOnceWithTheirKindsAndActions() throws {
        let app = launch(state: "feed")
        XCTAssertTrue(app.buttons["tab.today"].isSelected)
        XCTAssertTrue(app.buttons["root.settings"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["feed.refresh"].exists)
        let read = app.buttons["feed.readAll"].firstMatch
        reveal(read, in: app)
        XCTAssertTrue(read.exists)
        read.tap()
        XCTAssertTrue(app.descendants(matching: .any)["brief.view"].firstMatch.waitForExistence(timeout: 10))
        // Each item is its own row with its actions (2873aa234, P152), so the brief page has no Cards section.
        let row = app.descendants(matching: .any).matching(identifier: "brief.item").firstMatch
        reveal(row, in: app)
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["feed.like"].firstMatch.exists)
        XCTAssertTrue(app.buttons["feed.notRight"].firstMatch.exists)
    }

    func testTodayInDarkAppearance() throws {
        let app = launch(state: "feed", appearance: "Dark")
        XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 10))
        attach(app, named: "today-dark")
    }

    func testOfflineMemoryCorrectionQueuesWithUndo() throws {
        let app = launch(state: "feed-offline", ownerCheck: "allow")
        XCTAssertTrue(app.buttons["today.connection.line"].waitForExistence(timeout: 15))
        app.buttons["feed.readAll"].tap()
        // A remembered item's one action is Not quite, corrected inline on the brief page (P151).
        let notRight = app.buttons["brief.notRight"].firstMatch
        scrollIntoView(notRight, in: app)
        XCTAssertTrue(notRight.isHittable, app.debugDescription)
        notRight.tap()
        let field = app.descendants(matching: .any)["brief.correction.field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("This is about evenings")
        app.buttons["brief.correction.send"].tap()
        XCTAssertEqual(XCTWaiter.wait(for: [XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "exists == false"), object: field)], timeout: 10), .completed)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        let pending = app.descendants(matching: .any)["feed.pending"].firstMatch
        scrollIntoView(pending, in: app)
        XCTAssertTrue(pending.waitForExistence(timeout: 10))
        XCTAssertTrue(pending.buttons["Undo"].exists)
    }

    func testAnEmptyDayKeepsTheEarlierDays() throws {
        let app = launch(state: "feed-empty")
        XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["Nothing new yet."].exists)
        openPastBriefs(in: app)
        XCTAssertTrue(app.buttons.matching(identifier: "pastBriefs.row").firstMatch.waitForExistence(timeout: 10))
    }

    func testPastBriefsHasNoLibraryEntryPoint() throws {
        let app = launch(state: "feed")
        openPastBriefs(in: app)
        XCTAssertTrue(app.descendants(matching: .any)["pastBriefs.screen"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["pastBriefs.library"].exists)
    }

    func testHearYourBriefPlaysPausesAndKeepsProgressAcrossTabs() throws {
        let app = launch(state: "feed-audio", appearance: "Light")
        XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 10))
        app.buttons["feed.readAll"].tap()
        let play = app.buttons["brief.play"]
        XCTAssertTrue(play.waitForExistence(timeout: 10))
        XCTAssertEqual(play.label, "Hear your brief")
        let progress = app.sliders["brief.progress"]
        // The time line is read in words now (BriefSpokenTime); the slider's value keeps "0:12 / 0:30".
        let duration = progress
        XCTAssertTrue(progress.waitForExistence(timeout: 5))
        XCTAssertEqual(duration.value as? String, "0:00 / 0:30")
        attach(app, named: "hear-your-brief-light-ready")

        play.tap()
        let playing = NSPredicate(format: "label == %@", "Pause your brief")
        expectation(for: playing, evaluatedWith: play)
        waitForExpectations(timeout: 5)
        expectation(for: NSPredicate(format: "value != %@", "0:00 / 0:30"), evaluatedWith: duration)
        waitForExpectations(timeout: 5)
        let beforeNavigation = try elapsedSeconds(in: duration)
        leaveForChatAndComeBack(app)
        XCTAssertTrue(play.waitForExistence(timeout: 5))
        XCTAssertEqual(play.label, "Pause your brief", "Playback continues while another tab is open")
        expectation(for: NSPredicate { _, _ in
            ((try? self.elapsedSeconds(in: duration)) ?? -1) > beforeNavigation
        }, evaluatedWith: duration)
        waitForExpectations(timeout: 5)
        XCTAssertGreaterThan(try elapsedSeconds(in: duration), beforeNavigation)
        attach(app, named: "hear-your-brief-light-playing-after-navigation")

        play.tap()
        XCTAssertEqual(play.label, "Hear your brief")
        let pausedDuration = duration.value as? String
        let pausedProgress = progress.value as? String
        attach(app, named: "hear-your-brief-light-paused")

        leaveForChatAndComeBack(app)
        XCTAssertTrue(play.waitForExistence(timeout: 5))
        XCTAssertEqual(duration.value as? String, pausedDuration)
        XCTAssertEqual(progress.value as? String, pausedProgress)
        play.tap()
        expectation(for: playing, evaluatedWith: play)
        waitForExpectations(timeout: 5)
        play.tap()
    }

    func testTodayCardIsThePlayerWhileItPlays() throws {
        // The Today card has no waveform: while its brief plays, Play carries the progress and the mini player waits.
        let app = launch(state: "feed-audio", appearance: "Light")
        let play = app.buttons["brief.hearBrief"]
        XCTAssertTrue(play.waitForExistence(timeout: 10))
        play.tap()
        expectation(for: NSPredicate(format: "label == %@", "Pause your brief"), evaluatedWith: play)
        waitForExpectations(timeout: 5)
        expectation(for: NSPredicate { _, _ in
            let value = play.value as? String ?? ""
            return value.hasSuffix("/ 0:30") && !value.hasPrefix("0:00 ")
        }, evaluatedWith: play)
        waitForExpectations(timeout: 10)
        XCTAssertFalse(app.descendants(matching: .any)["brief.waveform"].firstMatch.exists)
        XCTAssertFalse(app.descendants(matching: .any)["player.mini"].exists)
        app.buttons["tab.chat"].tap()
        XCTAssertTrue(app.buttons["tab.chat"].isSelected)
    }

    /// Your topics left Today (phone-today v1.0.0 rule 20); a topic opens from its link in the brief.
    private func openTopicFromBrief(in app: XCUIApplication) {
        openBrief(in: app)
        let topic = app.buttons["brief.topic.1"]
        reveal(topic, in: app)
        XCTAssertTrue(topic.waitForExistence(timeout: 15))
        topic.tap()
    }

    /// Past briefs is in the brief card's ··· menu (phone-today v1.0.0 rule 2).
    private func openPastBriefs(in app: XCUIApplication) {
        let menu = app.buttons["feed.brief.menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        let past = app.buttons["feed.pastBriefs"].firstMatch
        XCTAssertTrue(past.waitForExistence(timeout: 5))
        past.tap()
    }

    /// The brief page hides the tab bar: back to Today, over to Chat and back, then Read all opens the brief again.
    private func leaveForChatAndComeBack(_ app: XCUIApplication) {
        app.navigationBars.buttons.firstMatch.tap()
        app.buttons["tab.chat"].tap()
        XCTAssertTrue(app.buttons["tab.chat"].isSelected)
        app.buttons["tab.today"].tap()
        openBrief(in: app)
    }

    private func elapsedSeconds(in duration: XCUIElement) throws -> Int {
        // The slider's value: "0:12 / 0:30".
        let stamp = try XCTUnwrap((duration.value as? String ?? "").split(separator: " ").first)
        let parts = stamp.split(separator: ":")
        XCTAssertEqual(parts.count, 2)
        guard parts.count == 2 else { throw NSError(domain: "BriefAudioUITest", code: 1) }
        return try XCTUnwrap(Int(parts[0])) * 60 + XCTUnwrap(Int(parts[1]))
    }

    func testHearYourBriefInDarkAppearance() throws {
        let app = launch(state: "feed-audio", appearance: "Dark")
        XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 10))
        app.buttons["feed.readAll"].tap()
        let play = app.buttons["brief.play"]
        XCTAssertTrue(play.waitForExistence(timeout: 10))
        XCTAssertEqual(play.label, "Hear your brief")
        XCTAssertTrue(app.sliders["brief.progress"].waitForExistence(timeout: 5))
        XCTAssertEqual(app.sliders["brief.progress"].value as? String, "0:00 / 0:30")
        attach(app, named: "hear-your-brief-dark-ready")
    }

    func testHearYourBriefExplainsWhenTheComputerIsStillRendering() throws {
        let app = launch(state: "feed-audio-rendering")
        XCTAssertTrue(app.buttons["feed.readAll"].waitForExistence(timeout: 10))
        app.buttons["feed.readAll"].tap()
        // The brief view explains missing audio when Play is tapped, not before.
        let play = app.buttons["brief.play"]
        XCTAssertTrue(play.waitForExistence(timeout: 10))
        play.tap()
        let status = app.descendants(matching: .any)["brief.audioStatus"]
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        XCTAssertTrue(status.staticTexts["Not available yet"].waitForExistence(timeout: 5))
        XCTAssertTrue(status.staticTexts["Audio is still rendering on your computer."].exists)
        XCTAssertFalse(app.sliders["brief.progress"].exists)
        attach(app, named: "hear-your-brief-rendering")
    }

    func testTodaysBriefOnTheFeedAsksCardWordsInASheet() throws {
        for (appearance, language) in [("Light", "en"), ("Dark", "en"), ("Light", "zh-Hans")] {
            let english = language == "en"
            let app = XCUIApplication()
            app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", english ? "en_US" : "zh_CN"]
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-conversation"
            app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
            app.launchEnvironment["YANSU_UITEST_BRIEF_ASK"] = "1"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
            app.launch()
            let name = "\(appearance.lowercased())-\(language)"
            // The card shows the brief's first lines (phone-today v1.0.0 rule 2); the cards are on the brief page.
            let words = app.descendants(matching: .any)["feed.brief.words"].firstMatch
            XCTAssertTrue(words.waitForExistence(timeout: 15))
            XCTAssertTrue(words.label.hasPrefix(english ? "Start with the project notes today." : "今天可以先整理项目笔记。"), words.label)
            XCTAssertFalse(app.descendants(matching: .any)["feed.prompt"].exists)
            app.buttons["feed.readAll"].tap()
            let button = app.buttons["brief.card.card-plan"]
            let talk = app.buttons["brief.talk"]
            scrollIntoView(talk, in: app)
            XCTAssertEqual(button.label, english ? "Draft a plan" : "拟一份计划")
            XCTAssertEqual(talk.label, english ? "Talk about this brief" : "聊聊这份简报")
            attach(app, named: "feed-brief-\(name)")

            button.tap()
            let question = app.staticTexts["brief.ask.question"]
            XCTAssertTrue(question.waitForExistence(timeout: 10), "an answer card asks in a sheet over Today")
            XCTAssertEqual(question.label, "Draft a plan for the project notes from this brief.")
            XCTAssertTrue(!app.buttons["tab.chat"].exists || !app.buttons["tab.chat"].isSelected, "the tap never jumps to Chat")
            attach(app, named: "feed-brief-card-asks-\(name)")
            app.terminate()
        }
    }

    // P036 and P114: a chip asks in a sheet; a paragraph opens a sheet with the paragraph quoted and a focused field.
    func testBriefChipAndParagraphTapAskInASheet() throws {
        for appearance in ["Light", "Dark"] {
            let app = launch(state: "feed-conversation", appearance: appearance)
            openBrief(in: app)
            let chip = app.buttons["brief.chip.0"]
            XCTAssertTrue(chip.waitForExistence(timeout: 15))
            XCTAssertEqual(chip.label, "Review the project notes")
            attach(app, named: "brief-chip-\(appearance.lowercased())")
            if appearance == "Light" {
                chip.tap()
                let question = app.staticTexts["brief.ask.question"]
                XCTAssertTrue(question.waitForExistence(timeout: 10))
                XCTAssertEqual(question.label, "What changed in the project notes?")
                XCTAssertFalse(app.buttons["tab.chat"].exists && app.buttons["tab.chat"].isSelected, "the chip tap never jumps to Chat")
            }
            app.terminate()
        }

        let app = launch(state: "feed-conversation")
        openBrief(in: app)
        let paragraph = app.buttons["brief.paragraph.0"]
        XCTAssertTrue(paragraph.waitForExistence(timeout: 15))
        XCTAssertEqual(paragraph.label, "Start with the project notes today.")
        // A tap shows the paragraph's actions (a second tap hides them); Reply about this opens the sheet.
        let reply = app.buttons["brief.askAbout.0"].firstMatch
        if !reply.exists { paragraph.tap() }
        XCTAssertTrue(reply.waitForExistence(timeout: 5))
        reply.tap()
        XCTAssertTrue(app.descendants(matching: .any)["brief.ask.field"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertEqual(app.staticTexts["brief.ask.quote"].label, "Start with the project notes today.")
        XCTAssertFalse(app.staticTexts["brief.ask.question"].exists, "nothing is asked until the person asks")
        XCTAssertFalse(app.buttons["tab.chat"].exists && app.buttons["tab.chat"].isSelected)
        app.terminate()
    }

    // P043: a Your week chip asks in a sheet; holds staying read-only is covered by TodayReviewUITests.
    func testYourWeekCardAsksInASheet() throws {
        for appearance in ["Light", "Dark"] {
            let app = launch(state: "feed-your-week", appearance: appearance)
            XCTAssertTrue(app.staticTexts["brief.yourWeek.title"].waitForExistence(timeout: 15))
            XCTAssertTrue(app.staticTexts["You made steady progress on the launch plan."].exists)
            XCTAssertTrue(app.staticTexts["Where your time went"].exists)
            XCTAssertTrue(app.staticTexts["Finished the launch outline"].exists)
            attach(app, named: "your-week-\(appearance.lowercased())")
            if appearance == "Light" {
                let chip = app.buttons["brief.yourWeek.chip.open-launch"]
                reveal(chip, in: app)
                XCTAssertTrue(app.staticTexts["Review the launch plan"].exists)
                XCTAssertTrue(chip.waitForExistence(timeout: 10))
                XCTAssertEqual(chip.label, "Review the plan")
                chip.tap()
                let question = app.staticTexts["brief.ask.question"]
                XCTAssertTrue(question.waitForExistence(timeout: 10))
                XCTAssertEqual(question.label, "Review the launch plan from this week.")
                XCTAssertFalse(app.buttons["tab.chat"].isSelected, "the chip tap never jumps to Chat")
            }
            app.terminate()
        }
    }

    // MARK: RC 1 topics

    func testTodaysBriefShowsTopicsQuestionsAndClosingAndKeepsCardsAndEarlier() throws {
        for appearance in ["Light", "Dark"] {
            let app = launchTopics(appearance: appearance)
            openBrief(in: app)
            XCTAssertTrue(app.buttons["brief.paragraph.0"].waitForExistence(timeout: 15))
            // This computer lists the brief's items, so the brief page shows no paragraph chips (review 2026-11-15).
            XCTAssertFalse(app.staticTexts["Swipe for more"].exists)
            // The topic is linked from the brief now; Your topics left Today (rule 20).
            let topic = app.buttons["brief.topic.1"]
            reveal(topic, in: app)
            XCTAssertTrue(topic.waitForExistence(timeout: 10))
            app.terminate()
        }
    }

    func testAQuestionAndAnAnswerOnlyFillTheBox() throws {
        throw XCTSkip("Chips and answer cards ask in a sheet over the brief now, never filling Chat's box (P036); TodayReviewUITests.testBriefCardsAskInSheetWithoutPrefillingChat covers it.")
        for (cardID, words) in [("brief.chip.0", "What changed in the SOW?"),
                                ("brief.card.answer-hold", "Hold Thursday 1:00 to 4:00 PM for the Meridian memo.")] {
            let app = launchTopics()
            if cardID == "brief.card.answer-hold" { app.buttons["Read all"].tap() }
            let button = app.buttons[cardID]
            scrollIntoView(button, in: app)
            XCTAssertTrue(button.waitForExistence(timeout: 15))
            button.tap()
            let composer = app.descendants(matching: .any)["chat.composer"].firstMatch
            XCTAssertTrue(composer.waitForExistence(timeout: 10))
            XCTAssertTrue(app.buttons["tab.chat"].isSelected)
            XCTAssertEqual(composer.value as? String, words)
            XCTAssertFalse(app.staticTexts[words].exists, "a tap sends nothing")
            attach(app, named: "feed-topics-in-box-\(cardID)")
            app.terminate()
        }
    }

    func testTheTopicScreenShowsTheFilteredMainChatAndSendsWithTheTopic() throws {
        // A declining owner check proves that neither Not now nor Send asks for one.
        let app = launchTopics(ownerCheck: "decline")
        openTopicFromBrief(in: app)
        let event = app.descendants(matching: .any)["topic.event"].firstMatch
        XCTAssertTrue(event.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Only by skipping the 2 open review comments on v0.3. Both touch invoice mapping, so they are worth keeping."].exists)
        let replyCard = app.otherElements["topic.replyCard"]
        XCTAssertTrue(replyCard.exists)
        XCTAssertFalse(app.staticTexts["Read the draft in Isoform on your computer."].exists)
        XCTAssertFalse(app.buttons["topic.sendReply"].exists, "Send stays hidden until the draft is visible")
        let faceID = NSPredicate(format: "label CONTAINS[c] 'Face ID' OR label CONTAINS[c] 'Touch ID' OR label CONTAINS[c] 'passcode'")
        XCTAssertEqual(app.descendants(matching: .any).matching(faceID).count, 0, "nothing on the screen mentions Face ID")
        app.buttons["topic.readDraft"].tap()
        XCTAssertTrue(app.staticTexts["To ops@example.com"].exists)
        XCTAssertTrue(app.staticTexts["Subject Halcyon ERP sync"].exists)
        XCTAssertTrue(app.staticTexts["Nov 1 still works for us. We can offer a staging preview on Oct 28."].exists)
        XCTAssertTrue(app.buttons["topic.sendReply"].exists)
        attach(app, named: "topic-light")

        let notNow = app.buttons["topic.notNow"]
        XCTAssertEqual(notNow.label, "Not now")
        notNow.tap()
        let result = app.staticTexts["topic.replyCard.result"]
        XCTAssertTrue(result.waitForExistence(timeout: 10))
        XCTAssertEqual(result.label, "Saved for later", "Not now went through with no owner check")
        XCTAssertFalse(notNow.exists, "an answered card offers nothing more")
        attach(app, named: "topic-not-now")

        let composer = app.descendants(matching: .any)["topic.composer"].firstMatch
        composer.tap()
        composer.typeText("Keep Nov 1 in the reply.")
        XCTAssertEqual(app.staticTexts["topic.composer.note"].label, "Sends to your chat with this topic attached.")
        app.buttons["topic.send"].tap()
        XCTAssertTrue(app.staticTexts["On it. I will keep Nov 1 in the reply."].waitForExistence(timeout: 15),
                      "Send sends to the chat with the topic, and Isoform's reply shows here")
        XCTAssertEqual(app.descendants(matching: .any).matching(faceID).count, 0)
        attach(app, named: "topic-sent")

        let earlier = app.buttons["topic.earlier"]
        app.swipeDown()
        if earlier.waitForExistence(timeout: 5) {
            earlier.tap()
            XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'opened the Halcyon thread'")).firstMatch
                .waitForExistence(timeout: 10), "an earlier page goes above")
        }
    }

    func testAParagraphTopicOpensItsTopicInDarkAndChinese() throws {
        let app = launchTopics(appearance: "Dark", language: "zh-Hans")
        let readBrief = app.buttons["feed.readAll"]
        reveal(readBrief, in: app)
        readBrief.tap()
        let topic = app.buttons["brief.topic.1"]
        reveal(topic, in: app)
        XCTAssertTrue(topic.waitForExistence(timeout: 15))
        XCTAssertEqual(topic.label, "Halcyon ERP sync")
        attach(app, named: "feed-topics-dark-zh")
        topic.tap()
        XCTAssertTrue(app.staticTexts["你的聊天，只显示这个话题。"].waitForExistence(timeout: 10))
        let read = app.buttons["topic.readDraft"]
        XCTAssertEqual(read.label, "查看草稿")
        read.tap()
        XCTAssertTrue(app.staticTexts["收件人：ops@example.com"].exists)
        XCTAssertTrue(app.staticTexts["主题：Halcyon ERP sync"].exists)
        XCTAssertEqual(app.buttons["topic.sendReply"].label, "发送回复")
        XCTAssertEqual(app.buttons["topic.notNow"].label, "暂时不用", "the card's own action, in the computer's words")
        attach(app, named: "topic-dark-zh")
    }

    func testTopicDraftSendUsesTheVisibleDraftAndNoOwnerCheck() throws {
        let app = launchTopics(ownerCheck: "decline")
        openTopicFromBrief(in: app)
        let read = app.buttons["topic.readDraft"]
        XCTAssertTrue(read.waitForExistence(timeout: 15))
        read.tap()
        let send = app.buttons["topic.sendReply"]
        XCTAssertTrue(send.waitForExistence(timeout: 5))
        send.tap()

        let result = app.staticTexts["topic.replyCard.result"]
        XCTAssertTrue(result.waitForExistence(timeout: 10))
        XCTAssertEqual(result.label, "Done")
        XCTAssertTrue(app.staticTexts["Nov 1 still works for us. We can offer a staging preview on Oct 28."].exists)
        XCTAssertFalse(send.exists)
        attach(app, named: "topic-draft-sent")
    }

    func testAPhoneWithoutChatReadSeesTopicsButIsNotAllowedToOpenOne() throws {
        throw XCTSkip("Tasks tab removed (phone-today v1.0.0 rule 19); its work is Your work on Today.")
        let app = launchTopics(state: "feed-topics-no-chat-read")
        let card = app.buttons.matching(identifier: "feed.topic").firstMatch
        scrollIntoView(card, in: app)
        XCTAssertTrue(card.waitForExistence(timeout: 15), "the list needs only brief:read")
        card.tap()
        let state = app.descendants(matching: .any)["chat.repair"]
        XCTAssertTrue(state.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Open Settings"].exists)
        attach(app, named: "topic-not-allowed")
    }

    /// Scrolls until the element sits inside the window, above the tab bar, and stops there.
    private func scrollIntoView(_ element: XCUIElement, in app: XCUIApplication) {
        let tabBarTop = app.buttons["tab.today"].exists ? app.buttons["tab.today"].frame.minY : app.frame.maxY
        for _ in 0..<20 {
            let frame = element.frame
            if element.exists, !frame.isEmpty, frame.minY >= 100, frame.maxY <= tabBarTop { return }
            if element.exists, !frame.isEmpty, frame.minY < 100 {
                app.swipeDown(velocity: .slow)
            } else {
                app.swipeUp(velocity: .slow)
            }
        }
    }

    private func launchTopics(state: String = "feed-topics", appearance: String = "Light",
                              language: String = "en", ownerCheck: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_OWNER_CHECK"] = ownerCheck
        app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language == "en" ? "en_US" : "zh_CN"]
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        app.launch()
        return app
    }

    private func ideaCard(_ title: String, in app: XCUIApplication) -> XCUIElement {
        app.otherElements.matching(identifier: "tasks.offer")
            .containing(NSPredicate(format: "label == %@", title)).firstMatch
    }

    private func launch(state: String, ownerCheck: String? = nil, appearance: String? = nil,
                        cause: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_CONNECTION_CAUSE"] = cause
        app.launchEnvironment["YANSU_UITEST_OWNER_CHECK"] = ownerCheck
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        if let appearance {
            app.launchArguments += ["-AppleInterfaceStyle", appearance]
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance.lowercased()
        }
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        return app
    }

    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<20 {
            if element.exists && element.isHittable { return }
            app.swipeUp()
        }
    }

    private func attach(_ app: XCUIApplication, named name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    /// The brief's paragraphs and chips are on the brief page, which Read all opens from the Today card
    /// (phone-today v1.0.0 rule 2).
    private func openBrief(in app: XCUIApplication) {
        let read = app.buttons["feed.readAll"].firstMatch
        // Once read or answered, the card folds to its title (phone-today rule 5); a tap unfolds it.
        if !read.waitForExistence(timeout: 10) { app.descendants(matching: .any)["feed.brief"].firstMatch.tap() }
        XCTAssertTrue(read.waitForExistence(timeout: 5))
        read.tap()
    }
}
