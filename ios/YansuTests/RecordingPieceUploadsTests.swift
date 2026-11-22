import XCTest
@testable import Yansu

/// A relay that takes pieces the way PROTOCOL.md 24.1a says, and can be told to cut requests.
private final class StubPieceRelay: RecordingPieceRelay, @unchecked Sendable {
    private let lock = NSLock()
    var support: RelayClient.UploadPieceSupport = .inOrder
    /// Any-order uploads: the piece size and which pieces are held, by upload id.
    private var marks: [String: (size: Int, have: [Bool], total: Int)] = [:]
    private var sparse: [String: Data] = [:]
    private(set) var progressAsked = 0
    /// Bodies still arriving, by upload id, with their declared size.
    private var arriving: [String: (total: Int, bytes: Data)] = [:]
    private(set) var finished: [String: Data] = [:]
    /// Every piece request as (offset, length), also the ones that were cut or refused.
    private(set) var pieces: [(Int, Int)] = []
    private(set) var healthAsked = 0
    /// Piece requests (counted from 1) that fail on the way: nothing of them is kept.
    var cutRequests: Set<Int> = []
    /// Piece requests that are stored although the answer is lost.
    var lostAnswers: Set<Int> = []
    var error: Error?

    func uploadPieceSupport(_ origin: URL) async -> RelayClient.UploadPieceSupport {
        lock.withLock { healthAsked += 1 }
        return support
    }

    private func sparseProgress(_ id: String) -> RelayClient.UploadProgress? {
        guard let mark = marks[id] else { return nil }
        let held = mark.have.enumerated().filter(\.element).reduce(0) { $0 + min(mark.size, mark.total - $1.offset * mark.size) }
        return .init(held: held, total: mark.total, complete: false, size: mark.size,
                     have: String(mark.have.map { $0 ? "1" : "0" }))
    }

    /// Stores a piece of an any-order upload, as a background upload would.
    func storeSized(_ id: String, offset: Int, total: Int, size: Int, piece: Data) -> RelayClient.UploadProgress {
        lock.withLock { storeSizedLocked(id, offset: offset, total: total, size: size, piece: piece) }
    }

    private func storeSizedLocked(_ id: String, offset: Int, total: Int, size: Int, piece: Data) -> RelayClient.UploadProgress {
        if let done = finished[id] { return .init(held: done.count, total: done.count, complete: true) }
        let count = (total + size - 1) / size
        var mark = marks[id] ?? (size, Array(repeating: false, count: count), total)
        var body = sparse[id] ?? Data(count: total)
        precondition(offset % size == 0 && (piece.count == size || offset + piece.count == total), "a piece off the cut")
        body.replaceSubrange(offset..<(offset + piece.count), with: piece)
        mark.have[offset / size] = true
        if mark.have.allSatisfy({ $0 }) {
            marks[id] = nil
            sparse[id] = nil
            finished[id] = body
            return .init(held: total, total: total, complete: true)
        }
        marks[id] = mark
        sparse[id] = body
        return sparseProgress(id)!
    }

    func uploadProgress(_ credentials: RelayCredentials, id: String) async throws -> RelayClient.UploadProgress? {
        try lock.withLock {
            progressAsked += 1
            if let error { throw error }
            if let done = finished[id] { return .init(held: done.count, total: done.count, complete: true) }
            if let sized = sparseProgress(id) { return sized }
            guard let held = arriving[id] else { return nil }
            return .init(held: held.bytes.count, total: held.total, complete: false)
        }
    }

    func putUploadPiece(_ credentials: RelayCredentials, id: String, offset: Int, total: Int, size: Int?,
                        piece: Data) async throws -> RelayClient.UploadProgress {
        try lock.withLock {
            pieces.append((offset, piece.count))
            if let error { throw error }
            if cutRequests.contains(pieces.count) { throw RelayClientError.offline }
            if let size {
                let answer = storeSizedLocked(id, offset: offset, total: total, size: size, piece: piece)
                if lostAnswers.contains(pieces.count) { throw RelayClientError.timedOut }
                return answer
            }
            if let done = finished[id] {
                guard done.count == total else { throw RelayClientError.conflict }
                return .init(held: total, total: total, complete: true)
            }
            var held = arriving[id] ?? (total, Data())
            if held.total != total {
                guard offset == 0 else { return .init(held: 0, total: total, complete: false) }
                held = (total, Data())
            }
            guard offset == held.bytes.count else { return .init(held: held.bytes.count, total: total, complete: false) }
            precondition(offset + piece.count <= total, "a piece runs past the end")
            held.bytes.append(piece)
            if held.bytes.count == total {
                arriving[id] = nil
                finished[id] = held.bytes
            } else {
                arriving[id] = held
            }
            if lostAnswers.contains(pieces.count) { throw RelayClientError.timedOut }
            return .init(held: held.bytes.count, total: total, complete: held.bytes.count == total)
        }
    }

    func start(_ id: String, total: Int, with bytes: Data) { lock.withLock { arriving[id] = (total, bytes) } }
}

private final class StubWhole: RecordingUploadTransport, @unchecked Sendable {
    private let lock = NSLock()
    private(set) var put: [String] = []
    private(set) var stopped: [String] = []
    func putUpload(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        lock.withLock { put.append(id) }
    }
    func stop(_ id: String) { lock.withLock { stopped.append(id) } }
}

/// Stands for iOS: it takes the pieces handed over and delivers them when told to.
private final class StubSystem: RecordingBackgroundPieces, @unchecked Sendable {
    private let lock = NSLock()
    let relay: StubPieceRelay
    /// Pieces iOS holds: (id, number, bytes, total, size).
    private var held: [(String, Int, Data, Int, Int)] = []
    private(set) var handed: [[Int]] = []
    var refusal: Error?
    init(relay: StubPieceRelay) { self.relay = relay }

    /// How often a hand-over was waited for.
    private(set) var waited = 0

    func putPieces(_ credentials: RelayCredentials, id: String, sealed: Data, size: Int, missing: [Int]) async throws {
        let waiting = try lock.withLock { () -> Bool in
            if let refusal { throw refusal }
            waited += 1
            let flying = Set(held.filter { $0.0 == id }.map(\.1))
            let new = missing.filter { !flying.contains($0) }
            handed.append(new)
            for index in new {
                let start = index * size
                held.append((id, index, sealed.subdata(in: start..<min(start + size, sealed.count)), sealed.count, size))
            }
            return held.contains { $0.0 == id }
        }
        if waiting { throw RecordingUploadInFlight() }
    }

    func handOver(_ credentials: RelayCredentials, id: String, sealed: Data, size: Int, missing: [Int]) async throws {
        try lock.withLock {
            if let refusal { throw refusal }
            let flying = Set(held.filter { $0.0 == id }.map(\.1))
            let new = missing.filter { !flying.contains($0) }
            handed.append(new)
            for index in new {
                let start = index * size
                held.append((id, index, sealed.subdata(in: start..<min(start + size, sealed.count)), sealed.count, size))
            }
        }
    }

    func holds(_ id: String) async -> Bool { lock.withLock { held.contains { $0.0 == id } } }

