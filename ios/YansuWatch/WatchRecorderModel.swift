import AVFoundation
import Foundation
import SwiftUI
import WatchKit
import WidgetKit

// Records on the watch. The microphone is opened once, when the wearer starts listening with the
// app in front, and stays open until listening ends: watchOS does not let an app open it again
// with the screen off (WatchRecordingSession, WatchCapture). Audio is written to the watch's own
// storage as AAC, mono, in parts of WatchRecordingWire.partSeconds; a part ends by switching the
// file, never by stopping the microphone, and each finished part is handed to the phone link at
// once. Recordings are kept by the clock hour (WatchHourClock): at the hour the next file belongs
// to a new recording. The audio session keeps recording with the screen off
// (UIBackgroundModes: audio in Info.plist).
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
    private let capture = WatchCapture()
    private let gaps: WatchGapLog
    /// The listening session: which recording and part the microphone is writing into.
    private let listening: WatchRecordingSession
    /// Looks every two seconds whether the microphone still delivers audio, and whether a part
    /// or the hour is over.
    private var watchdog: Timer?
    private var lastResumeTry = Date.distantPast
    /// When the wearer last marked a moment, for the short "Marked" line.
    @Published private(set) var lastMarkAt: Date?
    /// Listening is on but the microphone is not held (a call, Siri, or it went quiet by itself):
    /// nothing is being recorded until the app is in front again. The watch buzzes once and
    /// shows it; the phone is told.
    @Published private(set) var interrupted = false {
        didSet {
            guard interrupted != oldValue else { return }
            if interrupted { WKInterfaceDevice.current().play(.failure) }
            tellPhone()
        }
    }
    private let heartRate = WatchHeartRateSampler()
    private let location = WatchLocationSampler()

    override init() {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        library = WatchRecordingLibrary(directory: base.appendingPathComponent("recordings", isDirectory: true))
        link = WatchPhoneLink(library: library)
        gaps = WatchGapLog(url: base.appendingPathComponent("listening-gaps.json"))
        listening = WatchRecordingSession(library: library, capture: capture, gaps: gaps)
        super.init()
        listening.model = WKInterfaceDevice.current().model
        listening.partContext = { [weak self] in
            MainActor.assumeIsolated {
                guard let self else { return nil }
                return WatchRecordingContext(heartRate: self.heartRate.end(),
                                             location: WatchContextSettings.location ? self.location.end() : nil)
            }
        }
        listening.onPartBegan = { [weak self] date in
            MainActor.assumeIsolated {
                guard let self else { return }
                // Only when the wearer turned it on, and only while a part is being recorded.
                if WatchContextSettings.heartRate { self.heartRate.begin(at: date) }
                if WatchContextSettings.location { self.location.begin() } else { self.location.reset() }
            }
        }
        listening.onPartKept = { [weak self] _ in
            MainActor.assumeIsolated { self?.link.sendUnsent() }
        }
        capture.onFileHolds = { [weak self] in
            Task { @MainActor in self?.partIsDue() }
        }
        capture.onWriteFailed = { [weak self] in
            Task { @MainActor in self?.stopAfterLimit() }
        }
        link.onChange = { [weak self] in
            Task { @MainActor in self?.reload() }
        }
        link.onPause = { [weak self] pause in
            Task { @MainActor in self?.apply(pause: pause) }
        }
        link.onListenOff = { [weak self] since in
            Task { @MainActor in self?.turnedOffOnPhone(since: since) }
        }
        // A recording the app was killed in the middle of is closed and sent.
        let recovered = library.recoverInterrupted { url in
            (try? AVAudioPlayer(contentsOf: url))?.duration ?? 0
        }
        // Listening was on when the app last ran: the system ended the app. That is a gap, and
        // the watch says so; listening starts again by itself when the app is in front.
        gaps.foundAfterLaunch(at: Date(), lastAudioAt: nil)
        link.activate()
        // Nothing is recording at launch: clear what a killed app may have left for the complication.
        publishLiveState()
        // What the watch is doing goes to the phone with the first word it sends.
        link.currentStatus = liveStatus
        if !recovered.isEmpty { link.sendUnsent() }
        NotificationCenter.default.addObserver(self, selector: #selector(interruption(_:)),
                                               name: AVAudioSession.interruptionNotification, object: nil)
        NotificationCenter.default.addObserver(self, selector: #selector(audioChanged(_:)),
                                               name: .AVAudioEngineConfigurationChange, object: nil)
        reload()
    }

    var isRecording: Bool {
        if case .recording = phase { return true }
        return false
    }

    func toggle() {
        if isRecording { stopByWearer() } else { start() }
    }

    /// "Listen on your watch" was turned off on the phone: stop, and stay off until the wearer
    /// starts it here. Each off is acted on once.
    func turnedOffOnPhone(since: Date) {
        guard WatchAutoListen.takesOff(since: since) else { return }
        WatchAutoListen.setOffFromPhone(true)
        stop()
        tellPhone()
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
            stoppedToday: WatchAutoListen.offFromPhone() || WatchAutoListen.stopHolds(stoppedAt: WatchAutoListen.stoppedAt(), now: Date()))
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
            stoppedByWearer: WatchAutoListen.offFromPhone() || WatchAutoListen.stopHolds(stoppedAt: WatchAutoListen.stoppedAt(), now: Date()),
            isRecording: isRecording, heldByInterruption: interrupted && !listening.capturing,
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
        guard isRecording, listening.mark() else { return }
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
        WatchAutoListen.setOffFromPhone(false)
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
            try listening.begin(at: now)
            phase = .recording(since: now)
            WKInterfaceDevice.current().play(.start)
            startWatchdog()
            reload()
        } catch {
            phase = .failed
            WKInterfaceDevice.current().play(.failure)
        }
    }

    func stop() {
        guard isRecording else { return }
        stopWatchdog()
        listening.end(at: Date())
        location.reset()
        interrupted = false
        phase = .idle
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        WKInterfaceDevice.current().play(.stop)
        reload()
    }

    /// A part or the hour is over: the session goes on in the next file. The microphone is not
    /// touched. When the hour changed, the list and the complication are told.
    private func partIsDue() {
        let before = listening.recordingID
        listening.partIsDue(at: Date())
        guard isRecording, listening.recordingID != before else { return }
        // Say again that the watch records, so the complication keeps showing it (it trusts the
        // last word for WatchLiveState.longest only).
        WatchLiveState.write(recordingSince: Date())
        WidgetCenter.shared.reloadTimelines(ofKind: WatchLiveState.complicationKind)
        reload()
    }

    /// The watch cannot write any more (storage): what was recorded is kept, listening ends, and
    /// the watch says so.
    private func stopAfterLimit() {
        guard isRecording else { return }
        stopWatchdog()
        listening.end(at: Date())
        location.reset()
        interrupted = false
        phase = .failed
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        WKInterfaceDevice.current().play(.failure)
        reload()
    }

    private func startWatchdog() {
        stopWatchdog()
        let timer = Timer(timeInterval: WatchRecordingSession.quietSeconds, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.watch() }
        }
        timer.tolerance = 0.5
        RunLoop.main.add(timer, forMode: .common)
        watchdog = timer
    }

    private func stopWatchdog() {
        watchdog?.invalidate()
        watchdog = nil
    }

    /// Every two seconds while listening is on: a microphone that went quiet is opened again at
    /// once and the gap is logged; a part or hour that is over is rolled even if the capture's
    /// own word was missed; and a microphone that was lost is asked for again.
    private func watch() {
        guard isRecording else { return }
        let now = Date()
        if listening.microphoneWentQuiet(at: now) {
            lostMicrophone(at: now, reason: .quiet)
            resumeAfterInterruption()
            return
        }
        if interrupted {
            // In front it can be taken back at once; in the background watchOS refuses, so it
            // is asked for only now and then.
            let inFront = WKApplication.shared().applicationState == .active
            if inFront || now.timeIntervalSince(lastResumeTry) >= 30 { resumeAfterInterruption() }
            return
        }
        partIsDue()
    }

    /// Listening stays on, but nothing is recorded until the microphone is back.
    private func lostMicrophone(at now: Date, reason: WatchGapLog.Reason) {
        guard isRecording, listening.capturing else { return }
        listening.lostMicrophone(at: now, reason: reason)
        interrupted = true
        reload()
    }

    @objc private func interruption(_ notification: Notification) {
        guard let raw = notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
        Task { @MainActor in
            switch type {
            case .began:
                // A call or Siri took the microphone: keep what was recorded so far.
                self.lostMicrophone(at: Date(), reason: .interrupted)
            case .ended:
                self.resumeAfterInterruption()
            @unknown default:
                break
            }
        }
    }

    /// The audio route or format changed and the engine stopped: keep what was recorded and open
    /// the microphone again at once.
    @objc private func audioChanged(_ notification: Notification) {
        Task { @MainActor in
            guard self.isRecording, self.listening.capturing else { return }
            self.lostMicrophone(at: Date(), reason: .routeChanged)
            self.resumeAfterInterruption()
        }
    }

    /// Opens the microphone again after a call, Siri, or a capture that stopped. watchOS allows
    /// it only while the app is in front and does not always say when a call ended, so this also
    /// runs when the app comes to the front and from the watchdog.
    private func resumeAfterInterruption() {
        guard interrupted, isRecording, !listening.capturing else { return }
        lastResumeTry = Date()
        do {
            try AVAudioSession.sharedInstance().setActive(true)
            try listening.resume(at: Date())
            interrupted = false
            reload()
        } catch {
            // The microphone is still held, or the app is not in front: try again at the next chance.
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
