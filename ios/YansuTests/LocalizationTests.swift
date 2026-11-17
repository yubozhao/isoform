import Foundation
import XCTest

final class LocalizationTests: XCTestCase {
    func testYourAgentStringsHaveEnglishAndSimplifiedChineseValues() throws {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Yansu/Resources/Localizable.xcstrings")
        let catalog = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        let strings = try XCTUnwrap(catalog["strings"] as? [String: Any])
        let keys = [
            "Agent of You",
            "Soon",
            "Coming soon",
            "Soon, people can hire Agent of You.",
            "It works the way you do, makes things for them in minutes, and you get paid for every minute it works.",
            "Use 3 to 30 letters, numbers or dots, not starting or ending with a dot.",
            "Page name, after isoform.ai/agents/",
            "Too many tries. Try again at %@.",
            "What it would specialize in",
            "From what Isoform learned on your computer.",
            "Isoform is still learning how you work. Your specialties show here once it has seen you do something a few times.",
            "A taste of what it knows",
            "Request your page",
            "Send request",
            "Your page",
            "Your email",
            "yourname",
            "Only the name and this email are sent. We'll email you to confirm, and once more when it opens.",
            "Taken. Try %@.",
            "That name isn't available.",
            "Check the email address.",
            "Connect to the internet to send the request.",
            "Couldn't send the request. Try again.",
            "Request sent",
            "isoform.ai/agents/%@ · We'll confirm at %@.",
            "Change",
            "Ideas, or want in early? Email Bo at bo@isoform.ai.",
            "Clients ask for anything; it's best at your specialties.",
            "You set your rate, and can offer your review for a fee.",
            "Your memory, Mail and Messages never go into it.",
            "My agent: isoform.ai/agents/%@",
            "My agent",
            "Copied bo@isoform.ai",
            "Done",
        ]
        for key in keys {
            let entry = try XCTUnwrap(strings[key] as? [String: Any], key)
            let locales = try XCTUnwrap(entry["localizations"] as? [String: Any], key)
            for language in ["en", "zh-Hans"] {
                let locale = try XCTUnwrap(locales[language] as? [String: Any], "\(key): \(language)")
                let unit = try XCTUnwrap(locale["stringUnit"] as? [String: Any], "\(key): \(language)")
                let value = try XCTUnwrap(unit["value"] as? String, "\(key): \(language)")
                XCTAssertFalse(value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, "\(key): \(language)")
            }
        }
    }

    func testReleasedLocaleValuesDoNotUseTheMainChatQualifier() throws {
        let catalogURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("Yansu/Resources/Localizable.xcstrings")
        let data = try Data(contentsOf: catalogURL)
        let catalog = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let strings = try XCTUnwrap(catalog["strings"] as? [String: Any])
        var checkedValues = 0

        for (key, entry) in strings {
            let entry = try XCTUnwrap(entry as? [String: Any], key)
            let localizations = try XCTUnwrap(entry["localizations"] as? [String: Any], key)
            for language in ["en", "zh-Hans"] {
                guard let localization = localizations[language] as? [String: Any] else { continue }
                let stringUnit = try XCTUnwrap(localization["stringUnit"] as? [String: Any], "\(key): \(language)")
                let value = try XCTUnwrap(stringUnit["value"] as? String, "\(key): \(language)")
                XCTAssertNil(value.range(of: "main chat", options: .caseInsensitive), "\(key): \(language)")
                XCTAssertFalse(value.contains("主聊天"), "\(key): \(language)")
                XCTAssertFalse(value.contains("主对话"), "\(key): \(language)")
                checkedValues += 1
            }
        }
        XCTAssertGreaterThan(checkedValues, 0)
    }

    func testDuoStringsHaveEnglishAndSimplifiedChineseValues() throws {
        let catalogURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("Yansu/Resources/Localizable.xcstrings")
        let catalog = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: catalogURL))
                                    as? [String: Any])
        let strings = try XCTUnwrap(catalog["strings"] as? [String: Any])
        for key in ["Stop", "When it’s done", "You review the result. Nothing is sent or merged without you.",
                    "Stop this task? Isoform stops working on it on your computer.", "Step %d of %d",
                    "Full screen", "Exit full screen", "Your day", "Topics", "Search"] {
            let entry = try XCTUnwrap(strings[key] as? [String: Any], key)
            let localizations = try XCTUnwrap(entry["localizations"] as? [String: Any], key)
            for language in ["en", "zh-Hans"] {
                let locale = try XCTUnwrap(localizations[language] as? [String: Any], "\(key): \(language)")
                let unit = try XCTUnwrap(locale["stringUnit"] as? [String: Any], "\(key): \(language)")
                let value = try XCTUnwrap(unit["value"] as? String, "\(key): \(language)")
                XCTAssertFalse(value.isEmpty, "\(key): \(language)")
            }
        }
    }
}
