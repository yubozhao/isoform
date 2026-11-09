import XCTest

final class YourAgentUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testSettingsRowOpensPageAndDoneClosesSheet() {
        let app = launch("opened")
        XCTAssertTrue(app.staticTexts["Agent of You"].exists)
        app.buttons["settings.done"].tap()
        XCTAssertTrue(app.buttons["root.settings"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["settings.yourAgent.open"].exists)
    }

    func testStillLearningHidesTaste() {
        let app = launch("learning")
        XCTAssertTrue(app.staticTexts["Isoform is still learning how you work. Your specialties show here once it has seen you do something a few times."].exists)
        XCTAssertFalse(app.staticTexts["A taste of what it knows"].exists)
        XCTAssertFalse(app.staticTexts["Lead the weekly update with what changed, not what happened."].exists)
    }

    func testOpenedFieldsAreEmptyAndSendDisabled() {
        let app = launch("opened")
        let name = app.textFields["yourAgent.name"]
        XCTAssertEqual(name.label, "Page name, after isoform.ai/agents/")
        XCTAssertEqual(app.textFields["yourAgent.email"].label, "Your email")
        reveal(name, in: app)
        XCTAssertEqual(name.value as? String, "yourname")
        XCTAssertEqual(app.textFields["yourAgent.email"].value as? String, "Your email")
        XCTAssertFalse(app.buttons["yourAgent.reserve"].isEnabled)
    }

    /// P146: a copy that cannot be read leaves both sections out, with no headings and no gray bars.
    func testUnreadableCopyLeavesBothSectionsOut() {
        let app = launch("placeholder")
        XCTAssertFalse(app.staticTexts["What it would specialize in"].exists, "no heading without a copy")
        XCTAssertFalse(app.staticTexts["A taste of what it knows"].exists, "no heading without a copy")
        XCTAssertFalse(app.descendants(matching: .any)["yourAgent.specialties"].firstMatch.exists)
        XCTAssertFalse(app.staticTexts["Weekly updates"].exists)
        XCTAssertFalse(app.staticTexts["yourAgent.stillLearning"].exists)
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS[c] 'loading'")).firstMatch.exists)
        reveal(app.buttons["yourAgent.reserve"], in: app)
        XCTAssertFalse(app.buttons["yourAgent.reserve"].isEnabled)
    }

    func testFailedSendKeepsSendEnabledAndEmailBoInCard() {
        let app = launch("failed")
        submit(app)
        let failure = app.staticTexts["Couldn't send the request. Try again."]
        XCTAssertTrue(failure.waitForExistence(timeout: 5))
        reveal(failure, in: app)
        XCTAssertTrue(app.buttons["yourAgent.writeBo"].exists)
        XCTAssertTrue(app.buttons["yourAgent.reserve"].isEnabled)
    }

    func testRateLimitNamesTimeAndDimsSend() {
        let app = launch("rate")
        submit(app)
        let rate = app.staticTexts["yourAgent.rateLimited"]
        XCTAssertTrue(rate.waitForExistence(timeout: 5))
        XCTAssertTrue(rate.label.hasPrefix("Too many tries. Try again at "))
        XCTAssertFalse(app.buttons["yourAgent.reserve"].isEnabled)
    }

    private func submit(_ app: XCUIApplication) {
        fill(app, name: "alex.rivera")
        let send = app.buttons["yourAgent.reserve"]
        revealSend(send, in: app); send.tap()
    }

    /// At the sheet's first scroll position the Send button and the links below it sit in the bottom system zone,
    /// where a tap never reaches them. Scroll the element into the band from y 200 to 700 and let the scroll settle.
    private func revealSend(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<12 {
            if element.exists && (200...700).contains(element.frame.midY) && element.isHittable { break }
            if element.exists && element.frame.midY < 200 { app.swipeDown(velocity: .slow) }
            else { app.swipeUp(velocity: .slow) }
        }
        var settled = element.frame.minY
        for _ in 0..<10 {
            Thread.sleep(forTimeInterval: 0.3)
            if element.frame.minY == settled { break }
            settled = element.frame.minY
        }
        XCTAssertTrue((200...700).contains(element.frame.midY))
    }

