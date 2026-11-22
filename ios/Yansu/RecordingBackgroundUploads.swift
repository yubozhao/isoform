import Foundation
import os

// A recording part goes to the relay as a background upload.
//
// Until 2026-11-17 the phone sent a part (about 3 MB) with an ordinary request. iOS stops such a
// request when the app leaves the screen, so on a slow link most uploads were cut in the middle:
// on 2026-11-16 five of six were, and a day of recordings stayed on the phone.
//
// A background upload belongs to iOS. It goes on while the app is suspended or closed, waits for
// a connection, and tries again by itself. The app only hands over a file and later reads how it
// ended. A part keeps the same upload id and the same bytes for every try (the forwarder sees to
// that), so a second hand-over of a part that already arrived is answered 409 by the relay and
// counts as arrived.

/// The upload was handed to iOS and has not ended yet. The part is asked about again on a later pass.
struct RecordingUploadInFlight: Error, Equatable {}

final class RecordingBackgroundUploads: NSObject, RecordingUploadTransport, RecordingBackgroundPieces, URLSessionDataDelegate, @unchecked Sendable {
    static let shared = RecordingBackgroundUploads()
    static let sessionIdentifier = (Bundle.main.bundleIdentifier ?? "isoform") + ".recording-uploads"

    /// How long a pass waits for one upload before it moves on. The upload itself goes on.
    static let patience: TimeInterval = 20

    /// How an upload ended, kept until the forwarder has read it.
    struct Ended: Codable, Equatable {
        /// The relay's status, or 0 when no answer came.
        var status: Int
        var retryAfter: TimeInterval?
        /// A 403 the relay did not write (`RelayEdgeForbidden`): about the moment, not the part.
        var edge: Bool?
    }

    /// Called when any upload ended, so the forwarder makes a pass.
    var onEnded: (@Sendable () -> Void)?

    private let lock = NSLock()
    private let directory: URL
    private var ended: [String: Ended]
    private var inFlight: Set<String> = []
    /// The pieces of a part that iOS holds, by number.
    private var piecesInFlight: [String: Set<Int>] = [:]
    /// Tasks stopped by the app; how these end is not kept.
    private var stopped: Set<Int> = []
    /// When the upload of a part was handed to iOS, kept across launches.
    private var started: [String: TimeInterval] = [:]
    private var waiters: [String: [CheckedContinuation<Void, Never>]] = [:]
    private var bodies: [Int: Data] = [:]
    private var systemCompletion: (() -> Void)?
    private var loaded = false
    private var session: URLSession!

    private var endedURL: URL { directory.appendingPathComponent("ended.json") }

