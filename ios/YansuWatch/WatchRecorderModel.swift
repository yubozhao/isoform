import AVFoundation
import Foundation
import SwiftUI
import WatchKit
import WidgetKit

// Records on the watch. Audio is written to the watch's own storage as AAC, mono, in parts of
// WatchRecordingWire.partSeconds; each finished part is handed to the phone link at once, so a
// long recording never waits for its end to start travelling. Recordings are kept by the clock
// hour (WatchHourClock): at each hour the open recording is closed and the next begins by itself. The audio session keeps recording
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

    @Published private(set) var phase: Phase = .idle {
        didSet {
            guard phase != oldValue else { return }
            publishLiveState()
        }
    }
    @Published private(set) var entries: [WatchRecordingEntry] = []

    let library: WatchRecordingLibrary
    private let link: WatchPhoneLink
    private var recorder: AVAudioRecorder?
    private var recordingID: String?
    private var partStartedAt: Date?
    /// The number of the part being written, and how long it was asked to run.
    private var partNumber = 0
    private var partPlannedSeconds: TimeInterval = 0
    /// The running part ends at the hour: the recording closes with it and the next one begins.
    private var partClosesHour = false
    /// When the open recording is closed and the next one begins.
    private var hourCut: Date?
    private var interrupted = false
    private let heartRate = WatchHeartRateSampler()
    private let location = WatchLocationSampler()

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
        // Nothing is recording at launch: clear what a killed app may have left for the complication.
        publishLiveState()
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
            let now = Date()
            openHour(at: now)
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
        hourCut = nil
        phase = .idle
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        WKInterfaceDevice.current().play(.stop)
        reload()
    }

    /// Opens the recording for the hour `date` falls in. The one before, if any, is already closed.
    private func openHour(at date: Date) {
        let id = WatchRecordingWire.newRecordingID()
        library.begin(id: id, at: date)
        recordingID = id
        partNumber = 0
        hourCut = WatchHourClock.cut(after: date)
    }

    private func startPart(at date: Date) throws {
        guard let recordingID, let hourCut else { return }
        let url = library.fileURL(recordingID: recordingID, part: partNumber)
        let plan = WatchHourClock.part(from: date, cut: hourCut)
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: 16_000,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: 24_000,
            AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
        ]
        let recorder = try AVAudioRecorder(url: url, settings: settings)
        recorder.delegate = self
        // The recorder ends the part itself at the part length or at the hour, also with the
        // screen off; the delegate then cuts it and starts the next one.
        guard recorder.record(forDuration: plan.seconds) else { throw CocoaError(.fileWriteUnknown) }
        self.recorder = recorder
        partStartedAt = date
        partPlannedSeconds = plan.seconds
        partClosesHour = plan.closesHour
        // Only when the wearer turned it on, and only while a part is being recorded.
        if WatchContextSettings.heartRate { heartRate.begin(at: date) }
        if WatchContextSettings.location { location.begin() } else { location.reset() }
    }

    /// The running part reached its length: keep it and start the next one. At the hour the
    /// recording is closed with it and the next hour's recording begins, without stopping.
    private func roll(_ finished: AVAudioRecorder) {
        guard isRecording, finished === recorder else { return }
        let closes = partClosesHour || WatchHourClock.mustClose(afterPart: partNumber)
        finishPart(last: closes, length: partPlannedSeconds)
        let now = Date()
        if closes { openHour(at: now) }
        do {
            try startPart(at: now)
            if closes { reload() }
        } catch {
            if closes, let recordingID { library.discardIfEmpty(recordingID: recordingID) }
            stopAfterLimit()
        }
    }

    /// The next part could not be started: what was recorded is kept and the watch goes idle.
    private func stopAfterLimit() {
        location.reset()
        if let recordingID { library.close(recordingID: recordingID) }
        recordingID = nil
        hourCut = nil
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
        let context = WatchRecordingContext(heartRate: heartRate.end(),
                                            location: WatchContextSettings.location ? location.end() : nil)
        if last { location.reset() }
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
                        model: WKInterfaceDevice.current().model, context: context)
        partNumber += 1
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
                    let now = Date()
                    if let cut = self.hourCut, WatchHourClock.isPast(cut, at: now) {
                        // The call ran across the hour: what came before it is one recording,
                        // what comes now is the next.
                        if let open = self.recordingID { self.library.close(recordingID: open) }
                        self.openHour(at: now)
                        self.link.sendUnsent()
                    }
                    try self.startPart(at: now)
                    self.reload()
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

    /// Tells the complication whether a recording is running. Only the start time is shared.
    private func publishLiveState() {
        var since: Date?
        if case .recording(let started) = phase { since = started }
        guard WatchLiveState.recordingSince() != since else { return }
        WatchLiveState.write(recordingSince: since)
        WidgetCenter.shared.reloadTimelines(ofKind: WatchLiveState.complicationKind)
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