    /// iOS has held what it holds for minutes without an end.
    var isStuck = false
    /// Pieces that were stopped. Handing over never stops any: only the app's word does.
    private(set) var stoppedPieces: [Int] = []
    func stop(_ id: String) {
        lock.withLock {
            stoppedPieces += held.filter { $0.0 == id }.map(\.1)
            held.removeAll { $0.0 == id }
        }
    }
    func stuck(_ id: String) async -> Bool { lock.withLock { isStuck && held.contains { $0.0 == id } } }

    /// iOS holds nothing any more, and nothing of it arrived.
    func drop() { lock.withLock { held = [] } }

    /// iOS sends only these pieces and goes on holding the rest.
    func deliverOnly(_ numbers: Set<Int>) {
        let pieces = lock.withLock { () -> [(String, Int, Data, Int, Int)] in
            let going = held.filter { numbers.contains($0.1) }
            held.removeAll { numbers.contains($0.1) }
            return going
        }
        for (id, index, bytes, total, size) in pieces {
            _ = relay.storeSized(id, offset: index * size, total: total, size: size, piece: bytes)
        }
    }

    /// iOS sends what it holds, last piece first, except the numbers it is told to lose.
    func deliver(losing lost: Set<Int> = []) {
        let pieces = lock.withLock { () -> [(String, Int, Data, Int, Int)] in
            defer { held = [] }
            return held
        }
        for (id, index, bytes, total, size) in pieces.reversed() where !lost.contains(index) {
            _ = relay.storeSized(id, offset: index * size, total: total, size: size, piece: bytes)
        }
    }
}

/// The time the system still gives, one answer after the other; the last one stays.
private final class Times: @unchecked Sendable {
    private let lock = NSLock()
    private var left: [TimeInterval]
    init(_ left: [TimeInterval]) { self.left = left }
    func next() -> TimeInterval { lock.withLock { left.count > 1 ? left.removeFirst() : left[0] } }
}

/// The time of day, moved on by hand.
private final class StubClock: @unchecked Sendable {
    private let lock = NSLock()
    private var at = Date(timeIntervalSince1970: 1_795_000_000)
    func now() -> Date { lock.withLock { at } }
    func pass(_ seconds: TimeInterval) { lock.withLock { at = at.addingTimeInterval(seconds) } }
}

private final class Flag: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: Bool
    init(_ value: Bool) { stored = value }
    var value: Bool {
        get { lock.withLock { stored } }
        set { lock.withLock { stored = newValue } }
    }
}

/// 2026-11-18: a tunnel cut every 3 MB upload after some seconds and nothing arrived for two days.
/// In pieces, a cut costs one piece and the part goes on from what the relay holds.
final class RecordingPieceUploadsTests: XCTestCase {
    private let credentials = RelayCredentials(origin: URL(string: "https://relay.example")!, bearer: "b")
    private let id = "6f1c2c9e-3b1a-4c55-9f4e-2a7b8c9d0e1f"
    private let sealed = Data((0..<2500).map { UInt8(truncatingIfNeeded: $0 &* 31 &+ 7) })

    private func make(_ relay: StubPieceRelay, _ whole: StubWhole, open: Flag = Flag(true), system: StubSystem? = nil,
                      clock: @escaping @Sendable () -> Date = { Date() },
                      timeLeft: @escaping @Sendable () async -> TimeInterval = { 25 }) -> RecordingPieceUploads {
        RecordingPieceUploads(relay: relay, whole: whole, background: system, appIsOpen: { open.value },
                              stopWhole: { whole.stop($0); system?.stop($0) }, pieceBytes: 1000, now: clock, timeLeft: timeLeft, kept: nil)
    }

    // MARK: The send in a wake