    override init() {
        let base = (try? FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask,
                                                 appropriateFor: nil, create: true))
            ?? FileManager.default.temporaryDirectory
        directory = base.appendingPathComponent("recording-uploads", isDirectory: true)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        ended = (try? JSONDecoder().decode([String: Ended].self,
                                           from: Data(contentsOf: directory.appendingPathComponent("ended.json")))) ?? [:]
        started = (try? JSONDecoder().decode([String: TimeInterval].self,
                                             from: Data(contentsOf: directory.appendingPathComponent("started.json")))) ?? [:]
        super.init()
        let configuration = URLSessionConfiguration.background(withIdentifier: Self.sessionIdentifier)
        configuration.sessionSendsLaunchEvents = true
        configuration.isDiscretionary = false
        configuration.allowsCellularAccess = true
        configuration.httpCookieStorage = nil
        configuration.httpShouldSetCookies = false
        configuration.urlCache = nil
        // iOS keeps trying for this long; after that the part is handed over again.
        configuration.timeoutIntervalForResource = 12 * 60 * 60
        session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
    }

    /// iOS started the app to tell it about uploads that ended while it was closed.
    func systemWoke(completion: @escaping () -> Void) {
        lock.withLock { systemCompletion = completion }
    }

    func putUpload(_ credentials: RelayCredentials, id: String, sealed: Data) async throws {
        await loadTasks()
        if let result = take(id) { return try Self.check(result) }
        var running = lock.withLock { inFlight.contains(id) }
        if running, heldFor(id) >= Self.stuckAfter {
            // iOS has held this upload for minutes without an end. After failures it waits
            // longer and longer before it tries again (2026-11-18: four uploads from 12:16 were
            // still held at 12:41 and Sync now could start nothing). It is started afresh.
            await stop(id)
            running = false
        }
        if !running {
            try start(credentials, id: id, sealed: sealed)
        }
        await wait(for: id, seconds: Self.patience)
        guard let result = take(id) else { throw RecordingUploadInFlight() }
        try Self.check(result)
    }

    /// 2xx: the relay holds it. 409: it already held this id, from an earlier try of the same part.
    static func check(_ result: Ended) throws {
        switch result.status {
        case 200..<300, 409: return
        case 0: throw RelayClientError.offline
        case 401: throw RelayClientError.unauthorized
        case 403:
            if result.edge == true { throw RelayEdgeForbidden() }
            throw RelayClientError.forbidden
        case 413: throw RelayClientError.payloadTooLarge
        case 429: throw RelayClientError.rateLimited(retryAfter: result.retryAfter)
        case 503: throw RelayClientError.unavailable
        default: throw RelayClientError.server(status: result.status, message: "The relay did not store the upload.")
        }
    }

    private func start(_ credentials: RelayCredentials, id: String, sealed: Data) throws {
        // iOS cannot reach an address inside this app; an upload for it would never end.
        guard !RecordingPieceUploads.isInsideTheApp(credentials.origin) else { throw RelayClientError.offline }
        // A file of its own for each try, so a stopped try that ends late removes only its own.
        let name = id + "-" + UUID().uuidString + ".bin"
        let file = directory.appendingPathComponent(name)
        let sent = RelayUploadBody.sent(sealed, to: credentials.origin)
        try sent.body.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        var request = URLRequest(url: credentials.origin.appendingPathComponent("v1").appendingPathComponent("uploads")
            .appendingPathComponent(id))
        request.httpMethod = "PUT"
        request.setValue("Bearer \(credentials.bearer)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(sent.type, forHTTPHeaderField: "Content-Type")
        let task = session.uploadTask(with: request, fromFile: file)
        task.taskDescription = id + Self.separator + name
        lock.withLock {
            inFlight.insert(id)
            started[id] = Date().timeIntervalSince1970
            saveStarted()
        }
        task.resume()
    }

    /// An upload iOS has held this long with the app running is started afresh.
    static let stuckAfter: TimeInterval = 5 * 60
    static let separator = "|"

    /// The part a task carries, the file it reads, and for a piece its number. Tasks from before
    /// 2026-11-18 name only the part.
    static func parse(_ description: String?) -> (id: String, file: String, piece: Int?)? {
        guard let description, !description.isEmpty else { return nil }
        let parts = description.split(separator: Character(separator), maxSplits: 2).map(String.init)
        let piece = parts.count > 2 ? Int(parts[2]) : nil
        if parts.count > 2, piece == nil { return nil }
        return (parts[0], parts.count > 1 ? parts[1] : parts[0] + ".bin", piece)
    }

    // MARK: Pieces (PROTOCOL.md 24.1a, any order)

    /// Under this key the first failure of a part's pieces is kept until the forwarder reads it.
    private static func pieceKey(_ id: String) -> String { "pieces:" + id }

    /// Hands the missing pieces of a part to iOS, each as its own upload from its own file. iOS
    /// sends them while the app is closed, in the order it likes, and retries each by itself.
    /// Returns when none is left with iOS; throws `RecordingUploadInFlight` while some are, and
    /// the relay's refusal when a piece was refused.
    func putPieces(_ credentials: RelayCredentials, id: String, sealed: Data, size: Int, missing: [Int]) async throws {
        try await handOver(credentials, id: id, sealed: sealed, size: size, missing: missing)
        let key = Self.pieceKey(id)
        if lock.withLock({ (piecesInFlight[id] ?? []).isEmpty }) { return }
        await wait(for: key, seconds: Self.patience)
        if let failed = take(key) { return try Self.checkPiece(failed) }
        if lock.withLock({ !(piecesInFlight[id] ?? []).isEmpty }) { throw RecordingUploadInFlight() }
    }

    /// Hands the missing pieces to iOS and returns at once: it takes milliseconds, so a visit to
    /// the app of a few seconds is enough to leave a part in iOS's hands.
    func handOver(_ credentials: RelayCredentials, id: String, sealed: Data, size: Int, missing: [Int]) async throws {
        await loadTasks()
        let key = Self.pieceKey(id)
        if let failed = take(key) { return try Self.checkPiece(failed) }
        // The whole part in iOS's hands would put the same bytes again. Pieces iOS holds are
        // never stopped here, however long it has held them: until 2026-11-21 they were after
        // five minutes, wakes come every fifteen, and so every wake stopped the pieces iOS had
        // just begun to send. Whether a part is started afresh is decided where the relay was
        // asked what it holds (RecordingPieceUploads.lookAtHeld), and stopped from there.
        if lock.withLock({ inFlight.contains(id) }) { await stop(id) }
        let flying = lock.withLock { piecesInFlight[id] ?? [] }
        for index in missing where !flying.contains(index) {
            let start = index * size
            guard start < sealed.count else { continue }
            let end = min(start + size, sealed.count)
            try startPiece(credentials, id: id, index: index, offset: start, total: sealed.count, size: size,
                           piece: sealed.subdata(in: (sealed.startIndex + start)..<(sealed.startIndex + end)))
        }
    }

    /// Whether iOS holds anything of this part.
    func holds(_ id: String) async -> Bool {
        await loadTasks()
        return lock.withLock { inFlight.contains(id) || !(piecesInFlight[id] ?? []).isEmpty }
    }

    /// Whether iOS has held pieces of this part for `stuckAfter` or longer without an end.
    func stuck(_ id: String) async -> Bool {
        await loadTasks()
        let any = lock.withLock { !(piecesInFlight[id] ?? []).isEmpty }
        return any && heldFor(id) >= Self.stuckAfter
    }

    /// A piece is never "already there under this id" the way a whole upload is: 409 means the
    /// relay holds this id for something else.
    static func checkPiece(_ result: Ended) throws {
        if result.status == 409 { throw RelayClientError.server(status: 409, message: "The relay holds this upload under another shape.") }
        try check(result)
    }

    private func startPiece(_ credentials: RelayCredentials, id: String, index: Int, offset: Int, total: Int,
                            size: Int, piece: Data) throws {
        guard !RecordingPieceUploads.isInsideTheApp(credentials.origin) else { throw RelayClientError.offline }
        let name = id + "-p\(index)-" + UUID().uuidString + ".bin"
        let file = directory.appendingPathComponent(name)
        // The file holds the body as it goes: a piece sent again by itself goes the same way.
        let sent = RelayUploadBody.sent(piece, to: credentials.origin)
        try sent.body.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        var request = URLRequest(url: try RelayClient.uploadPieceURL(credentials.origin, id: id, offset: offset, total: total, size: size))
        request.httpMethod = "PUT"
        request.setValue("Bearer \(credentials.bearer)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(sent.type, forHTTPHeaderField: "Content-Type")
        // A zrok public share answers browser-like clients with a warning page; this asks it not to.
        request.setValue("1", forHTTPHeaderField: "skip_zrok_interstitial")
        let task = session.uploadTask(with: request, fromFile: file)
        task.taskDescription = [id, name, String(index)].joined(separator: Self.separator)
        lock.withLock {
            piecesInFlight[id, default: []].insert(index)
            if started[id] == nil {
                started[id] = Date().timeIntervalSince1970
                saveStarted()
            }
        }
        task.resume()
    }

    /// Whether a piece's answer ends its part's other pieces. A refusal does: they would be
    /// refused the same way. "Too many requests" (429) does not: it is about the minute, not the
    /// part, and the pieces already on their way are wanted. On 2026-11-19 one 429 stopped ten
    /// pieces that were going, and all eleven were handed over again.
    static func stopsThePart(_ status: Int, edge: Bool = false) -> Bool {
        !(200..<300).contains(status) && status != 0 && status != 429 && !isOfTheMoment(status, edge: edge)
    }

    /// An answer that is about the moment, not about the piece or its part: the relay timed out
    /// or failed inside (408, 5xx). 507 is not one: the relay is full, and sending again fills
    /// nothing. On 2026-11-20 at noon eight pieces were answered 500 on a slow link; each
    /// stopped its whole part, and fourteen pieces that were going were cut off.
    /// So is a 403 the relay did not write: a tunnel's edge refused the request, not the relay.
    static func isOfTheMoment(_ status: Int, edge: Bool = false) -> Bool {
        status == 408 || ((500..<600).contains(status) && status != 507) || (status == 403 && edge)
    }

    /// How often one piece is sent again by itself after such an answer.
    static let pieceTries = 2

    /// What follows a piece's answer.
    enum AfterPiece: Equatable {
        /// The same piece goes again; its part's other pieces go on.
        case again
        /// The piece is over. The part's other pieces go on.
        case over
        /// The piece is over and the part's other pieces are stopped: they would be refused the same way.
        case stopThePart
    }

    static func afterPiece(status: Int, triesSoFar: Int, edge: Bool = false) -> AfterPiece {
        if isOfTheMoment(status, edge: edge) { return triesSoFar < pieceTries ? .again : .over }
        return stopsThePart(status, edge: edge) ? .stopThePart : .over
    }

    /// Pieces sent again by themselves, by part and piece: how often so far.
    private var pieceAgain: [String: Int] = [:]

    /// Sends one piece again from its file when its answer was about the moment and it has
    /// tries left. True when the piece is on its way again: it stays among the part's pieces.
    private func sendPieceAgain(_ task: URLSessionTask, id: String, file: String, index: Int, status: Int, edge: Bool) -> Bool {
        let key = id + Self.separator + String(index)
        let tries = lock.withLock { pieceAgain[key] ?? 0 }
        guard Self.afterPiece(status: status, triesSoFar: tries, edge: edge) == .again, let request = task.originalRequest else { return false }
        let url = directory.appendingPathComponent(file)
        guard FileManager.default.fileExists(atPath: url.path) else { return false }
        lock.withLock { pieceAgain[key] = tries + 1 }
        let next = session.uploadTask(with: request, fromFile: url)
        next.taskDescription = task.taskDescription
        next.resume()
        return true
    }

    /// One piece ended. The first refusal is kept and the part's other pieces are stopped: they
    /// would be refused the same way. When none is left the forwarder makes a pass.
    private func pieceEnded(_ task: URLSessionTask, id: String, index: Int, result: Ended) {
        let said = !(200..<300).contains(result.status) && result.status != 0
        let refused = Self.stopsThePart(result.status, edge: result.edge == true)
        let none = lock.withLock { () -> Bool in
            pieceAgain[id + Self.separator + String(index)] = nil
            piecesInFlight[id]?.remove(index)
            if said, ended[Self.pieceKey(id)] == nil {
                ended[Self.pieceKey(id)] = result
                save()
            }
            let none = (piecesInFlight[id] ?? []).isEmpty
            if none {
                piecesInFlight[id] = nil
                started[id] = nil
                saveStarted()
            }
            return none
        }
        if refused, !none {
            Task { await self.stop(id) }
        }
        if none || refused {
            wake(Self.pieceKey(id))
            onEnded?()
        }
    }

    /// How long iOS has held the upload of this part; forever when it was started before this was kept.
    private func heldFor(_ id: String) -> TimeInterval {
        lock.withLock {
            guard let at = started[id] else { return .infinity }
            return Date().timeIntervalSince1970 - at
        }
    }

    /// Stops what iOS holds for this part. How the stopped try ends is not kept.
    private func stop(_ id: String) async {
        for task in await session.allTasks where Self.parse(task.taskDescription)?.id == id {
            lock.withLock { _ = stopped.insert(task.taskIdentifier) }
            task.cancel()
        }
        lock.withLock {
            inFlight.remove(id)
            piecesInFlight[id] = nil
            started[id] = nil
            saveStarted()
        }
    }

    /// The person asked to send now: every upload iOS has held longer than this is stopped, so
    /// the pass that follows starts it afresh.
    func stopHeld(olderThan seconds: TimeInterval) async {
        await loadTasks()
        let ids = lock.withLock { Array(inFlight.union(piecesInFlight.keys)) }
        for id in ids where heldFor(id) >= seconds { await stop(id) }
    }

    private var startedURL: URL { directory.appendingPathComponent("started.json") }

    private func saveStarted() {
        try? JSONEncoder().encode(started).write(to: startedURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    /// Stops the upload iOS holds for this part, if any, and forgets how an earlier one ended: the
    /// part is going up another way now.
    func cancel(_ id: String) async {
        await loadTasks()
        let running = lock.withLock { () -> Bool in
            ended[id] = nil
            ended[Self.pieceKey(id)] = nil
            save()
            return inFlight.contains(id) || !(piecesInFlight[id] ?? []).isEmpty
        }
        if running { await stop(id) }
    }

    /// Uploads iOS still runs from before the app was closed are known before anything is started twice.
    private func loadTasks() async {
        if lock.withLock({ loaded }) { return }
        let tasks = await session.allTasks
        lock.withLock {
            for task in tasks where task.state == .running || task.state == .suspended {
                guard let named = Self.parse(task.taskDescription) else { continue }
                if let piece = named.piece {
                    piecesInFlight[named.id, default: []].insert(piece)
                } else {
                    inFlight.insert(named.id)
                }
            }
            loaded = true
        }
    }

    private func take(_ id: String) -> Ended? {
        lock.withLock {
            guard let result = ended.removeValue(forKey: id) else { return nil }
            save()
            return result
        }
    }

    private func save() {
        try? JSONEncoder().encode(ended).write(to: endedURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    private func wait(for id: String, seconds: TimeInterval) async {
        if lock.withLock({ ended[id] != nil }) { return }
        let timeout = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            self?.wake(id)
        }
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            let already = lock.withLock { () -> Bool in
                if ended[id] != nil { return true }
                waiters[id, default: []].append(continuation)
                return false
            }
            if already { continuation.resume() }
        }
        timeout.cancel()
    }

    private func wake(_ id: String) {
        let waiting = lock.withLock { waiters.removeValue(forKey: id) ?? [] }
        waiting.forEach { $0.resume() }
    }

    // MARK: URLSession

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        // The credential goes to the relay the phone is paired with, nowhere else.
        completionHandler(nil)
    }

    /// The start of an answer's body is kept until its upload ends: it tells a 403 the relay
    /// wrote from one a tunnel's edge wrote. Only that is read from it, and it is not stored.
    static let bodyKept = 2048

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        lock.withLock {
            var body = bodies[dataTask.taskIdentifier] ?? Data()
            guard body.count < Self.bodyKept else { return }
            body.append(data.prefix(Self.bodyKept - body.count))
            bodies[dataTask.taskIdentifier] = body
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        noteCancelReason(error)
        let body = lock.withLock { bodies.removeValue(forKey: task.taskIdentifier) } ?? Data()
        guard let (id, file, piece) = Self.parse(task.taskDescription) else { return }
        let http = task.response as? HTTPURLResponse
        let edge = error == nil && http?.statusCode == 403 && !RelayEdgeForbidden.isTheRelays(body)
        RelayUploadBody.refused(at: task.originalRequest?.url, status: http?.statusCode ?? 0)
        // A piece the relay answered about the moment goes again by itself, from the same file,
        // while the part's other pieces go on.
        if let piece, error == nil, let status = http?.statusCode,
           !lock.withLock({ stopped.contains(task.taskIdentifier) }),
           sendPieceAgain(task, id: id, file: file, index: piece, status: status, edge: edge) {
            return
        }
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(file))
        // A try the app stopped: another one carries the part now, and its state is not touched.
        if lock.withLock({ stopped.remove(task.taskIdentifier) != nil }) { return }
        var result = Ended(status: error == nil ? (http?.statusCode ?? 0) : 0, retryAfter: nil)
        if edge { result.edge = true }
        if result.status == 429, let text = http?.value(forHTTPHeaderField: "Retry-After"), let seconds = TimeInterval(text) {
            result.retryAfter = seconds
        }
        if let piece { return pieceEnded(task, id: id, index: piece, result: result) }
        lock.withLock {
            inFlight.remove(id)
            started[id] = nil
            saveStarted()
            ended[id] = result
            save()
        }
        wake(id)
        onEnded?()
    }

    /// Called with iOS's reason when it stopped an upload itself: 0 the person swiped the app
    /// away, 1 Background App Refresh is off, 2 the system ran short. Only the code is kept.
    var onCancelReason: (@Sendable (Int) -> Void)?
    private static let log = Logger(subsystem: "com.isoform.companion", category: "recordings")

    private func noteCancelReason(_ error: Error?) {
        guard let error = error as NSError?, error.domain == NSURLErrorDomain,
              let reason = error.userInfo[NSURLErrorBackgroundTaskCancelledReasonKey] as? Int else { return }
        Self.log.info("background upload stopped by the system, reason \(reason, privacy: .public)")
        onCancelReason?(reason)
    }

    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        let completion = lock.withLock { () -> (() -> Void)? in
            defer { systemCompletion = nil }
            return systemCompletion
        }
        if let completion { DispatchQueue.main.async(execute: completion) }
    }
}
