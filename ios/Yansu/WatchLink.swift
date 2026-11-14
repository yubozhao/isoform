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
        guard forwarder == nil, WCSession.isSupported() else { return }
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("watch-recordings", isDirectory: true))
        let forwarder = WatchRecordingForwarder(inbox: inbox, hub: hub, session: { try vault.session() })
        self.inbox = inbox
        self.forwarder = forwarder
        Task { [weak self] in
            await forwarder.onStatesChanged { states in self?.push(states) }
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
        guard let forwarder else { return }
        Task { @MainActor in
            // Ask for time to finish when the app is in the background (a part just arrived).
            let application = UIApplication.shared
            let task = application.beginBackgroundTask(withName: "watch-recordings")
            await forwarder.drain()
            if task != .invalid { application.endBackgroundTask(task) }
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
        Task { push(await forwarder.currentStates()) }
        drain()
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
            self.drain()
        }
    }
}