    /// 2026-11-20, Bo's phone: pieces handed to iOS in a background wake reached the relay only
    /// while the app was awake, 2 in two wakes and none in the 11 minutes between. In a wake
    /// the app sends them itself, and nothing is handed to iOS.
    func testInAWakeTheAppSendsThePiecesItself() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(false), system: system)
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        XCTAssertEqual(known, .sentInWake)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertTrue(system.handed.isEmpty, "nothing went to iOS")
    }

    /// The switch is the computer's: without its word, and with the app in front, the pieces
    /// go to iOS as before.
    func testWithoutTheSwitchOrInFrontThePiecesGoToTheSystem() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let open = Flag(false)
        let uploads = make(relay, StubWhole(), open: open, system: system)
        var known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: false)
        XCTAssertEqual(known, .handed)
        open.value = true
        known = await uploads.beginKnowing(credentials, id: "other", sealed: sealed, inWake: true)
        XCTAssertEqual(known, .handed)
        XCTAssertEqual(system.handed, [[0, 1, 2], [0, 1, 2]])
        XCTAssertTrue(relay.pieces.isEmpty, "the app sent nothing itself")
    }

    /// The time runs out after the first piece: the app stops there and the rest goes to iOS.
    func testAWakeThatEndsLeavesTheRestWithTheSystem() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let left = Times([25, 25, 5])
        let uploads = make(relay, StubWhole(), open: Flag(false), system: system, timeLeft: { left.next() })
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        XCTAssertEqual(known, .handed)
        XCTAssertEqual(relay.pieces.map(\.0), [0], "one piece went from the app")
        XCTAssertEqual(system.handed, [[1, 2]], "only what is missing goes to iOS")
        system.deliver()
        XCTAssertEqual(relay.finished[id], sealed)
    }

    /// Too little time for even one piece: the part goes the way it went before.
    func testAWakeWithNoTimeLeftHandsOverAsBefore() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(false), system: system, timeLeft: { 4 })
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        XCTAssertEqual(known, .handed)
        XCTAssertEqual(system.handed, [[0, 1, 2]])
        XCTAssertTrue(relay.pieces.isEmpty)
        XCTAssertEqual(relay.progressAsked, 0)
    }

    /// A piece that is cut ends the app's own send: nothing of it is kept, and it goes to iOS
    /// with the pieces behind it.
    func testAPieceCutInAWakeGoesToTheSystem() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        relay.cutRequests = [2]
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(false), system: system)
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        XCTAssertEqual(known, .handed)
        XCTAssertEqual(system.handed, [[1, 2]])
    }

    /// A part iOS holds and of which the relay got no more in three looks: what iOS holds is
    /// stopped and the app sends what the relay does not hold. The four noon parts of
    /// 2026-11-20 are this case.
    func testAHeldPartIsSentInTheWake() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let whole = StubWhole()
        let open = Flag(true)
        let clock = StubClock()
        let uploads = make(relay, whole, open: open, system: system, clock: clock.now)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        system.deliverOnly([0])
        system.isStuck = true
        open.value = false
        for _ in 0..<3 {
            let left = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
            XCTAssertEqual(left, .handed)
            clock.pass(15 * 60)
        }
        XCTAssertTrue(whole.stopped.isEmpty, "nothing is stopped before the looks are over")
        XCTAssertTrue(relay.pieces.isEmpty)
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        let word = await uploads.takeHeldWord(id)
        XCTAssertEqual(word, "held-afresh-in-wake")
        XCTAssertEqual(known, .sentInWake)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(whole.stopped, [id], "what iOS held is stopped")
        XCTAssertEqual(relay.pieces.map(\.0), [1000, 2000], "the piece the relay holds does not go again")
    }

    /// The relay does not say what it holds: what iOS holds is left as it is.
    func testAHeldPartIsLeftAloneInAWakeWhenTheRelayDoesNotAnswer() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let whole = StubWhole()
        let open = Flag(true)
        let uploads = make(relay, whole, open: open, system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        system.isStuck = true
        open.value = false
        relay.error = RelayClientError.offline
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        XCTAssertEqual(known, .handed)
        XCTAssertTrue(whole.stopped.isEmpty)
        XCTAssertTrue(system.stoppedPieces.isEmpty)
    }

    /// A part that went up before and of which the relay holds nothing: the computer may have
    /// taken it. Its word is waited for, and a part it has does not go up a second time.
    func testAPartTheComputerTookDoesNotGoUpAgain() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let open = Flag(true)
        let uploads = make(relay, StubWhole(), open: open, system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        system.drop()
        open.value = false
        var known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true, taken: { true })
        XCTAssertEqual(known, .no)
        XCTAssertTrue(relay.pieces.isEmpty, "nothing went up")
        // The computer still expects it: it goes up.
        known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true, taken: { false })
        XCTAssertEqual(known, .sentInWake)
    }

    /// The relay holds pieces of the part, so the computer cannot have taken it: its word is
    /// not waited for.
    func testThePiecesGoWithoutWaitingForTheComputersWord() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let open = Flag(true)
        let uploads = make(relay, StubWhole(), open: open, system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true)
        system.deliverOnly([0])
        system.drop()
        open.value = false
        let asked = Flag(false)
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed, inWake: true, taken: {
            asked.value = true
            return true
        })
        XCTAssertEqual(known, .sentInWake)
        XCTAssertFalse(asked.value)
    }

    /// 2026-11-20: four parts iOS held since noon were asked about on every wake; the relay
    /// answered and the phone had no answer, so they were left as they were into the night.
    /// The pass is told why, and after three asks without an answer the part is started
    /// afresh from its file, every piece.
    func testAHeldPartWhoseAskGetsNoAnswerThreeTimesIsStartedAfreshWhole() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let whole = StubWhole()
        let uploads = make(relay, whole, open: Flag(true), system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        system.deliverOnly([0])
        system.isStuck = true
        relay.error = RelayClientError.timedOut
        for _ in 0..<2 {
            let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
            XCTAssertEqual(known, .handed, "no answer: what iOS holds is left as it is")
            let word = await uploads.takeHeldWord(id)
            XCTAssertEqual(word, "held-ask-relay-timed-out")
        }
        XCTAssertTrue(whole.stopped.isEmpty)
        XCTAssertEqual(system.handed, [[0, 1, 2]])

        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handedAgain)
        XCTAssertEqual(whole.stopped, [id], "what iOS held is stopped")
        XCTAssertEqual(system.handed.last, [0, 1, 2], "every piece goes again, the relay was not heard")
        let word = await uploads.takeHeldWord(id)
        XCTAssertEqual(word, "held-afresh-unasked")
        let none = await uploads.takeHeldWord(id)
        XCTAssertNil(none, "the word is read once")
        // The part arrives whole all the same.
        relay.error = nil
        system.isStuck = false
        system.deliver()
        XCTAssertEqual(relay.finished[id], sealed)
    }

    /// An answer in between starts the count again.
    func testAnAnswerStartsTheCountOfUnansweredAsksAgain() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let whole = StubWhole()
        let uploads = make(relay, whole, open: Flag(true), system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        system.isStuck = true
        relay.error = RelayClientError.timedOut
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        relay.error = nil
        var known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed, "answered: looked at and left alone")
        relay.error = RelayClientError.timedOut
        for _ in 0..<2 {
            known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
            XCTAssertEqual(known, .handed, "two asks without an answer are not three")
        }
        XCTAssertTrue(whole.stopped.isEmpty)
    }

    /// Parts asked for at the same moment go one after the other, in the order asked.
    func testPartsGoOneAfterTheOtherInAWake() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(false), system: system)
        let credentials = credentials, sealed = sealed
        _ = await uploads.beginKnowing(credentials, id: "warm", sealed: sealed, inWake: true)
        async let first = uploads.beginKnowing(credentials, id: "a", sealed: sealed, inWake: true)
        async let second = uploads.beginKnowing(credentials, id: "b", sealed: sealed, inWake: true)
        let both = await [first, second]
        XCTAssertEqual(both, [.sentInWake, .sentInWake])
        XCTAssertEqual(relay.finished.count, 3)
    }

    func testAPartGoesUpInPiecesAndArrivesWhole() async throws {
        let relay = StubPieceRelay()
        let whole = StubWhole()
        try await make(relay, whole).putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(relay.pieces.map(\.0), [0, 1000, 2000])
        XCTAssertEqual(relay.pieces.map(\.1), [1000, 1000, 500])
        XCTAssertTrue(whole.put.isEmpty, "the whole-body upload is not used")
        XCTAssertEqual(whole.stopped, [id], "an upload iOS still holds for this part is stopped first")
    }

    func testACutCostsOnePieceAndTheNextTryGoesOnFromThere() async throws {
        let relay = StubPieceRelay()
        let whole = StubWhole()
        let uploads = make(relay, whole)
        relay.cutRequests = [2]
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("the cut must end this try")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .offline)
        }
        XCTAssertNil(relay.finished[id])
        relay.cutRequests = []
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(relay.pieces.map(\.0), [0, 1000, 1000, 2000], "the first piece is not sent again")
    }

    func testAPieceWhoseAnswerWasLostIsNotStoredTwice() async throws {
        let relay = StubPieceRelay()
        let whole = StubWhole()
        let uploads = make(relay, whole)
        relay.lostAnswers = [1, 3]
        for _ in 0..<2 {
            do { try await uploads.putUpload(credentials, id: id, sealed: sealed) } catch {}
        }
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed, "the same bytes arrive, in order, once")
        XCTAssertEqual(relay.pieces.map(\.0), [0, 1000, 2000], "the relay is asked what it holds, not sent a piece again")
    }

    func testTheRelaySaysWhereItIsWhenThePhoneIsWrong() async throws {
        let relay = StubPieceRelay()
        let whole = StubWhole()
        // The relay holds the same part under another size: it starts again at the first piece.
        relay.start(id, total: 9999, with: Data(count: 4000))
        try await make(relay, whole).putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(relay.pieces.first?.0, 0)
    }

    func testARelayWithoutPiecesAndAClosedAppUseTheWholeUpload() async throws {
        let relay = StubPieceRelay()
        relay.support = .none
        let whole = StubWhole()
        let open = Flag(true)
        let clock = Flag(false)
        let start = Date()
        let uploads = make(relay, whole, open: open) { clock.value ? start.addingTimeInterval(RecordingPieceUploads.askAgainSeconds + 1) : start }
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(whole.put, [id, id], "an older relay gets the whole body")
        XCTAssertEqual(relay.healthAsked, 1, "an older relay is not asked before every part")
        XCTAssertTrue(relay.pieces.isEmpty)
        XCTAssertTrue(whole.stopped.isEmpty, "the upload iOS holds is left alone when it is the way")

        // The relay was updated: after a while the phone asks again and uses pieces.
        relay.support = .inOrder
        clock.value = true
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(relay.healthAsked, 2)

        // With the app in the background the system's upload carries the part, as before.
        open.value = false
        let other = "0b1c2d3e-4f50-4a61-8b72-9c8d7e6f5a4b"
        try await uploads.putUpload(credentials, id: other, sealed: sealed)
        XCTAssertEqual(whole.put, [id, id, other])
        XCTAssertNil(relay.finished[other])
    }

    // MARK: Any order

    func testWithTheAppOpenOnlyTheMissingPiecesAreSent() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let whole = StubWhole()
        // The system delivered the last piece while the app was closed.
        _ = relay.storeSized(id, offset: 2000, total: sealed.count, size: 1000, piece: sealed.subdata(in: 2000..<2500))
        try await make(relay, whole).putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(relay.pieces.map(\.0), [0, 1000], "the piece the relay holds is not sent again")
        XCTAssertEqual(whole.stopped, [id], "what iOS still holds for this part is stopped first")
    }

    func testACutInAnyOrderCostsOnePiece() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let uploads = make(relay, StubWhole())
        relay.cutRequests = [2]
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("the cut must end this try")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .offline)
        }
        relay.cutRequests = []
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)
        XCTAssertEqual(relay.pieces.map(\.0), [0, 1000, 1000, 2000])
    }

    /// The app is in the background: every missing piece is handed to iOS once, iOS delivers
    /// them in its own order, and the part counts as sent only when the relay says it is whole.
    func testInTheBackgroundThePiecesGoToTheSystem() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let whole = StubWhole()
        let system = StubSystem(relay: relay)
        let uploads = make(relay, whole, open: Flag(false), system: system)

        func attempt() async -> Error? {
            do { try await uploads.putUpload(credentials, id: id, sealed: sealed); return nil } catch { return error }
        }
        var outcome = await attempt()
        XCTAssertTrue(outcome is RecordingUploadInFlight, "the pieces are with the system")
        XCTAssertEqual(system.handed, [[0, 1, 2]])
        XCTAssertTrue(relay.pieces.isEmpty, "the app sends nothing itself in the background")
        XCTAssertTrue(whole.put.isEmpty)

        // Asked again before iOS did anything: nothing is handed over twice.
        outcome = await attempt()
        XCTAssertTrue(outcome is RecordingUploadInFlight)
        XCTAssertEqual(system.handed.last, [])

        // iOS delivers, and loses piece 1. Only that one is handed over again.
        system.deliver(losing: [1])
        outcome = await attempt()
        XCTAssertTrue(outcome is RecordingUploadInFlight)
        XCTAssertEqual(system.handed.last, [1])
        XCTAssertNil(relay.finished[id])

        system.deliver()
        outcome = await attempt()
        XCTAssertNil(outcome, "the relay holds the whole part")
        XCTAssertEqual(relay.finished[id], sealed)
    }

    /// 2026-11-18, 15:00: the app was open for 25 seconds, had not sent a piece itself by then,
    /// and had handed nothing to iOS, so nothing moved for an hour. With the app open the pieces
    /// for a relay iOS can reach are iOS's too, so they go on when the app leaves.
    func testWithTheAppOpenThePiecesForThePinnedRelayAreTheSystems() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let whole = StubWhole()
        let system = StubSystem(relay: relay)
        let uploads = make(relay, whole, open: Flag(true), system: system)
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("the pieces are with the system")
        } catch {
            XCTAssertTrue(error is RecordingUploadInFlight)
        }
        XCTAssertEqual(system.handed, [[0, 1, 2]])
        XCTAssertTrue(relay.pieces.isEmpty, "the app sends nothing itself to a relay iOS can reach")
        system.deliver()
        try await uploads.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(relay.finished[id], sealed)

        // Over the direct path, a door inside the app, the app sends the pieces itself.
        let direct = RelayCredentials(origin: URL(string: "http://127.0.0.1:4321")!, bearer: "d")
        let other = "0b1c2d3e-4f50-4a61-8b72-9c8d7e6f5a4b"
        try await uploads.putUpload(direct, id: other, sealed: sealed)
        XCTAssertEqual(relay.finished[other], sealed)
        XCTAssertEqual(relay.pieces.count, 3)
    }

    /// 2026-11-18, 19:40, build 20261119030739: the direct path was chosen first and did not
    /// answer. The phone then handed four whole parts to iOS for the direct path's address, which
    /// is a door inside the app that iOS cannot reach. They never ended, the phone waited for
    /// them, and nothing reached either relay. Nothing for that address goes to iOS: a direct
    /// path that does not answer is a path that is down, and the part takes the other one.
    func testNothingForTheDirectPathIsEverHandedToTheSystem() async throws {
        let direct = RelayCredentials(origin: URL(string: "http://127.0.0.1:4321")!, bearer: "d")
        let relay = StubPieceRelay()
        relay.support = .none
        let whole = StubWhole()
        let system = StubSystem(relay: relay)
        let open = Flag(true)
        let uploads = make(relay, whole, open: open, system: system)
        do {
            try await uploads.putUpload(direct, id: id, sealed: sealed)
            XCTFail("a direct path that does not answer must fail as a path that is down")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .offline)
            XCTAssertTrue(SessionContext.isPathFailure(error), "so the part takes the other path")
        }
        XCTAssertTrue(whole.put.isEmpty, "no whole upload is handed to the system for an address inside the app")
        XCTAssertTrue(system.handed.isEmpty)
        XCTAssertEqual(whole.stopped, [id], "and what the system held for this part from before is stopped")
        let began = await uploads.begin(direct, id: id, sealed: sealed)
        XCTAssertFalse(began)

        // The same with the app in the background, whatever the relay says.
        relay.support = .anyOrder
        open.value = false
        do {
            try await uploads.putUpload(direct, id: id, sealed: sealed)
            XCTFail("must throw")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .offline)
        }
        XCTAssertTrue(whole.put.isEmpty)
        XCTAssertTrue(system.handed.isEmpty)

        // The pinned relay with the same silence still gets the whole upload, as before.
        relay.support = .none
        let other = make(relay, whole, open: Flag(true), system: system)
        try await other.putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(whole.put, [id])
    }

    /// A part can be left with the system before the computer is asked anything.
    func testAPartIsLeftWithTheSystemAhead() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(true), system: system)
        var began = await uploads.begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        XCTAssertEqual(system.handed, [[0, 1, 2]])
        XCTAssertEqual(relay.progressAsked, 0, "nothing is asked first: handing over is the first thing that happens")
        // Asked again while the system holds it: nothing is handed over twice.
        began = await uploads.begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        XCTAssertEqual(system.handed.count, 1)
        // Not over the direct path, and not to a relay that takes no pieces in any order.
        let direct = RelayCredentials(origin: URL(string: "http://127.0.0.1:4321")!, bearer: "d")
        began = await uploads.begin(direct, id: "0b1c2d3e-4f50-4a61-8b72-9c8d7e6f5a4b", sealed: sealed)
        XCTAssertFalse(began)
        let older = StubPieceRelay()
        older.support = .inOrder
        let olderUploads = make(older, StubWhole(), open: Flag(true), system: StubSystem(relay: older))
        began = await olderUploads.begin(credentials, id: id, sealed: sealed)
        XCTAssertFalse(began)
    }

    /// 2026-11-19, Bo's phone: every pass handed all eleven pieces of a part to iOS again, so
    /// each piece went up three to four times. A part handed over once is asked about first.
    func testAPartHandedOverOnceIsNotHandedOverWholeAgain() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(true), system: system)
        var began = await uploads.begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        XCTAssertEqual(system.handed, [[0, 1, 2]])
        // iOS sent two of the three; the middle one was lost on the way.
        system.deliver(losing: [1])
        began = await uploads.begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        XCTAssertEqual(system.handed, [[0, 1, 2], [1]], "only the piece the relay does not hold goes again")
        XCTAssertEqual(relay.progressAsked, 1)
        // The relay holds the whole part: nothing is handed over.
        system.deliver()
        began = await uploads.begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        XCTAssertEqual(system.handed.count, 2)
        XCTAssertEqual(relay.finished[id], sealed)
        // And it is said as it is: whole on the relay, not on its way.
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .whole)
        XCTAssertEqual(system.handed.count, 2)
    }

    /// 2026-11-21, Bo's phone: a part handed to iOS in one wake was stopped in the next, a
    /// quarter of an hour later, seconds after iOS had begun to send it: the same two pieces
    /// of one part were cut at 64 KiB in three wakes in a row. Minutes with iOS say nothing.
    /// A part seen at the next wake is left alone, and so is one the relay gets more of.
    func testAPartHandedOverInOneWakeIsNotStoppedInTheNext() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let whole = StubWhole()
        let clock = StubClock()
        let uploads = make(relay, whole, open: Flag(false), system: system, clock: clock.now)
        var known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed)

        // The next wake: iOS has held the pieces for a quarter of an hour and is sending them.
        clock.pass(15 * 60)
        system.isStuck = true
        known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed)
        var word = await uploads.takeHeldWord(id)
        XCTAssertEqual(word, "held-seen")
        XCTAssertTrue(system.stoppedPieces.isEmpty, "what iOS is sending is not stopped")
        XCTAssertTrue(whole.stopped.isEmpty)
        XCTAssertEqual(system.handed, [[0, 1, 2]], "and nothing is handed over again")

        // The pass comes to send the part in the same wake: it stops nothing either.
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("the part is on its way")
        } catch {
            XCTAssertTrue(error is RecordingUploadInFlight)
        }
        XCTAssertTrue(system.stoppedPieces.isEmpty)
        XCTAssertEqual(system.handed.last, [], "the pieces iOS holds are not handed over twice")

        // A wake later one piece has arrived: the part is going.
        system.deliverOnly([0])
        clock.pass(15 * 60)
        known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed)
        word = await uploads.takeHeldWord(id)
        XCTAssertEqual(word, "held-growing")
        XCTAssertTrue(system.stoppedPieces.isEmpty)
        let holds = await system.holds(id)
        XCTAssertTrue(holds)
    }

    /// 2026-11-20, Bo's phone: four parts handed to iOS at noon were still "on their way" the
    /// next morning, with 130 answered asks and no piece stored. A part of which the relay got
    /// no more in three looks, minutes apart, is started afresh, and only its missing pieces go.
    func testAHeldPartTheRelayGetsNoMoreOfIsStartedAfreshWithItsMissingPieces() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let clock = StubClock()
        let uploads = make(relay, StubWhole(), open: Flag(true), system: system, clock: clock.now)
        var known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed)
        // iOS sent the first piece and holds the other two.
        system.deliverOnly([0])
        known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed, "held for a moment only: left alone")
        var word = await uploads.takeHeldWord(id)
        XCTAssertEqual(word, "held-young")
        XCTAssertEqual(relay.progressAsked, 0, "and the relay is not asked")

        system.isStuck = true
        var words: [String?] = []
        for _ in 0..<3 {
            known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
            XCTAssertEqual(known, .handed)
            words.append(await uploads.takeHeldWord(id))
            clock.pass(15 * 60)
        }
        XCTAssertEqual(words, ["held-seen", "held-no-progress-1", "held-no-progress-2"])
        XCTAssertTrue(system.stoppedPieces.isEmpty)
        XCTAssertEqual(system.handed, [[0, 1, 2]])

        known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handedAgain)
        word = await uploads.takeHeldWord(id)
        XCTAssertEqual(word, "held-afresh-no-progress")
        XCTAssertEqual(system.stoppedPieces.sorted(), [1, 2], "what iOS held is stopped")
        XCTAssertEqual(system.handed, [[0, 1, 2], [1, 2]], "the piece the relay holds does not go again")
        system.isStuck = false
        system.deliver()
        XCTAssertEqual(relay.finished[id], sealed)
    }

    /// An open app makes a pass every few seconds. Looks that close together count as one.
    func testLooksSecondsApartDoNotStartAPartAfresh() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let clock = StubClock()
        let uploads = make(relay, StubWhole(), open: Flag(true), system: system, clock: clock.now)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        system.isStuck = true
        for _ in 0..<8 {
            let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
            XCTAssertEqual(known, .handed)
            clock.pass(3)
        }
        XCTAssertTrue(system.stoppedPieces.isEmpty)
        XCTAssertEqual(system.handed, [[0, 1, 2]])
    }

    /// In the background the pass does not wait for a part that is with iOS. On 2026-11-21 a
    /// wake was 19 to 33 seconds, and 20 of them went to waiting for the first such part.
    func testInTheBackgroundThePassDoesNotWaitForAPartOnItsWay() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(false), system: system)
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("the part is on its way")
        } catch {
            XCTAssertTrue(error is RecordingUploadInFlight)
        }
        XCTAssertEqual(system.waited, 0, "handed over, not waited for")
        XCTAssertEqual(system.handed, [[0, 1, 2]])
    }

    /// The relay is asked before anything is stopped. With no answer, what iOS holds is kept:
    /// stopping it blind could stop the try that is about to arrive.
    func testAHeldPartIsNotStoppedWhenTheRelayDoesNotAnswer() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let uploads = make(relay, StubWhole(), open: Flag(true), system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        system.isStuck = true
        relay.error = RelayClientError.timedOut
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .handed)
        XCTAssertTrue(system.stoppedPieces.isEmpty)
        XCTAssertEqual(system.handed, [[0, 1, 2]])
        let holds = await system.holds(id)
        XCTAssertTrue(holds, "iOS still holds every piece")
    }

    /// A held part the relay already has whole: what iOS holds is stopped and the part is whole.
    func testAHeldPartTheRelayHasWholeIsSaidWhole() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        let whole = StubWhole()
        let uploads = make(relay, whole, open: Flag(true), system: system)
        _ = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        _ = relay.storeSized(id, offset: 0, total: sealed.count, size: 1000, piece: sealed.subdata(in: 0..<1000))
        _ = relay.storeSized(id, offset: 1000, total: sealed.count, size: 1000, piece: sealed.subdata(in: 1000..<2000))
        _ = relay.storeSized(id, offset: 2000, total: sealed.count, size: 1000, piece: sealed.subdata(in: 2000..<2500))
        system.isStuck = true
        let known = await uploads.beginKnowing(credentials, id: id, sealed: sealed)
        XCTAssertEqual(known, .whole)
        XCTAssertEqual(whole.stopped, [id])
        XCTAssertEqual(system.handed, [[0, 1, 2]], "nothing is handed over again")
    }

    func testWhatIsHandedOverIsKeptAcrossLaunches() async throws {
        let name = "pieces-begun-" + UUID().uuidString
        let kept = try XCTUnwrap(UserDefaults(suiteName: name))
        defer { kept.removePersistentDomain(forName: name) }
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let system = StubSystem(relay: relay)
        func launch() -> RecordingPieceUploads {
            RecordingPieceUploads(relay: relay, whole: StubWhole(), background: system, appIsOpen: { true },
                                  pieceBytes: 1000, kept: kept)
        }
        var began = await launch().begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        system.deliver()
        // iOS starts the app again: the part is known, the relay is asked, nothing goes twice.
        began = await launch().begin(credentials, id: id, sealed: sealed)
        XCTAssertTrue(began)
        XCTAssertEqual(system.handed, [[0, 1, 2]])
    }

    func testInTheBackgroundARefusalIsSaidAndOlderRelaysGetTheWholePart() async throws {
        let relay = StubPieceRelay()
        relay.support = .anyOrder
        let whole = StubWhole()
        let system = StubSystem(relay: relay)
        let uploads = make(relay, whole, open: Flag(false), system: system)
        system.refusal = RelayClientError.rateLimited(retryAfter: 600)
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("must throw")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .rateLimited(retryAfter: 600))
        }

        // A relay that takes pieces only in order cannot be fed by the system piece by piece.
        let older = StubPieceRelay()
        older.support = .inOrder
        let wholeForOlder = StubWhole()
        try await make(older, wholeForOlder, open: Flag(false), system: StubSystem(relay: older))
            .putUpload(credentials, id: id, sealed: sealed)
        XCTAssertEqual(wholeForOlder.put, [id])
        XCTAssertTrue(older.pieces.isEmpty)
    }

    func testWhatIsMissingIsReadFromTheRelaysAnswer() {
        typealias Progress = RelayClient.UploadProgress
        func missing(_ p: Progress?, _ total: Int, _ size: Int) -> [Int] { RecordingPieceUploads.missing(p, total: total, size: size) }
        XCTAssertEqual(missing(nil, 2500, 1000), [0, 1, 2])
        XCTAssertEqual(missing(Progress(held: 1500, total: 2500, complete: false, size: 1000, have: "101"), 2500, 1000), [1])
        XCTAssertEqual(missing(Progress(held: 2500, total: 2500, complete: false, size: 1000, have: "111"), 2500, 1000), [])
        // Begun in order: whole pieces at the front count, a partly held one does not.
        XCTAssertEqual(missing(Progress(held: 1400, total: 2500, complete: false), 2500, 1000), [1, 2])
        // Another total, another piece size, or marks of the wrong length: all of it again.
        XCTAssertEqual(missing(Progress(held: 1000, total: 9999, complete: false), 2500, 1000), [0, 1, 2])
        XCTAssertEqual(missing(Progress(held: 1000, total: 2500, complete: false, size: 500, have: "11000"), 2500, 1000), [0, 1, 2])
        XCTAssertEqual(missing(Progress(held: 1000, total: 2500, complete: false, size: 1000, have: "10"), 2500, 1000), [0, 1, 2])
        // An exact multiple has no short last piece.
        XCTAssertEqual(missing(nil, 2000, 1000), [0, 1])
    }

    func testARefusedPhoneAndAFullRelayAreSaidAsTheyAre() async throws {
        let relay = StubPieceRelay()
        let whole = StubWhole()
        let uploads = make(relay, whole)
        for refusal in [RelayClientError.unauthorized, .rateLimited(retryAfter: 600)] {
            relay.error = refusal
            do {
                try await uploads.putUpload(credentials, id: id, sealed: sealed)
                XCTFail("must throw")
            } catch {
                XCTAssertEqual(error as? RelayClientError, refusal)
            }
        }
    }

    func testARelayThatStoresNothingEndsTheTry() async throws {
        final class Stuck: RecordingPieceRelay, @unchecked Sendable {
            var asked = 0
            func uploadPieceSupport(_ origin: URL) async -> RelayClient.UploadPieceSupport { .inOrder }
            func uploadProgress(_ credentials: RelayCredentials, id: String) async throws -> RelayClient.UploadProgress? { nil }
            func putUploadPiece(_ credentials: RelayCredentials, id: String, offset: Int, total: Int, size: Int?,
                                piece: Data) async throws -> RelayClient.UploadProgress {
                asked += 1
                return .init(held: 0, total: total, complete: false)
            }
        }
        let relay = Stuck()
        let uploads = RecordingPieceUploads(relay: relay, whole: StubWhole(), appIsOpen: { true }, pieceBytes: 1000, kept: nil)
        do {
            try await uploads.putUpload(credentials, id: id, sealed: sealed)
            XCTFail("must throw")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .unavailable)
        }
        XCTAssertLessThan(relay.asked, 20)
    }
}

