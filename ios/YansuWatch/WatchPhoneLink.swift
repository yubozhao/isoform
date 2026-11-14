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

    /// Queues every part still on the watch that is not already on its way.
    func sendUnsent() {
        let session = WCSession.default
        guard session.activationState == .activated else { return }
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
        library.apply(states: WatchRecordingWire.states(fromContext: session.receivedApplicationContext))
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
        onChange?()
    }

    func session(_ session: WCSession, didReceiveApplicationContext applicationContext: [String: Any]) {
        library.apply(states: WatchRecordingWire.states(fromContext: applicationContext))
        onChange?()
    }
}
