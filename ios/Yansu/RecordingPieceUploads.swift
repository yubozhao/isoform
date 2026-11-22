import Foundation
import UIKit

// A recording part goes to the relay in pieces.
//
// On 2026-11-18 the public address of a relay cut every request after 10 to 25 seconds. A part is
// about 3 MB in one request, so none arrived, and each try started again at zero: two days of
// recordings stayed on the phone. Sent as pieces of 256 KiB, a cut costs the piece in flight
// (PROTOCOL.md 24.1a, docs/design/watch-parts-reach-the-computer.md).
//
// The bytes are the same sealed bytes under the same upload id as the whole-body upload, so the
// ways can take turns on one part:
//
// - App open: the app sends the missing pieces itself, one after the other, and asks the relay
//   first what it holds.
// - App in the background, and a relay that takes pieces in any order: every missing piece is
//   handed to iOS as its own background upload. iOS sends them while the app is closed, in the
//   order it likes, and retries each by itself. The relay is asked again what it holds before
//   the part counts as sent.
// - A relay that takes no pieces (or none in any order, with the app in the background): one
//   background upload of the whole part, as before (RecordingBackgroundUploads).

/// The relay calls pieces need; RelayClient in the app, a stub in tests.
protocol RecordingPieceRelay: Sendable {
    func uploadPieceSupport(_ origin: URL) async -> RelayClient.UploadPieceSupport
    func uploadProgress(_ credentials: RelayCredentials, id: String) async throws -> RelayClient.UploadProgress?
    func uploadProgress(_ credentials: RelayCredentials, id: String, timeout: TimeInterval) async throws -> RelayClient.UploadProgress?
    func putUploadPiece(_ credentials: RelayCredentials, id: String, offset: Int, total: Int, size: Int?,
                        piece: Data) async throws -> RelayClient.UploadProgress
    /// This phone's uploads on the relay; nil when the relay cannot say (24.1c).
    func uploadsOnRelay(_ credentials: RelayCredentials) async -> [RelayClient.UploadOnRelay]?
}

extension RecordingPieceRelay {
    func uploadsOnRelay(_ credentials: RelayCredentials) async -> [RelayClient.UploadOnRelay]? { nil }
    func uploadProgress(_ credentials: RelayCredentials, id: String, timeout: TimeInterval) async throws -> RelayClient.UploadProgress? {
        try await uploadProgress(credentials, id: id)
    }
}

extension RelayClient: RecordingPieceRelay {}

/// Hands pieces to the system; RecordingBackgroundUploads in the app, a stub in tests. It
/// returns when every piece handed over has ended well, and throws `RecordingUploadInFlight`
/// while some are still with the system.
protocol RecordingBackgroundPieces: Sendable {
    func putPieces(_ credentials: RelayCredentials, id: String, sealed: Data, size: Int, missing: [Int]) async throws
    /// Hands the pieces over and returns at once, without waiting for any of them.
    func handOver(_ credentials: RelayCredentials, id: String, sealed: Data, size: Int, missing: [Int]) async throws
    /// Whether the system holds anything of this part.
    func holds(_ id: String) async -> Bool
    /// Whether the system has held this part for minutes: the relay is then asked what it holds.
    func stuck(_ id: String) async -> Bool
}

extension RecordingBackgroundPieces {
    func stuck(_ id: String) async -> Bool { false }
}

/// A transport that can take a part before the computer was asked anything: the upload needs
/// only the relay (WatchRecordingForwarder hands waiting parts ahead on every wake).
protocol RecordingUploadAhead: Sendable {
    /// Leaves the part with the system if this relay and this path allow it. True when the
    /// system now holds it.
    func begin(_ credentials: RelayCredentials, id: String, sealed: Data) async -> Bool
    /// The same, saying also when nothing was left to hand over because the relay holds the
    /// part whole.
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data) async -> RecordingBegun
    /// The same for a computer that allows the send in a wake: with the app in the background
    /// the app sends the pieces itself while it is awake.
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool) async -> RecordingBegun
    /// The same for a part the computer was told of before. `taken` says whether the computer
    /// has the part by now; it is asked only when the relay holds nothing of a part that went
    /// up before, which is how a part looks once the computer has taken it.
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool,
                      taken: (@Sendable () async -> Bool)?) async -> RecordingBegun
    /// Why the part the system holds was left as it is in the last hand-over, as a short word;
    /// nil when it was not, or when that was read already.
    func takeHeldWord(_ id: String) async -> String?
    /// The ids of this phone's uploads the relay holds something of, whole or not. Nil when
    /// the relay cannot say. A pass asks once, to finish what is begun before it begins more.
    func onRelay(_ credentials: RelayCredentials) async -> Set<String>?
}

