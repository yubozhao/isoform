import CryptoKit
import Foundation
import XCTest
@testable import Yansu

private func pieces(of data: Data) -> [Data] {
    let size = 24 * 1024
    return stride(from: 0, to: data.count, by: size).map { data.subdata(in: $0..<min($0 + size, data.count)) }
}

private func parameters(_ query: String) -> [String: String] {
    var values: [String: String] = [:]
    for pair in query.split(separator: "&") {
        let parts = pair.split(separator: "=", maxSplits: 1).map(String.init)
        if parts.count == 2 { values[parts[0]] = parts[1] }
    }
    return values
}

private func envelope(_ object: [String: Any]) -> String {
    let data = try! JSONSerialization.data(withJSONObject: ["ok": true, "data": object])
    return String(decoding: data, as: UTF8.self)
}

private func failure(_ status: Int, _ message: String) -> (Int, String) {
    (status, String(decoding: try! JSONSerialization.data(withJSONObject: ["ok": false, "error": message]), as: UTF8.self))
}

/// Polls a condition until it holds or the timeout passes.
func awaitCondition(timeout: TimeInterval = 5, _ condition: () async -> Bool) async -> Bool {
    let deadline = Date().addingTimeInterval(timeout)
    repeat {
        if await condition() { return true }
        try? await Task.sleep(for: .milliseconds(10))
    } while Date() < deadline
    return await condition()
}

// Stands in for the Mac's brief routes at the BriefBodyAPI seam. It records what the sync path asks for and what
// it acknowledges, and whether the library already held that version when the acknowledgement was sent.
actor FakeBriefBodies: BriefBodyAPI {
    private let savedProbe: @Sendable (String, String) -> Bool
    private var packages: [String: Data] = [:]
    private var legacy: [String: MobileBriefV1] = [:]
    private var pause: Duration?
    private var failures: [String: Int] = [:]
    private(set) var packageRequests: [String] = []
    private(set) var legacyRequests: [String] = []
    private(set) var acknowledgements: [(id: String, version: String, savedFirst: Bool)] = []

    init(savedProbe: @escaping @Sendable (String, String) -> Bool) {
        self.savedProbe = savedProbe
    }

    func serve(_ id: String, _ data: Data) { packages[id] = data }
    func serveLegacy(_ id: String, _ brief: MobileBriefV1) { legacy[id] = brief }
    func pauseEachPackage(for duration: Duration) { pause = duration }
    func fail(_ id: String, times: Int) { failures[id] = times }

    func briefPackage(id: String) async throws -> (version: String, package: Data) {
        packageRequests.append(id)
        if let pause { try await Task.sleep(for: pause) }
        if let remaining = failures[id], remaining > 0 {
            failures[id] = remaining - 1
            throw HubRequestError.noAnswer
        }
        guard let data = packages[id] else { throw HubRequestError.rejected(status: 404, message: "brief not found") }
        return (BriefCache.sha256Hex(data), data)
    }

    func brief(id: String) async throws -> MobileBriefV1 {
        legacyRequests.append(id)
        if let remaining = failures[id], remaining > 0 {
            failures[id] = remaining - 1
            throw HubRequestError.noAnswer
        }
        guard let brief = legacy[id] else { throw HubRequestError.rejected(status: 404, message: "brief not found") }
        return brief
    }

    func briefReceived(id: String, version: String) async throws {
        acknowledgements.append((id, version, savedProbe(id, version)))
    }
}

// The brief library (design section 6) and its one sync path: packages are verified against the version the Mac
// named, parts are reassembled, a 409 starts again from the head, the current brief follows one rule, a snapshot
// refresh does not cancel a fetch that is still named, acknowledgements follow the save, retention is 30 days for
// text and 7 for audio, and a Mac before 1.0 is read as one object.
@MainActor
final class BriefLibraryTests: XCTestCase {
    private let now = RelayTime.parse("2026-10-20T12:00:00Z")!

    // MARK: Hub transport that answers on the wire

