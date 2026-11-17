import XCTest
@testable import Yansu

// Forget everything from the computer (forget_all): the phone deletes what it keeps for that computer, keeps the
// pairing, and confirms; a resent job deletes again and confirms again.
final class ForgetAllTests: XCTestCase {
    private actor RecordingAPI: ForgetAllAPI {
        var confirms: [(String, ForgetAllConfirm)] = []
        var fails = false
        func setFails(_ value: Bool) { fails = value }
        func confirmForgetAll(jobID: String, confirm: ForgetAllConfirm) async throws {
            if fails { throw HubRequestError.noAnswer }
            confirms.append((jobID, confirm))
        }
    }

    func testJobDecodesAndOnlyASafeVersionOneJobIsActedOn() throws {
        let job = try JSONDecoder().decode(ForgetAllJob.self,
                                           from: Data(#"{"v":1,"jobId":"7f1c2a8e-0000-4000-8000-000000000001","at":"2026-11-16T08:00:00Z"}"#.utf8))
        XCTAssertTrue(job.isValid)
        XCTAssertFalse(ForgetAllJob(v: 2, jobId: "abc").isValid)
        XCTAssertFalse(ForgetAllJob(v: 1, jobId: "../status").isValid)
    }

    /// The forget_all envelope's plaintext as vectors-v1.json carries it (PROTOCOL.md 28).
    func testTheVectorPlaintextIsAJobThePhoneActsOn() throws {
        let plaintext = #"{"v":1,"jobId":"c4b8f1e0-2d3a-4b5c-9e8f-7a6b5c4d3e2f","at":"2026-11-16T08:00:00Z"}"#
        let job = try JSONDecoder().decode(ForgetAllJob.self, from: Data(plaintext.utf8))
        XCTAssertTrue(job.isValid)
        XCTAssertEqual(ForgetAllRunner.key(for: job), "forget-all-c4b8f1e0-2d3a-4b5c-9e8f-7a6b5c4d3e2f")
    }

    @MainActor
    func testRunConfirmsAfterTheEraseAndAReplayConfirmsTheSameWay() async {
        let api = RecordingAPI()
        let job = ForgetAllJob(v: 1, jobId: "job-1")
        var erased = 0
        let first = await ForgetAllRunner.run(job, erase: { erased += 1; return ForgetAllPart.allCases }, api: api)
        let again = await ForgetAllRunner.run(job, erase: { erased += 1; return ForgetAllPart.allCases }, api: api)
        XCTAssertTrue(first)
        XCTAssertTrue(again)
        XCTAssertEqual(erased, 2, "a resent job deletes again")
        let confirms = await api.confirms
        XCTAssertEqual(confirms.map(\.0), ["job-1", "job-1"])
        XCTAssertEqual(confirms.first?.1.idempotencyKey, confirms.last?.1.idempotencyKey, "the same job is the same confirm")
        XCTAssertTrue(confirms.first?.1.forgot.contains("watch") == true)
    }

    @MainActor
    func testAFailedEraseOrConfirmLeavesTheJobForTheComputerToResend() async {
        let api = RecordingAPI()
        let job = ForgetAllJob(v: 1, jobId: "job-2")
        let failedErase = await ForgetAllRunner.run(job, erase: { nil }, api: api)
        XCTAssertFalse(failedErase)
        let confirmsAfterFailedErase = await api.confirms.count
        XCTAssertEqual(confirmsAfterFailedErase, 0, "nothing is confirmed that wasn't deleted")
        await api.setFails(true)
        let failedConfirm = await ForgetAllRunner.run(job, erase: { ForgetAllPart.allCases }, api: api)
        XCTAssertFalse(failedConfirm)
    }

    /// The caches are empty afterward: the saved briefs, Your work and its waiting answers, and chat.
    @MainActor
    func testEraseEmptiesThePhonesCachesAndKeepsThePairing() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let briefs = directory.appendingPathComponent("briefs")
        let chats = directory.appendingPathComponent("chats")
        let journal = directory.appendingPathComponent("writes.json")
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: briefs, journalURL: journal, chatDirectory: chats)
        let fileManager = FileManager.default
        try fileManager.createDirectory(at: briefs, withIntermediateDirectories: true)
        try fileManager.createDirectory(at: chats, withIntermediateDirectories: true)
        try Data(#"{"saved":"brief"}"#.utf8).write(to: briefs.appendingPathComponent("a.json"))
        try Data(#"{"saved":"chat"}"#.utf8).write(to: chats.appendingPathComponent("main.json"))
        let workFile = directory.appendingPathComponent("work.json")
        try Data(#"{"saved":"work"}"#.utf8).write(to: workFile)
        let pairingFiles = Set(try fileManager.contentsOfDirectory(atPath: directory.path))

        let forgot = await environment.eraseForComputer()

        XCTAssertEqual(forgot.map(Set.init), Set(ForgetAllPart.allCases))
        XCTAssertFalse(fileManager.fileExists(atPath: workFile.path), "Your work and its waiting answers are gone")
        XCTAssertEqual(Self.files(in: briefs), [], "no saved brief is left")
        XCTAssertEqual(Self.files(in: chats), [], "no chat is left")
        XCTAssertNil(environment.brief.current)
        XCTAssertNil(environment.work.page)
        XCTAssertTrue(environment.work.pending.isEmpty)
        let after = Set(try fileManager.contentsOfDirectory(atPath: directory.path))
        XCTAssertTrue(after.isSuperset(of: pairingFiles.subtracting(["work.json", "briefs", "chats"])),
                      "the pairing's own files stay")
    }

    private static func files(in directory: URL) -> [String] {
        let names = (try? FileManager.default.contentsOfDirectory(atPath: directory.path)) ?? []
        return names.filter { !$0.hasPrefix(".") }.filter { name in
            var isDirectory: ObjCBool = false
            FileManager.default.fileExists(atPath: directory.appendingPathComponent(name).path, isDirectory: &isDirectory)
            return !isDirectory.boolValue
        }
    }
}