    func testTakenChecksOnLeavingFieldAndSuggestionFillsName() {
        let app = launch("taken")
        fill(app, name: "alex")
        let suggestion = app.staticTexts["yourAgent.suggestion"]
        XCTAssertTrue(suggestion.waitForExistence(timeout: 5))
        XCTAssertEqual(suggestion.label, "Taken. Try alex.rivera.")
        reveal(suggestion, in: app)
        // On iOS 17 the inline link is part of the text, not its own element: tap the name at the end of the line.
        suggestion.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.5)).tap()
        XCTAssertEqual(app.textFields["yourAgent.name"].value as? String, "alex.rivera")
    }

    func testSendAndChangePrefillsForm() {
        let app = launch("opened")
        fill(app, name: "alex.rivera")
        let send = app.buttons["yourAgent.reserve"]
        revealSend(send, in: app)
        XCTAssertTrue(send.isEnabled)
        send.tap()
        XCTAssertTrue(app.staticTexts["Check your email"].waitForExistence(timeout: 5))
        // The change link is inline text on iOS 17, not a Link element: tap its trailing words.
        let change = app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Change the name or email'")).firstMatch
        revealSend(change, in: app)
        change.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.5)).tap()
        XCTAssertEqual(app.textFields["yourAgent.name"].value as? String, "alex.rivera")
        XCTAssertEqual(app.textFields["yourAgent.email"].value as? String, "alex.rivera@example.com")
    }

    func testCodeVerificationAndWrongCode() {
        for state in ["code", "wrongCode"] {
            let app = launch(state)
            let field = app.textFields["yourAgent.codeField"]
            reveal(field, in: app)
            XCTAssertEqual(field.label, "6-digit code")
            field.tap(); field.typeText("123456")
            let result = state == "code" ? app.staticTexts["isoform.ai/agents/alex.rivera is yours."]
                : app.staticTexts["That code didn't match. Try again."]
            XCTAssertTrue(result.waitForExistence(timeout: 5))
            if state == "wrongCode" { XCTAssertEqual(field.value as? String ?? "", "") }
            app.terminate()
        }
    }

    func testChoiceOlderAndRanOutStates() {
        let choice = launch("choice")
        XCTAssertTrue(choice.buttons["yourAgent.keep"].exists)
        let switchButton = choice.buttons["yourAgent.switch"]
        reveal(switchButton, in: choice); switchButton.tap()
        XCTAssertTrue(choice.staticTexts["isoform.ai/agents/alex.rivera is yours."].waitForExistence(timeout: 5))
        choice.terminate()
        let older = launch("older")
        let send = older.buttons["yourAgent.sendCode"]
        reveal(send, in: older); send.tap()
        XCTAssertTrue(older.staticTexts["Check your email"].waitForExistence(timeout: 5))
        older.terminate()
        let ranOut = launch("ranOut")
        XCTAssertTrue(ranOut.staticTexts["yourAgent.ranOut"].waitForExistence(timeout: 5))
        XCTAssertEqual(ranOut.textFields["yourAgent.name"].value as? String, "alex.rivera")
        XCTAssertEqual(ranOut.textFields["yourAgent.email"].value as? String, "alex.rivera@example.com")
    }

    func testOfflineDisablesSend() {
        let app = launch("offline")
        let offline = app.staticTexts["yourAgent.offline"]
        XCTAssertTrue(offline.waitForExistence(timeout: 5))
        reveal(offline, in: app)
        XCTAssertEqual(offline.label, "Connect to the internet to send the request.")
        XCTAssertFalse(app.buttons["yourAgent.reserve"].isEnabled)
    }

    func testReviewScreenshots() throws {
        for appearance in ["light", "dark"] {
            for state in ["opened", "learning", "taken", "sent", "placeholder", "failed", "rate", "code", "wrongCode", "yours", "choice", "ranOut", "older"] {
                let app = launch(state, appearance: appearance)
                if state == "taken" {
                    fill(app, name: "alex")
                    XCTAssertTrue(app.staticTexts["yourAgent.suggestion"].waitForExistence(timeout: 5))
                }
                if state == "failed" || state == "rate" {
                    submit(app)
                    let status = state == "failed" ? app.staticTexts["Couldn't send the request. Try again."]
                        : app.staticTexts["yourAgent.rateLimited"]
                    XCTAssertTrue(status.waitForExistence(timeout: 5))
                }
                if state == "wrongCode" {
                    let field = app.textFields["yourAgent.codeField"]
                    reveal(field, in: app); field.tap(); field.typeText("123456")
                    XCTAssertTrue(app.staticTexts["That code didn't match. Try again."].waitForExistence(timeout: 5))
                }
                let target: XCUIElement
                switch state {
                case "placeholder": target = app.staticTexts["Agent of You"]
                case "sent", "yours": target = app.buttons["yourAgent.share"]
                case "code", "wrongCode": target = app.textFields["yourAgent.codeField"]
                case "choice": target = app.buttons["yourAgent.switch"]
                case "older": target = app.buttons["yourAgent.sendCode"]
                default: target = app.buttons["yourAgent.reserve"]
                }
                reveal(target, in: app)
                let screenshot = app.screenshot()
                let size = app.frame.width < 400 ? "se" : "pro-max"
                let name = "\(size)-\(state)-\(appearance)"
                let attachment = XCTAttachment(screenshot: screenshot)
                attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
                if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"] {
                    let directory = URL(fileURLWithPath: path, isDirectory: true)
                    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    try screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"), options: .atomic)
                }
                app.terminate()
            }
        }
    }

    /// The home indicator zone on an iPhone 15 (852 pt tall) starts 34 pt above the bottom of the screen.
    private func homeIndicatorZoneTop(_ app: XCUIApplication) -> CGFloat { app.frame.maxY - 34 }

    /// Waits until the software keyboard is on screen: its frame starts above the bottom of the screen.
    private func keyboardOnScreen(_ app: XCUIApplication) -> Bool {
        for _ in 0..<40 {
            let keyboard = app.keyboards.firstMatch
            if keyboard.exists && keyboard.frame.minY < app.frame.maxY { return true }
            Thread.sleep(forTimeInterval: 0.2)
        }
        return false
    }

    /// P164: the lowest control in the Agent of You sheet ends above the home indicator zone, and Send request
    /// answers on the first tap.
    func testLowestControlEndsAboveGestureZoneAndSendAnswersFirstTap() {
        let app = launch("opened")
        let zoneTop = homeIndicatorZoneTop(app)
        // Controls that sit fully inside the scroll area, so a control clipped below its edge is not counted.
        let area = app.scrollViews["yourAgent.page"].frame
        let inView = (app.buttons.allElementsBoundByIndex + app.textFields.allElementsBoundByIndex)
            .filter { $0.exists && !$0.identifier.hasPrefix("tab.") && $0.frame.height > 0 && $0.frame.minY >= area.minY && $0.frame.maxY <= area.maxY }
        let lowest = inView.max { $0.frame.maxY < $1.frame.maxY }
        XCTAssertNotNil(lowest)
        XCTAssertLessThanOrEqual(lowest?.frame.maxY ?? .infinity, zoneTop, "lowest control rests in the gesture zone")
        fill(app, name: "alex.rivera")
        let send = app.buttons["yourAgent.reserve"]
        revealSend(send, in: app)
        XCTAssertLessThanOrEqual(send.frame.maxY, zoneTop, "Send request rests in the gesture zone")
        send.tap()
        XCTAssertTrue(app.staticTexts["Check your email"].waitForExistence(timeout: 5), "first tap did not answer")
    }

    /// P164: with the software keyboard up on the email field, the form and Send request sit above the keyboard,
    /// and the return key reads Send.
    func testKeyboardLiftsFormAndReturnKeyReadsSend() {
        let app = launch("opened")
        let name = app.textFields["yourAgent.name"]
        reveal(name, in: app); name.tap(); name.typeText("alex.rivera")
        let email = app.textFields["yourAgent.email"]
        reveal(email, in: app); email.tap()
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5))
        XCTAssertTrue(keyboardOnScreen(app), "the software keyboard is off screen")
        XCTAssertTrue(app.keyboards.buttons["Send"].exists, "return key does not read Send")
        XCTAssertLessThanOrEqual(email.frame.maxY, keyboard.frame.minY)
        XCTAssertLessThanOrEqual(app.buttons["yourAgent.reserve"].frame.maxY, keyboard.frame.minY)
    }

    /// P164 frames: Agent of You at first open, mid-scroll, scrolled to the end, and with the software keyboard up on the
    /// email field, in light and dark. Frames go to YANSU_UITEST_SCREENSHOT_DIRECTORY when set.
    func testP164WalkFrames() throws {
        for appearance in ["light", "dark"] {
            let app = launch("opened", appearance: appearance)
            func capture(_ frame: String) {
                let shot = XCUIScreen.main.screenshot()
                let attachment = XCTAttachment(screenshot: shot)
                attachment.name = "P164-\(frame)-\(appearance)"; attachment.lifetime = .keepAlways; add(attachment)
                if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty {
                    let directory = URL(fileURLWithPath: path, isDirectory: true)
                    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    try? shot.pngRepresentation.write(to: directory.appendingPathComponent("P164-\(frame)-\(appearance).png"), options: .atomic)
                }
            }
            capture("first-open")
            app.swipeUp(velocity: .slow)
            Thread.sleep(forTimeInterval: 0.6)
            capture("mid-scroll")
            // Scroll to the end: stop when the last control stops moving.
            var previous: CGFloat = -1
            for _ in 0..<20 {
                app.swipeUp(velocity: .fast)
                Thread.sleep(forTimeInterval: 0.4)
                let current = app.buttons["yourAgent.writeBo"].frame.minY
                if current == previous { break }
                previous = current
            }
            capture("scrolled-end")
            let name = app.textFields["yourAgent.name"]
            reveal(name, in: app); name.tap(); name.typeText("alex.rivera")
            let email = app.textFields["yourAgent.email"]
            reveal(email, in: app); email.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "no software keyboard")
            XCTAssertTrue(keyboardOnScreen(app), "the software keyboard is off screen")
            let keyboardTop = app.keyboards.firstMatch.frame.minY
            let report = "appearance \(appearance)\nkeyboard top \(keyboardTop)\nemail maxY \(email.frame.maxY)\nSend request maxY \(app.buttons["yourAgent.reserve"].frame.maxY)\n"
            let attachment = XCTAttachment(data: Data(report.utf8), uniformTypeIdentifier: "public.plain-text")
            attachment.name = "P164-keyboard-\(appearance).txt"; attachment.lifetime = .keepAlways; add(attachment)
            capture("keyboard-email")
            if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty {
                try report.write(toFile: path + "/P164-keyboard-\(appearance).txt", atomically: true, encoding: .utf8)
            }
            app.terminate()
        }
    }

    /// P137 on the Settings sheet: text scrolled under the title bar stays out of the bar's way, light and dark.
    /// Frames go to YANSU_UITEST_SCREENSHOT_DIRECTORY when set.
    func testSettingsSheetFrames() throws {
        for appearance in ["light", "dark"] {
            let app = XCUIApplication()
            app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
            app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "paired"
            app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
            app.launch()
            openSettings(app)
            XCTAssertTrue(app.buttons["settings.done"].waitForExistence(timeout: 5))
            func capture(_ frame: String) {
                let shot = XCUIScreen.main.screenshot()
                let attachment = XCTAttachment(screenshot: shot)
                attachment.name = "P137-settings-\(frame)-\(appearance)"; attachment.lifetime = .keepAlways; add(attachment)
                if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty {
                    let directory = URL(fileURLWithPath: path, isDirectory: true)
                    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    try? shot.pngRepresentation.write(to: directory.appendingPathComponent("P137-settings-\(frame)-\(appearance).png"), options: .atomic)
                }
            }
            capture("first-open")
            // Scroll to the end of the Settings page before measuring its last control.
            for _ in 0..<8 {
                app.swipeUp(velocity: .fast)
                Thread.sleep(forTimeInterval: 0.3)
            }
            let lowestTop = app.frame.maxY - 34
            let visible = (app.buttons.allElementsBoundByIndex + app.switches.allElementsBoundByIndex + app.staticTexts.allElementsBoundByIndex)
                .filter { $0.exists && $0.frame.height > 0 && $0.frame.maxY <= app.frame.maxY && $0.frame.minY < app.frame.maxY }
            let lowest = visible.map { $0.frame.maxY }.max() ?? -1
            let report = "appearance \(appearance)\nlowest control maxY \(lowest)\nzone top \(lowestTop)\n"
            let note = XCTAttachment(data: Data(report.utf8), uniformTypeIdentifier: "public.plain-text")
            note.name = "P137-settings-lowest-\(appearance).txt"; note.lifetime = .keepAlways; add(note)
            if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty {
                try report.write(toFile: path + "/P137-settings-lowest-\(appearance).txt", atomically: true, encoding: .utf8)
            }
            app.swipeUp(velocity: .slow)
            Thread.sleep(forTimeInterval: 0.6)
            capture("mid-scroll")
            app.terminate()
        }
    }

    /// P164 metrics at first open: the lowest control of the Agent of You sheet, counted only inside the sheet's own
    /// page scroll view (the tab bar and the Settings page under the sheet are not counted). Light and dark from one
    /// run. Frames and the metrics file go to YANSU_UITEST_SCREENSHOT_DIRECTORY when set.
    func testP164AgentOfYouMetricsAndFrames() throws {
        var lowestByAppearance: [String: CGFloat] = [:]
        var zoneTopSeen: CGFloat = 0
        for appearance in ["light", "dark"] {
            let app = launch("opened", appearance: appearance)
            let zoneTop = homeIndicatorZoneTop(app)
            zoneTopSeen = zoneTop
            let page = app.scrollViews["yourAgent.page"]
            XCTAssertTrue(page.waitForExistence(timeout: 5))
            func save(_ frame: String) {
                let shot = XCUIScreen.main.screenshot()
                let attachment = XCTAttachment(screenshot: shot)
                attachment.name = "P164-\(frame)-\(appearance)"; attachment.lifetime = .keepAlways; add(attachment)
                if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty {
                    let directory = URL(fileURLWithPath: path, isDirectory: true)
                    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    try? shot.pngRepresentation.write(to: directory.appendingPathComponent("P164-\(frame)-\(appearance).png"), options: .atomic)
                }
            }
            // The sheet's own controls are the yourAgent.* elements, wherever they lie, at their laid-out frames.
            func sheetReport(_ stage: String) -> (maxY: CGFloat, text: String) {
                let sheetControls = (app.buttons.allElementsBoundByIndex + app.textFields.allElementsBoundByIndex
                    + app.switches.allElementsBoundByIndex)
                    .filter { $0.exists && $0.frame.height > 0 && $0.identifier.hasPrefix("yourAgent.") }
                let lowest = sheetControls.max { $0.frame.maxY < $1.frame.maxY }
                let lowestMaxY = lowest?.frame.maxY ?? -1
                let lowestName = lowest.map { $0.identifier.isEmpty ? $0.label : $0.identifier } ?? "none"
                let listing = sheetControls.sorted { $0.frame.maxY > $1.frame.maxY }
                    .map { "  \($0.identifier) maxY \($0.frame.maxY)" }.joined(separator: "\n")
                let text = "stage \(stage), appearance \(appearance)\n"
                    + "lowest control \(lowestName) maxY \(lowestMaxY)\n"
                    + "zone top \(zoneTop)\n"
                    + "lowest control at or above zone top (maxY <= zone top) \(lowestMaxY <= zoneTop)\n"
                    + "sheet controls (yourAgent.*) \(sheetControls.count), scroll view \(page.frame)\n"
                    + listing + "\n"
                return (lowestMaxY, text)
            }
            // Metrics first, before any scroll: the sheet is at its first-open position.
            let firstOpen = sheetReport("first open")
            save("first-open")
            app.swipeUp(velocity: .slow)
            Thread.sleep(forTimeInterval: 0.6)
            save("mid-scroll")
            var previous: CGFloat = -1
            for _ in 0..<20 {
                app.swipeUp(velocity: .fast)
                Thread.sleep(forTimeInterval: 0.4)
                let current = app.buttons["yourAgent.writeBo"].frame.minY
                if current == previous { break }
                previous = current
            }
            save("scrolled-end")
            // The rule that counts: at the scrolled end every yourAgent.* control sits fully above the zone top.
            let scrolledEnd = sheetReport("scrolled end")
            lowestByAppearance[appearance] = scrolledEnd.maxY
            let combined = firstOpen.text + "\n" + scrolledEnd.text
            let note = XCTAttachment(data: Data(combined.utf8), uniformTypeIdentifier: "public.plain-text")
            note.name = "P164-agent-metrics-\(appearance).txt"; note.lifetime = .keepAlways; add(note)
            if let path = ProcessInfo.processInfo.environment["YANSU_UITEST_SCREENSHOT_DIRECTORY"], !path.isEmpty {
                try? FileManager.default.createDirectory(atPath: path, withIntermediateDirectories: true)
                try combined.write(toFile: path + "/P164-agent-metrics-\(appearance).txt", atomically: true, encoding: .utf8)
            }
            app.terminate()
        }
        for (appearance, maxY) in lowestByAppearance {
            XCTAssertLessThanOrEqual(maxY, zoneTopSeen, "lowest control in \(appearance) rests in the gesture zone")
        }
    }

    private func launch(_ state: String, appearance: String = "light") -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launchEnvironment["YANSU_UITEST_SETTINGS_STATE"] = "paired"
        app.launchEnvironment["YANSU_UITEST_YOUR_AGENT_STATE"] = state
        app.launchEnvironment["YANSU_UITEST_APPEARANCE"] = appearance
        app.launch()
        openSettings(app)
        let row = app.buttons["settings.yourAgent.open"]
        XCTAssertTrue(row.waitForExistence(timeout: 5))
        XCTAssertTrue(row.label.contains("Agent of You"))
        reveal(row, in: app); row.tap()
        XCTAssertTrue(app.staticTexts["Agent of You"].waitForExistence(timeout: 5))
        let ready = state == "placeholder" ? app.staticTexts["Request your page"] : state == "learning"
            ? app.staticTexts["Isoform is still learning how you work. Your specialties show here once it has seen you do something a few times."]
            : app.staticTexts["Weekly updates"]
        XCTAssertTrue(ready.waitForExistence(timeout: 5))
        return app
    }

    private func fill(_ app: XCUIApplication, name: String) {
        let nameField = app.textFields["yourAgent.name"]
        reveal(nameField, in: app); nameField.tap(); nameField.typeText(name)
        XCTAssertFalse(app.staticTexts["yourAgent.suggestion"].exists)
        let email = app.textFields["yourAgent.email"]
        reveal(email, in: app); email.tap(); email.typeText("alex.rivera@example.com")
        let scroll = app.scrollViews["yourAgent.page"]
        scroll.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
            .press(forDuration: 0.1, thenDragTo: scroll.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85)))
    }

    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<12 {
            if element.exists && element.isHittable { return }
            if element.exists && element.frame.minY < 100 { app.swipeDown(velocity: .slow) }
            else { app.swipeUp(velocity: .slow) }
        }
        XCTAssertTrue(element.isHittable)
    }
}
