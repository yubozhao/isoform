import XCTest

final class LocalNotificationUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testOfflineAwakeOfferAndSettingsFooters() throws {
        let app = launch(offline: true, instant: false)
        let offer = app.descendants(matching: .any)["today.keepAwakeOffer"].firstMatch
        XCTAssertTrue(offer.waitForExistence(timeout: 15), app.debugDescription)
        attachScreenshot(app, named: "today-asleep-awake-offer")
        app.buttons["Turn on"].tap()
        XCTAssertTrue(app.staticTexts["Turns on when your computer is back."].waitForExistence(timeout: 10))
        openSettings(app)
        let toggle = app.switches["settings.keepAwake"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["So this phone can reach it"].exists)
        XCTAssertTrue(app.staticTexts["Changes apply when your computer is back."].exists)
        attachScreenshot(app, named: "settings-keep-awake-offline")
        openNotifications(app)
        let footer = app.staticTexts["settings.notifications.lateFooter"]
        reveal(footer, in: app)
        XCTAssertTrue(footer.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "settings-local-notifications")
    }

    func testInstantPushHidesLateFooter() throws {
        let app = launch(offline: false, instant: true)
        openSettings(app)
        openNotifications(app)
        XCTAssertTrue(app.buttons["notifications.refresh"].firstMatch.waitForExistence(timeout: 10), "the Notifications page is open")
        XCTAssertFalse(app.staticTexts["settings.notifications.lateFooter"].exists)
    }

    func testOlderMacHidesUnknownKeepAwakeSwitch() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        openSettings(app)
        XCTAssertTrue(app.descendants(matching: .any)["settings.group.mac"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.switches["settings.keepAwake"].exists)
    }

    func testNotNowStaysDismissedAfterRelaunch() throws {
        let app = launch(offline: true, instant: false)
        let offer = app.descendants(matching: .any)["today.keepAwakeOffer"].firstMatch
        XCTAssertTrue(offer.waitForExistence(timeout: 15))
        app.buttons["Not now"].firstMatch.tap()
        XCTAssertFalse(offer.exists)
        app.terminate()
        app.launchEnvironment.removeValue(forKey: "YANSU_UITEST_RESET_AWAKE_OFFER")
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["feed.summary"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertFalse(offer.exists)
    }

    func testQuietHoursShortcutAndIconCountSwitch() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        openSettings(app)
        openNotifications(app)
        let quiet = app.buttons["notifications.quietHours"].firstMatch
        reveal(quiet, in: app)
        quiet.tap()
        let save = app.buttons["notifications.quietHours.save"]
        XCTAssertTrue(save.waitForExistence(timeout: 10))
        save.tap()
        XCTAssertTrue(quiet.waitForExistence(timeout: 10))
        let badge = app.switches["notifications.iconBadge"]
        for _ in 0..<8 where !(badge.exists && badge.isHittable) { app.swipeDown() }
        XCTAssertTrue(badge.waitForExistence(timeout: 10))
        badge.tap()
        attachScreenshot(app, named: "notifications-icon-count")
        badge.tap()
    }

    func testQuietHoursFailureStaysInEditor() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
        app.launchEnvironment["YANSU_UITEST_FAIL_QUIET_SAVE"] = "1"
        app.launch()
        openSettings(app)
        openNotifications(app)
        let quiet = app.buttons["notifications.quietHours"].firstMatch
        reveal(quiet, in: app)
        quiet.tap()
        let save = app.buttons["notifications.quietHours.save"]
        XCTAssertTrue(save.waitForExistence(timeout: 10))
        save.tap()
        XCTAssertTrue(app.staticTexts["notifications.quietHours.failure"].waitForExistence(timeout: 10))
        XCTAssertEqual(save.label, "Try again")
        attachScreenshot(app, named: "quiet-hours-save-failure")
    }

    func testNotificationPreferencesInChineseDark() throws {
        let app = XCUIApplication()
        app.launchArguments += ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN", "-AppleInterfaceStyle", "Dark"]
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "tasks-seeded"
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = "dark"
        app.launch()
        openSettings(app)
        openNotifications(app)
        XCTAssertTrue(app.switches["notifications.iconBadge"].waitForExistence(timeout: 10))
        attachScreenshot(app, named: "notifications-icon-count-zh-Hans-dark")
    }

    func testKeepAwakeReservationFailureStaysByTheToggle() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
        app.launchEnvironment["YANSU_UITEST_KEEP_AWAKE"] = "off"
        app.launchEnvironment["YANSU_UITEST_FAIL_AWAKE_SAVE"] = "1"
        app.launch()
        openSettings(app)
        let toggle = app.switches["settings.keepAwake"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10))
        let before = toggle.value as? String
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        let failure = app.descendants(matching: .any)["settings.group.mac"].staticTexts["Could not save this change. Try again."]
        XCTAssertTrue(failure.waitForExistence(timeout: 10))
        XCTAssertEqual(toggle.value as? String, before)
        attachScreenshot(app, named: "keep-awake-save-failure")
    }

    func testBriefDeletionFailureShowsNoticeInSettings() throws {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "feed"
        app.launchEnvironment["YANSU_UITEST_FAIL_BRIEF_DELETE"] = "1"
        app.launch()
        openSettings(app)
        // Clear this phone removes the saved briefs (phone-settings rule 10).
        let delete = app.buttons["settings.clearThisPhone"].firstMatch
        reveal(delete, in: app)
        delete.tap()
        app.buttons["Remove saved briefs"].firstMatch.tap()
        let notice = app.descendants(matching: .any)["settings.briefNotice"].firstMatch
        for _ in 0..<8 where !notice.isHittable { app.swipeDown() }
        XCTAssertTrue(notice.waitForExistence(timeout: 10))
        attachScreenshot(app, named: "brief-deletion-failure")
    }

    private func launch(offline: Bool, instant: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = offline ? "feed-offline" : "feed"
        app.launchEnvironment["YANSU_UITEST_KEEP_AWAKE"] = "off"
        app.launchEnvironment["YANSU_UITEST_INSTANT_PUSH"] = instant ? "true" : "false"
        app.launchEnvironment["YANSU_UITEST_RESET_AWAKE_OFFER"] = "1"
        // The keep-awake offer is for a computer that said goodbye for sleep; a seed with no goodbye reads "Can't
        // connect", so name the cause.
        if offline { app.launchEnvironment["YANSU_UITEST_CONNECTION_CAUSE"] = "sleeping" }
        app.launch()
        XCTAssertTrue(app.buttons["tab.today"].waitForExistence(timeout: 15))
        return app
    }

    /// Notifications is its own page in Settings (phone-settings rule 8).
    private func openNotifications(_ app: XCUIApplication) {
        let row = app.buttons["settings.notifications.open"].firstMatch
        reveal(row, in: app)
        row.tap()
    }

    /// Scrolls until the element sits clear of the tab bar and the home indicator, where a tap lands on it.
    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        _ = element.waitForExistence(timeout: 10)
        for _ in 0..<10 {
            if element.exists && element.isHittable && element.frame.maxY < app.frame.maxY - 120 { return }
            app.swipeUp()
        }
        XCTAssertTrue(element.exists)
    }
}
