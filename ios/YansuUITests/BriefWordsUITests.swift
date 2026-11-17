import XCTest

// The brief on the phone (handoff 2026-10-28, sections 2 to 4), over the seeded `feed-words` state: a brief with four
// paragraphs, the greeting joined into the first, its audio on the phone with a start time for each paragraph. No
// relay or hub is contacted. Today, Read all and Play name the same brief, and a newer brief that arrives while the
// person listens waits behind a row without taking the player away from the brief it plays.
final class BriefWordsUITests: XCTestCase {
    private let paragraphs = [
        "Good morning. 3 meetings today, and Priya is waiting on you.",
        "The launch review starts at 3 PM, and the deck still needs one more pass.",
        "Two replies from the design team are waiting in your inbox.",
        "Tomorrow is quiet until the afternoon, so the day is yours to shape.",
    ]

    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testTodayShowsTheOpeningParagraphAndPlaysTheBriefItNames() throws {
        let app = launch()
        let title = app.staticTexts["feed.brief.title"]
        XCTAssertTrue(title.waitForExistence(timeout: 15))
        XCTAssertTrue(title.label.hasPrefix("Morning brief · "), title.label)

        // The card shows the brief's first lines (phone-today v1.0.0 rule 2), starting with the opening paragraph.
        let opening = app.descendants(matching: .any)["feed.brief.words"].firstMatch
        XCTAssertTrue(opening.waitForExistence(timeout: 5))
        XCTAssertTrue(opening.label.hasPrefix(paragraphs[0]), opening.label)
        XCTAssertFalse(app.descendants(matching: .any)["brief.paragraph.1"].exists, "the paragraphs are on the brief")

        let play = app.buttons["brief.hearBrief"]
        XCTAssertTrue(play.waitForExistence(timeout: 10))
        // VoiceOver reads the button with the brief's length (pass-access rule 9).
        XCTAssertEqual(play.label, "Hear it, 1 min")
        // "Hear it · 1 min": the length in minutes (phone-today v1.0.0 rule 3).
        XCTAssertEqual(app.descendants(matching: .any)["feed.brief.length"].firstMatch.label, "1 min")
        play.tap()
        expectation(for: NSPredicate(format: "label == %@", "Pause your brief"), evaluatedWith: play)
        waitForExpectations(timeout: 5)

        // The card is the player: its progress shows on Play, and the mini player waits until the card scrolls away.
        expectation(for: NSPredicate { _, _ in
            let value = play.value as? String ?? ""
            return value.hasSuffix("/ 0:30") && !value.hasPrefix("0:00 ")
        }, evaluatedWith: play)
        waitForExpectations(timeout: 10)
        XCTAssertFalse(app.descendants(matching: .any)["player.mini"].exists)

        // Read all opens the same brief, with every paragraph's words and the player still on that brief.
        app.buttons["feed.readAll"].firstMatch.tap()
        for (index, text) in paragraphs.enumerated() {
            let paragraph = app.descendants(matching: .any)["brief.paragraph.\(index)"].firstMatch
            reveal(paragraph, in: app)
            XCTAssertTrue(paragraph.waitForExistence(timeout: 5), "paragraph \(index)")
            XCTAssertEqual(paragraph.label, text, "paragraph \(index) reads as on the card")
        }
        XCTAssertTrue(app.staticTexts["brief.title"].label.hasPrefix("Morning brief · "))
        XCTAssertEqual(app.buttons["brief.play"].label, "Pause your brief", "the brief view names the playing brief")
        attach(app, named: "brief-words-read-all")
    }

    func testNewerBriefWhileListeningWaitsBehindARowAndKeepsItsPlayer() throws {
        // Late enough that Play is tapped on the first brief before the newer one replaces the card.
        let app = launch(newerBriefAfter: 10)
        let play = app.buttons["brief.hearBrief"]
        XCTAssertTrue(play.waitForExistence(timeout: 15))
        play.tap()
        expectation(for: NSPredicate(format: "label == %@", "Pause your brief"), evaluatedWith: play)
        waitForExpectations(timeout: 5)

        // The newer brief replaces the card in place, with no row asking to be tapped (phone-today v1.0.0 rule 6),
        // and the brief being played keeps playing in the mini player.
        let title = app.staticTexts["feed.brief.title"]
        expectation(for: NSPredicate { _, _ in !title.label.hasPrefix("Morning brief · ") }, evaluatedWith: title)
        waitForExpectations(timeout: 20)
        XCTAssertFalse(app.buttons["feed.brief.newer"].exists, "no \"A newer brief is ready\" row")
        XCTAssertTrue(app.descendants(matching: .any)["player.mini"].firstMatch.waitForExistence(timeout: 5),
                      "the player keeps the brief it plays")
        attach(app, named: "brief-words-newer-in-place")
    }

    private func launch(newerBriefAfter seconds: Double? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed-words"
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        if let seconds {
            app.launchEnvironment["YANSU_UITEST_NEWER_BRIEF_AFTER"] = String(seconds)
        }
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        return app
    }

    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<12 where !(element.exists && element.isHittable) {
            app.swipeUp()
        }
    }

    private func attach(_ app: XCUIApplication, named name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