extension RecordingUploadAhead {
    func onRelay(_ credentials: RelayCredentials) async -> Set<String>? { nil }
    func takeHeldWord(_ id: String) async -> String? { nil }
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool,
                      taken: (@Sendable () async -> Bool)?) async -> RecordingBegun {
        await beginKnowing(credentials, id: id, sealed: sealed, inWake: inWake)
    }
    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool) async -> RecordingBegun {
        await beginKnowing(credentials, id: id, sealed: sealed)
    }
}

/// How leaving a part with the system ended.
enum RecordingBegun: Equatable, Sendable {
    /// Not handed over: this relay or path does not allow it, or the relay did not answer.
    case no
    /// The system holds pieces of it.
    case handed
    /// The relay got no more of it while the system held it: what the system held was stopped
    /// and the pieces the relay does not hold were handed over again.
    case handedAgain
    /// The relay holds the part whole: nothing is on its way and only the computer is left to tell.
    case whole
    /// The app sent the missing pieces itself in this wake, and the relay now holds the part whole.
    case sentInWake
}

actor RecordingPieceUploads: RecordingUploadTransport, RecordingUploadAhead {
    /// The size of one piece. A guess until a real transfer on a phone network is measured.
    static let pieceBytes = 256 * 1024
    /// How long "this relay takes no pieces" is believed before it is asked again.
    static let askAgainSeconds: TimeInterval = 10 * 60

    private let relay: RecordingPieceRelay
    private let whole: RecordingUploadTransport
    private let background: RecordingBackgroundPieces?
    private let appIsOpen: @Sendable () async -> Bool
    private let stopWhole: @Sendable (String) async -> Void
    private let now: @Sendable () -> Date
    private let pieceBytes: Int
    /// How long the system still lets the app run in the background.
    private let timeLeft: @Sendable () async -> TimeInterval
    /// The parts sent in a wake go one after the other, oldest first: the one before this one.
    private var inWakeTurn: Task<Void, Never>?
    /// A piece is started in a wake only with this much time left, and stopped when this little is.
    static let inWakeStartsWith: TimeInterval = 8
    static let inWakeStopsAt: TimeInterval = 3
    /// What each relay said, and when the ones that take no pieces in any order were asked.
    private var support: [URL: RelayClient.UploadPieceSupport] = [:]
    private var askedAt: [URL: Date] = [:]
    /// Parts whose pieces were all handed to iOS once already, newest last. Kept across launches:
    /// iOS starts the app again for seconds at a time, and each start would hand them all again.
    private var begun: [String]
    private let kept: UserDefaults?
    static let begunKey = "recordings.pieces.begun"
    static let begunKept = 400
    /// Why a part the system holds was left as it is, by upload id, until the pass reads it.
    private var heldWords: [String: String] = [:]
    /// How often in a row the relay did not say what it holds of a part the system holds, by
    /// upload id. Kept across launches and counted anew in each version of the app.
    private var heldAskFailures: [String: Int]
    static let heldAskFailuresKey = "recordings.pieces.heldAskFailures"
    static let heldAskBuildKey = "recordings.pieces.heldAskFailures.build"
    /// After this many the part is started afresh without the relay's word.
    static let heldAsksBeforeAFreshStart = 3
    /// How long the relay is given to say what it holds of a part the system holds. On
    /// 2026-11-20 the relay answered at once and the phone did not have the answer in time.
    static let heldAskSeconds: TimeInterval = 30
    /// What the relay held of a part the system holds when it was last looked at, by upload id:
    /// the pieces, the looks in a row that found no more, and when. Kept across launches.
    private var heldLooks: [String: [Double]]
    static let heldLooksKey = "recordings.pieces.heldLooks"
    static let heldLooksKept = 400
    /// A look counts only this long after the one before, and after this many in a row that
    /// found no more pieces on the relay the part is started afresh.
    static let lookEvery: TimeInterval = 5 * 60
    static let looksBeforeAFreshStart = 3

    init(relay: RecordingPieceRelay, whole: RecordingUploadTransport, background: RecordingBackgroundPieces? = nil,
         appIsOpen: @escaping @Sendable () async -> Bool = { await MainActor.run { UIApplication.shared.applicationState != .background } },
         stopWhole: @escaping @Sendable (String) async -> Void = { _ in },
         pieceBytes: Int = RecordingPieceUploads.pieceBytes,
         now: @escaping @Sendable () -> Date = { Date() },
         timeLeft: @escaping @Sendable () async -> TimeInterval = { await MainActor.run { UIApplication.shared.backgroundTimeRemaining } },
         build: String = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "",
         kept: UserDefaults? = .standard) {
        if kept?.string(forKey: Self.heldAskBuildKey) == build {
            self.heldAskFailures = (kept?.dictionary(forKey: Self.heldAskFailuresKey) as? [String: Int]) ?? [:]
        } else {
            self.heldAskFailures = [:]
            kept?.set(build, forKey: Self.heldAskBuildKey)
            kept?.removeObject(forKey: Self.heldAskFailuresKey)
        }
        self.heldLooks = (kept?.dictionary(forKey: Self.heldLooksKey) as? [String: [Double]]) ?? [:]
        self.kept = kept
        self.timeLeft = timeLeft
        self.begun = kept?.stringArray(forKey: Self.begunKey) ?? []
        self.relay = relay
        self.whole = whole
        self.background = background
        self.appIsOpen = appIsOpen
        self.stopWhole = stopWhole
        self.pieceBytes = max(1, pieceBytes)
        self.now = now
    }

    func putUpload(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        guard !sealed.isEmpty else { return try await whole.putUpload(credentials, id: id, sealed: sealed) }
        let open = await appIsOpen()
        let inside = Self.isInsideTheApp(credentials.origin)
        // The direct path is a door inside this app. Nothing for it is ever handed to iOS: iOS
        // cannot reach it, and an upload it holds for that address never ends. On 2026-11-18 at
        // 19:40 the direct path did not answer, four whole parts were handed to iOS for it, and
        // nothing reached either relay. Anything iOS still holds for this part is stopped first,
        // so a part stuck that way is freed.
        if inside {
            guard open else { throw RelayClientError.offline }
            await stopWhole(id)
        }
        let takes = await pieceSupport(credentials.origin)
        switch (takes, open) {
        case (.none, _):
            // A direct path that does not say what it takes is a path that is down: the part
            // goes over the other one.
            if inside { throw RelayClientError.offline }
            try await whole.putUpload(credentials, id: id, sealed: sealed)
        case (.inOrder, true):
            // An upload iOS still holds for this part would put the same bytes a second time.
            if !inside { await stopWhole(id) }
            try await sendInOrder(credentials, id: id, sealed: sealed)
        case (.anyOrder, true):
            // To a relay iOS can reach, the pieces are iOS's also while the app is open: they
            // start at once and go on when the app leaves. On 2026-11-18 the app was open for 25
            // seconds, sent nothing itself in that time, and had handed nothing over. The app
            // sends pieces itself only over the direct path, which is a door inside the app.
            if let background, !Self.isInsideTheApp(credentials.origin) {
                try await handToSystem(background, credentials, id: id, sealed: sealed, open: true)
            } else {
                if !inside { await stopWhole(id) }
                try await sendMissing(credentials, id: id, sealed: sealed)
            }
        case (.anyOrder, false):
            guard let background else { return try await whole.putUpload(credentials, id: id, sealed: sealed) }
            try await handToSystem(background, credentials, id: id, sealed: sealed, open: false)
        case (.inOrder, false):
            // In order needs the app for every next piece: the system carries the whole part.
            try await whole.putUpload(credentials, id: id, sealed: sealed)
        }
    }

    /// Leaves a part with the system before anything else is asked. The first time, every piece
    /// is handed over with no question to the relay. A part handed over before is asked about
    /// first, and only the pieces the relay does not hold go again. On 2026-11-19 every pass
    /// handed all eleven pieces of a part again, so each piece went up three to four times
    /// through a tunnel that was already dropping them.
    func begin(_ credentials: RelayCredentials, id: String, sealed: Data) async -> Bool {
        await beginKnowing(credentials, id: id, sealed: sealed) != .no
    }

    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data) async -> RecordingBegun {
        await beginKnowing(credentials, id: id, sealed: sealed, inWake: false)
    }

    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool) async -> RecordingBegun {
        await beginKnowing(credentials, id: id, sealed: sealed, inWake: inWake, taken: nil)
    }

    func beginKnowing(_ credentials: RelayCredentials, id: String, sealed: Data, inWake: Bool,
                      taken: (@Sendable () async -> Bool)?) async -> RecordingBegun {
        guard let background, !sealed.isEmpty, !Self.isInsideTheApp(credentials.origin) else { return .no }
        // iOS treats an upload handed over while the app is in the background as one that can
        // wait, whatever the session says. On 2026-11-20 pieces reached the relay only in the
        // minutes the app was awake: 2 in two wakes, none in the 11 minutes between or after.
        // So in a wake the app sends the pieces itself, for as long as it is let run.
        var wake = false
        if inWake { wake = !(await appIsOpen()) }
        if await background.holds(id) {
            // Held for a moment only: it is on its way.
            guard await background.stuck(id) else {
                heldWords[id] = "held-young"
                return .handed
            }
            return await lookAtHeld(background, credentials, id: id, sealed: sealed, wake: wake, taken: taken)
        }
        guard await pieceSupport(credentials.origin) == .anyOrder else { return .no }
        if wake, let sent = await inTurn(background, credentials, id: id, sealed: sealed, held: false, taken: taken) { return sent }
        var missing = Self.missing(nil, total: sealed.count, size: pieceBytes)
        if begun.contains(id) {
            // No answer: nothing is handed over blind a second time; the pass sends this part.
            let progress: RelayClient.UploadProgress?
            do { progress = try await relay.uploadProgress(credentials, id: id) } catch { return .no }
            if progress?.complete == true { return .whole }
            // Nothing held under this id any more. The computer may have taken the part.
            if progress == nil, let taken, await taken() { return .no }
            // Every piece goes again.
            missing = Self.missing(progress, total: sealed.count, size: pieceBytes)
        }
        do {
            try await background.handOver(credentials, id: id, sealed: sealed, size: pieceBytes, missing: missing)
            remember(id)
            return .handed
        } catch {
            return .no
        }
    }

    /// A part iOS has held for minutes. Minutes alone say nothing: iOS sends what it holds
    /// mostly while the app is awake, and the app is awake for half a minute every quarter of
    /// an hour. On 2026-11-21 every part still with iOS was stopped and handed over again at
    /// every wake, seconds after iOS had started sending it: the same two pieces of one part
    /// were cut at 64 KiB in three wakes in a row, and four parts from the day before had 130
    /// answered asks and not one piece stored.
    ///
    /// So the relay is asked what it holds, and the part is left alone while that grows. Only
    /// after `looksBeforeAFreshStart` looks in a row that found no more, each `lookEvery` after
    /// the one before, is what iOS holds stopped and the missing pieces handed over again. No
    /// answer: what iOS holds is left as it is, and after three asks without one the part is
    /// started afresh whole. Every way out leaves a word for the pass.
    /// Only iOS's uploads and their own piece copies are stopped; the part's file and its
    /// place among the waiting parts are not touched.
    private func lookAtHeld(_ background: RecordingBackgroundPieces, _ credentials: RelayCredentials,
                            id: String, sealed: Data, wake: Bool,
                            taken: (@Sendable () async -> Bool)?) async -> RecordingBegun {
        let progress: RelayClient.UploadProgress?
        do {
            progress = try await relay.uploadProgress(credentials, id: id, timeout: Self.heldAskSeconds)
            askAnswered(id)
        } catch {
            // On 2026-11-20 four parts stayed this way from noon into the night: the relay
            // answered every ask and the phone had none of the answers, so what iOS held was
            // never stopped. After three asks without an answer the part is started afresh
            // from its file, every piece: the relay only gets bytes it may hold already.
            guard askFailed(id) >= Self.heldAsksBeforeAFreshStart else {
                heldWords[id] = "held-ask-" + RecordingPassRecord.word(error)
                return .handed
            }
            return await afresh(background, credentials, id: id, sealed: sealed,
                                missing: Self.missing(nil, total: sealed.count, size: pieceBytes), word: "held-afresh-unasked")
        }
        if progress?.complete == true {
            // The relay has it all: what iOS still holds would only put the same bytes again.
            await stopWhole(id)
            forgetLooks(id)
            heldWords[id] = "held-whole"
            return .whole
        }
        let missing = Self.missing(progress, total: sealed.count, size: pieceBytes)
        guard !missing.isEmpty else {
            heldWords[id] = "held-nothing-missing"
            return .handed
        }
        let pieces = (sealed.count + pieceBytes - 1) / pieceBytes
        switch look(id, have: pieces - missing.count) {
        case .first:
            heldWords[id] = "held-seen"
            return .handed
        case .more:
            heldWords[id] = "held-growing"
            return .handed
        case .noMore(let looks):
            heldWords[id] = "held-no-progress-\(looks)"
            return .handed
        case .due:
            // In a wake the app sends the missing pieces itself.
            if wake, let sent = await inTurn(background, credentials, id: id, sealed: sealed, held: true, taken: taken) {
                forgetLooks(id)
                heldWords[id] = "held-afresh-in-wake"
                return sent
            }
            return await afresh(background, credentials, id: id, sealed: sealed, missing: missing, word: "held-afresh-no-progress")
        }
    }

    /// Stops what iOS holds of a part and hands these pieces over again.
    private func afresh(_ background: RecordingBackgroundPieces, _ credentials: RelayCredentials,
                        id: String, sealed: Data, missing: [Int], word: String) async -> RecordingBegun {
        await stopWhole(id)
        forgetLooks(id)
        do {
            try await background.handOver(credentials, id: id, sealed: sealed, size: pieceBytes, missing: missing)
            askAnswered(id)
            remember(id)
            heldWords[id] = word
            return .handedAgain
        } catch {
            heldWords[id] = "held-hand-over-" + RecordingPassRecord.word(error)
            return .no
        }
    }

    private enum Look {
        /// Not looked at before: this is what the next look is held against.
        case first
        /// The relay holds more than at the last look.
        case more
        /// No more than at the last look, for this many looks in a row.
        case noMore(Int)
        /// No more for so many looks that the part is started afresh.
        case due
    }

    private func look(_ id: String, have: Int) -> Look {
        let at = now().timeIntervalSince1970
        guard let last = heldLooks[id], last.count == 3 else {
            keepLook(id, [Double(have), 0, at])
            return .first
        }
        if Double(have) > last[0] {
            keepLook(id, [Double(have), 0, at])
            return .more
        }
        // An open app looks every few seconds: a look counts only some minutes after the last.
        guard at - last[2] >= Self.lookEvery else { return .noMore(Int(last[1])) }
        let looks = Int(last[1]) + 1
        guard looks < Self.looksBeforeAFreshStart else { return .due }
        keepLook(id, [last[0], Double(looks), at])
        return .noMore(looks)
    }

    private func keepLook(_ id: String, _ look: [Double]) {
        heldLooks[id] = look
        if heldLooks.count > Self.heldLooksKept, let oldest = heldLooks.min(by: { ($0.value.last ?? 0) < ($1.value.last ?? 0) }) {
            heldLooks[oldest.key] = nil
        }
        kept?.set(heldLooks, forKey: Self.heldLooksKey)
    }

    private func forgetLooks(_ id: String) {
        guard heldLooks.removeValue(forKey: id) != nil else { return }
        kept?.set(heldLooks, forKey: Self.heldLooksKey)
    }

    func takeHeldWord(_ id: String) async -> String? { heldWords.removeValue(forKey: id) }

    func onRelay(_ credentials: RelayCredentials) async -> Set<String>? {
        guard !Self.isInsideTheApp(credentials.origin) else { return nil }
        return await relay.uploadsOnRelay(credentials).map { Set($0.map(\.id)) }
    }

    private func askFailed(_ id: String) -> Int {
        let count = (heldAskFailures[id] ?? 0) + 1
        heldAskFailures[id] = count
        kept?.set(heldAskFailures, forKey: Self.heldAskFailuresKey)
        return count
    }

    private func askAnswered(_ id: String) {
        guard heldAskFailures.removeValue(forKey: id) != nil else { return }
        kept?.set(heldAskFailures, forKey: Self.heldAskFailuresKey)
    }

    /// Sends a part in a wake when the parts before it are done: one part at a time, in the
    /// order they were asked for, which is oldest first.
    private func inTurn(_ background: RecordingBackgroundPieces, _ credentials: RelayCredentials,
                        id: String, sealed: Data, held: Bool,
                        taken: (@Sendable () async -> Bool)?) async -> RecordingBegun? {
        let before = inWakeTurn
        let mine = Task { () -> RecordingBegun? in
            await before?.value
            return await sendInWake(background, credentials, id: id, sealed: sealed, held: held, taken: taken)
        }
        inWakeTurn = Task { _ = await mine.value }
        return await mine.value
    }

    /// The app sends the missing pieces of one part itself while it is awake in the background.
    /// Nil when nothing was done and the part goes the way it went before: too little time is
    /// left, or the relay did not say what it holds (what iOS holds is then left as it is).
    /// A piece is started only with time left for it and stopped before the time is over; the
    /// relay keeps a piece whole or not at all. What could not go in this wake is handed to iOS.
    /// Only iOS's uploads and their piece copies are ever stopped here, never the part's file
    /// or its place among the waiting parts.
    private func sendInWake(_ background: RecordingBackgroundPieces, _ credentials: RelayCredentials,
                            id: String, sealed: Data, held: Bool,
                            taken: (@Sendable () async -> Bool)?) async -> RecordingBegun? {
        guard await timeLeft() >= Self.inWakeStartsWith else { return nil }
        let progress: RelayClient.UploadProgress?
        do { progress = try await relay.uploadProgress(credentials, id: id) } catch { return nil }
        // The relay holds nothing of a part that went up before: the computer may have taken
        // it, and its word is waited for before the whole part goes up a second time.
        if progress == nil, begun.contains(id), let taken, await taken() {
            if held { await stopWhole(id) }
            return .no
        }
        // What iOS has held for minutes would put the same bytes again.
        if held { await stopWhole(id) }
        if progress?.complete == true { return .whole }
        var left = Self.missing(progress, total: sealed.count, size: pieceBytes)
        for index in left {
            let time = await timeLeft()
            guard time >= Self.inWakeStartsWith else { break }
            guard let answer = await putInTime(credentials, id: id, index: index, sealed: sealed,
                                               seconds: time - Self.inWakeStopsAt) else { break }
            left.removeAll { $0 == index }
            if answer.complete {
                remember(id)
                return .sentInWake
            }
        }
        if left.isEmpty, (try? await relay.uploadProgress(credentials, id: id))?.complete == true {
            remember(id)
            return .sentInWake
        }
        // Every piece was answered and the relay does not say the part is whole: nothing is
        // handed over blind; the pass asks again when it comes to this part.
        guard !left.isEmpty else { return .no }
        do {
            try await background.handOver(credentials, id: id, sealed: sealed, size: pieceBytes, missing: left)
            remember(id)
            return held ? .handedAgain : .handed
        } catch {
            return .no
        }
    }

    /// One piece, given up when the time is over. Nil when it did not arrive.
    private func putInTime(_ credentials: RelayCredentials, id: String, index: Int, sealed: Data,
                           seconds: TimeInterval) async -> RelayClient.UploadProgress? {
        let relay = self.relay, total = sealed.count, size = pieceBytes, bytes = piece(index, of: sealed)
        return await withTaskGroup(of: RelayClient.UploadProgress?.self) { group in
            group.addTask {
                try? await relay.putUploadPiece(credentials, id: id, offset: index * size, total: total, size: size, piece: bytes)
            }
            group.addTask {
                try? await Task.sleep(nanoseconds: UInt64(max(0, seconds) * 1_000_000_000))
                return nil
            }
            let first = await group.next() ?? nil
            group.cancelAll()
            return first
        }
    }

    private func remember(_ id: String) {
        guard !begun.contains(id) else { return }
        begun.append(id)
        if begun.count > Self.begunKept { begun.removeFirst(begun.count - Self.begunKept) }
        kept?.set(begun, forKey: Self.begunKey)
    }

    /// The direct path's address: the listener this app opens on the phone itself.
    static func isInsideTheApp(_ origin: URL) -> Bool {
        guard let host = origin.host?.lowercased() else { return false }
        return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
    }

    /// The pieces the relay does not hold yet, by number. Nothing known, or known under another
    /// size, means all of them.
    static func missing(_ progress: RelayClient.UploadProgress?, total: Int, size: Int) -> [Int] {
        let count = (total + size - 1) / size
        guard let progress, progress.total == total else { return Array(0..<count) }
        if let have = progress.have {
            guard progress.size == size, have.count == count else { return Array(0..<count) }
            return have.enumerated().filter { $0.element != "1" }.map(\.offset)
        }
        // Begun in order: the whole pieces at the front are held.
        let held = min(max(progress.held, 0), total) / size
        return Array(min(held, count)..<count)
    }

    private func piece(_ index: Int, of sealed: Data) -> Data {
        let start = index * pieceBytes
        let end = min(start + pieceBytes, sealed.count)
        return sealed.subdata(in: (sealed.startIndex + start)..<(sealed.startIndex + end))
    }

    /// App open, any order: asks what is missing and sends exactly that.
    private func sendMissing(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        let total = sealed.count
        // Two rounds: the second sends what a first answer said was still missing.
        for _ in 0..<2 {
            let progress = try await relay.uploadProgress(credentials, id: id)
            if progress?.complete == true { return }
            for index in Self.missing(progress, total: total, size: pieceBytes) {
                try Task.checkCancellation()
                let answer = try await relay.putUploadPiece(credentials, id: id, offset: index * pieceBytes, total: total,
                                                            size: pieceBytes, piece: piece(index, of: sealed))
                if answer.complete { return }
            }
        }
        if try await relay.uploadProgress(credentials, id: id)?.complete == true { return }
        // The relay took every piece and still holds no whole upload: it is not storing them.
        throw RelayClientError.unavailable
    }

    /// The missing pieces go to iOS, which sends them by itself. Pieces iOS already holds are
    /// left as they are: only `lookAtHeld` decides that a part is started afresh.
    ///
    /// In the background nothing is waited for. On 2026-11-21 a wake was 19 to 33 seconds and
    /// 20 of them went to waiting for the first part that was on its way, so the parts behind
    /// it got nothing of the wake.
    private func handToSystem(_ background: RecordingBackgroundPieces, _ credentials: RelayCredentials,
                              id: String, sealed: Data, open: Bool) async throws {
        let progress = try await relay.uploadProgress(credentials, id: id)
        if progress?.complete == true { return }
        let missing = Self.missing(progress, total: sealed.count, size: pieceBytes)
        if open {
            try await background.putPieces(credentials, id: id, sealed: sealed, size: pieceBytes, missing: missing)
        } else {
            try await background.handOver(credentials, id: id, sealed: sealed, size: pieceBytes, missing: missing)
            if await background.holds(id) { throw RecordingUploadInFlight() }
        }
        // Every piece handed over has ended. Only the relay's word makes the part sent.
        if try await relay.uploadProgress(credentials, id: id)?.complete == true { return }
        throw RecordingUploadInFlight()
    }

    /// App open, a relay that takes pieces only in order.
    private func sendInOrder(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        let total = sealed.count
        var held = 0
        if let progress = try await relay.uploadProgress(credentials, id: id) {
            if progress.complete { return }
            // Held under another size: the relay starts the id again at the first piece.
            held = progress.total == total ? min(max(progress.held, 0), total) : 0
        }
        // Every answer either moves forward or says where the relay is. A relay that does
        // neither for a whole part's worth of tries is not storing anything.
        var triesLeft = (total / pieceBytes + 1) * 2 + 4
        while held < total {
            try Task.checkCancellation()
            guard triesLeft > 0 else { throw RelayClientError.unavailable }
            triesLeft -= 1
            let end = min(held + pieceBytes, total)
            let piece = sealed.subdata(in: (sealed.startIndex + held)..<(sealed.startIndex + end))
            let answer = try await relay.putUploadPiece(credentials, id: id, offset: held, total: total, size: nil, piece: piece)
            if answer.complete { return }
            held = min(max(answer.held, 0), total)
        }
    }

    private func pieceSupport(_ origin: URL) async -> RelayClient.UploadPieceSupport {
        // A relay only gains abilities: the best answer is kept, a lesser one is asked again later.
        // One that does not take bodies as text yet is asked again later too: it may be replaced
        // by one that does while the app runs (PROTOCOL.md 24.1b).
        if support[origin] == .anyOrder, RelayUploadBody.asText(origin) { return .anyOrder }
        if let known = support[origin], let asked = askedAt[origin], now().timeIntervalSince(asked) < Self.askAgainSeconds {
            return known
        }
        let answer = await relay.uploadPieceSupport(origin)
        askedAt[origin] = now()
        // No answer does not take away what the relay said before.
        if answer == .none, let known = support[origin], known != .none { return known }
        support[origin] = answer
        return answer
    }
}
