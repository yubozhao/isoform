import XCTest
@testable import Yansu

/// A relay pairing that also holds the computer's direct path falls back between the two (PROTOCOL.md 21.4).
final class DirectRouteTests: XCTestCase {
    private let ticket = "endpointabcdefghijklmnopqrstuvwxyz"
    private let directOrigin = URL(string: "http://127.0.0.1:41999")!

    func testTheVaultSwitchesToTheDirectPathAndBack() throws {
        let (vault, started) = try makeVault(mapped: true)
        XCTAssertFalse(vault.hasDirectRoute)
        let relay = try vault.session()
        XCTAssertEqual(relay.route, .relay)
        XCTAssertFalse(vault.switchRoute(after: relay), "A pairing without a second path has nothing to switch to")

        let bearer = RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))
        try vault.storeDirectRoute(DirectRoute(ticket: ticket, bearer: bearer), deviceID: relay.deviceID)
        XCTAssertTrue(vault.hasDirectRoute)
        XCTAssertEqual(vault.forwarderTicket(), ticket)
        XCTAssertEqual(try vault.session().route, .relay, "The relay stays first until it fails")

        XCTAssertTrue(vault.switchRoute(after: relay))
        let direct = try vault.session()
        XCTAssertEqual(direct.route, .direct)
        XCTAssertEqual(direct.credentials.origin, directOrigin)
        XCTAssertEqual(direct.credentials.bearer, bearer)
        XCTAssertEqual(direct.keys.d2h, relay.keys.d2h, "Both paths carry the same end-to-end keys")
        XCTAssertTrue(started.tickets.contains(ticket))

        XCTAssertTrue(vault.switchRoute(after: direct))
        XCTAssertEqual(try vault.session().route, .relay)
    }

    func testADirectPathThatIsStillOpeningLeavesTheRelayInCharge() throws {
        let (vault, started) = try makeVault(mapped: false)
        let relay = try vault.session()
        try vault.storeDirectRoute(DirectRoute(ticket: ticket, bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))),
                                   deviceID: relay.deviceID)
        XCTAssertTrue(vault.switchRoute(after: relay))
        let session = try vault.session()
        XCTAssertEqual(session.route, .relay)
        XCTAssertEqual(session.credentials.origin, relay.credentials.origin)
        XCTAssertTrue(started.tickets.contains(ticket))
    }

    func testPairingAgainDropsTheOldDirectPath() throws {
        let (vault, _) = try makeVault(mapped: true)
        let relay = try vault.session()
        try vault.storeDirectRoute(DirectRoute(ticket: ticket, bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))),
                                   deviceID: relay.deviceID)
        try storePairing(in: vault)
        XCTAssertFalse(vault.hasDirectRoute)
    }

    func testOnlyFailuresOnTheWayCountAsPathFailures() {
        for error: RelayClientError in [.offline, .timedOut, .invalidResponse, .unauthorized, .unavailable,
                                        .server(status: 502, message: "")] {
            XCTAssertTrue(SessionContext.isPathFailure(error), "\(error)")
        }
        for error: RelayClientError in [.conflict, .rateLimited(retryAfter: nil), .badRequest(message: "")] {
            XCTAssertFalse(SessionContext.isPathFailure(error), "\(error)")
        }
        XCTAssertFalse(SessionContext.isPathFailure(CancellationError()))
    }

    /// The relay cannot be reached (Bo's phone, Nov 6 to Nov 14): the request goes out over the direct path and
    /// its answer is read there.
    func testARequestTheRelayCannotCarryIsAnsweredOverTheDirectPath() async throws {
        let (vault, _) = try makeVault(mapped: true)
        let relay = try vault.session()
        try vault.storeDirectRoute(DirectRoute(ticket: ticket, bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))),
                                   deviceID: relay.deviceID)
        let transport = BrokenRelayTransport(brokenOrigin: relay.credentials.origin, context: relay)
        let channel = HubChannel(vault: vault, relay: transport)
        let status = try await channel.perform(.status)
        XCTAssertEqual(status.status, 200)
        let origins = await transport.sendOrigins
        XCTAssertEqual(origins, [relay.credentials.origin, directOrigin])
        XCTAssertEqual(try vault.session().route, .direct, "The path that worked stays first")
        await channel.prepareForPairingChange()
    }

    func testTheAwayLineSaysWhenThePhoneLastConnected() {
        let line = TodayStore.ConnectionCause.firstLine(.cantConnect, reason: nil, sleptAt: nil, asOf: "Nov 5",
                                                         lastConnected: "Nov 6")
        XCTAssertTrue(line.contains("Nov 6"))
        XCTAssertFalse(line.contains("Nov 5"), "The snapshot's date says nothing about the connection")
        XCTAssertEqual(TodayStore.ConnectionCause.firstLine(.cantConnect, reason: nil, sleptAt: nil, asOf: "Nov 5"),
                       TodayStore.ConnectionCause.firstLine(.cantConnect, reason: nil, sleptAt: nil, asOf: "Nov 5",
                                                            lastConnected: nil))
    }

    func testADayWithoutTheComputerSaysWhatToDo() {
        let now = Date()
        let recent = TodayStore.ConnectionCause.directions(.cantConnect, lastConnected: now.addingTimeInterval(-600), now: now)
        XCTAssertEqual(recent, TodayStore.ConnectionCause.cantConnect.directions)
        let stale = TodayStore.ConnectionCause.directions(.cantConnect, lastConnected: now.addingTimeInterval(-2 * 86_400), now: now)
        XCTAssertNotEqual(stale, TodayStore.ConnectionCause.cantConnect.directions)
        XCTAssertTrue(stale.contains("pair again"))
        XCTAssertEqual(TodayStore.ConnectionCause.directions(.sleeping, lastConnected: now.addingTimeInterval(-2 * 86_400), now: now),
                       TodayStore.ConnectionCause.sleeping.directions, "A sleeping computer keeps its own steps")
    }

    // MARK: Helpers

    private final class StartedTickets: @unchecked Sendable {
        private let lock = NSLock()
        private var values: [String] = []
        var tickets: [String] { lock.lock(); defer { lock.unlock() }; return values }
        func append(_ ticket: String) { lock.lock(); values.append(ticket); lock.unlock() }
    }

    private func makeVault(mapped: Bool) throws -> (PairingVault, StartedTickets) {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let started = StartedTickets()
        let origin = directOrigin
        let ticket = self.ticket
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore(),
                                 remoteOrigin: { mapped && $0 == ticket ? origin : nil },
                                 startRemote: { started.append($0) })
        try storePairing(in: vault)
        return (vault, started)
    }

    private func storePairing(in vault: PairingVault) throws {
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Test iPhone", scopes: ["chat:read"], pairedAt: Date(),
                                  counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
    }
}