    /// A hub that opens each request with the device keys and seals a scripted reply, so HubChannel's real envelope
    /// path runs. `answer` receives the method, path, query and body, and returns the status and body.
    private actor ScriptedHub: HubTransport {
        typealias Answer = (status: Int, body: String)
        private let session: SessionContext
        private let answer: @Sendable (String, String, String, String) -> Answer
        private var replies: [RelayEnvelope] = []
        private var replySequence: UInt64 = 0
        private(set) var requests: [(method: String, path: String, query: String, body: String)] = []

        init(session: SessionContext, answer: @escaping @Sendable (String, String, String, String) -> Answer) {
            self.session = session
            self.answer = answer
        }

        func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
            let plaintext = try RelayCrypto.openEnvelope(try RelayCrypto.base64URLDecode(envelope.ct), key: session.keys.d2h,
                                                         hub: session.hubID, dev: session.deviceID, dir: "d2h",
                                                         seq: envelope.seq, kind: "req")
            guard let object = try JSONSerialization.jsonObject(with: plaintext) as? [String: Any] else {
                throw HubRequestError.malformedResponse
            }
            let method = object["method"] as? String ?? ""
            let path = object["path"] as? String ?? ""
            let query = object["query"] as? String ?? ""
            let body = object["body"] as? String ?? ""
            requests.append((method, path, query, body))
            let scripted = answer(method, path, query, body)
            replySequence += 1
            let reply = try JSONSerialization.data(withJSONObject: [
                "id": object["id"] as? String ?? "", "status": scripted.status, "body": scripted.body,
            ])
            let ciphertext = try RelayCrypto.sealEnvelope(reply, key: session.keys.h2d, hub: session.hubID,
                                                          dev: session.deviceID, dir: "h2d", seq: replySequence, kind: "res")
            replies.append(RelayEnvelope(v: RelayCrypto.protocolVersion, hub: session.hubID, dev: session.deviceID,
                                         dir: "h2d", seq: replySequence, kind: "res",
                                         ct: RelayCrypto.base64URLEncode(ciphertext)))
        }

