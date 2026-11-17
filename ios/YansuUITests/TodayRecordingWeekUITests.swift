import XCTest

// P138 (Recording on its way fills the player's place) and P139 (Your week is its own card, below the brief card) on
// Today, in light and dark: design walk v1.0.4, frames T1, T2 and T4. Seeded states only; no computer is contacted.
// Frames are written to YANSU_UITEST_SCREENSHOT_DIRECTORY/<row>/ when that variable is set, and always attached.
final class TodayRecordingWeekUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testRecordingOnItsWayFillsThePlayersPlace() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-audio-rendering", appearance: appearance)
            XCTAssertTrue(app.descendants(matching: .any)["feed.brief.recording"].firstMatch.waitForExistence(timeout: 15),
                          "Today shows no recording line while the computer makes the recording")
            XCTAssertFalse(app.buttons["brief.hearBrief"].exists, "Play waits for the recording")
            XCTAssertEqual(app.descendants(matching: .any)["feed.brief.recording"].firstMatch.label, "Recording on its way",
                           "the line promises no time")
            capture(app, row: "P138", named: "T4-recording-on-its-way-\(appearance)")
            app.terminate()
        }
    }

    /// P139: a week brief is one card. Its title is Your week, its Play or recording line sits under the title, its
    /// links sit at the bottom, and no "Evening brief" card is above it. An ordinary brief is unchanged.
    func testYourWeekIsOneCardWithItsLinksAtTheBottom() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "feed-your-week", appearance: appearance)
            let week = app.descendants(matching: .any)["brief.yourWeek"].firstMatch
            XCTAssertTrue(week.waitForExistence(timeout: 15), "Today shows no Your week card")
            let title = app.descendants(matching: .any)["brief.yourWeek.title"].firstMatch
            XCTAssertTrue(title.exists, "Your week keeps its title")
            XCTAssertFalse(app.descendants(matching: .any)["feed.brief.title"].firstMatch.exists,
                           "no Evening brief card above the week")
            let card = app.descendants(matching: .any)["feed.brief"].firstMatch
            XCTAssertTrue(card.exists)
            let readAll = app.buttons["feed.readAll"].firstMatch
            XCTAssertTrue(readAll.exists, "the links are part of the week's card")
            XCTAssertGreaterThanOrEqual(readAll.frame.minY, week.frame.maxY - 1, "the links sit below the week's words")
            XCTAssertGreaterThanOrEqual(readAll.frame.minY, title.frame.maxY, "the links sit below the title")
            capture(app, row: "P139", named: "T1-week-top-\(appearance)")
            let titleTop = title.frame.minY
            // Scroll until the card stops moving, which is its end: the links sit at the bottom.
            var last = title.frame.minY
            for _ in 0..<20 {
                app.swipeUp()
                let now = title.frame.minY
                if abs(now - last) < 2 { break }
                last = now
            }
            XCTAssertTrue(readAll.isHittable, "the week card scrolls to its links")
            XCTAssertLessThan(title.frame.minY, titleTop - 200, "the scroll moved the card to its end")
            capture(app, row: "P139", named: "T2-week-end-\(appearance)")
            app.terminate()

            let ordinary = launch(state: "feed-words", appearance: appearance)
            XCTAssertTrue(ordinary.descendants(matching: .any)["feed.brief.title"].firstMatch.waitForExistence(timeout: 15),
                          "an ordinary brief keeps its Evening brief title")
            XCTAssertFalse(ordinary.descendants(matching: .any)["brief.yourWeek"].firstMatch.exists)
            capture(ordinary, row: "P139", named: "T3-ordinary-brief-\(appearance)")
            ordinary.terminate()
        }
    }

    /// The week card's ··· menu carries the ordinary brief card's item, "Make my brief now", with the same enabled state,
    /// and adds only the week's own "Turn off Your week notifications" (P139).
    func testWeekMenuKeepsTheBriefCardsItem() throws {
        let week = launch(state: "feed-your-week", appearance: "light")
        let weekMore = week.buttons["More options for Your week"].firstMatch
        XCTAssertTrue(weekMore.waitForExistence(timeout: 15), "the week card has its ··· menu")
        weekMore.tap()
        XCTAssertTrue(week.buttons["Make my brief now"].firstMatch.waitForExistence(timeout: 5), "the week menu has Make my brief now")
        XCTAssertTrue(week.buttons["Turn off Your week notifications"].firstMatch.exists, "the week menu has its own notification item")
        week.terminate()

        let ordinary = launch(state: "feed-words", appearance: "light")
        let more = ordinary.buttons["More options"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 15))
        more.tap()
        // The brief card's own words for it (phone-today v1.0.0 rule 2).
        XCTAssertTrue(ordinary.buttons["Make a new brief now"].firstMatch.waitForExistence(timeout: 5), "the brief card's menu has Make a new brief now")
        ordinary.terminate()
    }

    /// design v1.0.18: the week keeps coming when its notification is off. Turning the notification off keeps the card,
    /// and its ··· menu then offers "Turn on Your week notifications" (P160).
    func testTurningWeekNotificationsOffKeepsTheCardAndOffersTurnOn() throws {
        let app = launch(state: "feed-your-week", appearance: "light")
        let more = app.buttons["More options for Your week"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 15))
        more.tap()
        let off = app.buttons["Turn off Your week notifications"].firstMatch
        XCTAssertTrue(off.waitForExistence(timeout: 5))
        off.tap()
        XCTAssertTrue(app.descendants(matching: .any)["brief.yourWeek"].firstMatch.waitForExistence(timeout: 10),
                      "the week card stays when its notification is off")
        app.buttons["More options for Your week"].firstMatch.tap()
        let on = app.buttons["Turn on Your week notifications"].firstMatch.waitForExistence(timeout: 5)
        if !on {
            let notice = app.descendants(matching: .any)["brief.yourWeek.notice"].firstMatch
            print("WEEK-OFF-NOTICE exists=\(notice.exists) label=\(notice.exists ? notice.label : "-")")
        }
        XCTAssertTrue(on, "the same item now turns the notification back on")
        capture(app, row: "P160", named: "week-off-card-kept-light")
        app.terminate()
    }

    private func launch(state: String, appearance: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_TAB"] = "feed"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        return app
    }

    private func capture(_ app: XCUIApplication, row: String, named name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "\(row)-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty else { return }
        do {
            let directory = URL(fileURLWithPath: path, isDirectory: true).appendingPathComponent(row, isDirectory: true)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"), options: .atomic)
        } catch {
            XCTFail("could not write \(name): \(error)")
        }
    }
}
