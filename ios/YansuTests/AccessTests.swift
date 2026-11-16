import XCTest
@testable import Yansu

// Pass access on the phone (pass-access v1.0.0 rules 7 to 10).
final class AccessTests: XCTestCase {
    func testThePlayersPositionIsReadInWords() {
        let spoken = BriefSpokenTime.position(41, of: 134)
        XCTAssertTrue(spoken.contains("41"), spoken)
        XCTAssertTrue(spoken.contains("2"), spoken)
        XCTAssertFalse(spoken.contains(":"), "never a clock reading like 0:41")
    }
}
