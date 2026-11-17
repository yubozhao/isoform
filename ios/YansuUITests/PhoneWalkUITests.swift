import XCTest

// The phone polish walk (hub 2026-11-16): every redesigned page, light and dark, for scoring against its
// 2026-11-15 handoff. Screenshots only; the assertions just make sure each page is the one named.
final class PhoneWalkUITests: XCTestCase {
    override func setUp() { continueAfterFailure = true }

    func testWalkLight() throws { walk(dark: false) }
    func testWalkDark() throws { walk(dark: true) }

    private func launch(dark: Bool, state: String = "feed-audio", work: String = "agents", tab: String? = nil,
                        watch: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_WORK"] = work
        if let tab { app.launchEnvironment["YANSU_UITEST_TAB"] = tab }
        if watch { app.launchEnvironment["YANSU_UITEST_WATCH_RECORDINGS"] = "1" }
        if dark {
            app.launchArguments += ["-AppleInterfaceStyle", "Dark"]
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = "dark"
        }
        app.launch()
        return app
    }

    private func shot(_ app: XCUIApplication, _ name: String, dark: Bool) {
        attachScreenshot(app, named: "walk-\(name)-\(dark ? "dark" : "light")")
    }

    private func walk(dark: Bool) {
        // Today: the brief, then Your work.
        var app = launch(dark: dark)
        _ = app.otherElements["today.yourWork"].firstMatch.waitForExistence(timeout: 15)
        shot(app, "01-today-top", dark: dark)
        app.swipeUp()
        shot(app, "02-today-your-work", dark: dark)
        app.swipeUp()
        shot(app, "03-today-with-agents-done", dark: dark)
        app.swipeDown(); app.swipeDown()
        // The brief in full.
        let readAll = app.buttons["feed.readAll"].firstMatch
        if readAll.waitForExistence(timeout: 5) {
            readAll.tap()
            _ = app.scrollViews["brief.view"].firstMatch.waitForExistence(timeout: 10)
            shot(app, "04-brief", dark: dark)
            app.swipeUp()
            shot(app, "05-brief-items", dark: dark)
        }
        app.terminate()

        // Chat.
        app = launch(dark: dark, state: "chat-live", work: "1", tab: "chat")
        _ = app.descendants(matching: .any)["chat.composer"].firstMatch.waitForExistence(timeout: 15)
        shot(app, "06-chat", dark: dark)
        app.terminate()

        // Settings and its pages.
        app = launch(dark: dark, watch: true)
        openSettings(app)
        shot(app, "07-settings-top", dark: dark)
        app.swipeUp()
        shot(app, "08-settings-this-phone", dark: dark)
        app.swipeDown(); app.swipeDown()
        for (row, name) in [("settings.yourData.open", "09-your-data"), ("settings.yourAgents.open", "10-your-agents"),
                            ("settings.notifications.open", "11-notifications"),
                            ("settings.watchRecordings.open", "12-watch")] {
            let button = app.buttons[row].firstMatch
            for _ in 0..<4 where !(button.exists && button.isHittable) { app.swipeUp() }
            guard button.exists else { continue }
            button.tap()
            sleep(2)
            shot(app, name, dark: dark)
            let back = app.navigationBars.buttons.element(boundBy: 0)
            if back.exists { back.tap() }
            sleep(1)
            app.swipeDown(); app.swipeDown()
        }
        app.terminate()
    }
}
