import Foundation
import XCTest
import UserNotifications
@testable import Yansu

// Holds the first token response without blocking delivery of a concurrent second request.
private final class HeldPushURLProtocol: URLProtocol {
    static var firstRequest: (() -> Void)?
    static var secondRequest: (() -> Void)?
    private static let lock = NSLock()
    private static var held: HeldPushURLProtocol?
    private static var recorded: [URLRequest] = []

    static func reset() {
        lock.lock()
        defer { lock.unlock() }
        held = nil
        recorded = []
        firstRequest = nil
        secondRequest = nil
    }

    static var requests: [URLRequest] {
        lock.lock()
        defer { lock.unlock() }
        return recorded
    }

    static func releaseFirst() {
        lock.lock()
        let response = held
        held = nil
        lock.unlock()
        response?.respond()
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lock.lock()
        Self.recorded.append(request)
        let isFirst = Self.recorded.count == 1
        if isFirst { Self.held = self }
        Self.lock.unlock()
        if isFirst {
            Self.firstRequest?()
        } else {
            Self.secondRequest?()
            respond()
        }
    }
    override func stopLoading() {}

    private func respond() {
        let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                       httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{\"ok\":true,\"data\":{\"enabled\":true}}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
}

// Pauses retry backoff without a real clock delay; cancellation is checked by the registration loop.
/// The direct tunnel's local origin: nil until it opens.
private final class TunnelOrigin: @unchecked Sendable {
    private let lock = NSLock()
    private var value: URL?
    var origin: URL? { lock.lock(); defer { lock.unlock() }; return value }
    func open(_ url: URL) { lock.lock(); value = url; lock.unlock() }
}

private actor PushRetryGate {
    private var sleeping: [CheckedContinuation<Void, Never>] = []
    private var waiting: [(Int, CheckedContinuation<Void, Never>)] = []
    private(set) var delays: [UInt64] = []

    func sleep(_ delay: UInt64) async {
        delays.append(delay)
        await withCheckedContinuation { continuation in
            sleeping.append(continuation)
            for (count, waiter) in waiting where delays.count >= count { waiter.resume() }
            waiting.removeAll { delays.count >= $0.0 }
        }
    }

    func waitUntilSleeping(count: Int? = nil) async {
        let count = count ?? (sleeping.isEmpty ? delays.count + 1 : delays.count)
        guard delays.count < count else { return }
        await withCheckedContinuation { waiting.append((count, $0)) }
    }

    func release() {
        guard !sleeping.isEmpty else { return }
        sleeping.removeFirst().resume()
    }
}

// Push behaviour for the brief alert: payload recognition, tab routing, and the opt-in rules.
// Permission prompts and APNs registration are never triggered from tests.
@MainActor
final class AgentPushTests: XCTestCase {
    func testTopicFilterOpensAndClearsInChat() {
        let router = NotificationRouter()
        let topic = TopicRoute(id: "halcyon", name: "Halcyon launch")
        router.openTopic(topic)
        XCTAssertEqual(router.selectedTab, .chat)
        XCTAssertEqual(router.topicFilter, topic)
        router.openMainChat()
        XCTAssertNil(router.topicFilter)
        XCTAssertEqual(router.selectedTab, .chat)
    }

    private var suiteName: String!
    private var defaults: UserDefaults!

    override func setUpWithError() throws {
        try super.setUpWithError()
        suiteName = "yansu.tests.push.\(UUID().uuidString)"
        defaults = try XCTUnwrap(UserDefaults(suiteName: suiteName))
    }

    override func tearDownWithError() throws {
        defaults.removePersistentDomain(forName: suiteName)
        try super.tearDownWithError()
    }

    func testBriefPayloadIsRecognisedAndOtherPayloadsAreNot() {
        let brief: [AnyHashable: Any] = ["aps": ["alert": ["title": "Isoform", "body": "Your brief is ready."]],
                                         "yansu": ["kind": "brief", "hub": "11111111-2222-4333-8444-555555555555", "eid": "7"]]
        XCTAssertTrue(NotificationRouter.isBriefPush(brief))
        XCTAssertFalse(NotificationRouter.isBriefPush(["yansu": ["kind": "chat"]]))
        XCTAssertFalse(NotificationRouter.isBriefPush(["aps": [:]]))
        XCTAssertFalse(NotificationRouter.isBriefPush(["yansu": "brief"]))
    }

    func testALegacyNeedsYouPushOpensTheBriefLikeABriefPush() {
        let needsYou: [AnyHashable: Any] = ["yansu": ["kind": "needs_you", "hub": "11111111-2222-4333-8444-555555555555", "eid": "event:7"]]
        XCTAssertTrue(NotificationRouter.opensBrief(needsYou))
        XCTAssertFalse(NotificationRouter.isBriefPush(needsYou))
        XCTAssertTrue(NotificationRouter.opensBrief(["yansu": ["kind": "brief"]]))
        XCTAssertFalse(NotificationRouter.opensBrief(["yansu": ["kind": "chat"]]))
        XCTAssertFalse(NotificationRouter.opensBrief(["aps": [:]]))
    }

    func testBriefPushSelectsTheFeedTabAndCountsTheRefresh() {
        let router = NotificationRouter()
        router.selectedTab = .tasks
        router.briefPushReceived()
        XCTAssertEqual(router.selectedTab, .today)
        XCTAssertEqual(router.briefPushes, 1)
    }

    func testWorkNoticesOpenChatAndOtherNoticesOpenTheFeed() {
        let router = NotificationRouter()
        router.selectedTab = .tasks
        router.noticeOpened(NoticeRouting.route(kind: "work_blocked"))
        XCTAssertEqual(router.selectedTab, .chat)
        XCTAssertEqual(router.briefPushes, 0)

        router.noticeOpened(NoticeRouting.route(kind: "meeting_ended"))
        XCTAssertEqual(router.selectedTab, .today)
        XCTAssertEqual(router.briefPushes, 1)
    }

    func testWeeklyRecapNoticeOpensToday() {
        let router = NotificationRouter()
        router.selectedTab = .chat
        router.noticeOpened(NoticeRouting.route(kind: "weekly_recap"))
        XCTAssertEqual(router.selectedTab, .today)
        XCTAssertEqual(router.briefPushes, 1)
    }

    func testTodayIsTheFirstTabAndDeepLinksOpenTheirTab() throws {
        let router = NotificationRouter()
        XCTAssertEqual(router.selectedTab, .today)
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://brief")))
        XCTAssertEqual(router.selectedTab, .today, "the widget's Brief link opens the Feed")
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://chat")))
        XCTAssertEqual(router.selectedTab, .chat)
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://desk")))
        XCTAssertEqual(router.selectedTab, .today, "an older widget's Desk link opens the Feed")
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://goals")))
        XCTAssertEqual(router.selectedTab, .today)
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://ideas")))
        XCTAssertEqual(router.selectedTab, .tasks)
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://somewhere-else")))
        XCTAssertEqual(router.selectedTab, .today)
        XCTAssertNotNil(router.linkNotice)
    }

    func testTodayNoticeTargetsAndBriefLink() throws {
        let router = NotificationRouter()
        router.openTodayNotice(kind: "task", id: "task-1")
        XCTAssertEqual(router.selectedTab, .tasks)
        XCTAssertEqual(router.pendingTaskID, "task-1")
        router.consumePendingTaskID("task-1")
        XCTAssertNil(router.pendingTaskID)
        router.openTodayNotice(kind: "chat", id: "moment-1")
        XCTAssertEqual(router.selectedTab, .chat)
        XCTAssertEqual(router.pendingMomentID, "moment-1")
        router.openTodayNotice(kind: "task", id: "bad/id")
        XCTAssertEqual(router.selectedTab, .today)
        XCTAssertNil(router.pendingTaskID)
        router.openDeepLink(try XCTUnwrap(URL(string: "isoform://brief")), currentBriefID: "brief-1")
        XCTAssertEqual(router.briefRoute?.briefID, "brief-1")
        let cold = NotificationRouter()
        cold.openDeepLink(try XCTUnwrap(URL(string: "isoform://brief")))
        XCTAssertTrue(cold.pendingCurrentBrief)
        cold.resolveCurrentBrief("brief-2")
        XCTAssertEqual(cold.briefRoute?.briefID, "brief-2")
        XCTAssertFalse(cold.pendingCurrentBrief)
    }

    func testSettingsOpensOverTheTabThatIsShowing() {
        let router = NotificationRouter()
        router.selectedTab = .tasks
        // Older callers still select the Settings tab; Settings opens and the Goals tab stays.
        router.selectedTab = .settings
        XCTAssertEqual(router.selectedTab, .tasks)
        XCTAssertEqual(router.sheet, .settings)
        router.sheet = nil
        router.openSettings()
        XCTAssertEqual(router.sheet, .settings)
        XCTAssertEqual(router.selectedTab, .tasks)
    }

    func testRoutesFromPushesNoticesAndLinksCloseSettings() throws {
        let router = NotificationRouter()
        router.openSettings()
        router.briefPushReceived()
        XCTAssertNil(router.sheet, "a brief tap shows the Feed, not Settings")

        router.openPairing()
        router.noticeOpened(NoticeRouting.route(kind: "work_blocked"))
        XCTAssertNil(router.sheet)
        XCTAssertEqual(router.selectedTab, .chat)

        router.openSettings()
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://brief")))
        XCTAssertNil(router.sheet, "the widget's link shows the Feed")
        XCTAssertEqual(router.selectedTab, .today)

        router.openSettings()
        router.openDeepLink(try XCTUnwrap(URL(string: "yansu://somewhere-else")))
        XCTAssertNil(router.sheet)
        XCTAssertEqual(router.selectedTab, .today)
        XCTAssertNotNil(router.linkNotice)
    }

    func testPairingIsItsOwnSheetSoSettingsNeverReopensIt() {
        let router = NotificationRouter()
        router.openPairing()
        XCTAssertEqual(router.sheet, .pairing)
        router.sheet = nil
        // Settings opened later holds no pairing request to replay.
        router.openSettings()
        XCTAssertEqual(router.sheet, .settings)
        // A pairing link while Settings is open shows Pairing in its place.
        router.openPairing()
        XCTAssertEqual(router.sheet, .pairing)
    }

    func testPairingSuccessPointsAtTheFeed() {
        XCTAssertTrue(PairingView.pairedDetail.contains("Today"))
        XCTAssertTrue(PairingView.pairedDetail.contains("brief"))
    }

    func testOpenPairingOpensSettingsAndCountsTheRequest() {
        let router = NotificationRouter()
        router.openPairing()
        XCTAssertEqual(router.sheet, .pairing)
        XCTAssertEqual(router.selectedTab, .today)
        router.pairingFinished()
        XCTAssertNil(router.sheet)
        XCTAssertEqual(router.selectedTab, .today, "the first brief shows on the Feed")
    }

    func testPushEnvironmentIsAlwaysDevelopmentOrProduction() {
        XCTAssertTrue(["development", "production"].contains(PushEnvironment.current))
    }

    func testPushStaysOffUntilTheUserOptsIn() async throws {
        let registration = try makeRegistration()
        XCTAssertEqual(registration.state, .off)
        XCTAssertFalse(registration.isOptedIn)
        await registration.resumeIfOptedIn()
        XCTAssertEqual(registration.state, .off)
    }

    func testOptedInLaunchDoesNotAskAPNsBeforeThePhoneIsPaired() async throws {
        defaults.set(true, forKey: PushRegistration.optInKey)
        let registration = try makeRegistration()
        XCTAssertEqual(registration.state, .registering)
        await registration.resumeIfOptedIn()
        XCTAssertEqual(registration.state, .registering)
    }

    func testUnpairedLaunchCanRetryRevocationsEvenWhenPushOptInSurvived() throws {
        defaults.set(true, forKey: PushRegistration.optInKey)
        let registration = try makeRegistration()
        XCTAssertNil(registration.token)
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertTrue(registration.isReadyForRevocation)
    }

    func testTokenStaysInMemoryAndIsNotUploadedWithoutOptIn() async throws {
        let registration = try makeRegistration()
        await registration.receiveToken(Data([0x01, 0xab, 0xff]))
        XCTAssertEqual(registration.token, "01abff")
        XCTAssertEqual(registration.state, .off)
        XCTAssertTrue(defaults.dictionaryRepresentation().keys.allSatisfy { !$0.contains("token") })
    }

    func testRegistrationFailureKeepsTheSwitchOnAndTriesAgainOnTheNextLaunch() async throws {
        let vault = try makePairedVault()
        let permission = PushRetryGate()
        var registrations = 0
        let registration = PushRegistration(vault: vault, defaults: defaults,
                                            authorize: { await permission.sleep(0); return true },
                                            registerRemoteNotifications: { registrations += 1 },
                                            unregisterRemoteNotifications: {})
        // A token delivered while opted out must not make the next attempt look successful.
        await registration.receiveToken(Data([0x01]))
        let enable = Task { await registration.enable() }
        await permission.waitUntilSleeping()
        XCTAssertEqual(registration.state, .registering)
        XCTAssertFalse(registration.isEnabled)
        XCTAssertNotNil(registration.statusMessage)
        await permission.release()
        await enable.value
        XCTAssertEqual(registrations, 1)
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertFalse(registration.isEnabled, "APNs has not accepted this build yet")

        // One failed launch (no connection, a passing APNs error) leaves the person's switch on.
        registration.receiveRegistrationFailure()
        XCTAssertFalse(registration.isEnabled)
        XCTAssertTrue(registration.isOptedIn, "only the switch or iOS Settings turns notifications off")
        XCTAssertEqual(registration.state, .unavailable)
        XCTAssertEqual(registration.statusMessage,
                       "This phone couldn't register for push notifications. The test notification still works.")

        // The next launch asks APNs again.
        var retried = 0
        let restored = PushRegistration(vault: vault, defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)),
                                        authorizationStatus: { .authorized },
                                        registerRemoteNotifications: { retried += 1 })
        await restored.resumeIfOptedIn()
        XCTAssertTrue(restored.isOptedIn)
        XCTAssertEqual(retried, 1, "a failed registration is tried again, not left off")
        XCTAssertEqual(restored.state, .registering)
    }

