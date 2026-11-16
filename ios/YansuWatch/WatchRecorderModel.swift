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
            tellPhone()
        }
    }
    @Published private(set) var entries: [WatchRecordingEntry] = []
    /// Observing is paused on the computer, as the phone last said. The watch does not record
    /// while it lasts.
    @Published private(set) var pausedOnComputer: WatchPause?
    /// The pause the wearer started on this watch. It holds here at once, whether or not the
    /// phone could tell the computer yet.
    @Published private(set) var pausedHere: WatchPause?
    /// What the phone last said about the computer's pause.
    private var computerPause: WatchPause?
    /// The pause that holds was started on this watch, so it can be ended here.
    @Published private(set) var pauseStartedHere = false

    let library: WatchRecordingLibrary
    private let link: WatchPhoneLink
    private var recorder: AVAudioRecorder?
    private var recordingID: String?
    private var partStartedAt: Date?
    /// The number of the part being written, and how long it was asked to run.
    private var partNumber = 0
    private var partPlannedSeconds: TimeInterval = 0
    /// The moments the wearer marked in the running part, seconds from its start.
    private var partMarks: [Double] = []
    /// When the wearer last marked a moment, for the short "Marked" line.
    @Published private(set) var lastMarkAt: Date?
    /// The running part ends at the hour: the recording closes with it and the next one begins.
    private var partClosesHour = false
    /// When the open recording is closed and the next one begins.
    private var hourCut: Date?
    /// A call or Siri holds the microphone: nothing is being recorded until it is given back.
    @Published private(set) var interrupted = false {
        didSet {
            if interrupted != oldValue { tellPhone() }
        }
    }
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
        link.onPause = { [weak self] pause in
            Task { @MainActor in self?.apply(pause: pause) }
        }
        // A recording the app was killed in the middle of is closed and sent.
        let recovered = library.recoverInterrupted { url in
            (try? AVAudioPlayer(contentsOf: url))?.duration ?? 0
        }
        link.activate()
        // Nothing is recording at launch: clear what a killed app may have left for the complication.
        publishLiveState()
        // What the watch is doing goes to the phone with the first word it sends.
        link.currentStatus = liveStatus
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
        if isRecording { stopByWearer() } else { start() }
    }

    /// The wearer stopped it (the hold, or the Stop shortcut): it stays off for the rest of the
    /// day, or until they start it.
    func stopByWearer() {
        WatchAutoListen.setStopped(Date())
        stop()
        tellPhone()
    }

    /// Says to the phone what the watch is doing now, with what it still holds.
    private func tellPhone() {
        link.currentStatus = liveStatus
        link.pushSummaries()
    }

    /// What the watch is doing right now, for the phone's "Your watch" card.
    var liveStatus: WatchLiveStatus {
        var denied = false
        if case .denied = phase { denied = true }
        return WatchLiveStatus.of(
            paused: isPausedOnComputer, pauseStartedOnWatch: pauseStartedHere, recording: isRecording,
            waitingForMicrophone: interrupted, microphoneDenied: denied,
            stoppedToday: WatchAutoListen.stopHolds(stoppedAt: WatchAutoListen.stoppedAt(), now: Date()))
    }

    /// The app came to the front: record by itself unless the wearer stopped it, and take the
    /// microphone back if a call ended while the app was away.
    func cameToFront(consentSeen: Bool) {
        let microphone: WatchAutoListen.Microphone
        switch AVAudioApplication.shared.recordPermission {
        case .granted: microphone = .allowed
        case .denied: microphone = .denied
        default: microphone = .notAsked
        }
        let step = WatchAutoListen.step(
            consentSeen: consentSeen, microphone: microphone,
            stoppedByWearer: WatchAutoListen.stopHolds(stoppedAt: WatchAutoListen.stoppedAt(), now: Date()),
            isRecording: isRecording, heldByInterruption: interrupted && recorder == nil,
            pausedOnComputer: isPausedOnComputer)
        switch step {
        case .nothing: break
        case .start: start()
        case .resume: resumeAfterInterruption()
        case .showDenied: phase = .denied
        }
    }

    /// The wearer marked this moment: "this matters". It is only a time; the computer shows which
    /// words were being said. Nothing is marked while nothing is being recorded.
    func mark() {
        guard isRecording, let recorder, recorder.isRecording else { return }
        partMarks.append(recorder.currentTime)
        lastMarkAt = Date()
        WKInterfaceDevice.current().play(.click)
    }

    /// Whether the pause the phone last told of still holds (a timed pause ends by itself).
    var isPausedOnComputer: Bool { pausedOnComputer?.isActive(at: Date()) == true }

    /// The phone said whether observing is paused on the computer. One pause covers all of
    /// Isoform: the watch stops, and what it recorded since the pause began is deleted before it
    /// leaves the watch. This is the first guard; the computer drops such audio too.
    func apply(pause: WatchPause?) {
        computerPause = pause
        if pause != nil {
            // The computer has the pause now: its word replaces the watch's own.
            pausedHere = nil
        }
        applyEffectivePause()
    }

    /// The wearer paused Isoform from the watch. It takes effect here at once; the phone is asked
    /// to pause the computer, which then tells every device.
    func pauseFromWatch(minutes: Int = WatchPauseRequest.defaultMinutes) {
        let now = Date()
        pausedHere = WatchPause(since: now, until: now.addingTimeInterval(Double(minutes) * 60))
        pauseStartedHere = true
        let wasRecording = isRecording
        link.ask(.pause(minutes: minutes))
        applyEffectivePause()
        // Stopping a recording already gave its haptic.
        if !wasRecording { WKInterfaceDevice.current().play(.stop) }
    }

    /// The wearer ended the pause they started on the watch. The phone is asked to end it on the
    /// computer too; a pause made at the computer stays, and the phone says so.
    func resumeFromWatch() {
        pausedHere = nil
        computerPause = nil
        pauseStartedHere = false
        link.ask(.resume)
        applyEffectivePause()
    }

    private func applyEffectivePause() {
        let active = WatchPause.effective(computer: computerPause, watch: pausedHere, now: Date())
        guard active != pausedOnComputer else { return }
        pausedOnComputer = active
        defer { tellPhone() }
        guard let active else {
            pauseStartedHere = false
            return
        }
        if isRecording { stop() }
        if let since = active.since, library.dropUnsent(since: since) > 0 {
            link.pushSummaries()
        }
        reload()
    }

    /// The wearer, the complication or a shortcut asked to record. Clears an earlier stop.
    func start() {
        WatchAutoListen.setStopped(nil)
        // Paused on the computer: the pause is ended there, not here.
        guard !isRecording, !isPausedOnComputer else { return }
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
        interrupted = false
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
        // Say again that the watch records, so the complication keeps showing it (it trusts the
        // last word for WatchLiveState.longest only).
        // (When recording begins, the phase change says it.)
        guard isRecording else { return }
        WatchLiveState.write(recordingSince: date)
        WidgetCenter.shared.reloadTimelines(ofKind: WatchLiveState.complicationKind)
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
        partMarks = []
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
                        model: WKInterfaceDevice.current().model, context: context, marks: partMarks)
        partMarks = []
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
                self.resumeAfterInterruption()
            @unknown default:
                break
            }
        }
    }

    /// Opens the microphone again after a call or Siri. watchOS does not always say when one
    /// ended while the app was in the background, so this also runs when the app comes to the front.
    private func resumeAfterInterruption() {
        guard interrupted, isRecording, recorder == nil else { return }
        do {
            try AVAudioSession.sharedInstance().setActive(true)
            let now = Date()
            if let cut = hourCut, WatchHourClock.isPast(cut, at: now) {
                // The call ran across the hour: what came before it is one recording, what comes
                // now is the next.
                if let open = recordingID { library.close(recordingID: open) }
                openHour(at: now)
                link.sendUnsent()
            }
            try startPart(at: now)
            interrupted = false
            reload()
        } catch {
            // The microphone is still held (the call goes on): try again at the next chance.
        }
    }

    func reload() {
        entries = library.all()
    }

    /// Tells the complication whether a recording is running. Only one time is shared.
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
