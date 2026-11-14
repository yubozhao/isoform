import Foundation
import XCTest
@testable import Yansu

// Holds the relay response across cancellation, as a request already in flight may still complete.
private actor HeldPairingTransport: PairingTransport {
    private let hub: TestHub
    private var claim: PairingClaimBody?
    private var response: CheckedContinuation<PairingPoll, Error>?
    private var polling: CheckedContinuation<Void, Never>?
    private(set) var unpaired: [RelayCredentials] = []
    private(set) var claims: [PairingClaimBody] = []
    private let loseClaimResponses: Bool
    private var claimResponse: CheckedContinuation<Void, Error>?
    private var claiming: CheckedContinuation<Void, Never>?

    init(hub: TestHub, loseClaimResponses: Bool = false) {
        self.hub = hub
        self.loseClaimResponses = loseClaimResponses
    }

    func claimPairing(origin: URL, pairingID: String, body: PairingClaimBody) async throws {
        claim = body
        claims.append(body)
        if loseClaimResponses {
            if claims.count == 1 {
                try await withCheckedThrowingContinuation { continuation in
                    claimResponse = continuation
                    claiming?.resume()
                    claiming = nil
                }
            }
            throw RelayClientError.timedOut
        }
    }

    func waitUntilClaiming() async {
        guard claimResponse == nil else { return }
        await withCheckedContinuation { claiming = $0 }
    }

    func loseClaimResponse() {
        claimResponse?.resume(throwing: RelayClientError.timedOut)
        claimResponse = nil
    }

    func pollPairing(origin: URL, pairingID: String, claimToken: String) async throws -> PairingPoll {
        try await withCheckedThrowingContinuation { continuation in
            response = continuation
            polling?.resume()
            polling = nil
        }
    }

    func waitUntilPolling() async {
        guard response == nil else { return }
        await withCheckedContinuation { polling = $0 }
    }

    func approve() throws {
        let claim = try XCTUnwrap(claim)
        let result = try hub.approval(for: claim)
        let response = try XCTUnwrap(response)
        self.response = nil
        response.resume(returning: result)
    }

    func respond(_ state: String) throws {
        let response = try XCTUnwrap(response)
        self.response = nil
        response.resume(returning: PairingPoll(state: state, approval: nil))
    }

    func claimedBearerHash() throws -> String {
        try hub.openClaim(XCTUnwrap(claim)).bearerHash
    }

    func unpair(_ credentials: RelayCredentials) async throws {
        unpaired.append(credentials)
    }
}

private final class FailOncePairingSecrets: SecretStore, @unchecked Sendable {
    private let base: InMemorySecretStore
    private let lock = NSLock()
    private var failBearer = false

    init(base: InMemorySecretStore) { self.base = base }

    func failNextBearerSave() {
        lock.lock()
        defer { lock.unlock() }
        failBearer = true
    }

    func accountNames() throws -> [String] { try base.accountNames() }
    func read(_ account: String) throws -> String? { try base.read(account) }
    func delete(_ account: String) throws { try base.delete(account) }

    func save(_ value: String, account: String) throws {
        lock.lock()
        defer { lock.unlock() }
        if failBearer && PairingSecretAccount.isDeviceBearer(account) {
            failBearer = false
            throw YansuError.message("Keychain unavailable in test")
        }
        try base.save(value, account: account)
    }
}

private final class PairingRemoteOriginRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var origins: [String: URL]
    private var requestedTickets: [String] = []

    init(origins: [String: URL]) {
        self.origins = origins
    }

    func origin(for ticket: String) -> URL? {
        lock.lock()
        defer { lock.unlock() }
        requestedTickets.append(ticket)
        return origins[ticket]
    }

    func setOrigin(_ origin: URL?, for ticket: String) {
        lock.lock()
        defer { lock.unlock() }
        origins[ticket] = origin
    }

    func calls() -> [String] {
        lock.lock()
        defer { lock.unlock() }
        return requestedTickets
    }

    func clearCalls() {
        lock.lock()
        defer { lock.unlock() }
        requestedTickets = []
    }
}

private final class PairingRemoteActivationRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var tickets: [String?] = []

    func record(_ ticket: String?) {
        lock.lock()
        defer { lock.unlock() }
        tickets.append(ticket)
    }

    func calls() -> [String?] {
        lock.lock()
        defer { lock.unlock() }
        return tickets
    }

    func clearCalls() {
        lock.lock()
        defer { lock.unlock() }
        tickets = []
    }
}

