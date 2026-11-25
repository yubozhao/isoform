import XCTest
@testable import Yansu

/// The silent `ready` push (PROTOCOL.md 9.5) wakes the phone to fetch its brief without being opened.
@MainActor
final class BriefReadyWakeTests: XCTestCase {
    private let hub = "7c9e6679-7425-40de-944b-e07fc1f90ae8"

    func testAReadyPushFromThePairedComputerRefreshes() async {
        var refreshes = 0
        let push: [AnyHashable: Any] = ["aps": ["content-available": 1], "yansu": ["kind": "ready", "hub": hub]]
        let result = await BriefReadyWake.handle(push, pairedHub: hub) { refreshes += 1; return true }
        XCTAssertEqual(result, .newData)
        XCTAssertEqual(refreshes, 1)
        let failed = await BriefReadyWake.handle(push, pairedHub: hub) { false }
        XCTAssertEqual(failed, .failed)
    }

    func testOtherPushesAndOtherComputersDoNotRefresh() async {
        var refreshes = 0
        let other: [AnyHashable: Any] = ["yansu": ["kind": "ready", "hub": "another-hub"]]
        let foreign = await BriefReadyWake.handle(other, pairedHub: hub) { refreshes += 1; return true }
        XCTAssertEqual(foreign, .noData)
        let unpaired = await BriefReadyWake.handle(other, pairedHub: nil) { refreshes += 1; return true }
        XCTAssertEqual(unpaired, .noData)
        let withdraw: [AnyHashable: Any] = ["yansu": ["kind": "withdraw", "hub": hub, "ids": ["brief:x"]]]
        let notReady = await BriefReadyWake.handle(withdraw, pairedHub: hub) { refreshes += 1; return true }
        XCTAssertNil(notReady, "a withdraw push is left to its own handler")
        XCTAssertEqual(refreshes, 0)
    }

    func testTheCompletionHandlerIsCalledOnce() {
        var results: [UIBackgroundFetchResult] = []
        let once = BriefReadyWake.Completion { results.append($0) }
        once.finish(.newData)
        once.finish(.failed)
        XCTAssertEqual(results, [.newData], "the work and the time limit never both answer iOS")
    }
}
