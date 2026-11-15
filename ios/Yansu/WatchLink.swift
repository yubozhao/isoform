import Foundation
import UIKit
import WatchConnectivity

// The phone's end of the watch link. It receives recording parts from the watch app, hands them
// to the forwarder, and tells the watch where each recording is. It asks for no permission and
// shows nothing; without a paired watch it does nothing.
final class WatchLink: NSObject, WCSessionDelegate, @unchecked Sendable {
    static let shared = WatchLink()

    private var inbox: WatchRecordingInbox?
    private var forwarder: WatchRecordingForwarder?
    private var observers: [NSObjectProtocol] = []

    /// Called once at launch with the pairing vault and the hub channel.
    func start(directory: URL, vault: PairingVault, hub: RecordingHubAPI) {
        guard forwarder == nil, WCSession.isSupported(), !WatchRecordingsModel.isSeeded else { return }
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("watch-recordings", isDirectory: true))
        let forwarder = WatchRecordingForwarder(inbox: inbox, hub: hub, session: { try vault.session() })
        self.inbox = inbox
        self.forwarder = forwarder
        Task { [weak self] in
            await forwarder.onStatesChanged { states in self?.push(states) }
            await forwarder.onActivityChanged { self?.refreshModel() }
            let recordings = await forwarder.recordings()
            let outcome = await forwarder.lastOutcome
            await WatchRecordingsModel.shared.show(recordings, outcome: outcome)
        }
        WCSession.default.delegate = self
        WCSession.default.activate()
        observers.append(NotificationCenter.default.addObserver(
            forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
                self?.drain()
            })
    }

    /// Sends what is waiting; called when the app opens, a part arrives, or the computer answers.
    func drain() {
        Task { await drainNow() }
    }

    @MainActor
    private func drainNow() async {
        guard let forwarder else { return }
        // Ask for time to finish when the app is in the background (a part just arrived).
        let application = UIApplication.shared
        let task = application.beginBackgroundTask(withName: "watch-recordings")
        let outcome = await forwarder.drain()
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: outcome)
        WatchRecordingsModel.shared.checked(at: Date())
        if task != .invalid { application.endBackgroundTask(task) }
    }

    /// The person tapped Sync now: ask the watch to send what it holds, send what is here, and
    /// read the computer's states again.
    @MainActor
    func syncNow() async {
        guard let forwarder else { return }
        let session = WCSession.default
        if session.activationState == .activated, session.isReachable {
            session.sendMessage([WatchRecordingWire.syncMessageKey: true], replyHandler: nil, errorHandler: nil)
        }
        await drainNow()
        await forwarder.refreshStates()
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
    }

    /// Deletes a recording from this phone, the watch and the computer.
    @MainActor
    func delete(_ id: String) async {
        guard let forwarder else { return }
        await forwarder.delete(id)
        WatchRecordingsModel.shared.forget(id)
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
    }

    /// Tries again to send a recording the computer refused.
    @MainActor
    func retry(_ id: String) async {
        guard let forwarder else { return }
        let outcome = await forwarder.retry(id)
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: outcome)
    }

    func transcript(_ id: String) async throws -> RecordingTranscript? {
        guard let forwarder else { return WatchRecordingsModel.isSeeded ? WatchRecordingsModel.seededTranscript : nil }
        return try await forwarder.transcript(id)
    }

    func audioOnPhone(_ id: String) async -> [URL] {
        await forwarder?.audioOnPhone(id) ?? []
    }

    private func refreshModel() {
        guard let forwarder else { return }
        Task { await WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil) }
    }

    private func publishConnection(_ session: WCSession) {
        let connection: WatchConnection = !session.isPaired ? .noWatch
            : !session.isWatchAppInstalled ? .appMissing : .ready(reachable: session.isReachable)
        let onWatch = WatchRecordingWire.summaries(fromContext: session.receivedApplicationContext)
        Task { @MainActor in
            WatchRecordingsModel.shared.connection = connection
            WatchRecordingsModel.shared.showOnWatch(onWatch)
        }
    }

    private func push(_ states: [String: WatchRecordingRemoteState]) {
        let session = WCSession.default
        guard session.activationState == .activated, session.isPaired, session.isWatchAppInstalled else { return }
        try? session.updateApplicationContext(WatchRecordingWire.context(states))
    }

    // MARK: WCSessionDelegate

    func session(_ session: WCSession, activationDidCompleteWith activationState: WCSessionActivationState, error: Error?) {
        guard activationState == .activated, let forwarder else { return }
        publishConnection(session)
        Task { push(await forwarder.currentStates()) }
        drain()
    }

    func sessionReachabilityDidChange(_ session: WCSession) { publishConnection(session) }

    func sessionWatchStateDidChange(_ session: WCSession) { publishConnection(session) }

    /// The watch says what it still holds.
    func session(_ session: WCSession, didReceiveApplicationContext applicationContext: [String: Any]) {
        publishConnection(session)
    }

    func sessionDidBecomeInactive(_ session: WCSession) {}

    func sessionDidDeactivate(_ session: WCSession) {
        // A newly paired watch: activate again so its transfers arrive.
        session.activate()
    }

    func session(_ session: WCSession, didReceive file: WCSessionFile) {
        // The file is removed when this method returns, so it is copied before anything else.
        guard let inbox, let forwarder, let info = WatchRecordingPartInfo(metadata: file.metadata) else { return }
        do {
            try inbox.accept(file: file.fileURL, info: info)
        } catch {
            return
        }
        Task {
            await forwarder.received(info)
            await WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
            self.drain()
        }
    }
}