/// 2026-11-18: uploads iOS held from 12:16 were still held at 12:41 and Sync now could start
/// nothing. A held upload is stopped and started afresh; each try has its own task and file, so
/// the stopped one ending late cannot be taken for the new one.
final class RecordingBackgroundUploadsTests: XCTestCase {
    func testATaskNamesItsPartAndItsOwnFile() {
        let id = "6f1c2c9e-3b1a-4c55-9f4e-2a7b8c9d0e1f"
        let named = RecordingBackgroundUploads.parse(id + RecordingBackgroundUploads.separator + id + "-A1.bin")
        XCTAssertEqual(named?.id, id)
        XCTAssertEqual(named?.file, id + "-A1.bin")
        // A task from a build before this one names only the part.
        let old = RecordingBackgroundUploads.parse(id)
        XCTAssertEqual(old?.id, id)
        XCTAssertEqual(old?.file, id + ".bin")
        // A piece names its number too.
        let piece = RecordingBackgroundUploads.parse([id, id + "-p3-B2.bin", "3"].joined(separator: RecordingBackgroundUploads.separator))
        XCTAssertEqual(piece?.id, id)
        XCTAssertEqual(piece?.piece, 3)
        XCTAssertNil(named?.piece)
        XCTAssertNil(RecordingBackgroundUploads.parse(id + "|f.bin|x"), "a name this build cannot read is left alone")
        XCTAssertNil(RecordingBackgroundUploads.parse(nil))
        XCTAssertNil(RecordingBackgroundUploads.parse(""))
        XCTAssertGreaterThanOrEqual(RecordingBackgroundUploads.stuckAfter, 60, "a healthy upload is not restarted")
        // "Too many requests" is about the minute, not the part: the other pieces go on.
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(429))
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(201))
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(0))
        XCTAssertTrue(RecordingBackgroundUploads.stopsThePart(409))
        XCTAssertTrue(RecordingBackgroundUploads.stopsThePart(413))
        // An answer about the moment stops nothing: on 2026-11-20 eight 500s on a slow link
        // each stopped a whole part, and fourteen pieces that were going were cut off.
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(500))
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(503))
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(408))
        XCTAssertTrue(RecordingBackgroundUploads.stopsThePart(507), "a full relay is not about the moment")
        XCTAssertTrue(RecordingBackgroundUploads.stopsThePart(401))
        // Such a piece goes again by itself, twice, and is then over without stopping the others.
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 500, triesSoFar: 0), .again)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 500, triesSoFar: 1), .again)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 500, triesSoFar: 2), .over)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 409, triesSoFar: 0), .stopThePart)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 507, triesSoFar: 0), .stopThePart)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 429, triesSoFar: 0), .over)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 201, triesSoFar: 0), .over)
    }

    /// 2026-11-21: one 403 that the relay did not write ended a pass as "part-relay-403". A
    /// tunnel's edge refused that request; it is about the moment. The relay's own 403 stays
    /// a refusal.
    func testA403TheRelayDidNotWriteIsAboutTheMoment() {
        XCTAssertTrue(RelayEdgeForbidden.isTheRelays(Data(#"{"ok":false,"error":"Device not allowed"}"#.utf8)))
        XCTAssertFalse(RelayEdgeForbidden.isTheRelays(Data("<html>Forbidden</html>".utf8)))
        XCTAssertFalse(RelayEdgeForbidden.isTheRelays(Data()))
        XCTAssertFalse(RelayEdgeForbidden.isTheRelays(Data(#"{"message":"forbidden"}"#.utf8)))

        XCTAssertTrue(RecordingBackgroundUploads.stopsThePart(403), "the relay's own 403 is a refusal")
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 403, triesSoFar: 0), .stopThePart)
        XCTAssertFalse(RecordingBackgroundUploads.stopsThePart(403, edge: true), "the other pieces go on")
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 403, triesSoFar: 0, edge: true), .again)
        XCTAssertEqual(RecordingBackgroundUploads.afterPiece(status: 403, triesSoFar: 2, edge: true), .over)
        XCTAssertFalse(RecordingBackgroundUploads.isOfTheMoment(401, edge: true), "only a 403 is ever the edge's")

        XCTAssertThrowsError(try RecordingBackgroundUploads.check(.init(status: 403, retryAfter: nil))) {
            XCTAssertEqual($0 as? RelayClientError, .forbidden)
        }
        XCTAssertThrowsError(try RecordingBackgroundUploads.checkPiece(.init(status: 403, retryAfter: nil, edge: true))) {
            XCTAssertTrue($0 is RelayEdgeForbidden)
        }
        XCTAssertEqual(RecordingPassRecord.word(RelayEdgeForbidden()), "edge-403")
        XCTAssertEqual(RecordingPassRecord.word(RelayClientError.forbidden), "relay-403")
    }
}

/// The relay's answers for pieces, as the client reads them.
final class RelayClientUploadPiecesTests: XCTestCase {
    private let credentials = RelayCredentials(origin: URL(string: "https://relay.example")!, bearer: "token")
    private let id = "6f1c2c9e-3b1a-4c55-9f4e-2a7b8c9d0e1f"

    private func answer(_ request: URLRequest, _ status: Int, _ text: String) -> (HTTPURLResponse, Data) {
        (HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!, Data(text.utf8))
    }

    /// 2026-11-21: the tunnel's edge refused the same pieces in every pass, by their first
    /// bytes. A relay that says it takes text gets every upload body as base64 text; one that
    /// does not, or that answers 415, gets the bytes.
    func testUploadBodiesGoAsTextWhenTheRelaySaysSo() async throws {
        let suite = "relay-upload-body-" + UUID().uuidString
        RelayUploadBody.defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer {
            RelayUploadBody.defaults.removePersistentDomain(forName: suite)
            RelayUploadBody.defaults = .standard
        }
        let client = RelayClient(session: StubURLProtocol.session())
        var status = 200
        var body = #"{"ok":true,"data":{"status":"ok","uploadPieces":true,"uploadPiecesAnyOrder":true}}"#
        StubURLProtocol.install { [self] request, _ in answer(request, status, body) }
        let piece = Data((0..<3000).map { _ in UInt8.random(in: 0...255) })
        let stored = #"{"ok":true,"data":{"id":"x","held":3000,"total":3000,"complete":true,"size":3000,"have":"1"}}"#

        // A relay that does not say so gets the bytes.
        _ = await client.uploadPieceSupport(credentials.origin)
        XCTAssertFalse(RelayUploadBody.asText(credentials.origin))
        body = stored
        _ = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 3000, size: 3000, piece: piece)
        var sent = try XCTUnwrap(StubURLProtocol.requests.last)
        XCTAssertEqual(sent.0.value(forHTTPHeaderField: "Content-Type"), "application/octet-stream")
        XCTAssertEqual(sent.1, piece)

        // One that says so gets text that stands for the same bytes, piece or whole.
        body = #"{"ok":true,"data":{"status":"ok","uploadPieces":true,"uploadPiecesAnyOrder":true,"uploadsAsText":true}}"#
        let takes = await client.uploadPieceSupport(credentials.origin)
        XCTAssertEqual(takes, .anyOrder)
        XCTAssertTrue(RelayUploadBody.asText(credentials.origin))
        XCTAssertFalse(RelayUploadBody.asText(URL(string: "https://other.example")!), "each relay speaks for itself")
        body = stored
        _ = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 3000, size: 3000, piece: piece)
        sent = try XCTUnwrap(StubURLProtocol.requests.last)
        XCTAssertEqual(sent.0.value(forHTTPHeaderField: "Content-Type"), "text/plain")
        XCTAssertEqual(sent.0.url?.query, "offset=0&total=3000&size=3000", "the numbers count the bytes, not the text")
        let text = try XCTUnwrap(sent.1)
        XCTAssertEqual(Data(base64Encoded: text), piece)
        XCTAssertTrue(text.allSatisfy { ($0 >= 0x30 && $0 <= 0x39) || ($0 >= 0x41 && $0 <= 0x5A) || ($0 >= 0x61 && $0 <= 0x7A)
            || $0 == 0x2B || $0 == 0x2F || $0 == 0x3D }, "letters, digits, +, / and = only")
        body = #"{"ok":true,"data":{"id":"x"}}"#
        status = 201
        try await client.putUpload(credentials, id: id, sealed: piece)
        sent = try XCTUnwrap(StubURLProtocol.requests.last)
        XCTAssertEqual(sent.0.value(forHTTPHeaderField: "Content-Type"), "text/plain")
        XCTAssertEqual(sent.1.flatMap { Data(base64Encoded: $0) }, piece)
        let file = RelayUploadBody.sent(piece, to: credentials.origin)
        XCTAssertEqual(file.type, "text/plain")
        XCTAssertEqual(Data(base64Encoded: file.body), piece, "what the system sends from a file is the same text")

        // No answer leaves what the relay said; an answer without the word takes it back.
        status = 502
        _ = await client.uploadPieceSupport(credentials.origin)
        XCTAssertTrue(RelayUploadBody.asText(credentials.origin))

        // An older relay took its place and answers 415: the next try goes as bytes.
        status = 415
        body = #"{"ok":false,"error":"Content-Type must be application/octet-stream"}"#
        do {
            _ = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 3000, size: 3000, piece: piece)
            XCTFail("a 415 is an error")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .unsupportedMediaType)
        }
        XCTAssertFalse(RelayUploadBody.asText(credentials.origin))
        XCTAssertEqual(RelayUploadBody.sent(piece, to: credentials.origin).body, piece)

        // The same for an upload the system sent, known by its address alone.
        RelayUploadBody.remember(credentials.origin, takesText: true)
        RelayUploadBody.refused(at: URL(string: "https://relay.example/v1/uploads/\(id)/pieces?offset=0"), status: 500)
        XCTAssertTrue(RelayUploadBody.asText(credentials.origin), "only a 415 says the relay takes no text")
        RelayUploadBody.refused(at: URL(string: "https://relay.example/v1/uploads/\(id)/pieces?offset=0"), status: 415)
        XCTAssertFalse(RelayUploadBody.asText(credentials.origin))
    }

    /// A part the edge refused once says "edge-403"; in three passes in a row or more it says
    /// so with the count, so a part that cannot pass as it is sent is seen.
    func testAPartTheEdgeRefusesPassAfterPassSaysSo() {
        XCTAssertEqual(WatchRecordingForwarder.edgeWord(passes: 1), "edge-403")
        XCTAssertEqual(WatchRecordingForwarder.edgeWord(passes: 2), "edge-403")
        XCTAssertEqual(WatchRecordingForwarder.edgeWord(passes: 3), "edge-403-in-3-passes")
        XCTAssertEqual(WatchRecordingForwarder.edgeWord(passes: 16), "edge-403-in-16-passes")
    }

    func testPieceRequestsAndAnswers() async throws {
        let client = RelayClient(session: StubURLProtocol.session())
        var status = 200
        var body = #"{"ok":true,"data":{"id":"x","held":1000,"total":2500,"complete":false}}"#
        StubURLProtocol.install { [self] request, _ in answer(request, status, body) }

        var progress = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 2500, piece: Data(count: 1000))
        XCTAssertEqual(progress, .init(held: 1000, total: 2500, complete: false))
        let sent = try XCTUnwrap(StubURLProtocol.requests.last)
        XCTAssertEqual(sent.0.httpMethod, "PUT")
        XCTAssertEqual(sent.0.url?.path, "/v1/uploads/\(id)/pieces")
        XCTAssertEqual(sent.0.url?.query, "offset=0&total=2500")
        XCTAssertEqual(sent.0.value(forHTTPHeaderField: "Content-Type"), "application/octet-stream")
        XCTAssertEqual(sent.0.value(forHTTPHeaderField: "Authorization"), "Bearer token")
        XCTAssertEqual(sent.1?.count, 1000)

        // 409 with the held size is an answer: the phone goes on from there.
        status = 409
        body = #"{"ok":false,"data":{"id":"x","held":2000,"total":2500,"complete":false},"error":"The piece does not start where the upload ends"}"#
        progress = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 2500, piece: Data(count: 1000))
        XCTAssertEqual(progress.held, 2000)
        // 409 without it means the id is not this phone's to fill.
        body = #"{"ok":false,"error":"Upload id is in use"}"#
        do {
            _ = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 2500, piece: Data(count: 1000))
            XCTFail("must throw")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .conflict)
        }
        status = 401
        do {
            _ = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 2500, piece: Data(count: 1000))
            XCTFail("must throw")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .unauthorized)
        }

        status = 201
        body = #"{"ok":true,"data":{"id":"x","held":2500,"total":2500,"complete":true}}"#
        progress = try await client.putUploadPiece(credentials, id: id, offset: 2000, total: 2500, piece: Data(count: 500))
        XCTAssertTrue(progress.complete)
    }

    func testProgressAndHealth() async throws {
        let client = RelayClient(session: StubURLProtocol.session())
        var status = 404
        var body = #"{"ok":false,"error":"Upload not found"}"#
        StubURLProtocol.install { [self] request, _ in answer(request, status, body) }
        let unknown = try await client.uploadProgress(credentials, id: id)
        XCTAssertNil(unknown)
        XCTAssertEqual(StubURLProtocol.requests.last?.0.url?.path, "/v1/uploads/\(id)/pieces")
        XCTAssertEqual(StubURLProtocol.requests.last?.0.httpMethod, "GET")
        // A 403 the relay wrote is its refusal; one it did not write is the edge's.
        status = 403
        body = #"{"ok":false,"error":"Device not allowed"}"#
        do {
            _ = try await client.uploadProgress(credentials, id: id)
            XCTFail("must throw")
        } catch {
            XCTAssertEqual(error as? RelayClientError, .forbidden)
        }
        body = "<html>Forbidden</html>"
        do {
            _ = try await client.uploadProgress(credentials, id: id)
            XCTFail("must throw")
        } catch {
            XCTAssertTrue(error is RelayEdgeForbidden)
        }
        do {
            _ = try await client.putUploadPiece(credentials, id: id, offset: 0, total: 2500, size: 1000, piece: Data(count: 1000))
            XCTFail("must throw")
        } catch {
            XCTAssertTrue(error is RelayEdgeForbidden)
        }
        do {
            try await client.putUpload(credentials, id: id, sealed: Data(count: 10))
            XCTFail("must throw")
        } catch {
            XCTAssertTrue(error is RelayEdgeForbidden)
        }
        status = 200
        body = #"{"ok":true,"data":{"id":"x","held":512,"total":2500,"complete":false}}"#
        let held = try await client.uploadProgress(credentials, id: id)
        XCTAssertEqual(held?.held, 512)

        body = #"{"ok":true,"data":{"protocolVersion":1,"service":"isoform-relay","status":"ok","uploadPieces":true}}"#
        let takes = await client.uploadPieceSupport(credentials.origin)
        XCTAssertEqual(takes, .inOrder)
        XCTAssertNil(StubURLProtocol.requests.last?.0.value(forHTTPHeaderField: "Authorization"), "health needs no credential")
        body = #"{"ok":true,"data":{"protocolVersion":1,"service":"isoform-relay","status":"ok"}}"#
        let older = await client.uploadPieceSupport(credentials.origin)
        XCTAssertEqual(older, RelayClient.UploadPieceSupport.none)
        body = #"{"ok":true,"data":{"protocolVersion":1,"service":"isoform-relay","status":"ok","uploadPieces":true,"uploadPiecesAnyOrder":true}}"#
        let any = await client.uploadPieceSupport(credentials.origin)
        XCTAssertEqual(any, .anyOrder)
        status = 502
        let down = await client.uploadPieceSupport(credentials.origin)
        XCTAssertEqual(down, RelayClient.UploadPieceSupport.none)

        // A piece of an any-order upload names its size, and the answer carries the marks.
        status = 200
        body = #"{"ok":true,"data":{"id":"x","held":1500,"total":2500,"complete":false,"size":1000,"have":"101"}}"#
        let sized = try await client.putUploadPiece(credentials, id: id, offset: 2000, total: 2500, size: 1000, piece: Data(count: 500))
        XCTAssertEqual(sized.have, "101")
        XCTAssertEqual(sized.size, 1000)
        XCTAssertEqual(StubURLProtocol.requests.last?.0.url?.query, "offset=2000&total=2500&size=1000")
    }
}