@MainActor
final class PairingStoreTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    override func setUpWithError() throws {
        try super.setUpWithError()
        PairingStore.retryDelayNanoseconds = 1_000_000
    }

    private var exp: Int64 {
        Int64(now.timeIntervalSince1970) + 600
    }

    // MARK: Invitation parsing

    func testValidInvitationParsesEveryField() throws {
        let hub = TestHub()
        let invitation = try PairingInvitation.parse(hub.invitationText(exp: exp), now: now)
        XCTAssertEqual(invitation.hubID, hub.hubID)
        XCTAssertEqual(invitation.pairingID, hub.pairingID)
        XCTAssertEqual(invitation.relayOrigin.absoluteString, "https://relay.example")
        XCTAssertEqual(invitation.secret, hub.secret)
        XCTAssertEqual(invitation.hubPublicKey, hub.hubPublicKey)
        XCTAssertEqual(invitation.expiresAt.timeIntervalSince1970, Double(exp))
    }

    func testLegacyYansuSchemeStillParses() throws {
        let hub = TestHub()
        let current = hub.invitationText(exp: exp)
        let legacy = current.replacingOccurrences(of: "isoform://pair#", with: "yansu://pair#")
        XCTAssertNoThrow(try PairingInvitation.parse(legacy, now: now))
    }

    func testSurroundingWhitespaceIsIgnored() throws {
        let hub = TestHub()
        XCTAssertNoThrow(try PairingInvitation.parse("  \(hub.invitationText(exp: exp))\n", now: now))
    }

    func testExpiredInvitationIsRejected() {
        let hub = TestHub()
        XCTAssertThrowsError(try PairingInvitation.parse(hub.invitationText(exp: Int64(now.timeIntervalSince1970) - 1),
                                                         now: now)) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .expired)
        }
    }

    func testUnsupportedVersionIsRejected() {
        let hub = TestHub()
        XCTAssertThrowsError(try PairingInvitation.parse(invitation(["v": 2], hub: hub), now: now)) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .unsupportedVersion(2))
        }
    }

    func testPlainHTTPRelayIsRejected() {
        let hub = TestHub()
        XCTAssertThrowsError(try PairingInvitation.parse(invitation(["relay": "http://relay.example"], hub: hub), now: now)) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .insecureRelay)
        }
    }

    func testRelayWithAPathQueryOrCredentialsIsRejected() {
        let hub = TestHub()
        for relay in ["https://relay.example/mobile", "https://relay.example?x=1", "https://user:pass@relay.example"] {
            XCTAssertThrowsError(try PairingInvitation.parse(invitation(["relay": relay], hub: hub), now: now),
                                 relay) { error in
                XCTAssertEqual(error as? PairingInvitation.ParseError, .malformed, relay)
            }
        }
    }

    func testRelayWithTrailingSlashIsAcceptedAsOrigin() throws {
        let hub = TestHub()
        let invitation = try PairingInvitation.parse(invitation(["relay": "https://relay.example/"], hub: hub), now: now)
        XCTAssertEqual(invitation.relayOrigin.absoluteString, "https://relay.example")
    }

    func testLoopbackHTTPRelayIsAcceptedOnlyInDebugBuilds() {
        let hub = TestHub()
        let text = invitation(["relay": "http://127.0.0.1:8787"], hub: hub)
        #if DEBUG
        XCTAssertNoThrow(try PairingInvitation.parse(text, now: now))
        #else
        XCTAssertThrowsError(try PairingInvitation.parse(text, now: now))
        #endif
    }

    func testIrohTicketsAreValidatedAndPermitLoopbackInReleaseMode() throws {
        let hub = TestHub()
        let ticket = "endpointabcdefghijklmnopqrst"
        XCTAssertTrue(PairingInvitation.isValidIrohTicket(ticket))
        let parsed = try PairingInvitation.parse(
            invitation(["relay": "http://127.0.0.1:8787", "iroh": ticket], hub: hub),
            now: now, allowsDevelopmentLoopback: false)
        XCTAssertEqual(parsed.irohTicket, ticket)
        XCTAssertEqual(parsed.relayOrigin.absoluteString, "http://127.0.0.1:8787")

        for invalid in ["", "endpointABCDEFGHIJKLMNOPQRST", "endpointabcdefghijklmnopqrs8",
                        "endpoint\(String(repeating: "a", count: 2041))"] {
            XCTAssertFalse(PairingInvitation.isValidIrohTicket(invalid), invalid)
            XCTAssertThrowsError(
                try PairingInvitation.parse(invitation(["iroh": invalid], hub: hub), now: now),
                invalid
            ) { error in
                XCTAssertEqual(error as? PairingInvitation.ParseError, .malformed)
            }
        }
    }

    func testTicketOnlyInvitationIsAccepted() throws {
        let hub = TestHub()
        let ticket = "endpointabcdefghijklmnopqrst"
        let parsed = try PairingInvitation.parse(invitation(["iroh": ticket], hub: hub, removing: ["relay"]), now: now)
        XCTAssertEqual(parsed.irohTicket, ticket)
        XCTAssertEqual(parsed.relayOrigin.absoluteString, "https://iroh.invalid")
        XCTAssertThrowsError(try PairingInvitation.parse(invitation([:], hub: hub, removing: ["relay"]), now: now))
    }

    func testLoopbackHTTPWithoutTicketIsRefusedWhenDevelopmentIsDisabled() {
        let hub = TestHub()
        XCTAssertThrowsError(
            try PairingInvitation.parse(invitation(["relay": "http://127.0.0.1:8787"], hub: hub),
                                        now: now, allowsDevelopmentLoopback: false)
        ) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .insecureRelay)
        }
    }

    func testHTTPSRelayRemainsValidWhenDevelopmentIsDisabled() throws {
        let hub = TestHub()
        let parsed = try PairingInvitation.parse(hub.invitationText(exp: exp), now: now,
                                                 allowsDevelopmentLoopback: false)
        XCTAssertEqual(parsed.relayOrigin.absoluteString, "https://relay.example")
        XCTAssertNil(parsed.irohTicket)
    }

    func testNonCanonicalIdentifiersAreRejected() {
        let hub = TestHub()
        XCTAssertThrowsError(try PairingInvitation.parse(invitation(["hub": hub.hubID.uppercased()], hub: hub), now: now)) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .malformed)
        }
        XCTAssertThrowsError(try PairingInvitation.parse(invitation(["pairing": "not-a-uuid"], hub: hub), now: now))
    }

    func testKeysMustBeThirtyTwoBytes() {
        let hub = TestHub()
        let short = RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(31))
        XCTAssertThrowsError(try PairingInvitation.parse(invitation(["secret": short], hub: hub), now: now))
        XCTAssertThrowsError(try PairingInvitation.parse(invitation(["hubPub": short], hub: hub), now: now))
    }

    func testMissingFieldIsMalformed() {
        let hub = TestHub()
        XCTAssertThrowsError(try PairingInvitation.parse(invitation([:], hub: hub, removing: ["exp"]), now: now)) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .malformed)
        }
    }

    func testTextThatIsNotAnInvitationIsRefused() {
        XCTAssertThrowsError(try PairingInvitation.parse("https://example.com/pair", now: now)) { error in
            XCTAssertEqual(error as? PairingInvitation.ParseError, .notAnInvitation)
        }
    }

    // MARK: State transitions

    func testApprovalStoresThePairingAndReachesPaired() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertEqual(fixture.store.phase, .paired)
        XCTAssertTrue(fixture.store.isPaired)
        let stored = try XCTUnwrap(fixture.vault.current())
        XCTAssertEqual(stored.hubID, fixture.hub.hubID)
        XCTAssertEqual(stored.hubName, "Test Mac")
        XCTAssertEqual(stored.relayOrigin, "https://relay.example")
        XCTAssertEqual(fixture.transport.claims.count, 1)
    }

    func testIrohPairingClaimsAndPollsUseMappedOriginAndPersistTicket() async throws {
        let ticket = "endpointabcdefghijklmnopqrst"
        let mappedOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:41237"))
        let fixture = try makeFixture(remoteOrigin: { $0 == ticket ? mappedOrigin : nil },
                                      activateRemote: { _ in })
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let text = invitation(["relay": "http://127.0.0.1:8787", "iroh": ticket], hub: fixture.hub)
        let task = try XCTUnwrap(fixture.store.begin(invitation: text))
        await task.value

        XCTAssertEqual(fixture.transport.claimOrigins, [mappedOrigin])
        XCTAssertEqual(fixture.transport.pollOrigins, [mappedOrigin])
        XCTAssertEqual(try fixture.vault.irohTicket(), ticket)
        XCTAssertTrue(fixture.store.isRemote)
    }

    func testIrohTicketStorageReplacementRemovalAndWipe() throws {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try FileManager.default.removeItem(at: directory) }
        let secrets = InMemorySecretStore()
        let vault = PairingVault(directory: directory, secrets: secrets, remoteOrigin: { _ in nil },
                                 startRemote: { _ in })
        let keyPair = RelayCrypto.makeDeviceKeyPair()
        var record = RelayCryptoTests.sampleRecord()
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "first-bearer", irohTicket: "endpointabcdefghijklmnopqrst")
        XCTAssertEqual(try vault.irohTicket(), "endpointabcdefghijklmnopqrst")
        XCTAssertTrue(vault.isRemote)
        XCTAssertTrue(try XCTUnwrap(SharedNoticeStore.load(secrets: secrets)).context.remote)
        XCTAssertNil(SharedNoticeStore.relayCredentials(secrets: secrets))

        record.deviceID = UUID().uuidString.lowercased()
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "second-bearer", irohTicket: "endpointbcdefghijklmnopqrstu")
        XCTAssertEqual(try vault.irohTicket(), "endpointbcdefghijklmnopqrstu")
        XCTAssertEqual(try vault.pendingDeviceRevocations().first?.irohTicket,
                       "endpointabcdefghijklmnopqrst")

        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "third-bearer")
        XCTAssertNil(try vault.irohTicket())
        XCTAssertFalse(vault.isRemote)

        let previousHubID = record.hubID
        record.hubID = UUID().uuidString.lowercased()
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "fourth-bearer", irohTicket: "endpointabcdefghijklmnopqrst")
        XCTAssertNil(try secrets.read(PairingSecretAccount.irohTicket(hubID: previousHubID)))
        XCTAssertEqual(try vault.irohTicket(), "endpointabcdefghijklmnopqrst")

        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "fifth-bearer", irohTicket: "endpointabcdefghijklmnopqrst")
        try vault.wipe()
        XCTAssertNil(try secrets.read(PairingSecretAccount.irohTicket(hubID: record.hubID)))
    }

    func testRemoteSessionOriginMapsOrReportsOffline() throws {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try FileManager.default.removeItem(at: directory) }
        let secrets = InMemorySecretStore()
        let keyPair = RelayCrypto.makeDeviceKeyPair()
        let record = RelayCryptoTests.sampleRecord()
        let mappedOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:41237"))
        let vault = PairingVault(directory: directory, secrets: secrets,
                                 remoteOrigin: { _ in mappedOrigin }, startRemote: { _ in })
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "remote-bearer", irohTicket: "endpointabcdefghijklmnopqrst")

        XCTAssertEqual(try vault.session().credentials.origin, mappedOrigin)

        let offlineVault = PairingVault(directory: directory, secrets: secrets,
                                        remoteOrigin: { _ in nil }, startRemote: { _ in })
        XCTAssertThrowsError(try offlineVault.session()) { error in
            XCTAssertEqual(error as? RelayClientError, .offline)
        }
    }

    func testClaimCarriesTheDeviceFactsTheHubChecks() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        let claim = try XCTUnwrap(fixture.transport.claims.first)
        let payload = try fixture.hub.openClaim(claim)
        XCTAssertEqual(payload.deviceId, claim.deviceId)
        XCTAssertEqual(payload.devicePub, claim.devicePub)
        XCTAssertEqual(payload.platform, "ios")
        XCTAssertEqual(payload.deviceName, PairingStore.deviceName())
        // Inspect the real outbound encrypted claim, not a test-supplied device marker.
        #if targetEnvironment(simulator)
        XCTAssertEqual(payload.simulator, true)
        #else
        XCTAssertNil(payload.simulator)
        #endif
        XCTAssertEqual(payload.scopes, ["brief:read", "brief:refresh", "presence:read", "push",
                                        "chat:read", "chat:write", "memory:correct", "handoff:create",
                                        "brief:feedback", "offers:read", "offers:decide", "goals:read",
                                        "goals:write", "library:read"])
        XCTAssertTrue(["development", "production"].contains(payload.pushEnvironment))
        let bearer = try fixture.vault.session().credentials.bearer
        XCTAssertEqual(payload.bearerHash, PairingStore.bearerHash(bearer))
        XCTAssertEqual(payload.bearerHash.count, 64)
    }

    func testDeclineLeavesNothingPaired() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([{ _ in PairingPoll(state: "declined", approval: nil) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertEqual(fixture.store.phase, .declined)
        XCTAssertNil(fixture.vault.current())
        XCTAssertFalse(fixture.store.isPaired)
    }

    func testExpiryReportedByTheRelayIsSurfaced() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([
            { _ in PairingPoll(state: "claimed", approval: nil) },
            { _ in PairingPoll(state: "expired", approval: nil) },
        ])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertEqual(fixture.store.phase, .expired)
        XCTAssertNil(fixture.vault.current())
    }

    func testApprovalNamingAnotherDeviceIsRefused() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([{ claim in
            try fixture.hub.approval(for: claim, deviceIDOverride: "00000000-0000-4000-8000-000000000000")
        }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertTrue(isFailed(fixture.store.phase))
        XCTAssertNil(fixture.vault.current())
    }

    func testApprovalWithTheWrongHubKeyIsRefused() async throws {
        let fixture = try makeFixture()
        let otherKey = RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim, hubPubOverride: otherKey) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertTrue(isFailed(fixture.store.phase))
        XCTAssertNil(fixture.vault.current())
    }

    func testRejectedClaimReportsAFailureAndStoresNothing() async throws {
        let fixture = try makeFixture()
        fixture.transport.failClaim(with: RelayClientError.unauthorized)
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertTrue(isFailed(fixture.store.phase))
        XCTAssertTrue(fixture.transport.claims.isEmpty)
        XCTAssertNil(fixture.vault.current())
    }

    func testOfflineClaimReportsAFailure() async throws {
        let fixture = try makeFixture()
        fixture.transport.failClaim(with: RelayClientError.offline)
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        guard case .failed(let message) = fixture.store.phase else {
            return XCTFail("expected a failure, got \(fixture.store.phase)")
        }
        XCTAssertTrue(message.contains("reach your computer"))
    }

    func testExpiredInvitationNeverReachesTheRelay() throws {
        let fixture = try makeFixture()
        let task = fixture.store.begin(invitation: fixture.hub.invitationText(exp: Int64(now.timeIntervalSince1970) - 5))
        XCTAssertNil(task)
        XCTAssertEqual(fixture.store.phase, .expired)
        XCTAssertTrue(fixture.transport.claims.isEmpty)
    }

    // MARK: Pair again

    func testAmbiguousCommitDoesNotRevokeThePossiblyStoredDevice() async throws {
        let directory = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        var readsBlocked = false
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore(), readRecord: { url in
            if readsBlocked { throw CocoaError(.fileReadNoPermission) }
            return try Data(contentsOf: url)
        }, writeRecord: { data, url in
            try data.write(to: url, options: .atomic)
            readsBlocked = true
            throw CocoaError(.fileWriteUnknown)
        })
        let hub = TestHub()
        let transport = StubPairingTransport()
        transport.script([{ claim in try hub.approval(for: claim) }])
        let store = PairingStore(vault: vault, transport: transport, now: { self.now })
        let claim = try XCTUnwrap(store.begin(invitation: hub.invitationText(exp: exp)))
        await claim.value

        XCTAssertNotNil(store.recoveryMessage)
        XCTAssertEqual(vault.loadError, .unreadableRecord)
        XCTAssertEqual(transport.unpairCount, 0, "The committed candidate must not be revoked while its record is unreadable")
        await store.retryPendingRevocations()
        XCTAssertEqual(transport.unpairCount, 0)

        readsBlocked = false
        store.retrySavedPairing()
        XCTAssertEqual(store.phase, .paired)
        XCTAssertEqual(store.paired?.deviceID, transport.claims.first?.deviceId)
        XCTAssertTrue(try vault.pendingDeviceRevocations().isEmpty, "The recovered current device is excluded from cleanup")
    }

    func testPreparingToPairAgainKeepsTheExistingPairing() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let session = try fixture.vault.session()

        fixture.store.prepareToPair()

        XCTAssertEqual(fixture.store.phase, .idle)
        try assertPairing(original, session: session, fixture: fixture)
        fixture.store.cancel()
        XCTAssertEqual(fixture.store.phase, .paired)
        try assertPairing(original, session: session, fixture: fixture)
    }

    func testPairAgainReplacesTheVaultOnlyAfterApproval() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let session = try fixture.vault.session()
        let replacement = TestHub()
        let transport = HeldPairingTransport(hub: replacement)
        var lifecycle: [String] = []
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now },
                                 prepareForPairingChange: {
                                     XCTAssertEqual(fixture.vault.current(), original)
                                     lifecycle.append("stop old poll")
                                 }, finishPairingChange: {
                                     XCTAssertNotEqual(fixture.vault.current()?.deviceID, original.deviceID)
                                     lifecycle.append("resume")
                                 }, didStorePairing: {
                                     XCTAssertEqual(fixture.vault.current()?.hubID, replacement.hubID)
                                     let revoked = await transport.unpaired
                                     XCTAssertTrue(revoked.isEmpty)
                                     lifecycle.append("register token")
                                     return true
                                 })

        store.prepareToPair()
        let task = try XCTUnwrap(store.begin(invitation: replacement.invitationText(exp: exp)))
        await transport.waitUntilPolling()
        XCTAssertTrue(store.isPaired)
        XCTAssertEqual(store.paired, original)
        try assertPairing(original, session: session, fixture: fixture)
        let beforeApproval = await transport.unpaired
        XCTAssertTrue(beforeApproval.isEmpty)
        XCTAssertTrue(lifecycle.isEmpty)

        try await transport.approve()
        await task.value

        XCTAssertEqual(store.phase, .paired)
        let updated = try XCTUnwrap(fixture.vault.current())
        XCTAssertEqual(store.paired, updated)
        XCTAssertEqual(updated.hubID, replacement.hubID)
        XCTAssertNotEqual(updated.deviceID, original.deviceID)
        let newSession = try fixture.vault.session()
        XCTAssertEqual(newSession.deviceID, updated.deviceID)
        XCTAssertNotEqual(newSession.credentials.bearer, session.credentials.bearer)
        XCTAssertNotEqual(newSession.keys, session.keys)
        XCTAssertEqual(SharedNoticeStore.load(secrets: fixture.secrets)?.context.deviceID, updated.deviceID)
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked, [session.credentials])
        XCTAssertEqual(lifecycle, ["stop old poll", "resume", "register token"])
    }

    func testFailedPairAgainKeepsTheExistingPairingWorking() async throws {
        for outcome in ["declined", "expired", "rejected", "lost"] {
            let fixture = try makeFixture()
            try await pair(fixture)
            let original = try XCTUnwrap(fixture.vault.current())
            let session = try fixture.vault.session()
            let notices = SharedNoticeStore.load(secrets: fixture.secrets)
            fixture.transport.script([{ claim in
                switch outcome {
                case "rejected":
                    return try fixture.hub.approval(for: claim, deviceIDOverride: "another-device")
                case "lost":
                    throw RelayClientError.notFound
                default:
                    return PairingPoll(state: outcome, approval: nil)
                }
            }])

            let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
            await task.value

            XCTAssertNotEqual(fixture.store.phase, .paired, outcome)
            try assertPairing(original, session: session, fixture: fixture)
            XCTAssertEqual(SharedNoticeStore.load(secrets: fixture.secrets), notices)
            fixture.store.cancel()
            XCTAssertEqual(fixture.store.phase, .paired)
        }
    }

    func testInvalidPairAgainInvitationKeepsTheExistingPairing() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let session = try fixture.vault.session()

        XCTAssertNil(fixture.store.begin(invitation: "not a pairing link"))

        XCTAssertTrue(isFailed(fixture.store.phase))
        try assertPairing(original, session: session, fixture: fixture)
        XCTAssertEqual(fixture.transport.claims.count, 1)
    }

    func testCancelledPairAgainRevokesALateApproval() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let session = try fixture.vault.session()
        let replacement = TestHub()
        let transport = HeldPairingTransport(hub: replacement)
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now })
        let task = try XCTUnwrap(store.begin(invitation: replacement.invitationText(exp: exp)))
        await transport.waitUntilPolling()

        store.cancel()
        // The first poll can finish before the Mac approves. Cleanup must keep polling.
        try await transport.respond("claimed")
        await transport.waitUntilPolling()
        try await transport.approve()
        await task.value
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked.count, 1)
        XCTAssertNotEqual(revoked.first, session.credentials)
        let claimHash = try await transport.claimedBearerHash()
        XCTAssertEqual(PairingStore.bearerHash(try XCTUnwrap(revoked.first).bearer), claimHash)

        XCTAssertEqual(store.phase, .paired)
        XCTAssertEqual(store.paired, original)
        try assertPairing(original, session: session, fixture: fixture)
    }

    func testSupersededPairAgainRevokesALateApproval() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let session = try fixture.vault.session()
        let replacement = TestHub()
        let transport = HeldPairingTransport(hub: replacement)
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now })
        let task = try XCTUnwrap(store.begin(invitation: replacement.invitationText(exp: exp)))
        await transport.waitUntilPolling()

        store.prepareToPair()
        XCTAssertNil(store.begin(invitation: "not a pairing link"))
        try await transport.approve()
        await task.value

        let revoked = await transport.unpaired
        XCTAssertEqual(revoked.count, 1)
        XCTAssertNotEqual(revoked.first, session.credentials)
        XCTAssertTrue(isFailed(store.phase))
        XCTAssertEqual(store.paired, original)
        try assertPairing(original, session: session, fixture: fixture)
    }

    func testSecondLinkDuringClaimOrApprovalDoesNotReplaceTheAttempt() async throws {
        let fixture = try makeFixture()
        let transport = HeldPairingTransport(hub: fixture.hub)
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now })
        let link = fixture.hub.invitationText(exp: exp)
        let task = try XCTUnwrap(store.begin(invitation: link))
        XCTAssertNil(store.begin(invitation: TestHub().invitationText(exp: exp)))
        XCTAssertEqual(store.phase, .claiming)
        await transport.waitUntilPolling()
        let waiting = store.phase
        XCTAssertNil(store.begin(invitation: link))
        XCTAssertNil(store.begin(invitation: "invalid"))
        XCTAssertEqual(store.phase, waiting)
        try await transport.approve()
        await task.value
        let claims = await transport.claims
        XCTAssertEqual(claims.count, 1)
        XCTAssertEqual(store.paired?.deviceID, claims.first?.deviceId)
        let revoked = await transport.unpaired
        XCTAssertTrue(revoked.isEmpty)
    }

    func testDismissedFirstPairingRevokesApprovalWithoutStoringIt() async throws {
        let fixture = try makeFixture()
        let transport = HeldPairingTransport(hub: fixture.hub)
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now })
        let task = try XCTUnwrap(store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await transport.waitUntilPolling()
        store.cancel()
        try await transport.approve()
        await task.value
        XCTAssertNil(fixture.vault.current())
        XCTAssertEqual(store.phase, .idle)
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked.count, 1)
        let claimHash = try await transport.claimedBearerHash()
        XCTAssertEqual(PairingStore.bearerHash(try XCTUnwrap(revoked.first).bearer), claimHash)
    }

    func testDismissalStopsClaimRetriesButStillCollectsAndRevokesApproval() async throws {
        let fixture = try makeFixture()
        let transport = HeldPairingTransport(hub: fixture.hub, loseClaimResponses: true)
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now })
        let task = try XCTUnwrap(store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await transport.waitUntilClaiming()
        store.cancel()
        await transport.loseClaimResponse()
        await transport.waitUntilPolling()
        try await transport.approve()
        await task.value
        XCTAssertNil(fixture.vault.current())
        XCTAssertEqual(store.phase, .idle)
        let claims = await transport.claims
        XCTAssertEqual(claims.count, 1, "dismissing stops claim retries")
        XCTAssertTrue(claims.allSatisfy { $0 == claims.first })
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked.count, 1)
        let claimHash = try await transport.claimedBearerHash()
        XCTAssertEqual(PairingStore.bearerHash(try XCTUnwrap(revoked.first).bearer), claimHash)
    }

    func testCancellationWhileStoppingTheOldPollResumesItWithoutStoringApproval() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let session = try fixture.vault.session()
        let transport = HeldPairingTransport(hub: fixture.hub)
        let preparing = expectation(description: "stopping old poll")
        var release: CheckedContinuation<Void, Never>?
        var finished = false
        var didStore = false
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now },
                                 prepareForPairingChange: {
                                     await withCheckedContinuation { continuation in
                                         release = continuation
                                         preparing.fulfill()
                                     }
                                 }, finishPairingChange: { finished = true },
                                 didStorePairing: { didStore = true; return true })
        let task = try XCTUnwrap(store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await transport.waitUntilPolling()
        try await transport.approve()
        await fulfillment(of: [preparing], timeout: 1)
        store.cancel()
        try XCTUnwrap(release).resume()
        await task.value
        XCTAssertTrue(finished)
        XCTAssertFalse(didStore)
        XCTAssertEqual(store.phase, .paired)
        try assertPairing(original, session: session, fixture: fixture)
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked.count, 1)
        XCTAssertNotEqual(revoked.first, session.credentials)
    }

    func testFailedOldDeviceRevocationIsRetriedFromKeychainAfterRestart() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let old = try fixture.vault.session()
        fixture.transport.failUnpair(with: RelayClientError.offline)
        try await pair(fixture)
        let pending = try fixture.vault.pendingDeviceRevocations()
        XCTAssertEqual(pending.map(\.credentials), [old.credentials])
        let reopened = PairingVault(directory: fixture.directory, secrets: fixture.secrets)
        let transport = HeldPairingTransport(hub: TestHub())
        let restarted = PairingStore(vault: reopened, transport: transport)
        await restarted.retryPendingRevocations()
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked, [old.credentials])
        XCTAssertTrue(try reopened.pendingDeviceRevocations().isEmpty)
        await restarted.retryPendingRevocations()
        let afterRetry = await transport.unpaired
        XCTAssertEqual(afterRetry.count, 1)
    }

    func testRemotePendingRevocationRestoresCurrentTicketAfterward() async throws {
        let oldTicket = "endpointabcdefghijklmnopqrst"
        let currentTicket = "endpointbcdefghijklmnopqrstu"
        let oldOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:40101"))
        let currentOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:40102"))
        let remoteOrigins = PairingRemoteOriginRecorder(origins: [oldTicket: oldOrigin, currentTicket: currentOrigin])
        let activations = PairingRemoteActivationRecorder()
        let fixture = try makeFixture(remoteOrigin: { remoteOrigins.origin(for: $0) },
                                      activateRemote: { activations.record($0) })
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let text = invitation(["relay": "http://127.0.0.1:8787", "iroh": currentTicket], hub: fixture.hub)
        let pairing = try XCTUnwrap(fixture.store.begin(invitation: text))
        await pairing.value
        XCTAssertEqual(fixture.store.phase, .paired)
        XCTAssertEqual(try fixture.vault.irohTicket(), currentTicket)

        remoteOrigins.clearCalls()
        activations.clearCalls()
        let oldRelayOrigin = try XCTUnwrap(URL(string: "https://old-relay.example"))
        try fixture.vault.enqueueDeviceRevocation(
            deviceID: "old-device",
            credentials: RelayCredentials(origin: oldRelayOrigin, bearer: "old-bearer"),
            irohTicket: oldTicket
        )

        let revocationTransport = HeldPairingTransport(hub: fixture.hub)
        let revocationStore = PairingStore(vault: fixture.vault, transport: revocationTransport,
                                           remoteOrigin: { remoteOrigins.origin(for: $0) },
                                           now: { self.now }, activateRemote: { activations.record($0) },
                                           deactivateRemote: {})
        await revocationStore.retryPendingRevocations()

        XCTAssertEqual(remoteOrigins.calls(), [oldTicket])
        let unpaired = await revocationTransport.unpaired
        XCTAssertEqual(unpaired.map(\.origin), [oldOrigin])
        XCTAssertEqual(unpaired.map(\.bearer), ["old-bearer"])
        let activationCalls = activations.calls()
        XCTAssertEqual(activationCalls.count, 1)
        XCTAssertEqual(activationCalls.last!, currentTicket)
    }

    func testRemotePendingRevocationDeactivatesWhenCurrentPairingHasNoTicket() async throws {
        let oldTicket = "endpointabcdefghijklmnopqrst"
        let oldOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:40101"))
        let remoteOrigins = PairingRemoteOriginRecorder(origins: [oldTicket: oldOrigin])
        let activations = PairingRemoteActivationRecorder()
        let fixture = try makeFixture(remoteOrigin: { remoteOrigins.origin(for: $0) },
                                      activateRemote: { activations.record($0) })
        try await pair(fixture)
        activations.clearCalls()
        remoteOrigins.clearCalls()
        try fixture.vault.enqueueDeviceRevocation(
            deviceID: "old-device",
            credentials: RelayCredentials(origin: try XCTUnwrap(URL(string: "https://old-relay.example")),
                                          bearer: "old-bearer"),
            irohTicket: oldTicket
        )

        await fixture.store.retryPendingRevocations()

        XCTAssertEqual(remoteOrigins.calls(), [oldTicket])
        let activationCalls = activations.calls()
        XCTAssertEqual(activationCalls.last!, nil)
    }

    func testRemoteUnpairWhileTheTunnelIsStartingKeepsThePairingAndRetries() async throws {
        let fixture = try await makeRemoteUnpairFixture()

        do {
            try await fixture.store.unpair()
            XCTFail("Expected the remote unpair to remain pending while the tunnel is offline")
        } catch let error as RelayClientError {
            XCTAssertEqual(error, .offline)
        } catch {
            XCTFail("Unexpected unpair error: \(error)")
        }

        let initialUnpairs = await fixture.transport.unpaired
        XCTAssertEqual(initialUnpairs.count, 0)
        XCTAssertNotNil(fixture.vault.current())
        XCTAssertEqual(try fixture.vault.irohTicket(), fixture.ticket)
        XCTAssertTrue(fixture.store.isPaired)

        fixture.origins.setOrigin(fixture.forwarderOrigin, for: fixture.ticket)
        try await fixture.store.unpair()

        let unpaired = await fixture.transport.unpaired
        XCTAssertEqual(unpaired.count, 1)
        XCTAssertEqual(unpaired.first?.origin, fixture.forwarderOrigin)
        XCTAssertNotEqual(unpaired.first?.origin, fixture.recordOrigin)
        XCTAssertNil(fixture.vault.current())
        XCTAssertNil(try fixture.vault.irohTicket())
        XCTAssertFalse(fixture.store.isPaired)
    }

    func testRemoteUnpairOfflineStillAllowsAnExplicitLocalForget() async throws {
        let fixture = try await makeRemoteUnpairFixture()

        do {
            try await fixture.store.unpair()
            XCTFail("Expected the remote unpair to remain pending while the tunnel is offline")
        } catch let error as RelayClientError {
            XCTAssertEqual(error, .offline)
        } catch {
            XCTFail("Unexpected unpair error: \(error)")
        }

        let initialUnpairs = await fixture.transport.unpaired
        XCTAssertEqual(initialUnpairs.count, 0)
        try fixture.store.forgetLocally()

        XCTAssertNil(fixture.vault.current())
        XCTAssertNil(try fixture.vault.irohTicket())
        XCTAssertFalse(fixture.store.isPaired)
    }

    func testReplacementWaitsForPushReadinessBeforeRevokingTheOldDevice() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let old = try fixture.vault.session()
        let replacement = TestHub()
        let transport = HeldPairingTransport(hub: replacement)
        let store = PairingStore(vault: fixture.vault, transport: transport, now: { self.now },
                                 didStorePairing: { false })
        let task = try XCTUnwrap(store.begin(invitation: replacement.invitationText(exp: exp)))
        await transport.waitUntilPolling()
        try await transport.approve()
        await task.value
        XCTAssertEqual(store.phase, .paired)
        let beforeReady = await transport.unpaired
        XCTAssertTrue(beforeReady.isEmpty)
        XCTAssertEqual(try fixture.vault.pendingDeviceRevocations().map(\.credentials), [old.credentials])
        // The push callback invokes this only after registration succeeds (or explicit opt-out).
        await store.retryPendingRevocations()
        let afterReady = await transport.unpaired
        XCTAssertEqual(afterReady, [old.credentials])
        XCTAssertTrue(try fixture.vault.pendingDeviceRevocations().isEmpty)
    }

    func testMissingOldPrivateKeyDoesNotSilentlySkipRevocation() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        let old = try fixture.vault.session()
        try fixture.secrets.delete(PairingSecretAccount.devicePrivateKey(secretID: original.secretID))
        let reopened = PairingVault(directory: fixture.directory, secrets: fixture.secrets)
        XCTAssertThrowsError(try reopened.session())
        let replacement = TestHub()
        let transport = HeldPairingTransport(hub: replacement)
        let store = PairingStore(vault: reopened, transport: transport, now: { self.now })
        let task = try XCTUnwrap(store.begin(invitation: replacement.invitationText(exp: exp)))
        await transport.waitUntilPolling()
        try await transport.approve()
        await task.value
        XCTAssertEqual(store.phase, .paired)
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked, [old.credentials])
    }

    func testPendingRevocationsCanRetryAfterThePhoneWasForgottenLocally() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let old = try fixture.vault.session()
        fixture.transport.failUnpair(with: RelayClientError.offline)
        try await pair(fixture)
        try fixture.store.forgetLocally()
        let reopened = PairingVault(directory: fixture.directory, secrets: fixture.secrets)
        let transport = HeldPairingTransport(hub: TestHub())
        let restarted = PairingStore(vault: reopened, transport: transport)
        XCTAssertFalse(restarted.isPaired)
        await restarted.retryPendingRevocations()
        let revoked = await transport.unpaired
        XCTAssertEqual(revoked, [old.credentials])
    }

    func testPairAgainAfterRestoreWithoutKeychainItemsSucceedsAndExplainsManualCleanup() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        // ThisDeviceOnly secrets do not migrate when the saved record is restored to another iPhone.
        for account in fixture.secrets.accounts { try fixture.secrets.delete(account) }
        let reopened = PairingVault(directory: fixture.directory, secrets: fixture.secrets)
        XCTAssertEqual(reopened.current()?.deviceID, original.deviceID)
        XCTAssertThrowsError(try reopened.session())
        let replacement = TestHub()
        let transport = HeldPairingTransport(hub: replacement)
        let store = PairingStore(vault: reopened, transport: transport, now: { self.now })
        let task = try XCTUnwrap(store.begin(invitation: replacement.invitationText(exp: exp)))
        await transport.waitUntilPolling()
        XCTAssertEqual(reopened.current()?.deviceID, original.deviceID, "The restored record stays until approval")
        try await transport.approve()
        await task.value

        XCTAssertEqual(store.phase, .paired)
        XCTAssertNotEqual(store.paired?.deviceID, original.deviceID)
        XCTAssertEqual(try reopened.session().deviceID, store.paired?.deviceID)
        let revoked = await transport.unpaired
        XCTAssertTrue(revoked.isEmpty, "No old bearer exists to authorize a revoke")
        XCTAssertTrue(try reopened.pendingDeviceRevocations().isEmpty)
        XCTAssertEqual(store.previousDeviceRemovalMessage,
                       "The old phone may still be listed on your computer. You can remove it in Isoform under Settings > Phone.")

        let restarted = PairingStore(vault: PairingVault(directory: fixture.directory, secrets: fixture.secrets),
                                     transport: transport)
        XCTAssertEqual(restarted.phase, .paired)
        XCTAssertEqual(restarted.previousDeviceRemovalMessage, store.previousDeviceRemovalMessage)
    }

    func testDismissalBeforeTheClaimStartsSendsNothingToTheMac() async throws {
        let fixture = try makeFixture()
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        fixture.store.cancel()
        await task.value
        XCTAssertTrue(fixture.transport.claimAttempts.isEmpty)
        XCTAssertEqual(fixture.transport.unpairCount, 0)
        XCTAssertNil(fixture.vault.current())
    }

    func testOldDeviceRevocationFailureDoesNotUndoTheNewPairing() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let original = try XCTUnwrap(fixture.vault.current())
        fixture.transport.failUnpair(with: RelayClientError.offline)
        try await pair(fixture)
        XCTAssertNotEqual(fixture.vault.current()?.deviceID, original.deviceID)
        XCTAssertEqual(fixture.store.paired, fixture.vault.current())
        XCTAssertEqual(fixture.transport.unpairCount, 1)
    }

    func testPairAgainSecretSaveFailureRestoresTheOldPrivateKey() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        let secrets = FailOncePairingSecrets(base: fixture.secrets)
        let vault = PairingVault(directory: fixture.directory, secrets: secrets)
        let store = PairingStore(vault: vault, transport: fixture.transport, now: { self.now })
        let original = try XCTUnwrap(vault.current())
        let session = try vault.session()
        let privateKey = try vault.devicePrivateKey()
        let notices = SharedNoticeStore.load(secrets: fixture.secrets)
        secrets.failNextBearerSave()
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])

        let task = try XCTUnwrap(store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertTrue(failureMessage(store.phase)?.contains("could not save") ?? false)
        XCTAssertEqual(store.paired, original)
        XCTAssertEqual(vault.current(), original)
        XCTAssertEqual(try vault.devicePrivateKey(), privateKey)
        XCTAssertEqual(SharedNoticeStore.load(secrets: fixture.secrets), notices)
        let reopened = PairingVault(directory: fixture.directory, secrets: fixture.secrets)
        XCTAssertEqual(try reopened.session().credentials, session.credentials)
        XCTAssertEqual(try reopened.session().keys, session.keys)
    }

    func testPairAgainRecordSaveFailureRestoresTheOldSecretsAndRecord() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        var failWrite = false
        let vault = PairingVault(directory: fixture.directory, secrets: fixture.secrets, writeRecord: { data, url in
            if failWrite { throw CocoaError(.fileWriteUnknown) }
            try data.write(to: url, options: .atomic)
        })
        let store = PairingStore(vault: vault, transport: fixture.transport, now: { self.now })
        let original = try XCTUnwrap(vault.current())
        let session = try vault.session()
        let notices = SharedNoticeStore.load(secrets: fixture.secrets)
        failWrite = true
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])

        let task = try XCTUnwrap(store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertTrue(failureMessage(store.phase)?.contains("could not save") ?? false)
        XCTAssertEqual(store.paired, original)
        XCTAssertEqual(vault.current(), original)
        XCTAssertEqual(try vault.session().credentials, session.credentials)
        XCTAssertEqual(SharedNoticeStore.load(secrets: fixture.secrets), notices)
        let reopened = PairingVault(directory: fixture.directory, secrets: fixture.secrets)
        XCTAssertEqual(try reopened.session().keys, session.keys)
    }

    // MARK: Unpair

    func testUnpairCallsTheRelayThenForgetsThePairing() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        try await fixture.store.unpair()

        XCTAssertEqual(fixture.transport.unpairCount, 1)
        XCTAssertNil(fixture.vault.current())
        XCTAssertFalse(fixture.store.isPaired)
        XCTAssertEqual(fixture.store.phase, .idle)
    }

    func testUnpairWhenTheRelayIsOfflineKeepsThePairingUntilForgottenLocally() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        fixture.transport.failUnpair(with: RelayClientError.offline)

        do {
            try await fixture.store.unpair()
            XCTFail("unpair should report the relay as unreachable")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .offline)
        }
        XCTAssertNotNil(fixture.vault.current())

        try fixture.store.forgetLocally()
        XCTAssertNil(fixture.vault.current())
        XCTAssertFalse(fixture.store.isPaired)
    }

    func testUnpairTreatsAnAlreadyRevokedDeviceAsDone() async throws {
        let fixture = try makeFixture()
        try await pair(fixture)
        fixture.transport.failUnpair(with: RelayClientError.unauthorized)
        try await fixture.store.unpair()
        XCTAssertNil(fixture.vault.current())
    }

    // MARK: Relay status codes (PROTOCOL.md section 5)

    func testClaimStatusCodesMapToTheirMessages() async throws {
        let cases: [(RelayClientError, String)] = [
            (.forbidden, "not valid"),
            (.notFound, "not valid"),
            (.conflict, "already used or closed"),
            (.rateLimited(retryAfter: 120), "Wait 2 minutes"),
        ]
        for (error, expected) in cases {
            let fixture = try makeFixture()
            fixture.transport.failClaim(with: error)
            let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
            await task.value
            guard case .failed(let message) = fixture.store.phase else {
                return XCTFail("\(error): expected a failure, got \(fixture.store.phase)")
            }
            XCTAssertTrue(message.contains(expected), "\(error): \(message)")
            XCTAssertNil(fixture.vault.current())
        }
    }

    func testClaimGoneReportsExpiry() async throws {
        let fixture = try makeFixture()
        fixture.transport.failClaim(with: RelayClientError.gone)
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertEqual(fixture.store.phase, .expired)
    }

    func testIdenticalClaimIsRetriedAfterTimeoutsWithTheSameBody() async throws {
        let fixture = try makeFixture()
        fixture.transport.queueClaimFailures([RelayClientError.timedOut, RelayClientError.offline])
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value

        XCTAssertEqual(fixture.store.phase, .paired)
        let attempts = fixture.transport.claimAttempts
        XCTAssertEqual(attempts.count, 3)
        XCTAssertEqual(attempts[0], attempts[1])
        XCTAssertEqual(attempts[1], attempts[2])
    }

    /// A tunnel or proxy in front of the relay answers 502 or 504, or a page that is not the relay's answer, once
    /// during the claim and once during the wait for approval: pairing goes on instead of failing and revoking the
    /// new device.
    func testAGatewayErrorDuringPairingIsRetriedLikeATimeout() async throws {
        let fixture = try makeFixture()
        fixture.transport.queueClaimFailures([RelayClientError.server(status: 502, message: ""), RelayClientError.invalidResponse])
        fixture.transport.script([
            { _ in throw RelayClientError.server(status: 504, message: "") },
            { claim in try fixture.hub.approval(for: claim) },
        ])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertEqual(fixture.store.phase, .paired)
        let attempts = fixture.transport.claimAttempts
        XCTAssertEqual(attempts.count, 3)
        XCTAssertEqual(attempts[0], attempts[1], "the identical claim each time")
        XCTAssertEqual(attempts[1], attempts[2])
    }

    func testPollConflictWhileStillOpenIsRetriedBeforeItBecomesARace() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([
            { _ in throw RelayClientError.conflict },
            { claim in try fixture.hub.approval(for: claim) },
        ])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertEqual(fixture.store.phase, .paired)
    }

    func testPollNotFoundAfterClaimMeansTheApprovalWasAlreadyDelivered() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([{ _ in throw RelayClientError.notFound }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertTrue(failureMessage(fixture.store.phase)?.contains("pair again") ?? false)
        XCTAssertNil(fixture.vault.current())
    }

    func testPollGoneMeansExpiredAndPollForbiddenMeansNotValid() async throws {
        let gone = try makeFixture()
        gone.transport.script([{ _ in throw RelayClientError.gone }])
        let goneTask = try XCTUnwrap(gone.store.begin(invitation: gone.hub.invitationText(exp: exp)))
        await goneTask.value
        XCTAssertEqual(gone.store.phase, .expired)

        let forbidden = try makeFixture()
        forbidden.transport.script([{ _ in throw RelayClientError.forbidden }])
        let forbiddenTask = try XCTUnwrap(forbidden.store.begin(invitation: forbidden.hub.invitationText(exp: exp)))
        await forbiddenTask.value
        XCTAssertTrue(failureMessage(forbidden.store.phase)?.contains("not valid") ?? false)
    }

    func testTransientPollFailureThenApprovalStillPairs() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([
            { _ in throw RelayClientError.offline },
            { claim in try fixture.hub.approval(for: claim) },
        ])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertEqual(fixture.store.phase, .paired)
    }

    func testRepeatedPollFailuresReportThatTheAnswerDidNotArrive() async throws {
        let fixture = try makeFixture()
        fixture.transport.script([
            { _ in throw RelayClientError.offline },
            { _ in throw RelayClientError.timedOut },
            { _ in throw RelayClientError.offline },
        ])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        let message = try XCTUnwrap(failureMessage(fixture.store.phase))
        XCTAssertTrue(message.contains("did not reach this phone"))
        XCTAssertTrue(message.contains("pair again"))
        XCTAssertNil(fixture.vault.current())
    }

    func testApprovalThatCannotBeSavedIsReportedAndNothingIsPaired() async throws {
        let fixture = try makeFixture()
        fixture.secrets.setFailSaves(true)
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertTrue(failureMessage(fixture.store.phase)?.contains("could not save") ?? false)
        XCTAssertNil(fixture.vault.current())
        XCTAssertFalse(fixture.store.isPaired)
    }

    private func failureMessage(_ phase: PairingPhase) -> String? {
        if case .failed(let message) = phase { return message }
        return nil
    }

    // MARK: Helpers

    private struct Fixture {
        let store: PairingStore
        let vault: PairingVault
        let transport: StubPairingTransport
        let hub: TestHub
        let secrets: InMemorySecretStore
        let directory: URL
    }

    private struct RemoteUnpairFixture {
        let vault: PairingVault
        let store: PairingStore
        let transport: HeldPairingTransport
        let origins: PairingRemoteOriginRecorder
        let ticket: String
        let recordOrigin: URL
        let forwarderOrigin: URL
    }

    private func makeRemoteUnpairFixture() async throws -> RemoteUnpairFixture {
        let ticket = "endpointabcdefghijklmnopqrst"
        let recordOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:8787"))
        let forwarderOrigin = try XCTUnwrap(URL(string: "http://127.0.0.1:41239"))
        let fixture = try makeFixture(remoteOrigin: { $0 == ticket ? forwarderOrigin : nil })
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let pairing = try XCTUnwrap(fixture.store.begin(invitation: invitation(
            ["relay": recordOrigin.absoluteString, "iroh": ticket], hub: fixture.hub
        )))
        await pairing.value
        XCTAssertEqual(fixture.store.phase, .paired)
        XCTAssertEqual(try fixture.vault.irohTicket(), ticket)

        let origins = PairingRemoteOriginRecorder(origins: [:])
        let vault = PairingVault(directory: fixture.directory, secrets: fixture.secrets,
                                 remoteOrigin: { origins.origin(for: $0) }, startRemote: { _ in })
        let transport = HeldPairingTransport(hub: fixture.hub)
        let store = PairingStore(vault: vault, transport: transport,
                                 remoteOrigin: { origins.origin(for: $0) }, now: { self.now },
                                 activateRemote: { _ in }, deactivateRemote: {})
        return RemoteUnpairFixture(vault: vault, store: store, transport: transport, origins: origins,
                                   ticket: ticket, recordOrigin: recordOrigin, forwarderOrigin: forwarderOrigin)
    }

    private func makeFixture(remoteOrigin: @escaping @Sendable (String) async -> URL? = { _ in nil },
                             activateRemote: @escaping @Sendable (String?) -> Void = { _ in }) throws -> Fixture {
        let secrets = InMemorySecretStore()
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try FileManager.default.removeItem(at: directory) }
        let vault = PairingVault(directory: directory, secrets: secrets)
        let transport = StubPairingTransport()
        let clock = now
        let store = PairingStore(vault: vault, transport: transport, remoteOrigin: remoteOrigin, now: { clock },
                                 activateRemote: activateRemote, deactivateRemote: {})
        return Fixture(store: store, vault: vault, transport: transport, hub: TestHub(), secrets: secrets,
                       directory: directory)
    }

    private func assertPairing(_ original: PairedDevice, session: SessionContext, fixture: Fixture,
                               persistedDirectory: URL? = nil, file: StaticString = #filePath, line: UInt = #line) throws {
        XCTAssertTrue(fixture.store.isPaired, file: file, line: line)
        XCTAssertEqual(fixture.store.paired, original, file: file, line: line)
        XCTAssertEqual(fixture.vault.current(), original, file: file, line: line)
        XCTAssertEqual(try fixture.vault.session().credentials, session.credentials, file: file, line: line)
        XCTAssertEqual(try fixture.vault.session().keys, session.keys, file: file, line: line)
        // Reopening bypasses the cached session, proving the saved secrets still work.
        let reopened = PairingVault(directory: persistedDirectory ?? fixture.directory, secrets: fixture.secrets)
        XCTAssertEqual(try reopened.session().deviceID, original.deviceID, file: file, line: line)
        XCTAssertEqual(try reopened.session().credentials, session.credentials, file: file, line: line)
        XCTAssertEqual(try reopened.session().keys, session.keys, file: file, line: line)
    }

    private func pair(_ fixture: Fixture) async throws {
        fixture.transport.script([{ claim in try fixture.hub.approval(for: claim) }])
        let task = try XCTUnwrap(fixture.store.begin(invitation: fixture.hub.invitationText(exp: exp)))
        await task.value
        XCTAssertEqual(fixture.store.phase, .paired)
    }

    private func isFailed(_ phase: PairingPhase) -> Bool {
        if case .failed = phase { return true }
        return false
    }

    // Builds an invitation from the hub's real values, with some fields overridden or removed.
    private func invitation(_ overrides: [String: Any], hub: TestHub, removing: [String] = []) -> String {
        var payload: [String: Any] = [
            "v": 1,
            "relay": hub.relay,
            "hub": hub.hubID,
            "pairing": hub.pairingID,
            "secret": RelayCrypto.base64URLEncode(hub.secret),
            "hubPub": hub.hubPublicText,
            "exp": exp,
        ]
        payload.merge(overrides) { _, new in new }
        for key in removing {
            payload[key] = nil
        }
        let json = try! JSONSerialization.data(withJSONObject: payload, options: [.sortedKeys])
        return "isoform://pair#" + RelayCrypto.base64URLEncode(json)
    }
}

