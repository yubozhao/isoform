import XCTest

// Screenshots of every brief state in the design (handoff 2026-10-28, section 2's table and sections 3 to 5), light
// and dark, for comparison with the design. Run it on a large and a small iPhone. Each state comes from a seeded
// fixture; no relay or hub is contacted.
final class BriefScreensUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testBriefScreenshots() throws {
        for appearance in ["light", "dark"] {
            // Section 2: the card at rest, then as the player; section 3: the brief view following the spoken
            // paragraph; section 4: the mini player once the card scrolls away.
            var app = launch(state: "feed-words", appearance: appearance)
            let play = app.buttons["brief.hearBrief"]
            XCTAssertTrue(play.waitForExistence(timeout: 15))
            attach(app, named: "today-rest-\(appearance)")
            play.tap()
            expectation(for: NSPredicate(format: "label == %@", "Pause your brief"), evaluatedWith: play)
            waitForExpectations(timeout: 5)
            attach(app, named: "today-playing-\(appearance)")
            app.buttons["feed.readAll"].tap()
            XCTAssertTrue(app.descendants(matching: .any)["brief.view"].firstMatch.waitForExistence(timeout: 10))
            attach(app, named: "brief-view-following-\(appearance)")
            app.terminate()

            // A newer brief arrives while the person listens: the card shows it, the mini player keeps the one playing.
            app = launch(state: "feed-words", appearance: appearance, extra: ["YANSU_UITEST_NEWER_BRIEF_AFTER": "10"])
            XCTAssertTrue(app.buttons["brief.hearBrief"].waitForExistence(timeout: 15))
            app.buttons["brief.hearBrief"].tap()
            // phone-today v1.0.0 rule 6: the newer brief replaces the card in place; the playing one moves to the mini
            // player.
            let mini = app.descendants(matching: .any)["player.mini"].firstMatch
            XCTAssertTrue(mini.waitForExistence(timeout: 20))
            XCTAssertFalse(app.buttons["feed.brief.newer"].exists)
            attach(app, named: "mini-player-\(appearance)")
            app.terminate()

            // Section 2: making, waiting for a sleeping computer, and a failed request.
            app = launch(state: "feed-words", appearance: appearance, extra: ["YANSU_UITEST_BRIEF_MOMENT": "making"])
            XCTAssertTrue(app.staticTexts["feed.brief.making"].waitForExistence(timeout: 15))
            attach(app, named: "today-making-\(appearance)")
            app.terminate()

            app = launch(state: "feed-words", appearance: appearance, extra: ["YANSU_UITEST_BRIEF_MOMENT": "waiting"])
            XCTAssertTrue(app.staticTexts["feed.brief.waiting"].waitForExistence(timeout: 20))
            attach(app, named: "today-waiting-asleep-\(appearance)")
            app.terminate()

            app = launch(state: "feed-words", appearance: appearance, extra: ["YANSU_UITEST_BRIEF_MOMENT": "failed"])
            let more = app.buttons["More options"].firstMatch
            XCTAssertTrue(more.waitForExistence(timeout: 15))
            more.tap()
            app.buttons["Make a new brief now"].tap()
            // Make it now only asks a computer the phone can reach (TodayStore.requestFresh). The seeded phone reads
            // its relay as unreachable ("Can't connect"), so then the request waits and no failure can come back.
            if app.descendants(matching: .any)["feed.brief.failed"].firstMatch.waitForExistence(timeout: 15) {
                attach(app, named: "today-failed-\(appearance)")
            } else {
                attach(app, named: "today-make-now-waits-\(appearance)")
            }
            app.terminate()

            // Section 2: the computer is out of reach, text without audio yet, and the first open after pairing.
            app = launch(state: "feed-offline", appearance: appearance)
            // Out of reach: the date stays and the away line sits under it (phone-today v1.0.0 rule 7).
            XCTAssertTrue(app.descendants(matching: .any)["today.connection.line"].firstMatch.waitForExistence(timeout: 15))
            attach(app, named: "today-offline-\(appearance)")
            app.terminate()

            app = launch(state: "feed-audio-rendering", appearance: appearance)
            XCTAssertTrue(app.descendants(matching: .any)["feed.brief"].firstMatch.waitForExistence(timeout: 15))
            XCTAssertFalse(app.buttons["brief.hearBrief"].exists, "Play waits for the audio")
            attach(app, named: "today-text-no-audio-\(appearance)")
            app.terminate()

            app = launch(state: "feed-no-history", appearance: appearance)
            XCTAssertTrue(app.descendants(matching: .any)["feed.skeleton"].firstMatch.waitForExistence(timeout: 15))
            attach(app, named: "today-placeholder-\(appearance)")
            app.terminate()

            // Section 5: Past briefs, then one opened from the list.
            app = launch(state: "feed", appearance: appearance)
            // Past briefs is in the brief card's ··· menu.
            let menu = app.buttons["feed.brief.menu"].firstMatch
            XCTAssertTrue(menu.waitForExistence(timeout: 15))
            menu.tap()
            app.buttons["feed.pastBriefs"].firstMatch.tap()
            XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Kept on this phone for 30 days"))
                .firstMatch.waitForExistence(timeout: 10))
            attach(app, named: "past-briefs-\(appearance)")
            app.buttons.matching(identifier: "pastBriefs.row").firstMatch.tap()
            XCTAssertTrue(app.descendants(matching: .any)["brief.view"].firstMatch.waitForExistence(timeout: 10))
            attach(app, named: "past-brief-opened-\(appearance)")
            app.terminate()
        }

        let chinese = launch(state: "feed-words", appearance: "light", language: "zh-Hans")
        XCTAssertTrue(chinese.buttons["brief.hearBrief"].waitForExistence(timeout: 15))
        attach(chinese, named: "today-rest-zh-hans")
        chinese.buttons["feed.readAll"].tap()
        XCTAssertTrue(chinese.descendants(matching: .any)["brief.view"].firstMatch.waitForExistence(timeout: 10))
        attach(chinese, named: "brief-view-zh-hans")
    }

    /// Bo's Today on Oct 29: the brief's audio was on the computer but not on the phone, and Today showed no player.
    /// The card's Play shows, plays from the computer on a tap, and is the player while the card is in view; the mini
    /// player takes over on another tab (design section 4).
    func testTodayCardPlaysAudioStillOnTheComputer() throws {
        let app = launch(state: "feed-words", appearance: "light", extra: ["YANSU_UITEST_AUDIO_ON_COMPUTER": "1"])
        let play = app.buttons["brief.hearBrief"]
        XCTAssertTrue(play.waitForExistence(timeout: 15), "Today shows no Play while the audio is on the computer")
        play.tap()
        expectation(for: NSPredicate(format: "label == %@", "Pause your brief"), evaluatedWith: play)
        waitForExpectations(timeout: 10)
        XCTAssertFalse(app.descendants(matching: .any)["player.mini"].firstMatch.exists,
                       "the card is the player while it is in view")
        attach(app, named: "today-player-audio-on-computer")
        app.buttons["tab.chat"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["player.mini"].firstMatch.waitForExistence(timeout: 5))
        app.buttons["tab.today"].tap()
        XCTAssertTrue(play.waitForExistence(timeout: 5), "back on Today, the card is the player again")
    }

    private func launch(state: String, appearance: String, language: String = "en",
                        extra: [String: String] = [:]) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments += ["-AppleLanguages", "(\(language))", "-AppleLocale", language == "en" ? "en_US" : "zh_CN"]
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
        for (key, value) in extra { app.launchEnvironment[key] = value }
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        return app
    }

    private func attach(_ app: XCUIApplication, named name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
