import Foundation
import XCTest
@testable import Yansu

/// Scope cut A6: the phone without Chat. The computer's `chat` switch (A1) leaves `chatMainSend` and `briefAsk`
/// out of /status and answers 404 under /chats.
@MainActor
final class ChatAvailabilityTests: XCTestCase {
    private func status(chatMainSend: Bool?, briefAsk: Bool?) -> HubStatus {
        HubStatus(hubName: "Mac", appVersion: "1.0.0", protocolVersion: 1, narrativeAvailable: true, lastBriefAt: nil,
                  chatMainSend: chatMainSend, briefAsk: briefAsk)
    }

    private func isolatedDefaults() -> UserDefaults {
        let name = "ChatAvailabilityTests.\(UUID().uuidString)"
        addTeardownBlock { UserDefaults().removePersistentDomain(forName: name) }
        return UserDefaults(suiteName: name)!
    }

    // MARK: The seam

    func testChatIsOnExactlyWhenTheComputerSendsChatMainSend() throws {
        let decoder = JSONDecoder()
        let older = try decoder.decode(HubStatus.self, from: Data(#"{"chatMainSend":true,"briefAsk":true}"#.utf8))
        XCTAssertTrue(older.chatAvailable, "a computer without A1 always sends it, so Chat stays")
        XCTAssertTrue(older.briefAskAvailable)
        let off = try decoder.decode(HubStatus.self, from: Data(#"{"hubName":"Mac","topics":true}"#.utf8))
        XCTAssertFalse(off.chatAvailable, "A1 omits the field; missing is off, not unknown")
        XCTAssertFalse(off.briefAskAvailable)
        XCTAssertFalse(status(chatMainSend: false, briefAsk: true).briefAskAvailable)
    }

    // MARK: The state machine

    func testTransitionsFollowTheComputer() {
        var state = ChatAvailability.unknown
        var effects: [ChatAvailability.Effect] = []
        func send(_ event: ChatAvailability.Event) {
            (state, effects) = ChatAvailability.transition(state, event)
        }

        send(.statusRead(chatMainSend: true, briefAsk: true))
        XCTAssertEqual(state, .on(briefAsk: true))
        XCTAssertEqual(effects, [.save])
        send(.statusRead(chatMainSend: true, briefAsk: true))
        XCTAssertEqual(effects, [], "the same answer saves nothing")

        send(.statusRead(chatMainSend: nil, briefAsk: nil))
        XCTAssertEqual(state, .off(cachePurged: false))
        XCTAssertEqual(effects, [.save, .purgeChatCache])
        send(.purgeFinished)
        XCTAssertEqual(state, .off(cachePurged: true))
        XCTAssertEqual(effects, [.save])
        send(.statusRead(chatMainSend: nil, briefAsk: nil))
        XCTAssertEqual(effects, [], "the purge runs once")
        send(.purgeFinished)
        XCTAssertEqual(effects, [])

        send(.statusRead(chatMainSend: true, briefAsk: nil))
        XCTAssertEqual(state, .on(briefAsk: false), "switched back on, without the brief's Ask")
        XCTAssertFalse(state.briefAskShown)
        XCTAssertTrue(state.chatShown)
        send(.unpaired)
        XCTAssertEqual(state, .unknown)
        XCTAssertEqual(effects, [.save])
        send(.unpaired)
        XCTAssertEqual(effects, [])
    }

    func testAFailedPurgeRunsAgainOnTheNextRead() {
        var (state, effects) = ChatAvailability.transition(.on(briefAsk: true), .statusRead(chatMainSend: nil, briefAsk: nil))
        XCTAssertEqual(effects, [.save, .purgeChatCache])
        (state, effects) = ChatAvailability.transition(state, .purgeFailed)
        XCTAssertEqual(state, .off(cachePurged: false))
        XCTAssertEqual(effects, [])
        (state, effects) = ChatAvailability.transition(state, .statusRead(chatMainSend: false, briefAsk: nil))
        XCTAssertEqual(effects, [.purgeChatCache])
    }

    func testUnknownKeepsWhatThePhoneShowedBefore() {
        XCTAssertTrue(ChatAvailability.unknown.chatShown)
        XCTAssertTrue(ChatAvailability.unknown.briefAskShown)
        XCTAssertFalse(ChatAvailability.off(cachePurged: true).chatShown)
        XCTAssertFalse(ChatAvailability.off(cachePurged: false).briefAskShown)
    }

    // MARK: RootView tabs

    func testRootTabsWithAndWithoutAWatchAndChat() {
        XCTAssertEqual(PhoneTabs.root(chatAvailable: true, watchPaired: true), [.today, .watch, .chat])
        XCTAssertEqual(PhoneTabs.root(chatAvailable: true, watchPaired: false), [.today, .chat])
        XCTAssertEqual(PhoneTabs.root(chatAvailable: false, watchPaired: true), [.today, .watch])
        XCTAssertEqual(PhoneTabs.root(chatAvailable: false, watchPaired: false), [.today])

        XCTAssertTrue(PhoneTabs.showsTabBar([.today, .watch]))
        XCTAssertFalse(PhoneTabs.showsTabBar([.today]), "without a watch the phone has one page and no tab bar")

        XCTAssertEqual(DuoTab.rail(showsWatch: true, showsChat: false), [.today, .watch])
        XCTAssertEqual(DuoTab.rail(showsWatch: false, showsChat: false), [.today])
        XCTAssertEqual(DuoTab.rail(showsWatch: true), [.today, .watch, .chat], "the wide layout keeps Chat by default")
        XCTAssertEqual(DuoTab.shown(for: .chat, showsWatch: true, showsChat: false), .today)
    }

    func testAGoneTabLandsOnToday() {
        let noChat = PhoneTabs.root(chatAvailable: false, watchPaired: true)
        XCTAssertEqual(PhoneTabs.landing(.chat, in: noChat), .today)
        XCTAssertEqual(PhoneTabs.landing(.watch, in: noChat), .watch)
        XCTAssertEqual(PhoneTabs.landing(.watch, in: [.today]), .today)
        XCTAssertEqual(PhoneTabs.landing(.today, in: [.today]), .today)
    }

    // MARK: The purge

    func testChatOffPurgesTheChatCacheOnceAndLeavesTheBriefCache() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let fileManager = FileManager.default
        let briefs = directory.appendingPathComponent("briefs")
        let chats = directory.appendingPathComponent("chats")
        try fileManager.createDirectory(at: briefs, withIntermediateDirectories: true)
        try fileManager.createDirectory(at: chats, withIntermediateDirectories: true)
        let defaults = isolatedDefaults()
        var answer = status(chatMainSend: nil, briefAsk: nil)
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: briefs, journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: chats, pushDefaults: defaults,
                                         chatStatusRead: { answer })
        // Written after start-up, which drops unreadable cache files of its own accord.
        try fileManager.createDirectory(at: briefs, withIntermediateDirectories: true)
        try fileManager.createDirectory(at: chats, withIntermediateDirectories: true)
        let briefFile = briefs.appendingPathComponent("a.json")
        try Data(#"{"saved":"brief"}"#.utf8).write(to: briefFile)
        try Data(#"{"saved":"chat"}"#.utf8).write(to: chats.appendingPathComponent("main.json"))
        try Data(#"[]"#.utf8).write(to: chats.appendingPathComponent("main-drafts.json"))
        let pairingFiles = Set(try fileManager.contentsOfDirectory(atPath: directory.path))

        await environment.chatAvailability.refresh()

        XCTAssertEqual(environment.chatAvailability.state, .off(cachePurged: true))
        XCTAssertFalse(fileManager.fileExists(atPath: chats.path), "the Chat cache is gone")
        XCTAssertTrue(fileManager.fileExists(atPath: briefFile.path), "the brief cache stays")
        let after = Set(try fileManager.contentsOfDirectory(atPath: directory.path))
        XCTAssertEqual(after, pairingFiles.subtracting(["chats"]), "only the Chat cache went")

        // Once: a later read does not purge again, so nothing written since is touched.
        try fileManager.createDirectory(at: chats, withIntermediateDirectories: true)
        let later = chats.appendingPathComponent("later.json")
        try Data("{}".utf8).write(to: later)
        await environment.chatAvailability.refresh()
        XCTAssertTrue(fileManager.fileExists(atPath: later.path))

        // Idempotent: the purge itself can run again on an empty cache.
        XCTAssertTrue(environment.chat.purgeForChatOff())
        XCTAssertTrue(environment.chat.purgeForChatOff())

        // The next launch remembers the answer, so Chat does not flash in before /status.
        let relaunched = ChatAvailabilityStore(defaults: defaults, readStatus: { answer }, purge: { XCTFail(); return false })
        XCTAssertEqual(relaunched.state, .off(cachePurged: true))
        XCTAssertFalse(relaunched.chatShown)

        answer = status(chatMainSend: true, briefAsk: true)
        await environment.chatAvailability.refresh()
        XCTAssertTrue(environment.chatAvailability.chatShown, "Chat comes back when the computer offers it again")
    }

    func testAFailedStatusReadChangesNothing() async {
        let store = ChatAvailabilityStore(state: .off(cachePurged: true), defaults: isolatedDefaults(),
                                          readStatus: { throw HubRequestError.noAnswer },
                                          purge: { XCTFail("no purge"); return true })
        await store.refresh()
        XCTAssertEqual(store.state, .off(cachePurged: true))
    }

    // MARK: Today with /chats answering 404

    private actor TodayHub: TodayHubAPI {
        let value: MobileTodaySnapshot
        init(value: MobileTodaySnapshot) { self.value = value }
        func today() async throws -> MobileTodaySnapshot { value }
        func pollTodayEvents() async throws -> [MobileTodayEvent] { [] }
        func requestTodayBrief(key: String) async throws -> BriefRefreshResult {
            BriefRefreshResult(status: "queued", nextAllowedAt: nil, readyAt: nil)
        }
    }

    /// The computer with Chat off: every route under /chats answers 404 ("mobile route not found").
    private final class NotFoundChatHub: ChatHubAPI, @unchecked Sendable {
        private let lock = NSLock()
        private var count = 0
        var requests: Int { lock.lock(); defer { lock.unlock() }; return count }
        private func notFound() -> Error {
            lock.lock(); count += 1; lock.unlock()
            return HubRequestError.rejected(status: 404, message: "mobile route not found")
        }
        func chats(limit: Int) async throws -> [ChatSummaryView] { throw notFound() }
        func chat(id: String, after: Int?, limit: Int) async throws -> ChatDetail { throw notFound() }
        func createChat(title: String, idempotencyKey: String) async throws -> String { throw notFound() }
        func sendMessage(chatID: String, text: String, idempotencyKey: String) async throws -> ChatMessageAccepted {
            throw notFound()
        }
        func sendMessage(chatID: String, text: String, offerReferences: [ChatOfferReference],
                         idempotencyKey: String) async throws -> ChatMessageAccepted { throw notFound() }
        func answerPermission(chatID: String, requestID: String, optionID: String,
                              idempotencyKey: String) async throws -> Bool { throw notFound() }
        func cancel(chatID: String, idempotencyKey: String) async throws -> Bool { throw notFound() }
        func pollChatTurns() async throws -> [MobileChatTurnEvent] { throw notFound() }
    }

    func testTodayRendersWhileChatsAnswers404() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        var value = MobileTodaySnapshot(generatedAt: "2026-11-24T12:00:00Z", latestBrief: nil, briefs: [],
                                        needsYou: [], needsYouCount: 0,
                                        day: TodayDay(next: nil, free: [], calendarConnected: false), topics: [],
                                        briefRequest: nil, schedule: MobileBriefSchedule(morning: "07:00", evening: "18:00"))
        value.latestBrief = TodayBriefIndex(id: "b1", slot: "morning", localDate: "2026-11-24", scheduledAt: nil,
                                            madeAt: "2026-11-24T12:00:00Z", catchUp: nil, durationSeconds: nil, lead: "Morning")
        value.briefs = [value.latestBrief!]
        let chatHub = NotFoundChatHub()
        let environment = AppEnvironment(directory: directory, secrets: InMemorySecretStore(),
                                         briefDirectory: directory.appendingPathComponent("briefs"),
                                         journalURL: directory.appendingPathComponent("writes.json"),
                                         chatDirectory: directory.appendingPathComponent("chats"),
                                         pushDefaults: isolatedDefaults(), chatHub: chatHub,
                                         chatStatusRead: { self.status(chatMainSend: nil, briefAsk: nil) })
        let today = TodayStore(hub: TodayHub(value: value),
                               cache: TodayDiskCache(fileURL: directory.appendingPathComponent("today.json")),
                               brief: environment.brief,
                               identity: { TodayPairingIdentity(hubID: "mac", deviceID: "phone") })

        await environment.chatAvailability.refresh()
        await today.refresh()

        XCTAssertFalse(environment.chatAvailability.chatShown)
        XCTAssertEqual(PhoneTabs.root(chatAvailable: environment.chatAvailability.chatShown, watchPaired: false), [.today])
        XCTAssertEqual(today.snapshot?.latestBrief?.id, "b1", "Today has its snapshot")
        XCTAssertFalse(today.hubUnreachable, "no error card")
        XCTAssertNil(today.connectionCause)
        XCTAssertEqual(chatHub.requests, 0, "nothing on Today asks /chats, so there is no retry loop")
    }
}
