import XCTest

// P144 (Settings words: saved on this iPhone, Remove saved briefs) and P145 (Open Settings after notifications were
// declined) in light and dark: design walk v1.0.4, frames S1, S2 and S3. Seeded states only; no computer is contacted.
// Frames are attached in the result bundle; a screen copy for the walk is exported from there.
final class SettingsWordsUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testSettingsSaysSavedAndRemoveSavedBriefs() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "paired", appearance: appearance)
            openSettingsPage(app)
            XCTAssertTrue(app.descendants(matching: .any)["settings.group.privacy"].firstMatch.waitForExistence(timeout: 10))
            attach(app, named: "S1-settings-top-\(appearance)")
            let privacy = app.descendants(matching: .any)["settings.group.privacy"].firstMatch
            for _ in 0..<10 where !(privacy.exists && privacy.isHittable) { app.swipeUp() }
            // phone-settings rule 10: This phone says what Clear removes, and that the computer keeps everything.
            let saved = app.staticTexts["Removes saved briefs and answers here. Your computer keeps everything."].firstMatch
            XCTAssertTrue(saved.waitForExistence(timeout: 5), "This phone says what Clear this phone removes")
            XCTAssertTrue(app.buttons["settings.clearThisPhone"].firstMatch.exists, "This phone offers Clear this phone")
            XCTAssertFalse(app.buttons["Delete cached briefs"].firstMatch.exists)
            attach(app, named: "S2-privacy-saved-\(appearance)")
            app.terminate()
        }
    }

    func testDeclinedNotificationsOfferOpenSettings() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "push-denied", appearance: appearance)
            openSettingsPage(app)
            // Notifications is its own page now (phone-settings rule 8).
            let row = app.buttons["settings.notifications.open"].firstMatch
            for _ in 0..<6 where !(row.exists && row.isHittable) { app.swipeUp() }
            row.tap()
            let open = app.buttons["settings.notifications.openSettings"].firstMatch
            for _ in 0..<10 where !(open.exists && open.isHittable) { app.swipeUp() }
            XCTAssertTrue(open.waitForExistence(timeout: 10), "a declined notification setting needs a way to Settings")
            XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'most notices wait until it wakes'"))
                .firstMatch.waitForExistence(timeout: 5), "while the computer sleeps, most notices wait until it wakes")
            XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'nothing new arrives'")).firstMatch.exists)
            attach(app, named: "S3-notifications-declined-\(appearance)")
            app.terminate()
        }
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

    private func openSettingsPage(_ app: XCUIApplication) {
        let settings = app.buttons["root.settings"].firstMatch
        XCTAssertTrue(settings.waitForExistence(timeout: 15), "the tab root has no profile button")
        settings.tap()
        XCTAssertTrue(app.buttons["settings.done"].firstMatch.waitForExistence(timeout: 10))
    }

    private func attach(_ app: XCUIApplication, named name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
