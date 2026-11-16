import Foundation
import WatchConnectivity

// The watch's end of the link to the phone. Each finished part is queued as a file transfer;
// the system delivers it when the phone is reachable, also while this app is not running. When
// the phone confirms a part, its audio is deleted from the watch.
final class WatchPhoneLink: NSObject, WCSessionDelegate, @unchecked Sendable {
    private let library: WatchRecordingLibrary
    var onChange: (() -> Void)?

    init(library: WatchRecordingLibrary) {
        self.library = library
    }

    func activate() {
        guard WCSession.isSupported() else { return }
        WCSession.default.delegate = self
        WCSession.default.activate()
    }

    /// Tells the phone what is still on the watch, so its list can say "On your watch".
    func pushSummaries() {
        let session = WCSession.default
        guard session.activationState == .activated else { return }
        try? session.updateApplicationContext(WatchRecordingWire.summaryContext(library.summaries()))
    }

    /// Queues every part still on the watch that is not already on its way.
    func sendUnsent() {
        let session = WCSession.default
        guard session.activationState == .activated else { return }
        defer { pushSummaries() }
        let queued = Set(session.outstandingFileTransfers.compactMap { transfer -> String? in
            WatchRecordingPartInfo(metadata: transfer.file.metadata).map { "\($0.recordingID)-\($0.part)" }
        })
        for item in library.unsentParts() where !queued.contains("\(item.info.recordingID)-\(item.info.part)") {
            guard FileManager.default.fileExists(atPath: item.url.path) else { continue }
            session.transferFile(item.url, metadata: item.info.metadata)
        }
    }

    // MARK: WCSessionDelegate

    func session(_ session: WCSession, activationDidCompleteWith activationState: WCSessionActivationState, error: Error?) {
        guard activationState == .activated else { return }
        apply(context: session.receivedApplicationContext)
        sendUnsent()
        onChange?()
    }

    func session(_ session: WCSession, didFinish fileTransfer: WCSessionFileTransfer, error: Error?) {
        guard let info = WatchRecordingPartInfo(metadata: fileTransfer.file.metadata) else { return }
        if error == nil {
            library.markSent(recordingID: info.recordingID, part: info.part)
        } else {
            // Not delivered: the part stays on the watch and is queued again.
            sendUnsent()
        }
        pushSummaries()
        onChange?()
    }

    /// The person tapped Sync now on the phone.
    func session(_ session: WCSession, didReceiveMessage message: [String: Any]) {
        guard message[WatchRecordingWire.syncMessageKey] != nil else { return }
        sendUnsent()
    }

    func session(_ session: WCSession, didReceiveApplicationContext applicationContext: [String: Any]) {
        apply(context: applicationContext)
        pushSummaries()
        onChange?()
    }

    /// What the phone says about the recordings: where each one is, and what the computer named
    /// the conversations in it.
    private func apply(context: [String: Any]) {
        let states = WatchRecordingWire.states(fromContext: context)
        library.apply(states: states)
        library.apply(names: WatchRecordingWire.names(fromContext: context), known: Set(states.keys))
    }
}