    func testDeniedPermissionKeepsSwitchOffWithAnIOSSettingsReason() async throws {
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        let registration = PushRegistration(vault: vault, defaults: defaults, authorize: { false },
                                            registerRemoteNotifications: { XCTFail("Denied permission must not ask APNs") },
                                            unregisterRemoteNotifications: {})

        await registration.enable()

        XCTAssertFalse(registration.isEnabled)
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertEqual(registration.state, .denied)
        XCTAssertEqual(registration.statusMessage, "Notifications for Isoform are off in iOS Settings.")

        let restored = PushRegistration(vault: vault, defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)),
                                        authorize: { XCTFail("Relaunch must not ask for permission"); return true },
                                        registerRemoteNotifications: { XCTFail("Denied permission must stay off after relaunch") })
        await restored.resumeIfOptedIn()
        XCTAssertFalse(restored.isEnabled)
        XCTAssertFalse(restored.isOptedIn)
        XCTAssertEqual(restored.state, .denied)
        XCTAssertEqual(restored.failureReason, .permissionDenied)
        XCTAssertEqual(restored.statusMessage, registration.statusMessage)
    }

    func testRelayFailureReasonsSurviveRelaunchUntilRegistrationSucceedsWithoutSavingPayloads() async throws {
        let cases: [(Int?, PushRegistration.FailureReason)] = [
            (400, .tokenRejected), (409, .deviceLimit), (503, .relayUnavailable), (nil, .relayUnreachable)
        ]
        for (status, reason) in cases {
            defaults.removePersistentDomain(forName: suiteName)
            let vault = try makePairedVault()
            defaults.set(true, forKey: PushRegistration.optInKey)
            let privatePayload = "private-relay-error-must-not-be-saved"
            StubURLProtocol.install { request, _ in
                guard let status else {
                    throw NSError(domain: NSURLErrorDomain, code: URLError.notConnectedToInternet.rawValue,
                                  userInfo: [NSLocalizedDescriptionKey: privatePayload])
                }
                let response = HTTPURLResponse(url: request.url!, statusCode: status,
                                               httpVersion: "HTTP/1.1", headerFields: nil)!
                return (response, Data("{\"ok\":false,\"error\":\"\(privatePayload)\"}".utf8))
            }
            let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults, retrySleep: { _ in throw CancellationError() })
            await registration.receiveToken(Data([0xde, 0xad, 0xbe, 0xef]))
            XCTAssertTrue(registration.isOptedIn)
            XCTAssertEqual(registration.state, .unavailable)
            XCTAssertEqual(registration.failureReason, reason)
            let saved = try XCTUnwrap(defaults.persistentDomain(forName: suiteName))
            XCTAssertEqual(Set(saved.keys), Set([PushRegistration.optInKey, PushRegistration.failureReasonKey]))
            XCTAssertEqual(saved[PushRegistration.failureReasonKey] as? String, reason.rawValue)
            let savedData = try PropertyListSerialization.data(fromPropertyList: saved, format: .xml, options: 0)
            let savedText = String(decoding: savedData, as: UTF8.self)
            XCTAssertFalse(savedText.contains(privatePayload))
            XCTAssertFalse(savedText.contains("deadbeef"))
            XCTAssertFalse(savedText.contains("new-bearer"))

            let restored = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)),
                                            authorize: { XCTFail("Relaunch must only inspect existing permission"); return false },
                                            authorizationStatus: { .authorized }, registerRemoteNotifications: {},
                                            unregisterRemoteNotifications: {})
            XCTAssertTrue(restored.isOptedIn)
            XCTAssertFalse(restored.isEnabled, "A token from the previous process must not be restored")
            XCTAssertNil(restored.token)
            XCTAssertEqual(restored.state, .unavailable)
            XCTAssertEqual(restored.statusMessage, registration.statusMessage)
            await restored.resumeIfOptedIn()
            XCTAssertEqual(restored.state, .registering)
            XCTAssertEqual(restored.failureReason, reason, "A launch retry keeps the last failure visible")
            XCTAssertEqual(restored.statusMessage, registration.statusMessage)

            StubURLProtocol.install { request, _ in
                let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                               httpVersion: "HTTP/1.1", headerFields: nil)!
                return (response, Data("{\"ok\":true,\"data\":{\"enabled\":true}}".utf8))
            }
            await restored.receiveToken(Data([0x01]))
            XCTAssertTrue(restored.isEnabled)
            XCTAssertEqual(restored.state, .on)
            XCTAssertNil(restored.failureReason)
            XCTAssertNil(restored.statusMessage)
            XCTAssertNil(defaults.object(forKey: PushRegistration.failureReasonKey))
            let afterSuccess = PushRegistration(vault: vault, defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)))
            XCTAssertNil(afterSuccess.failureReason)
            XCTAssertEqual(afterSuccess.state, .registering)
        }
    }

    func testExplicitDisableClearsEverySavedFailureAcrossRelaunch() async throws {
        let vault = try makePairedVault()
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        for reason in PushRegistration.FailureReason.allCases {
            defaults.set(true, forKey: PushRegistration.optInKey)
            defaults.set(reason.rawValue, forKey: PushRegistration.failureReasonKey)
            let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults, unregisterRemoteNotifications: {})
            XCTAssertEqual(registration.failureReason, reason)
            await registration.disable()
            XCTAssertNil(defaults.object(forKey: PushRegistration.failureReasonKey))
            let restored = PushRegistration(vault: vault, defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)))
            XCTAssertFalse(restored.isOptedIn)
            XCTAssertFalse(restored.isEnabled)
            XCTAssertEqual(restored.state, .off)
            XCTAssertNil(restored.statusMessage)
        }
    }

    func testAuthorizedProvisionalAndEphemeralLaunchResumeWithoutPrompting() async throws {
        let vault = try makePairedVault()
        for status: UNAuthorizationStatus in [.authorized, .provisional, .ephemeral] {
            defaults.set(true, forKey: PushRegistration.optInKey)
            var checks = 0
            var registrations = 0
            let registration = PushRegistration(vault: vault, defaults: defaults,
                                                authorize: { XCTFail("Launch must not request permission"); return false },
                                                authorizationStatus: { checks += 1; return status },
                                                registerRemoteNotifications: { registrations += 1 },
                                                unregisterRemoteNotifications: {})
            await registration.resumeIfOptedIn()
            XCTAssertEqual(checks, 1)
            XCTAssertEqual(registrations, 1)
            XCTAssertTrue(registration.isOptedIn)
            XCTAssertFalse(registration.isEnabled, "APNs must still provide a token")
            XCTAssertEqual(registration.state, .registering)
            XCTAssertNil(registration.failureReason)
            await registration.disable()
        }
    }

    func testDeniedAndUndeterminedLaunchStopRegistrationWithoutPromptingAndPersistTheReason() async throws {
        let vault = try makePairedVault()
        let cases: [(UNAuthorizationStatus, PushRegistration.FailureReason)] = [
            (.denied, .permissionDenied), (.notDetermined, .permissionRequired)
        ]
        for (status, reason) in cases {
            defaults.removePersistentDomain(forName: suiteName)
            defaults.set(true, forKey: PushRegistration.optInKey)
            var checks = 0
            var unregistrations = 0
            StubURLProtocol.install { request, _ in
                let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                               httpVersion: "HTTP/1.1", headerFields: nil)!
                return (response, Data("{\"ok\":true}".utf8))
            }
            let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults,
                                                authorize: { XCTFail("Launch must not request permission"); return true },
                                                authorizationStatus: { checks += 1; return status },
                                                registerRemoteNotifications: { XCTFail("Permission is missing") },
                                                unregisterRemoteNotifications: { unregistrations += 1 })
            XCTAssertNil(registration.token, "A cold launch cannot depend on the previous process's token")
            await registration.resumeIfOptedIn()
            let requests = StubURLProtocol.requests.map(\.0)
            XCTAssertEqual(requests.map(\.httpMethod), ["DELETE"], "Remove the previous relay registration without a new PUT")
            XCTAssertEqual(requests.first?.url?.path, "/v1/push/token")
            XCTAssertEqual(requests.first?.value(forHTTPHeaderField: "Authorization"), "Bearer new-bearer")
            XCTAssertEqual(checks, 1)
            XCTAssertEqual(unregistrations, 1)
            XCTAssertFalse(registration.isOptedIn)
            XCTAssertFalse(registration.isEnabled)
            XCTAssertEqual(registration.state, .denied)
            XCTAssertEqual(registration.failureReason, reason)
            XCTAssertEqual(defaults.string(forKey: PushRegistration.failureReasonKey), reason.rawValue)
            let restored = PushRegistration(vault: vault, defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)),
                                            authorizationStatus: { XCTFail("An opted-out launch needs no permission read"); return .authorized })
            await restored.resumeIfOptedIn()
            XCTAssertEqual(restored.state, .denied)
            XCTAssertEqual(restored.statusMessage, registration.statusMessage)
            XCTAssertFalse(restored.isEnabled)
        }
    }

    func testDeniedPermissionReasonSurvivesFailedRelayCleanupAndRelaunch() async throws {
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { _, _ in throw URLError(.notConnectedToInternet) }
        let relay = RelayClient(session: StubURLProtocol.session())
        let registration = PushRegistration(vault: vault, relay: relay, defaults: defaults,
                                            authorizationStatus: { .denied },
                                            registerRemoteNotifications: { XCTFail("Denied permission must not ask APNs") },
                                            unregisterRemoteNotifications: {})
        await registration.resumeIfOptedIn()
        XCTAssertEqual(StubURLProtocol.requests.map { $0.0.httpMethod }, ["DELETE"])
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertEqual(registration.failureReason, .permissionDenied)

        // A failed cleanup remains queued across launches without hiding the permission reason.
        let restored = PushRegistration(vault: vault, relay: relay,
                                        defaults: try XCTUnwrap(UserDefaults(suiteName: suiteName)),
                                        authorizationStatus: { XCTFail("Opt-out must survive a failed cleanup"); return .authorized },
                                        registerRemoteNotifications: { XCTFail("Relaunch must not restore opt-in") })
        await restored.resumeIfOptedIn()
        XCTAssertFalse(restored.isEnabled)
        XCTAssertFalse(restored.isOptedIn)
        XCTAssertEqual(restored.statusMessage, registration.statusMessage)
        XCTAssertEqual(restored.failureReason, .permissionDenied)
        XCTAssertEqual(StubURLProtocol.requests.map { $0.0.httpMethod }, ["DELETE", "DELETE"])
        XCTAssertTrue(restored.removalPending)
    }

    func testOfflineAlertOptOutSurvivesRelaunchAndRetriesRelayRemoval() async throws {
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { _, _ in throw URLError(.notConnectedToInternet) }
        let relay = RelayClient(session: StubURLProtocol.session())
        let registration = PushRegistration(vault: vault, relay: relay, defaults: defaults,
                                            unregisterRemoteNotifications: {})
        await registration.disable()
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertTrue(registration.removalPending)
        XCTAssertEqual(registration.statusMessage, String(localized: "notifications.alertsOffPending"))
        let restored = PushRegistration(vault: vault, relay: relay, defaults: defaults,
                                        unregisterRemoteNotifications: {})
        XCTAssertTrue(restored.removalPending)
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        await restored.resumeIfOptedIn()
        XCTAssertFalse(restored.removalPending)
        XCTAssertFalse(restored.isEnabled)
        XCTAssertNil(defaults.object(forKey: PushRegistration.pendingRemovalKey))
        XCTAssertNil(restored.statusMessage)
        XCTAssertEqual(StubURLProtocol.requests.map { $0.0.httpMethod }, ["DELETE"])
    }

    func testPendingOptOutWaitsForReadablePairingAndNeverDeletesReplacementToken() async throws {
        for replacePairing in [false, true] {
            let directory = try makeTemporaryDirectory()
            defer { try? FileManager.default.removeItem(at: directory) }
            let secrets = InMemorySecretStore()
            let writer = PairingVault(directory: directory, secrets: secrets)
            try storePairing(in: writer, deviceID: "original-phone", bearer: "original-bearer")
            let paired = try XCTUnwrap(writer.current())
            let pendingIdentity = "\(paired.hubID)/\(paired.deviceID)"
            defaults.set(false, forKey: PushRegistration.optInKey)
            defaults.set(pendingIdentity, forKey: PushRegistration.pendingRemovalKey)
            var readable = false
            let vault = PairingVault(directory: directory, secrets: secrets, readRecord: { url in
                guard readable else { throw CocoaError(.fileReadNoPermission) }
                return try Data(contentsOf: url)
            })
            StubURLProtocol.install { request, _ in
                let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                               httpVersion: "HTTP/1.1", headerFields: nil)!
                return (response, Data("{\"ok\":true}".utf8))
            }
            let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults, unregisterRemoteNotifications: {})
            await registration.resumeIfOptedIn()
            XCTAssertTrue(StubURLProtocol.requests.isEmpty)
            XCTAssertEqual(defaults.string(forKey: PushRegistration.pendingRemovalKey), pendingIdentity)
            if replacePairing {
                try storePairing(in: writer, deviceID: "replacement-phone", bearer: "replacement-bearer")
            }
            readable = true
            XCTAssertNotNil(vault.retryLoading())
            await registration.resumeIfOptedIn()
            XCTAssertNil(defaults.object(forKey: PushRegistration.pendingRemovalKey))
            XCTAssertFalse(registration.removalPending)
            XCTAssertFalse(registration.isOptedIn)
            if replacePairing {
                XCTAssertTrue(StubURLProtocol.requests.isEmpty, "an old opt-out must never delete the replacement token")
            } else {
                XCTAssertEqual(StubURLProtocol.requests.map { $0.0.httpMethod }, ["DELETE"])
                XCTAssertEqual(StubURLProtocol.requests.first?.0.value(forHTTPHeaderField: "Authorization"), "Bearer original-bearer")
            }
        }
    }

    func testOptOutRemainsQueuedWhilePairingCredentialsAreUnavailable() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let secrets = InMemorySecretStore()
        let vault = PairingVault(directory: directory, secrets: secrets)
        try storePairing(in: vault, deviceID: "phone", bearer: "test-bearer")
        let paired = try XCTUnwrap(vault.current())
        let bearerAccount = PairingSecretAccount.deviceBearer(secretID: paired.secretID)
        try secrets.delete(bearerAccount)
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults, unregisterRemoteNotifications: {})
        await registration.disable()
        XCTAssertTrue(registration.removalPending)
        XCTAssertNotNil(defaults.string(forKey: PushRegistration.pendingRemovalKey))
        XCTAssertTrue(StubURLProtocol.requests.isEmpty)
        XCTAssertTrue(registration.isReadyForRevocation, "an opted-out phone must not block unpairing")
        await registration.resumeIfOptedIn()
        XCTAssertTrue(registration.removalPending)
        XCTAssertTrue(StubURLProtocol.requests.isEmpty)
        try secrets.save("test-bearer", account: bearerAccount)
        await registration.resumeIfOptedIn()
        XCTAssertFalse(registration.removalPending)
        XCTAssertNil(defaults.object(forKey: PushRegistration.pendingRemovalKey))
        XCTAssertEqual(StubURLProtocol.requests.map { $0.0.httpMethod }, ["DELETE"])
    }

    func testExplicitOptOutSupersedesASuspendedLaunchPermissionCheck() async throws {
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        let permission = PushRetryGate()
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()), defaults: defaults,
                                            authorizationStatus: { await permission.sleep(0); return .denied },
                                            registerRemoteNotifications: { XCTFail("The newer opt-out wins") },
                                            unregisterRemoteNotifications: {})
        let resume = Task { await registration.resumeIfOptedIn() }
        await permission.waitUntilSleeping()
        await registration.disable()
        await permission.release()
        await resume.value
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertEqual(registration.state, .off)
        XCTAssertNil(registration.statusMessage)
        XCTAssertNil(defaults.object(forKey: PushRegistration.failureReasonKey))
    }

    func testChangedPairingRejectsASuspendedLaunchPermissionResult() async throws {
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        let permission = PushRetryGate()
        var checks = 0
        var registrations = 0
        let registration = PushRegistration(vault: vault, defaults: defaults,
                                            authorizationStatus: {
                                                checks += 1
                                                if checks == 1 { await permission.sleep(0); return .denied }
                                                return .authorized
                                            },
                                            registerRemoteNotifications: { registrations += 1 },
                                            unregisterRemoteNotifications: {})
        let resume = Task { await registration.resumeIfOptedIn() }
        await permission.waitUntilSleeping()
        try storePairing(in: vault, deviceID: "replacement-device", bearer: "replacement-bearer")
        await permission.release()
        await resume.value
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertEqual(registration.state, .registering)
        XCTAssertEqual(registrations, 0)
        XCTAssertNil(registration.failureReason)
        XCTAssertNil(defaults.object(forKey: PushRegistration.failureReasonKey))

        _ = await registration.pairingDidChange()
        XCTAssertEqual(checks, 2)
        XCTAssertEqual(registrations, 1, "The replacement pairing performs its own permission read")
        await registration.disable()
    }

    func testSavedFailureCodesHaveEnglishAndChineseCopyWithComputerAndPhoneLabels() {
        let english = Locale(identifier: "en-US")
        let chinese = Locale(identifier: "zh-Hans-CN")
        for reason in PushRegistration.FailureReason.allCases {
            let en = reason.message(locale: english)
            let zh = reason.message(locale: chinese)
            XCTAssertFalse(en.isEmpty)
            XCTAssertFalse(zh.isEmpty)
            XCTAssertNotEqual(en, zh)
            XCTAssertFalse(en.contains("Mac"))
            XCTAssertFalse(en.contains("iPhone"))
            XCTAssertFalse(en.contains("Yansu"))
        }
        XCTAssertTrue(PushRegistration.FailureReason.deviceLimit.message(locale: english).contains("computer"))
        XCTAssertTrue(PushRegistration.FailureReason.registration.message(locale: english).contains("phone"))
        XCTAssertEqual(PushRegistration.FailureReason.permissionDenied.message(locale: chinese),
                       "Isoform 的通知已在 iOS 设置中关闭。")
        XCTAssertEqual(PushRegistration.FailureReason.registration.message(locale: chinese),
                       "这部手机无法注册推送通知。测试通知仍可使用。")
    }

    func testLateAPNsFailureDoesNotChangeAnExplicitOptOut() async throws {
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults, unregisterRemoteNotifications: {})
        await registration.disable()
        registration.receiveRegistrationFailure()
        XCTAssertFalse(registration.isEnabled)
        XCTAssertEqual(registration.state, .off)
        XCTAssertNil(registration.statusMessage)
    }

    func testPairAgainUploadsTheExistingTokenWithTheNewCredentials() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-old", bearer: "old-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true,\"data\":{\"enabled\":true}}".utf8))
        }
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults)
        await registration.receiveToken(Data([0x01, 0xab, 0xff]))
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        await registration.pairingDidChange()

        let requests = StubURLProtocol.requests
        XCTAssertEqual(requests.count, 2)
        XCTAssertEqual(requests.map { $0.0.value(forHTTPHeaderField: "Authorization") },
                       ["Bearer old-bearer", "Bearer new-bearer"])
        XCTAssertTrue(requests.allSatisfy { $0.0.httpMethod == "PUT" && $0.0.url?.path == "/v1/push/token" })
        let firstBody = try JSONSerialization.jsonObject(with: XCTUnwrap(requests[0].1)) as? [String: String]
        let secondBody = try JSONSerialization.jsonObject(with: XCTUnwrap(requests[1].1)) as? [String: String]
        XCTAssertEqual(firstBody, secondBody)
        XCTAssertEqual(firstBody?["deviceToken"], "01abff")
        XCTAssertEqual(registration.state, .on)
        XCTAssertTrue(registration.isEnabled)
        XCTAssertEqual(registration.token, "01abff")
    }

    func testPairAgainWaitsForTheOldTokenUploadBeforeRegisteringTheNewDevice() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-old", bearer: "old-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        HeldPushURLProtocol.reset()
        defer { HeldPushURLProtocol.reset() }
        let oldStarted = expectation(description: "old token upload held")
        let newStartedEarly = expectation(description: "new token must wait for old upload")
        newStartedEarly.isInverted = true
        HeldPushURLProtocol.firstRequest = { oldStarted.fulfill() }
        HeldPushURLProtocol.secondRequest = { newStartedEarly.fulfill() }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [HeldPushURLProtocol.self]
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: URLSession(configuration: configuration)),
                                            defaults: defaults)
        let oldUpload = Task { await registration.receiveToken(Data([0x01, 0xab, 0xff])) }
        await fulfillment(of: [oldStarted], timeout: 1)
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        let newUpload = Task { await registration.pairingDidChange() }
        await fulfillment(of: [newStartedEarly], timeout: 0.05)
        HeldPushURLProtocol.secondRequest = nil
        HeldPushURLProtocol.releaseFirst()
        await oldUpload.value
        await newUpload.value
        XCTAssertEqual(HeldPushURLProtocol.requests.map { $0.value(forHTTPHeaderField: "Authorization") },
                       ["Bearer old-bearer", "Bearer new-bearer"])
        XCTAssertEqual(registration.state, .on)
    }

    func testPairAgainDoesNotUploadATokenWithoutOptIn() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        StubURLProtocol.install { _, _ in
            XCTFail("Pairing must not turn on notifications")
            throw URLError(.cancelled)
        }
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults)
        await registration.receiveToken(Data([0x01]))
        await registration.pairingDidChange()
        XCTAssertTrue(StubURLProtocol.requests.isEmpty)
        XCTAssertEqual(registration.state, .off)
    }

    func testPairAgainDefersRevocationUntilAFailedTokenUploadRetriesSuccessfully() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-old", bearer: "old-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { request, _ in
            if StubURLProtocol.requests.count == 2 { throw URLError(.notConnectedToInternet) }
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true,\"data\":{\"enabled\":true}}".utf8))
        }
        let retry = PushRetryGate()
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults, retrySleep: { await retry.sleep($0) })
        await registration.receiveToken(Data([0x01]))
        XCTAssertTrue(registration.isReadyForRevocation)
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        XCTAssertFalse(registration.isReadyForRevocation)
        let ready = expectation(description: "new token registered before old-device cleanup")
        registration.onRegistrationReady = { ready.fulfill() }

        let mayRevoke = await registration.pairingDidChange()
        XCTAssertFalse(mayRevoke)
        XCTAssertFalse(registration.isReadyForRevocation)
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertTrue(registration.isEnabled, "Relay retries stay visible and can be switched off")
        XCTAssertEqual(registration.state, .unavailable)
        await retry.waitUntilSleeping()
        let delays = await retry.delays
        XCTAssertEqual(delays, [1_000_000_000])
        await retry.release()
        await fulfillment(of: [ready], timeout: 2)

        XCTAssertTrue(registration.isReadyForRevocation)
        XCTAssertEqual(registration.state, .on)
        XCTAssertNil(registration.failure)
        XCTAssertEqual(StubURLProtocol.requests.map { $0.0.value(forHTTPHeaderField: "Authorization") },
                       ["Bearer old-bearer", "Bearer new-bearer", "Bearer new-bearer"])
    }

    /// A pairing over the computer's direct path: at launch the APNs token arrives before the tunnel opens, so the
    /// vault has no session yet. The upload is tried again once the tunnel is up, instead of never.
    func testATokenThatArrivesBeforeTheDirectTunnelOpensIsUploadedWhenItDoes() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let tunnel = TunnelOrigin()
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore(),
                                 remoteOrigin: { _ in tunnel.origin }, startRemote: { _ in })
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: "device-new", deviceName: "iPhone", scopes: ["push"],
                                  pairedAt: Date(), counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey), bearer: "new-bearer",
                        irohTicket: "endpointabcdefghijklmnopqrst")
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true,\"data\":{\"enabled\":true}}".utf8))
        }
        let retry = PushRetryGate()
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults, retrySleep: { await retry.sleep($0) })
        await registration.receiveToken(Data([0x01]))
        XCTAssertTrue(StubURLProtocol.requests.isEmpty, "no session yet, nothing sent")
        await retry.waitUntilSleeping()
        tunnel.open(URL(string: "http://127.0.0.1:41239")!)
        await retry.release()
        for _ in 0..<100 where registration.state != .on { try await Task.sleep(for: .milliseconds(20)) }
        XCTAssertEqual(registration.state, .on)
        XCTAssertEqual(StubURLProtocol.requests.count, 1)
        XCTAssertEqual(StubURLProtocol.requests.first?.0.url?.host, "127.0.0.1")
    }

    func testUnavailableAndServerTokenFailuresBackOffAndStopAfterSuccess() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { request, _ in
            let status: Int
            switch StubURLProtocol.requests.count {
            case 1: status = 503
            case 2: status = 500
            default: status = 200
            }
            let response = HTTPURLResponse(url: request.url!, statusCode: status,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            let json = status == 200 ? "{\"ok\":true,\"data\":{\"enabled\":true}}" : "{\"ok\":false}"
            return (response, Data(json.utf8))
        }
        let retry = PushRetryGate()
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults, retrySleep: { await retry.sleep($0) })
        let ready = expectation(description: "registration recovered")
        registration.onRegistrationReady = { ready.fulfill() }
        await registration.receiveToken(Data([0x01]))
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertFalse(registration.isReadyForRevocation)
        XCTAssertEqual(registration.state, .unavailable)
        await retry.waitUntilSleeping()
        await retry.release()
        await retry.waitUntilSleeping()
        let delays = await retry.delays
        XCTAssertEqual(delays, [1_000_000_000, 2_000_000_000])
        await retry.release()
        await fulfillment(of: [ready], timeout: 2)
        XCTAssertEqual(StubURLProtocol.requests.count, 3)
        XCTAssertEqual(registration.state, .on)
    }

    func testTurningPushOffDrainsAnOldUploadBeforeDeletingAndRejectsItsLateSuccess() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-old", bearer: "old-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        HeldPushURLProtocol.reset()
        defer { HeldPushURLProtocol.reset() }
        let started = expectation(description: "old upload started")
        let earlyDelete = expectation(description: "delete must wait for in-flight upload")
        earlyDelete.isInverted = true
        HeldPushURLProtocol.firstRequest = { started.fulfill() }
        HeldPushURLProtocol.secondRequest = { earlyDelete.fulfill() }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [HeldPushURLProtocol.self]
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: URLSession(configuration: configuration)),
                                            defaults: defaults)
        let upload = Task { await registration.receiveToken(Data([0x01])) }
        await fulfillment(of: [started], timeout: 2)
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        let replacementUpload = Task { await registration.pairingDidChange() }
        let disable = Task { await registration.disable() }
        await fulfillment(of: [earlyDelete], timeout: 0.05)
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertFalse(registration.isReadyForRevocation)
        XCTAssertEqual(registration.state, .off)
        HeldPushURLProtocol.secondRequest = nil
        HeldPushURLProtocol.releaseFirst()
        await upload.value
        _ = await replacementUpload.value
        await disable.value

        let requests = HeldPushURLProtocol.requests
        XCTAssertEqual(requests.map(\.httpMethod), ["PUT", "DELETE", "DELETE"])
        XCTAssertEqual(requests.map { $0.value(forHTTPHeaderField: "Authorization") },
                       ["Bearer old-bearer", "Bearer old-bearer", "Bearer new-bearer"])
        XCTAssertEqual(registration.state, .off)
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertNil(registration.token)
        XCTAssertNil(registration.failure)
        XCTAssertTrue(registration.isReadyForRevocation)
    }

    func testTurningPushOffDuringBackoffPreventsAnotherTokenUpload() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        let retried = expectation(description: "no retry after push is turned off")
        retried.isInverted = true
        StubURLProtocol.install { request, _ in
            if request.httpMethod == "PUT" {
                if StubURLProtocol.requests.count > 1 { retried.fulfill() }
                throw URLError(.notConnectedToInternet)
            }
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        let retry = PushRetryGate()
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults, retrySleep: { await retry.sleep($0) })
        await registration.receiveToken(Data([0x01]))
        await retry.waitUntilSleeping()
        await registration.disable()
        // The test sleeper deliberately returns even though the retry task was cancelled.
        await retry.release()
        await fulfillment(of: [retried], timeout: 0.05)
        XCTAssertEqual(StubURLProtocol.requests.map { $0.0.httpMethod }, ["PUT", "DELETE"])
        XCTAssertEqual(registration.state, .off)
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertNil(registration.failure)
        XCTAssertTrue(registration.isReadyForRevocation)
    }

    func testTerminalTokenFailuresDoNotAuthorizeOldDeviceRevocation() async throws {
        for status in [400, 409] {
            let directory = try makeTemporaryDirectory()
            defer { try? FileManager.default.removeItem(at: directory) }
            let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
            try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
            defaults.set(false, forKey: PushRegistration.optInKey)
            StubURLProtocol.install { request, _ in
                let code = request.httpMethod == "PUT" ? status : 200
                let response = HTTPURLResponse(url: request.url!, statusCode: code,
                                               httpVersion: "HTTP/1.1", headerFields: nil)!
                return (response, Data((code == 200 ? "{\"ok\":true}" : "{\"ok\":false}").utf8))
            }
            let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults)
            await registration.receiveToken(Data([0x01]))
            defaults.set(true, forKey: PushRegistration.optInKey)
            let ready = expectation(description: "HTTP \(status) must not authorize old-device cleanup")
            ready.isInverted = true
            registration.onRegistrationReady = { ready.fulfill() }

            let mayRevoke = await registration.pairingDidChange()

            XCTAssertFalse(mayRevoke)
            XCTAssertFalse(registration.isReadyForRevocation)
            XCTAssertTrue(registration.isOptedIn)
            XCTAssertEqual(registration.state, .unavailable)
            XCTAssertNotNil(registration.failure)
            await fulfillment(of: [ready], timeout: 0.05)
            XCTAssertEqual(StubURLProtocol.requests.count, 1)
            registration.onRegistrationReady = nil
            await registration.disable()
            XCTAssertTrue(registration.isReadyForRevocation, "Explicit opt-out permits cleanup")
        }
    }

    func testAPNsRegistrationFailureKeepsOptInAndLeavesCleanupToTheGracePeriod() async throws {
        StubURLProtocol.install { request, _ in
            let response = HTTPURLResponse(url: request.url!, statusCode: 200,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data("{\"ok\":true}".utf8))
        }
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer")
        defaults.set(true, forKey: PushRegistration.optInKey)
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                                defaults: defaults, unregisterRemoteNotifications: {})
        registration.receiveRegistrationFailure()

        // The opt-in stays, so the old device's cleanup waits for this phone's registration or the grace deadline,
        // as it does for any other registration still in progress.
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertFalse(registration.isEnabled)
        XCTAssertFalse(registration.isReadyForRevocation)
        XCTAssertEqual(registration.state, .unavailable)
    }

    func testAPNsFailureIgnoresTheLateSuccessOfAnUploadInFlight() async throws {
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        HeldPushURLProtocol.reset()
        defer { HeldPushURLProtocol.reset() }
        let started = expectation(description: "token upload held")
        HeldPushURLProtocol.firstRequest = { started.fulfill() }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [HeldPushURLProtocol.self]
        let registration = PushRegistration(vault: vault,
                                            relay: RelayClient(session: URLSession(configuration: configuration)),
                                            defaults: defaults, unregisterRemoteNotifications: {})
        let upload = Task { await registration.receiveToken(Data([0x01])) }
        await fulfillment(of: [started], timeout: 2)
        registration.receiveRegistrationFailure()

        XCTAssertTrue(registration.isOptedIn, "the switch stays on")
        HeldPushURLProtocol.releaseFirst()
        await upload.value
        XCTAssertEqual(HeldPushURLProtocol.requests.map(\.httpMethod), ["PUT"], "nothing deletes the token")
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertEqual(registration.state, .unavailable, "the upload sent before the failure does not turn it on")
        XCTAssertNotNil(registration.statusMessage)
    }

    func testEnableWaitsForDisableToDrainAndThenRegistersAgain() async throws {
        try await assertToggleAfterDrain(lastIntentOn: true)
    }

    func testAnotherOffRequestSupersedesAnEnableQueuedBehindTheDrain() async throws {
        try await assertToggleAfterDrain(lastIntentOn: false)
    }

    private func assertToggleAfterDrain(lastIntentOn: Bool) async throws {
        let vault = try makePairedVault()
        defaults.set(true, forKey: PushRegistration.optInKey)
        HeldPushURLProtocol.reset()
        defer { HeldPushURLProtocol.reset() }
        let uploadStarted = expectation(description: "token upload held")
        HeldPushURLProtocol.firstRequest = { uploadStarted.fulfill() }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [HeldPushURLProtocol.self]
        let offStarted = expectation(description: "disable started draining")
        var permissions = 0
        var registrations = 0
        var unregistrations = 0
        let registration = PushRegistration(
            vault: vault, relay: RelayClient(session: URLSession(configuration: configuration)), defaults: defaults,
            authorize: {
                permissions += 1
                XCTAssertEqual(HeldPushURLProtocol.requests.map(\.httpMethod), ["PUT", "DELETE"])
                return true
            },
            registerRemoteNotifications: { registrations += 1 },
            unregisterRemoteNotifications: {
                unregistrations += 1
                if unregistrations == 1 { offStarted.fulfill() }
            })
        let upload = Task { await registration.receiveToken(Data([0x01])) }
        await fulfillment(of: [uploadStarted], timeout: 2)
        let off = Task { await registration.disable() }
        await fulfillment(of: [offStarted], timeout: 2)
        let onStarted = expectation(description: "enable queued behind drain")
        let on = Task {
            onStarted.fulfill()
            await registration.enable()
        }
        await fulfillment(of: [onStarted], timeout: 2)
        var lastOff: Task<Void, Never>?
        if !lastIntentOn {
            let lastOffStarted = expectation(description: "latest off supersedes queued enable")
            lastOff = Task {
                lastOffStarted.fulfill()
                await registration.disable()
            }
            await fulfillment(of: [lastOffStarted], timeout: 2)
        }
        XCTAssertEqual(permissions, 0)
        XCTAssertEqual(registrations, 0)
        HeldPushURLProtocol.releaseFirst()
        await upload.value
        await off.value
        await on.value
        await lastOff?.value

        XCTAssertEqual(registration.isOptedIn, lastIntentOn)
        XCTAssertEqual(registration.state, lastIntentOn ? .registering : .off)
        XCTAssertEqual(permissions, lastIntentOn ? 1 : 0)
        XCTAssertEqual(registrations, lastIntentOn ? 1 : 0)
        XCTAssertEqual(HeldPushURLProtocol.requests.map(\.httpMethod), ["PUT", "DELETE"])
    }

    func testOffRequestWinsOverALatePermissionResult() async throws {
        let vault = try makePairedVault()
        let permission = PushRetryGate()
        var registrations = 0
        let registration = PushRegistration(vault: vault, defaults: defaults,
                                            authorize: { await permission.sleep(0); return true },
                                            registerRemoteNotifications: { registrations += 1 },
                                            unregisterRemoteNotifications: {})
        let on = Task { await registration.enable() }
        await permission.waitUntilSleeping()
        await registration.disable()
        await permission.release()
        await on.value
        XCTAssertFalse(registration.isOptedIn)
        XCTAssertEqual(registration.state, .off)
        XCTAssertEqual(registrations, 0)
    }

    func testNoTokenGraceKeepsTheCommittedDeadlineAcrossRelaunch() async throws {
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let secrets = InMemorySecretStore()
        let vault = PairingVault(directory: directory, secrets: secrets)
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer", pairedAt: clock)
        defaults.set(true, forKey: PushRegistration.optInKey)
        let firstTimer = PushRetryGate()
        var first: PushRegistration? = PushRegistration(vault: vault, defaults: defaults,
                                                        revocationSleep: { await firstTimer.sleep($0) }, now: { clock },
                                                        authorizationStatus: { .authorized },
                                                        registerRemoteNotifications: {}, unregisterRemoteNotifications: {})
        await first?.resumeIfOptedIn()
        await firstTimer.waitUntilSleeping()
        let firstDelays = await firstTimer.delays
        XCTAssertEqual(firstDelays, [60_000_000_000])
        first = nil

        clock = clock.addingTimeInterval(20)
        let restoredVault = PairingVault(directory: directory, secrets: secrets)
        let restoredTimer = PushRetryGate()
        let restored = PushRegistration(vault: restoredVault, defaults: defaults,
                                        revocationSleep: { await restoredTimer.sleep($0) }, now: { clock },
                                        authorizationStatus: { .authorized },
                                        registerRemoteNotifications: {}, unregisterRemoteNotifications: {})
        let ready = expectation(description: "cleanup resumes at the original deadline without an APNs token")
        restored.onRegistrationReady = { ready.fulfill() }
        await restored.resumeIfOptedIn()
        await restoredTimer.waitUntilSleeping()
        let remainingDelays = await restoredTimer.delays
        XCTAssertEqual(remainingDelays, [40_000_000_000])
        XCTAssertFalse(restored.isReadyForRevocation)
        clock = clock.addingTimeInterval(40)
        await restoredTimer.release()
        await fulfillment(of: [ready], timeout: 2)
        XCTAssertTrue(restored.isReadyForRevocation)
        XCTAssertTrue(restored.isOptedIn)
        XCTAssertNil(restored.token)
        await firstTimer.release()
    }

    func testTokenCapFailureRevokesAfterGraceThenRetriesRegistrationOnce() async throws {
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        let vault = try makePairedVault(pairedAt: clock)
        defaults.set(false, forKey: PushRegistration.optInKey)
        StubURLProtocol.install { request, _ in
            let status = StubURLProtocol.requests.count == 1 ? 409 : 200
            let response = HTTPURLResponse(url: request.url!, statusCode: status,
                                           httpVersion: "HTTP/1.1", headerFields: nil)!
            return (response, Data((status == 200 ? "{\"ok\":true,\"data\":{\"enabled\":true}}"
                                                   : "{\"ok\":false}").utf8))
        }
        let timer = PushRetryGate()
        let registration = PushRegistration(vault: vault, relay: RelayClient(session: StubURLProtocol.session()),
                                            defaults: defaults, revocationSleep: { await timer.sleep($0) }, now: { clock },
                                            authorizationStatus: { .authorized },
                                            registerRemoteNotifications: {}, unregisterRemoteNotifications: {})
        await registration.receiveToken(Data([0x01]))
        defaults.set(true, forKey: PushRegistration.optInKey)
        let cleanedUp = expectation(description: "old device cleanup frees the token cap")
        let registered = expectation(description: "token registered after cleanup")
        var callbacks = 0
        registration.onRegistrationReady = {
            callbacks += 1
            if callbacks == 1 {
                XCTAssertEqual(StubURLProtocol.requests.count, 1)
                XCTAssertEqual(registration.state, .unavailable)
                cleanedUp.fulfill()
            } else {
                XCTAssertEqual(registration.state, .on)
                registered.fulfill()
            }
        }
        let readyBeforeDeadline = await registration.pairingDidChange()
        XCTAssertFalse(readyBeforeDeadline)
        await timer.waitUntilSleeping()
        clock = clock.addingTimeInterval(60)
        await timer.release()
        await fulfillment(of: [cleanedUp, registered], timeout: 2, enforceOrder: true)
        XCTAssertEqual(StubURLProtocol.requests.count, 2)
        XCTAssertEqual(callbacks, 2)
        XCTAssertTrue(registration.isOptedIn)
        XCTAssertEqual(registration.state, .on)
        registration.onRegistrationReady = nil
    }

    func testASupersededGraceTimerCannotReleaseTheNextPairingEarly() async throws {
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        let vault = try makePairedVault(pairedAt: clock)
        defaults.set(true, forKey: PushRegistration.optInKey)
        let timer = PushRetryGate()
        let registration = PushRegistration(vault: vault, defaults: defaults,
                                            revocationSleep: { await timer.sleep($0) }, now: { clock },
                                            authorizationStatus: { .authorized },
                                            registerRemoteNotifications: {}, unregisterRemoteNotifications: {})
        _ = await registration.pairingDidChange()
        await timer.waitUntilSleeping(count: 1)
        clock = clock.addingTimeInterval(10)
        try storePairing(in: vault, deviceID: "device-replaced", bearer: "replacement-bearer", pairedAt: clock)
        _ = await registration.pairingDidChange()
        await timer.waitUntilSleeping(count: 2)
        let early = expectation(description: "old timer cannot release the replacement pairing")
        early.isInverted = true
        registration.onRegistrationReady = { early.fulfill() }
        clock = clock.addingTimeInterval(50)
        await timer.release()
        await fulfillment(of: [early], timeout: 0.05)
        XCTAssertFalse(registration.isReadyForRevocation)
        let ready = expectation(description: "replacement reaches its own deadline")
        registration.onRegistrationReady = { ready.fulfill() }
        clock = clock.addingTimeInterval(10)
        await timer.release()
        await fulfillment(of: [ready], timeout: 2)
        XCTAssertTrue(registration.isReadyForRevocation)
    }

    func testUnreadablePairingCannotAuthorizeOldDeviceRevocation() throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        try Data("unreadable".utf8).write(to: directory.appendingPathComponent("pairing.json"))
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        let registration = PushRegistration(vault: vault, defaults: defaults)
        XCTAssertFalse(registration.isReadyForRevocation)
        XCTAssertNotNil(vault.loadError)
    }

    private func makePairedVault(pairedAt: Date = Date()) throws -> PairingVault {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        try storePairing(in: vault, deviceID: "device-new", bearer: "new-bearer", pairedAt: pairedAt)
        return vault
    }

    private func storePairing(in vault: PairingVault, deviceID: String, bearer: String, pairedAt: Date = Date()) throws {
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: deviceID, deviceName: "iPhone", scopes: ["push"],
                                  pairedAt: pairedAt, counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey), bearer: bearer)
    }

    private func makeRegistration() throws -> PushRegistration {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore())
        return PushRegistration(vault: vault, defaults: defaults)
    }
}
