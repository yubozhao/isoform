import Foundation

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

final class RecordingBackgroundUploads: NSObject, RecordingUploadTransport, URLSessionDataDelegate, @unchecked Sendable {
    static let shared = RecordingBackgroundUploads()
    static let sessionIdentifier = (Bundle.main.bundleIdentifier ?? "isoform") + ".recording-uploads"

    /// How long a pass waits for one upload before it moves on. The upload itself goes on.
    static let patience: TimeInterval = 20

    /// How an upload ended, kept until the forwarder has read it.
    struct Ended: Codable, Equatable {
        /// The relay's status, or 0 when no answer came.
        var status: Int
        var retryAfter: TimeInterval?
    }

    /// Called when any upload ended, so the forwarder makes a pass.
    var onEnded: (@Sendable () -> Void)?

    private let lock = NSLock()
    private let directory: URL
    private var ended: [String: Ended]
    private var inFlight: Set<String> = []
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
        let running = lock.withLock { inFlight.contains(id) }
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
        case 403: throw RelayClientError.forbidden
        case 413: throw RelayClientError.payloadTooLarge
        case 429: throw RelayClientError.rateLimited(retryAfter: result.retryAfter)
        case 503: throw RelayClientError.unavailable
        default: throw RelayClientError.server(status: result.status, message: "The relay did not store the upload.")
        }
    }

    private func start(_ credentials: RelayCredentials, id: String, sealed: Data) throws {
        let file = directory.appendingPathComponent(id + ".bin")
        try sealed.write(to: file, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        var request = URLRequest(url: credentials.origin.appendingPathComponent("v1").appendingPathComponent("uploads")
            .appendingPathComponent(id))
        request.httpMethod = "PUT"
        request.setValue("Bearer \(credentials.bearer)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        let task = session.uploadTask(with: request, fromFile: file)
        task.taskDescription = id
        lock.withLock { _ = inFlight.insert(id) }
        task.resume()
    }

    /// Uploads iOS still runs from before the app was closed are known before anything is started twice.
    private func loadTasks() async {
        if lock.withLock({ loaded }) { return }
        let tasks = await session.allTasks
        lock.withLock {
            for task in tasks where task.state == .running || task.state == .suspended {
                if let id = task.taskDescription { inFlight.insert(id) }
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

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard let id = task.taskDescription else { return }
        let http = task.response as? HTTPURLResponse
        var result = Ended(status: error == nil ? (http?.statusCode ?? 0) : 0, retryAfter: nil)
        if result.status == 429, let text = http?.value(forHTTPHeaderField: "Retry-After"), let seconds = TimeInterval(text) {
            result.retryAfter = seconds
        }
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(id + ".bin"))
        lock.withLock {
            ended[id] = result
            inFlight.remove(id)
            save()
        }
        wake(id)
        onEnded?()
    }

    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        let completion = lock.withLock { () -> (() -> Void)? in
            defer { systemCompletion = nil }
            return systemCompletion
        }
        if let completion { DispatchQueue.main.async(execute: completion) }
    }
}
