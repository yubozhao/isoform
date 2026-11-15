import XCTest
@testable import Yansu

final class WorkOnItTests: XCTestCase {
    func testTextCarriesTheItemAndAsksForTheResult() {
        let text = WorkOnIt.text(title: "  Draft the  launch note ", detail: "For Thursday's\nrelease.", done: "A one-page note")
        XCTAssertEqual(text, "Draft the launch note\n\nFor Thursday's release.\n\nDone looks like: A one-page note\n\nWhen you're done, tell me what you did.")
        XCTAssertEqual(WorkOnIt.text(title: "Same", detail: "Same", done: nil), "Same\n\nWhen you're done, tell me what you did.")
    }

    func testInstalledKeepsCatalogOrderAndOnlyWhatOpens() {
        let installed = WorkOnIt.installed { url in url.scheme == "grok" || url.scheme == "claude" }
        XCTAssertEqual(installed.map(\.id), ["claude", "grok"])
        XCTAssertTrue(WorkOnIt.installed { _ in false }.isEmpty)
    }

    func testEveryDeclaredSchemeIsQueryable() throws {
        let declared = Bundle.main.object(forInfoDictionaryKey: "LSApplicationQueriesSchemes") as? [String] ?? []
        for agent in WorkOnIt.catalog {
            XCTAssertTrue(declared.contains(agent.scheme), "\(agent.scheme) is not in LSApplicationQueriesSchemes")
        }
    }
}