// The seeded chat UI tests and a first pairing both call store() on a vault with no saved record.
// A Keychain read that fails while looking for the iroh cleanup record must not make that look unreadable.
final class PairingVaultFreshStoreTests: XCTestCase {
    // Fails only the cleanup-record read, as a Keychain that is not yet readable would.
    private final class CleanupReadFailsSecrets: SecretStore, @unchecked Sendable {
        let base = InMemorySecretStore()

        func accountNames() throws -> [String] { try base.accountNames() }

        func read(_ account: String) throws -> String? {
            if account == PairingSecretAccount.irohTicketTransaction {
                throw YansuError.message("Unlock your phone to use the saved pairing.")
            }
            return try base.read(account)
        }

        func save(_ value: String, account: String) throws { try base.save(value, account: account) }
        func delete(_ account: String) throws { try base.delete(account) }
    }

    func testFirstStoreOnAFreshVaultIgnoresAFailedCleanupRead() throws {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let secrets = CleanupReadFailsSecrets()
        let vault = PairingVault(directory: directory, secrets: secrets, remoteOrigin: { _ in nil },
                                 startRemote: { _ in })
        let paired = RelayCryptoTests.sampleRecord()
        let keyPair = RelayCrypto.makeDeviceKeyPair()

        try vault.store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(keyPair.privateKey),
                        bearer: "first-bearer")

        XCTAssertNil(vault.loadError)
        XCTAssertEqual(vault.current()?.deviceID, paired.deviceID)
        XCTAssertNotNil(try? vault.session())
    }

    func testRelaunchWithASavedRecordLoadsItAndStoresAgain() throws {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let secrets = InMemorySecretStore()
        let paired = RelayCryptoTests.sampleRecord()
        let keyPair = RelayCrypto.makeDeviceKeyPair()
        let privateKey = RelayCrypto.base64URLEncode(keyPair.privateKey)
        try PairingVault(directory: directory, secrets: secrets, remoteOrigin: { _ in nil },
                         startRemote: { _ in })
            .store(paired, devicePrivateKey: privateKey, bearer: "first-bearer")

        // A new process sees the record on disk and must not treat it as absent or unreadable.
        let relaunched = PairingVault(directory: directory, secrets: secrets, remoteOrigin: { _ in nil },
                                      startRemote: { _ in })
        XCTAssertNil(relaunched.loadError)
        XCTAssertEqual(relaunched.current()?.deviceID, paired.deviceID)
        try relaunched.store(paired, devicePrivateKey: privateKey, bearer: "second-bearer")
        XCTAssertNil(relaunched.loadError)
    }
}
