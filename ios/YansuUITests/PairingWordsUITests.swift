import XCTest

// P140 (pairing words on Today): the unpaired card says the traffic is encrypted, and a saved pairing that cannot be
// read says so instead of showing the unpaired card. Design walk v1.0.4, frames L1 and L2, light and dark. Seeded
// states only; no computer is contacted. Frames are attached in the result bundle and exported from there.
final class PairingWordsUITests: XCTestCase {
    override func setUpWithError() throws {
        try super.setUpWithError()
        continueAfterFailure = false
    }

    func testUnpairedTodayIsEncryptedNotRelayStorage() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "unpaired", appearance: appearance)
            XCTAssertTrue(app.staticTexts["Everything is encrypted between this phone and your computer."].firstMatch
                .waitForExistence(timeout: 15) || app.descendants(matching: .any)
                .matching(NSPredicate(format: "label CONTAINS 'Everything is encrypted between this phone'")).firstMatch
                .waitForExistence(timeout: 5), "the unpaired card says the traffic is encrypted")
            XCTAssertFalse(app.descendants(matching: .any)
                .matching(NSPredicate(format: "label CONTAINS 'ciphertext'")).firstMatch.exists, "no relay-storage wording")
            XCTAssertTrue(app.buttons["Pair with your computer"].firstMatch.exists, "the unpaired card keeps its pairing button")
            attach(app, named: "L1-unpaired-today-\(appearance)")
            app.terminate()
        }
    }

    func testUnreadablePairingSaysItCouldNotBeReadAndPairAgain() throws {
        for appearance in ["light", "dark"] {
            let app = launch(state: "unreadable", appearance: appearance)
            // phone-setup rule 11: one amber line with Pair, never a page that blanks Today.
            XCTAssertTrue(app.descendants(matching: .any)["today.pairingLost"].firstMatch
                .waitForExistence(timeout: 15), "an unreadable pairing says so in one line")
            XCTAssertTrue(app.buttons["today.pairAgain"].firstMatch.exists, "it keeps a way to pair")
            XCTAssertFalse(app.descendants(matching: .any)
                .matching(NSPredicate(format: "label CONTAINS 'Open Settings to retry'")).firstMatch.exists,
                           "the body text is replaced by Try again")
            let retry = app.buttons["pairing.tryAgain"].firstMatch
            XCTAssertTrue(retry.waitForExistence(timeout: 5), "an unreadable pairing offers Try again")
            attach(app, named: "L2-unreadable-today-\(appearance)")
            retry.tap()
            XCTAssertTrue(app.descendants(matching: .any)["today.pairingLost"].firstMatch
                .waitForExistence(timeout: 10), "Try again re-reads the saved pairing; the file still cannot be read")
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

    private func attach(_ app: XCUIApplication, named name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