        func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
            let ready = replies.filter { $0.seq > after }
            if ready.isEmpty { try await Task.sleep(for: .milliseconds(2)) }
            return ready
        }

        func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {}
    }

    /// A transport that never answers, for a store whose hub calls are not under test.
    private struct UnansweredHub: HubTransport {
        func sendRequest(_ credentials: RelayCredentials, envelope: RelayEnvelope) async throws {
            throw HubRequestError.noAnswer
        }
        func pollResponses(_ credentials: RelayCredentials, after: UInt64, wait: Int) async throws -> [RelayEnvelope] {
            throw HubRequestError.noAnswer
        }
        func ackResponses(_ credentials: RelayCredentials, upTo: UInt64) async throws {}
    }

    // MARK: The Mac's brief routes, scripted

    private final class Recorder {
        var received: [(id: String, saved: Bool)] = []
    }

    // MARK: Fixtures

    private func full(_ id: String, asOf: String, text: String = "A full sentence about the day.") -> MobileBriefV1 {
        MobileBriefV1(briefId: id, slot: "morning", localDate: String(asOf.prefix(10)), asOf: asOf,
                      hubName: "Test Mac", narrativeStatus: "ok", narrative: [BriefParagraph(text: text)])
    }

    /// A brief long enough to need several 24 KiB parts.
    private func long(_ id: String, asOf: String, paragraphs: Int) -> MobileBriefV1 {
        let text = String(repeating: "A sentence about the work and the people in it. ", count: 20)
        return MobileBriefV1(briefId: id, slot: "evening", localDate: String(asOf.prefix(10)), asOf: asOf,
                             hubName: "Test Mac", narrativeStatus: "ok",
                             narrative: (0..<paragraphs).map { BriefParagraph(text: "\($0). " + text) })
    }

    private func package(_ brief: MobileBriefV1) throws -> Data {
        try JSONEncoder().encode(brief)
    }

    private func pairedVault(_ directory: URL) throws -> (PairingVault, SessionContext) {
        let vault = PairingVault(directory: directory.appendingPathComponent("pairing"), secrets: InMemorySecretStore())
        let hubKeys = RelayCrypto.makeDeviceKeyPair()
        let deviceKeys = RelayCrypto.makeDeviceKeyPair()
        let paired = PairedDevice(hubID: "hub", hubName: "Test Mac", hubPublicKey: RelayCrypto.base64URLEncode(hubKeys.publicKey),
                                  relayOrigin: "https://relay.example", deviceID: "phone", deviceName: "Test phone",
                                  scopes: ["brief:read"], pairedAt: Date(), counters: SequenceCounters())
        try vault.store(paired, devicePrivateKey: RelayCrypto.base64URLEncode(deviceKeys.privateKey),
                        bearer: RelayCrypto.base64URLEncode(RelayCrypto.randomBytes(32)))
        return (vault, try vault.session())
    }

    private struct Harness {
        let directory: URL
        let session: SessionContext
        let cache: BriefCache
        let bodies: FakeBriefBodies
        let store: BriefStore
    }

    private func harness() throws -> Harness {
        let directory = try makeTemporaryDirectory()
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        let (vault, session) = try pairedVault(directory)
        let cacheNow = now
        let cache = BriefCache(directory: directory.appendingPathComponent("briefs"), now: { cacheNow })
        let bodies = FakeBriefBodies(savedProbe: { identifier, version in
            cache.load(identifier: identifier)?.version == version
        })
        let store = BriefStore(pairing: PairingStore(vault: vault), vault: vault, cache: cache,
                               hub: HubChannel(vault: vault, relay: UnansweredHub()),
                               relay: RelayClient(session: StubURLProtocol.session()), bodies: bodies)
        return Harness(directory: directory, session: session, cache: cache, bodies: bodies, store: store)
    }

    private func snapshot(generated: String = "2026-10-20T12:00:00Z", current: TodayBriefRef? = nil,
                          briefs: [TodayBriefIndex] = []) -> MobileTodaySnapshot {
        var value = MobileTodaySnapshot(generatedAt: generated, latestBrief: nil, briefs: briefs, needsYou: [],
                                        needsYouCount: 0, day: TodayDay(next: nil, free: [], calendarConnected: false),
                                        topics: [], briefRequest: nil,
                                        schedule: MobileBriefSchedule(morning: "07:00", evening: "18:00"))
        value.currentBrief = current
        return value
    }

    private func index(_ id: String, version: String?, bodyVersion: String? = nil) -> TodayBriefIndex {
        var entry = TodayBriefIndex(id: id, slot: "morning", localDate: "2026-10-20", scheduledAt: nil,
                                    madeAt: "2026-10-20T07:00:00Z", catchUp: nil, durationSeconds: nil, lead: "Lead")
        entry.version = version
        entry.bodyVersion = bodyVersion ?? version
        return entry
    }

    private static func seal(_ brief: MobileBriefV1, sequence: UInt64, session: SessionContext) throws -> RelayBriefRecord {
        let ciphertext = try RelayCrypto.sealEnvelope(RelayCrypto.padBrief(JSONEncoder().encode(brief)),
                                                      key: session.keys.h2d, hub: session.hubID, dev: session.deviceID,
                                                      dir: "h2d", seq: sequence, kind: "brief")
        let envelope = RelayEnvelope(v: RelayCrypto.protocolVersion, hub: session.hubID, dev: session.deviceID,
                                     dir: "h2d", seq: sequence, kind: "brief", ct: RelayCrypto.base64URLEncode(ciphertext))
        return RelayBriefRecord(version: sequence, asOf: brief.asOf ?? "", env: envelope)
    }

    private static func serveHistory(_ records: [RelayBriefRecord]) throws {
        let objects = try records.map { record -> [String: Any] in
            let envelope = try JSONSerialization.jsonObject(with: JSONEncoder().encode(record.env))
            return ["version": record.version, "asOf": record.asOf, "env": envelope]
        }
        let body = try JSONSerialization.data(withJSONObject: ["ok": true, "data": ["briefs": objects]])
        StubURLProtocol.install { request, _ in
            guard request.url?.path == "/v1/briefs" else { throw URLError(.unsupportedURL) }
            return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil)!, body)
        }
    }

    // MARK: Hub routes: parts, restarts and acknowledgements

    func testHubAssemblesAPackageFromItsPartsAndNamesItsVersion() async throws {
        let directory = try makeTemporaryDirectory()
        let (vault, session) = try pairedVault(directory)
        let big = try package(long("brief-1", asOf: "2026-10-20T07:00:00Z", paragraphs: 60))
        let version = BriefCache.sha256Hex(big)
        let pieces = pieces(of: big)
        XCTAssertGreaterThan(pieces.count, 2)
        let hub = ScriptedHub(session: session) { method, path, query, _ in
            guard method == "GET", path == "/briefs/brief-1" else { return failure(404, "route not found") }
            if query == "package=1" {
                return (200, envelope(["briefId": "brief-1", "version": version, "parts": pieces.count,
                                            "data": RelayCrypto.base64URLEncode(pieces[0])]))
            }
            let values = parameters(query)
            guard values["version"] == version, let index = values["part"].flatMap(Int.init), pieces.indices.contains(index) else {
                return failure(409, "The brief changed.")
            }
            return (200, envelope(["version": version, "part": index, "data": RelayCrypto.base64URLEncode(pieces[index])]))
        }
        let fetched = try await HubChannel(vault: vault, relay: hub).briefPackage(id: "brief-1")
        XCTAssertEqual(fetched.version, version)
        XCTAssertEqual(fetched.package, big)
        XCTAssertEqual(BriefCache.sha256Hex(fetched.package), fetched.version)
        let requests = await hub.requests
        XCTAssertEqual(requests.count, pieces.count, "one head and one request for each later part")
    }

    func testAChangedPackageStartsTheFetchAgainFromTheHead() async throws {
        let directory = try makeTemporaryDirectory()
        let (vault, session) = try pairedVault(directory)
        let first = try package(long("brief-1", asOf: "2026-10-20T07:00:00Z", paragraphs: 60))
        let second = try package(full("brief-1", asOf: "2026-10-20T08:00:00Z", text: "Remade."))
        let firstVersion = BriefCache.sha256Hex(first)
        let secondVersion = BriefCache.sha256Hex(second)
        let firstPieces = pieces(of: first)
        final class Heads: @unchecked Sendable { var count = 0 }
        let heads = Heads()
        let hub = ScriptedHub(session: session) { method, path, query, _ in
            guard method == "GET", path == "/briefs/brief-1" else { return failure(404, "route not found") }
            if query == "package=1" {
                heads.count += 1
                if heads.count == 1 {
                    return (200, envelope(["briefId": "brief-1", "version": firstVersion, "parts": firstPieces.count,
                                                "data": RelayCrypto.base64URLEncode(firstPieces[0])]))
                }
                return (200, envelope(["briefId": "brief-1", "version": secondVersion, "parts": 1,
                                            "data": RelayCrypto.base64URLEncode(second)]))
            }
            // The Mac remade the brief between the head and the part, so the part is refused.
            return failure(409, "The brief changed.")
        }
        let fetched = try await HubChannel(vault: vault, relay: hub).briefPackage(id: "brief-1")
        XCTAssertEqual(fetched.version, secondVersion)
        XCTAssertEqual(fetched.package, second)
        XCTAssertEqual(heads.count, 2, "a 409 starts again from the head, never from the old version's parts")
    }

    func testAcknowledgementPostsTheVersionToTheReceivedRoute() async throws {
        let directory = try makeTemporaryDirectory()
        let (vault, session) = try pairedVault(directory)
        let version = String(repeating: "a", count: 64)
        let hub = ScriptedHub(session: session) { method, path, _, _ in
            guard method == "POST", path == "/briefs/brief-1/received" else { return failure(404, "route not found") }
            return (200, envelope(["received": true]))
        }
        try await HubChannel(vault: vault, relay: hub).briefReceived(id: "brief-1", version: version)
        let requests = await hub.requests
        let request = try XCTUnwrap(requests.last)
        XCTAssertEqual(request.method, "POST")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(request.body.utf8)) as? [String: String])
        XCTAssertEqual(body, ["version": version])
    }

    // MARK: Verification and the one current-brief rule

    func testAPackageThatDoesNotHashToItsVersionIsRejectedAndNothingIsSaved() throws {
        let h = try harness()
        let data = try package(full("brief-1", asOf: "2026-10-19T07:00:00Z"))
        XCTAssertThrowsError(try h.cache.save(package: data, version: String(repeating: "0", count: 64))) { error in
            XCTAssertEqual(error as? BriefPackageError, .versionMismatch)
        }
        XCTAssertNil(h.cache.load(identifier: "brief-1"))
        XCTAssertThrowsError(try h.cache.save(package: data, version: BriefCache.sha256Hex(data), identifier: "other")) { error in
            XCTAssertEqual(error as? BriefPackageError, .wrongBrief)
        }
        XCTAssertNil(h.cache.load(identifier: "brief-1"))
    }

    func testOnlyABriefWhoseFullTextIsOnThePhoneIsCurrent() throws {
        let h = try harness()
        try h.cache.save(full("older", asOf: "2026-10-18T07:00:00Z"))
        try h.cache.save(MobileBriefV1(briefId: "textless", asOf: "2026-10-20T07:00:00Z", narrativeStatus: "ok"))
        XCTAssertEqual(h.cache.current()?.brief.identifier, "older", "a brief without its text is never current")
        var withdrawn = full("withdrawn", asOf: "2026-10-20T08:00:00Z")
        withdrawn.narrativeStatus = "unavailable"
        withdrawn.audio = nil
        try h.cache.save(withdrawn)
        XCTAssertEqual(h.cache.current()?.brief.identifier, "older", "a withdrawn brief is never current")
    }

    func testAnOlderRelayCopyNeverReplacesANewerBriefOfTheSameID() async throws {
        let h = try harness()
        let newer = full("brief-1", asOf: "2026-10-20T07:00:00Z", text: "Newer text.")
        let newerPackage = try package(newer)
        try h.cache.save(package: newerPackage, version: BriefCache.sha256Hex(newerPackage))
        let older = full("brief-1", asOf: "2026-10-19T07:00:00Z", text: "Older text.")
        try Self.serveHistory([try Self.seal(older, sequence: 9, session: h.session)])
        await h.store.refreshHistory()
        XCTAssertEqual(h.cache.load(identifier: "brief-1")?.brief, newer)
        XCTAssertEqual(h.store.current, newer, "a copy that arrives later and is older never becomes current")
    }

    func testARelayCopyOfAWithdrawnBriefIsNotSavedAgain() async throws {
        let h = try harness()
        let withdrawn = full("brief-1", asOf: "2026-10-19T07:00:00Z", text: "Withdrawn text.")
        try h.cache.save(withdrawn)
        h.store.loadCached()
        h.store.reconcileToday(snapshot(current: nil, briefs: []))
        try Self.serveHistory([try Self.seal(withdrawn, sequence: 9, session: h.session)])
        await h.store.refreshHistory()
        XCTAssertNil(h.cache.load(identifier: "brief-1"))
        XCTAssertNil(h.store.current, "a withdrawn brief never returns as current from the relay's history")
    }

    // MARK: The one sync path

    func testSnapshotFetchesEveryNamedBriefAndAcknowledgesEachOnlyAfterItIsSaved() async throws {
        let h = try harness()
        let a = try package(full("a", asOf: "2026-10-20T07:00:00Z", text: "Brief a."))
        let b = try package(full("b", asOf: "2026-10-19T18:00:00Z", text: "Brief b."))
        await h.bodies.serve("a", a)
        await h.bodies.serve("b", b)
        let recorder = Recorder()
        h.store.onBriefReceived = { brief, _, fromCache, _ in
            guard !fromCache else { return }
            // The audio player hears about a brief only once its package is in the library.
            recorder.received.append((brief.identifier, h.cache.load(identifier: brief.identifier) != nil))
        }
        let versionA = BriefCache.sha256Hex(a)
        let versionB = BriefCache.sha256Hex(b)
        let value = snapshot(current: TodayBriefRef(id: "a", version: versionA),
                             briefs: [index("a", version: versionA), index("b", version: versionB)])
        XCTAssertEqual(BriefStore.namedBriefs(in: value).1.first, "a", "the current brief is fetched first")
        h.store.reconcileToday(value)
        let saved = await awaitCondition {
            h.cache.load(identifier: "a") != nil && h.cache.load(identifier: "b") != nil
        }
        XCTAssertTrue(saved)
        let acknowledged = await awaitCondition { (await h.bodies.acknowledgements).count == 2 }
        XCTAssertTrue(acknowledged)
        XCTAssertEqual(h.store.current?.identifier, "a")
        XCTAssertEqual(Set(recorder.received.map(\.id)), ["a", "b"])
        XCTAssertTrue(recorder.received.allSatisfy(\.saved))
        let acknowledgements = await h.bodies.acknowledgements
        XCTAssertTrue(acknowledgements.allSatisfy(\.savedFirst), "the Mac is told only after the package is saved")
        XCTAssertEqual(Set(acknowledgements.map(\.version)), [versionA, versionB])
    }

    func testASnapshotRefreshDoesNotCancelTheFetchOfABriefItStillNames() async throws {
        let h = try harness()
        let a = try package(full("a", asOf: "2026-10-20T07:00:00Z"))
        await h.bodies.serve("a", a)
        await h.bodies.pauseEachPackage(for: .milliseconds(300))
        let version = BriefCache.sha256Hex(a)
        let named = [index("a", version: version)]
        h.store.reconcileToday(snapshot(current: TodayBriefRef(id: "a", version: version), briefs: named))
        h.store.reconcileToday(snapshot(generated: "2026-10-20T12:01:00Z", current: TodayBriefRef(id: "a", version: version),
                                        briefs: named))
        let saved = await awaitCondition { h.cache.load(identifier: "a") != nil }
        XCTAssertTrue(saved, "the second snapshot cancelled the fetch that was still named")
        let requests = await h.bodies.packageRequests
        XCTAssertEqual(requests, ["a"], "the fetch was started once and not restarted")
    }

    func testAFetchStopsOnceTheSnapshotNoLongerNamesTheBrief() async throws {
        let h = try harness()
        let a = try package(full("a", asOf: "2026-10-20T07:00:00Z"))
        await h.bodies.serve("a", a)
        await h.bodies.fail("a", times: 1)
        let version = BriefCache.sha256Hex(a)
        h.store.reconcileToday(snapshot(current: TodayBriefRef(id: "a", version: version), briefs: [index("a", version: version)]))
        try await Task.sleep(for: .milliseconds(200))
        h.store.reconcileToday(snapshot(generated: "2026-10-20T12:01:00Z"))
        // The first retry is due after five seconds; by then the snapshot no longer names the brief.
        try await Task.sleep(for: .seconds(6))
        let requests = await h.bodies.packageRequests
        XCTAssertEqual(requests, ["a"], "a fetch kept retrying a brief the snapshot no longer names")
        XCTAssertNil(h.cache.load(identifier: "a"))
    }

    func testAWithdrawnBriefLeavesTheLibraryAndIsNeverCurrentAgain() throws {
        let h = try harness()
        try h.cache.save(full("gone", asOf: "2026-10-19T07:00:00Z"))
        h.store.loadCached()
        XCTAssertEqual(h.store.current?.identifier, "gone")
        h.store.reconcileToday(snapshot(current: nil, briefs: []))
        XCTAssertNil(h.cache.load(identifier: "gone"))
        XCTAssertNil(h.store.current)
        XCTAssertNil(h.store.cachedBrief(withID: "gone"))
    }

    func testOnlyABriefInsideTheMacIndexWindowIsWithdrawnByItsAbsence() throws {
        let h = try harness()
        try h.cache.save(full("aged", asOf: "2026-10-02T07:00:00Z"))
        try h.cache.save(full("recent", asOf: "2026-10-18T07:00:00Z"))
        h.store.loadCached()
        h.store.reconcileToday(snapshot(current: nil, briefs: []))
        XCTAssertNotNil(h.cache.load(identifier: "aged"), "a brief past the Mac's 14-day index is kept for 30 days")
        XCTAssertNil(h.cache.load(identifier: "recent"), "a brief inside the index window that it no longer names is withdrawn")
    }

    func testAMacBeforeVersionsIsReadAsOneObjectAndSavedUnderItsBodyVersion() async throws {
        let h = try harness()
        let legacy = full("old-mac", asOf: "2026-10-20T07:00:00Z", text: "Written by an older Mac.")
        await h.bodies.serveLegacy("old-mac", legacy)
        h.store.reconcileToday(snapshot(briefs: [index("old-mac", version: nil, bodyVersion: "body-1")]))
        let saved = await awaitCondition { h.cache.load(identifier: "old-mac")?.version == "body-1" }
        XCTAssertTrue(saved)
        let legacyRequests = await h.bodies.legacyRequests
        let packageRequests = await h.bodies.packageRequests
        XCTAssertEqual(legacyRequests, ["old-mac"])
        XCTAssertEqual(packageRequests, [], "a Mac before 1.0 has no package route to ask")
        XCTAssertEqual(h.cache.load(identifier: "old-mac")?.brief, legacy)
        XCTAssertNil(h.cache.load(identifier: "old-mac")?.package)
    }

    func testOpeningMissingHistoryFetchesAndCachesTheBody() async throws {
        let h = try harness()
        let old = full("past", asOf: "2026-10-19T07:00:00Z", text: "The full past brief.")
        await h.bodies.serveLegacy("past", old)
        let fetched = await h.store.fetchOpenedBrief("past")
        XCTAssertEqual(fetched, old)
        XCTAssertEqual(h.cache.load(identifier: "past")?.brief, old)
        let again = await h.store.fetchOpenedBrief("past")
        XCTAssertEqual(again, old)
        let requests = await h.bodies.legacyRequests
        XCTAssertEqual(requests, ["past"])
        let invalid = await h.store.fetchOpenedBrief("bad/id")
        XCTAssertNil(invalid)
        let requestsAfterInvalid = await h.bodies.legacyRequests
        XCTAssertEqual(requestsAfterInvalid, ["past"])
    }

    // MARK: Migration and retention

    func testAnEntryWrittenBeforeVersionsStillReadsAndIsFetchedAgain() throws {
        let h = try harness()
        try h.cache.save(full("kept", asOf: "2026-10-19T07:00:00Z"))
        let file = try XCTUnwrap(FileManager.default.contentsOfDirectory(at: h.directory.appendingPathComponent("briefs"),
                                                                         includingPropertiesForKeys: nil)
            .first { $0.pathExtension == "json" })
        // The file a phone before 1.0 wrote: a todayVersion, and no version or package.
        var object = try XCTUnwrap(JSONSerialization.jsonObject(with: try Data(contentsOf: file)) as? [String: Any])
        object.removeValue(forKey: "version")
        object["todayVersion"] = "body-1"
        try JSONSerialization.data(withJSONObject: object).write(to: file)
        let loaded = try XCTUnwrap(h.cache.load(identifier: "kept"))
        XCTAssertNil(loaded.version, "an old entry is version-less, so it is fetched when Today names a version")
        XCTAssertNil(loaded.package)
        XCTAssertEqual(loaded.brief.identifier, "kept")
        XCTAssertEqual(h.cache.current()?.brief.identifier, "kept")
    }

    /// The library reads its folder once and keeps it in step with every save, removal and prune, instead of
    /// decoding every brief file on the main actor for each read. A fresh read of the folder always agrees.
    func testTheLibraryIndexMatchesTheFolder() throws {
        let h = try harness()
        let folder = h.directory.appendingPathComponent("briefs")
        try h.cache.save(full("a", asOf: "2026-10-18T07:00:00Z"))
        try h.cache.save(full("b", asOf: "2026-10-19T07:00:00Z", text: "First words."))
        try h.cache.save(full("b", asOf: "2026-10-19T07:00:00Z", text: "Second words."))
        try h.cache.remove(identifier: "a")
        try h.cache.save(full("old", asOf: "2026-09-01T07:00:00Z"))
        let fixed = now
        let fromDisk = BriefCache(directory: folder, now: { fixed })
        XCTAssertEqual(h.cache.all().map(\.brief), fromDisk.all().map(\.brief))
        XCTAssertEqual(h.cache.all().map(\.brief.identifier), ["b"])
        XCTAssertEqual(h.cache.load(identifier: "b")?.brief.narrative.first?.text, "Second words.")

        // A file moved in from outside (the legacy migration) shows after a reload.
        let other = try makeTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: other) }
        try BriefCache(directory: other).save(full("moved", asOf: "2026-10-19T08:00:00Z"))
        BriefCacheMigration.moveLegacyFiles(from: other, to: folder)
        h.cache.reload()
        XCTAssertEqual(Set(h.cache.all().map(\.brief.identifier)), ["b", "moved"])
    }

    func testTextIsKeptThirtyDaysAndAudioSevenDays() throws {
        XCTAssertEqual(BriefCache.textRetentionInterval, 30 * 24 * 60 * 60)
        XCTAssertEqual(BriefAudioCache.retentionInterval, 7 * 24 * 60 * 60)
        let h = try harness()
        try h.cache.save(full("twenty-nine-days", asOf: "2026-09-21T07:00:00Z"))
        try h.cache.save(full("thirty-one-days", asOf: "2026-09-19T07:00:00Z"))
        XCTAssertNotNil(h.cache.load(identifier: "twenty-nine-days"))
        XCTAssertNil(h.cache.load(identifier: "thirty-one-days"))
    }
}
