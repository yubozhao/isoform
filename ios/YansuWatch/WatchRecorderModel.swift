import AVFoundation
import Foundation
import SwiftUI
import WatchKit

// Records on the watch. Audio is written to the watch's own storage as AAC, mono, in parts of
// WatchRecordingWire.partSeconds; each finished part is handed to the phone link at once, so a
// long recording never waits for its end to start travelling. The audio session keeps recording
// with the screen off (UIBackgroundModes: audio in Info.plist).
@MainActor
final class WatchRecorderModel: NSObject, ObservableObject {
    static let shared = WatchRecorderModel()

    enum Phase: Equatable {
        case idle
        case recording(since: Date)
        /// The microphone is not allowed; the watch says where to change it.
        case denied
        case failed
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var entries: [WatchRecordingEntry] = []

    let library: WatchRecordingLibrary
    private let link: WatchPhoneLink
    private var recorder: AVAudioRecorder?
    private var recordingID: String?
    private var partStartedAt: Date?
    private var interrupted = false

    override init() {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        library = WatchRecordingLibrary(directory: base.appendingPathComponent("recordings", isDirectory: true))
        link = WatchPhoneLink(library: library)
        super.init()
        link.onChange = { [weak self] in
            Task { @MainActor in self?.reload() }
        }
        // A recording the app was killed in the middle of is closed and sent.
        let recovered = library.recoverInterrupted { url in
            (try? AVAudioPlayer(contentsOf: url))?.duration ?? 0
        }
        link.activate()
        if !recovered.isEmpty { link.sendUnsent() }
        NotificationCenter.default.addObserver(self, selector: #selector(interruption(_:)),
                                               name: AVAudioSession.interruptionNotification, object: nil)
        reload()
    }

    var isRecording: Bool {
        if case .recording = phase { return true }
        return false
    }

    func toggle() {
        if isRecording { stop() } else { start() }
    }

    func start() {
        guard !isRecording else { return }
        AVAudioApplication.requestRecordPermission { [weak self] granted in
            Task { @MainActor in
                guard let self else { return }
                guard granted else {
                    self.phase = .denied
                    return
                }
                self.begin()
            }
        }
    }

    private func begin() {
        guard !isRecording else { return }
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.record, mode: .default)
            try session.setActive(true)
            let id = WatchRecordingWire.newRecordingID()
            let now = Date()
            library.begin(id: id, at: now)
            recordingID = id
            try startPart(at: now)
            phase = .recording(since: now)
            WKInterfaceDevice.current().play(.start)
            reload()
        } catch {
            if let recordingID { library.discardIfEmpty(recordingID: recordingID) }
            recordingID = nil
            phase = .failed
            WKInterfaceDevice.current().play(.failure)
        }
    }

    func stop() {
        guard isRecording else { return }
        finishPart(last: true)
        recordingID = nil
        phase = .idle
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        WKInterfaceDevice.current().play(.stop)
        reload()
    }

    private func startPart(at date: Date) throws {
        guard let recordingID, let entry = library.all().first(where: { $0.id == recordingID }) else { return }
        let url = library.fileURL(recordingID: recordingID, part: entry.parts.count)
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: 16_000,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: 24_000,
            AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
        ]
        let recorder = try AVAudioRecorder(url: url, settings: settings)
        recorder.delegate = self
        // The recorder ends the part itself at the part length, also with the screen off; the
        // delegate then cuts it and starts the next one.
        guard recorder.record(forDuration: WatchRecordingWire.partSeconds) else { throw CocoaError(.fileWriteUnknown) }
        self.recorder = recorder
        partStartedAt = date
    }

    /// The running part reached its length: keep it and start the next one.
    private func roll(_ finished: AVAudioRecorder) {
        guard isRecording, finished === recorder else { return }
        finishPart(last: false, length: WatchRecordingWire.partSeconds)
        guard recordingID != nil, library.all().first(where: { $0.id == recordingID })?.finished == false else {
            // The part limit closed the recording.
            stopAfterLimit()
            return
        }
        do {
            try startPart(at: Date())
        } catch {
            stopAfterLimit()
        }
    }

    private func stopAfterLimit() {
        recordingID = nil
        phase = .idle
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        reload()
    }

    /// Keeps the running part. `length` is given when the recorder already ended it at the part
    /// length; otherwise the recorder is stopped here and its own clock is the length.
    private func finishPart(last: Bool, length: TimeInterval? = nil) {
        guard let recordingID else { return }
        guard let recorder, let partStartedAt else {
            // No part is being written (a call holds the microphone, or a cut just happened).
            if last { library.close(recordingID: recordingID) }
            return
        }
        let seconds = length ?? recorder.currentTime
        recorder.delegate = nil
        recorder.stop()
        self.recorder = nil
        self.partStartedAt = nil
        if seconds < 0.5 {
            // Too short to hold anything: drop the file.
            try? FileManager.default.removeItem(at: recorder.url)
            if last { library.close(recordingID: recordingID) }
            return
        }
        library.addPart(recordingID: recordingID, startedAt: partStartedAt, durationSeconds: seconds, last: last,
                        model: WKInterfaceDevice.current().model)
        link.sendUnsent()
    }

    @objc private func interruption(_ notification: Notification) {
        guard let raw = notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
        Task { @MainActor in
            switch type {
            case .began:
                // A call or Siri took the microphone: keep what was recorded so far.
                guard self.isRecording else { return }
                self.interrupted = true
                self.finishPart(last: false)
            case .ended:
                guard self.interrupted, self.isRecording else { return }
                self.interrupted = false
                do {
                    try AVAudioSession.sharedInstance().setActive(true)
                    try self.startPart(at: Date())
                } catch {
                    self.stop()
                }
            @unknown default:
                break
            }
        }
    }

    func reload() {
        entries = library.all()
    }
}

extension WatchRecorderModel: AVAudioRecorderDelegate {
    nonisolated func audioRecorderDidFinishRecording(_ recorder: AVAudioRecorder, successfully flag: Bool) {
        Task { @MainActor in self.roll(recorder) }
    }

    nonisolated func audioRecorderEncodeErrorDidOccur(_ recorder: AVAudioRecorder, error: Error?) {
        Task { @MainActor in
            guard recorder === self.recorder else { return }
            self.stop()
        }
    }
}
