import XCTest
@testable import Yansu

/// "This phone needs pairing again" on Today and Pair again in Settings read one signal: the pinned relay's answer
/// to this phone's own credential (PROTOCOL.md 21.4). Bo's phone, Nov 16 to 17: the relay refused the credential
/// while the computer's direct path answered every request, so Today showed the line on some launches and Settings
/// said paired.
@MainActor
final class BriefPairAgainTests: XCTestCase {
    private let ticket = "endpointabcdefghijklmnopqrstuvwxyz"
    private let directOrigin = URL(string: "http://127.0.0.1:41997")!

    /// The relay refuses the credential: pairing again is needed whichever path comes first, and a refresh over
    /// the direct path does not clear it.
    func testARelayRefusalNeedsPairingAgainOnEitherPath() async throws {
        let h = try makeHarness()
        serve(relay: 401, direct: 200)

        await h.store.refresh()
        XCTAssertEqual(try h.vault.session().route, .relay)
        XCTAssertTrue(h.store.repairNeeded, "the relay refused this phone's credential")

        XCTAssertTrue(h.vault.switchRoute(after: try h.vault.session()))
        await h.store.refresh()
        XCTAssertEqual(try h.vault.session().route, .direct)
        XCTAssertTrue(h.store.repairNeeded, "the direct path answering does not make the relay accept the credential")
        XCTAssertTrue(StubURLProtocol.requests.contains { $0.0.url?.host == "relay.example" && $0.0.url?.path == "/v1/presence" },
                      "a refresh over the direct path still asks the relay")
    }

    /// The relay takes the credential again (a new pairing replaced it there): the next refresh clears the line,
    /// even over the direct path.
    func testTheLineClearsWhenTheRelayAcceptsTheCredential() async throws {
        let h = try makeHarness()
        serve(relay: 401, direct: 200)
        XCTAssertTrue(h.vault.switchRoute(after: try h.vault.session()))
        await h.store.refresh()
        XCTAssertTrue(h.store.repairNeeded)

        serve(relay: 200, direct: 200)
        await h.store.refresh()
        XCTAssertEqual(try h.vault.session().route, .direct)
        XCTAssertFalse(h.store.repairNeeded)
    }

    /// A refusal on the direct path is that path's own grant: the relay serves the read and nothing says to pair
    /// again.
    func testADirectPathRefusalIsNotAPairingLoss() async throws {
        let h = try makeHarness()
        serve(relay: 200, direct: 401)
        XCTAssertTrue(h.vault.switchRoute(after: try h.vault.session()))

        await h.store.refresh()
        XCTAssertFalse(h.store.repairNeeded)
        XCTAssertEqual(try h.vault.session().route, .relay, "the relay serves while the direct grant is refused")
    }

    /// A relay that cannot be reached says nothing about the credential: what the last answer said stands.
    func testAnUnreachableRelayLeavesTheAnswerAsItWas() async throws {
        let h = try makeHarness()
        serve(relay: 200, direct: 200)
        XCTAssertTrue(h.vault.switchRoute(after: try h.vault.session()))
        await h.store.refresh()
        XCTAssertFalse(h.store.repairNeeded)

        serve(relay: nil, direct: 200)
        await h.store.refresh()
        XCTAssertFalse(h.store.repairNeeded)
    }

    // MARK: Helpers

    private struct Harness {
        let vault: PairingVault
        let store: BriefStore
    }

    private func makeHarness() throws -> Harness {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let origin = directOrigin
        let ticket = self.ticket
        let vault = PairingVault(directory: directory.appendingPathComponent("pairing"), secrets: InMemorySecretStore(),
                                 remoteOrigin: { $0 == ticket ? origin : nil }, startRemote: { _ in })
        let hub = TestHub()
        let device = RelayCrypto.makeDeviceKeyPair()
        let record = PairedDevice(hubID: hub.hubID, hubName: "Test Mac", hubPublicKey: hub.hubPublicText,
                                  relayOrigin: hub.relay, deviceID: UUID().uuidString.lowercased(),
                                  deviceName: "Test iPhone", scopes: ["brief:read", "presence:read"], pairedAt: Date(),
                                  counters: SequenceCounters())
        try vault.store(record, devicePrivateKey: RelayCrypto.base64URLEncode(device.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
        let relay = try vault.session()
        try vault.storeDirectRoute(DirectRoute(ticket: ticket, bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32))),
                                   deviceID: relay.deviceID)
        let store = BriefStore(pairing: PairingStore(vault: vault), vault: vault,
                               cache: BriefCache(directory: directory.appendingPathComponent("briefs")),
                               hub: HubChannel(vault: vault, relay: UnreachableHub()),
                               relay: RelayClient(session: StubURLProtocol.session()))
        return Harness(vault: vault, store: store)
    }

    /// Each path answers presence and holds no brief yet, or answers `status`. A nil status cannot be reached.
    private func serve(relay: Int?, direct: Int?) {
        let directOrigin = self.directOrigin
        StubURLProtocol.install { request, _ in
            let status = request.url?.host == directOrigin.host && request.url?.port == directOrigin.port ? direct : relay
            guard let status else { throw URLError(.cannotConnectToHost) }
            let body: Data
            switch (status, request.url?.path) {
            case (200, "/v1/presence"):
                body = Data(#"{"ok":true,"data":{"hubOnline":true}}"#.utf8)
            case (200, "/v1/brief/latest"):
                return (HTTPURLResponse(url: request.url!, statusCode: 404, httpVersion: "HTTP/1.1", headerFields: nil)!,
                        Data(#"{"ok":false,"error":"No brief"}"#.utf8))
            case (200, _):
                throw URLError(.unsupportedURL)
            default:
                body = Data(#"{"ok":false,"error":"Device authentication required"}"#.utf8)
            }
            return (HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!, body)
        }
    }
}

/// The computer never answers a hub request: these tests read only the relay's own routes.
private struct UnreachableHub: HubTransport {
    func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
        throw HubRequestError.noAnswer
    }
    func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
        throw HubRequestError.noAnswer
    }
    func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {}
}