/// A relay that never reaches the computer, beside a direct path that answers every request.
private actor BrokenRelayTransport: HubTransport {
    private let brokenOrigin: URL
    private let context: SessionContext
    private var pending: [RelayEnvelope] = []
    private var replySeq: UInt64 = 0
    private(set) var sendOrigins: [URL] = []

    init(brokenOrigin: URL, context: SessionContext) {
        self.brokenOrigin = brokenOrigin
        self.context = context
    }

    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        sendOrigins.append(credentials.origin)
        if credentials.origin == brokenOrigin { throw RelayClientError.offline }
        let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct), key: context.keys.d2h,
                                                     hub: context.hubID, dev: context.deviceID, dir: "d2h",
                                                     seq: envelope.seq, kind: "req")
        let request = try JSONSerialization.jsonObject(with: plaintext) as? [String: Any]
        let id = request?["id"] as? String ?? ""
        replySeq += 1
        let reply = try JSONSerialization.data(withJSONObject: [
            "id": id, "status": 200, "body": #"{"ok":true,"data":{"hubName":"Test Mac"}}"#,
        ])
        let ciphertext = try RelayCrypto.sealEnvelope(reply, key: context.keys.h2d, hub: context.hubID,
                                                      dev: context.deviceID, dir: "h2d", seq: replySeq, kind: "res")
        pending.append(RelayEnvelope(v: RelayCrypto.protocolVersion, hub: context.hubID, dev: context.deviceID,
                                     dir: "h2d", seq: replySeq, kind: "res", ct: RelayCrypto.base64URLEncode(ciphertext)))
    }

    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
        if credentials.origin == brokenOrigin { throw RelayClientError.offline }
        return pending.filter { $0.seq > after }
    }

    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {
        pending.removeAll { $0.seq <= upTo }
    }
}

final class DirectRouteOtherSessionTests: XCTestCase {
    func testAnAttachmentReadCanUseTheOtherPathWithoutMovingThePreference() throws {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let ticket = "endpointabcdefghijklmnopqrstuvwxyz"
        let directOrigin = URL(string: "http://127.0.0.1:41998")!
        let vault = PairingVault(directory: directory, secrets: InMemorySecretStore(),
                                 remoteOrigin: { $0 == ticket ? directOrigin : nil }, startRemote: { _ in })
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Test iPhone", scopes: ["brief:read"], pairedAt: Date(),
                                  counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
        let relay = try vault.session()
        XCTAssertNil(vault.otherSession(than: relay), "One path, nothing else to try")
        let bearer = RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))
        try vault.storeDirectRoute(DirectRoute(ticket: ticket, bearer: bearer), deviceID: relay.deviceID)
        let other = try XCTUnwrap(vault.otherSession(than: relay))
        XCTAssertEqual(other.route, .direct)
        XCTAssertEqual(other.credentials.origin, directOrigin)
        XCTAssertEqual(other.credentials.bearer, bearer)
        XCTAssertEqual(try vault.session().route, .relay, "A one-off read leaves the relay first")
        XCTAssertEqual(vault.otherSession(than: other)?.credentials.origin, relay.credentials.origin)
    }
}
