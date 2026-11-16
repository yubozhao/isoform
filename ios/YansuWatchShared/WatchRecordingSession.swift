import Foundation

// One listening session on the watch, from the moment the wearer starts it until it ends.
//
// The rule it keeps (2026-11-16): the microphone is opened once and is never closed or reopened
// for a reason of time. watchOS lets an app start recording only while it is in front; a
// recording that is running may go on with the screen off. The earlier design started a new
// recorder for every part and every clock hour, with the screen off, and watchOS often refused:
// recording died at the hour edge and nobody was told.
//
// So the capture runs through, and only the file it writes into changes. A part ends when the
// capture is told to write into the next file: every buffer goes into exactly one file, and no
// audio falls between two parts. The clock hour is a label: at the hour the next file belongs to
// a new recording, and the capture does not notice.
//
// This type holds the bookkeeping and no audio code, so it can be tested with a capture that
// only counts.

/// The capture as the session drives it. The watch's is an audio engine (WatchCapture).
protocol WatchCaptureDriving: AnyObject {
    /// Opens the microphone and writes into `file`. Only works while the app is in front.
    func start(file: URL) throws
    /// Writes into `next` from now on, without stopping. Returns the seconds of audio in the file
    /// that was being written.
    func rotate(to next: URL) throws -> TimeInterval
    /// Closes the microphone. Returns the seconds of audio in the file that was being written.
    func stop() -> TimeInterval
    /// Seconds of audio in the file being written.
    var secondsInFile: TimeInterval { get }
    /// When the microphone last delivered audio; nil when it is closed.
    var lastAudioAt: Date? { get }
    /// Asks to be told (WatchRecordingSession.partIsDue) once the file holds this many seconds.
    func askWhenFileHolds(_ seconds: TimeInterval)
}

final class WatchRecordingSession {
    /// A part kept by the session, for whoever sends it on.
    typealias PartKept = (WatchRecordingPartInfo) -> Void

    let library: WatchRecordingLibrary
    private let capture: WatchCaptureDriving
    private let gaps: WatchGapLog
    private let calendar: Calendar
    private let newID: () -> String
    /// Heart rate and place of the part that just ended; the model samples them.
    var partContext: () -> WatchRecordingContext? = { nil }
    var model: String?
    var onPartKept: PartKept?
    /// A new part began (the model restarts its samplers).
    var onPartBegan: (Date) -> Void = { _ in }

    private(set) var recordingID: String?
    private var partNumber = 0
    private var partStartedAt: Date?
    private var partPlannedSeconds: TimeInterval = 0
    private var partClosesHour = false
    private var hourCut: Date?
    private var marks: [Double] = []
    /// The microphone is open and audio is being written.
    private(set) var capturing = false
    /// The session is on, whether or not the microphone is held right now.
    private(set) var isOn = false

    init(library: WatchRecordingLibrary, capture: WatchCaptureDriving, gaps: WatchGapLog,
         calendar: Calendar = .current, newID: @escaping () -> String = WatchRecordingWire.newRecordingID) {
        self.library = library
        self.capture = capture
        self.gaps = gaps
        self.calendar = calendar
        self.newID = newID
    }

    /// The wearer started listening. Throws when the microphone could not be opened; nothing is
    /// left behind then.
    func begin(at now: Date) throws {
        guard !isOn else { return }
        let id = openRecording(at: now)
        do {
            try capture.start(file: library.fileURL(recordingID: id, part: 0))
        } catch {
            library.discardIfEmpty(recordingID: id)
            recordingID = nil
            hourCut = nil
            throw error
        }
        isOn = true
        capturing = true
        gaps.began(at: now)
        beginPart(at: now)
    }

    /// The wearer, a pause or the phone ended it.
    func end(at now: Date) {
        guard isOn else { return }
        if capturing {
            keepPart(seconds: capture.stop(), last: true)
        } else if let recordingID {
            library.close(recordingID: recordingID)
        }
        gaps.ended(at: now)
        capturing = false
        isOn = false
        recordingID = nil
        hourCut = nil
        partStartedAt = nil
    }

    /// The file holds its part, or the hour is over: go on in the next file. The microphone stays
    /// open. Safe to call at any time; it does nothing until a part is due.
    func partIsDue(at now: Date) {
        guard isOn, capturing, let recordingID, let hourCut, let partStartedAt else { return }
        let hourOver = WatchHourClock.isPast(hourCut, at: now)
        guard hourOver || capture.secondsInFile >= partPlannedSeconds else {
            capture.askWhenFileHolds(partPlannedSeconds)
            return
        }
        let closes = hourOver || partClosesHour || WatchHourClock.mustClose(afterPart: partNumber)
        let nextID = closes ? newID() : recordingID
        let nextPart = closes ? 0 : partNumber + 1
        let seconds: TimeInterval
        do {
            seconds = try capture.rotate(to: library.fileURL(recordingID: nextID, part: nextPart))
        } catch {
            // The next file could not be opened (storage). The capture keeps writing where it
            // was; nothing is lost and the next call tries again.
            return
        }
        // The next part begins where this one's audio ends, so the parts line up without a gap.
        let nextStart = partStartedAt.addingTimeInterval(seconds)
        keepPart(seconds: seconds, last: closes)
        if closes {
            library.begin(id: nextID, at: nextStart)
            self.recordingID = nextID
            self.hourCut = WatchHourClock.cut(after: max(nextStart, now), calendar: calendar)
            partNumber = 0
        }
        beginPart(at: nextStart)
    }

    /// The microphone was taken (a call, Siri) or stopped delivering audio. What was recorded is
    /// kept; the session stays on and waits for `resume`.
    func lostMicrophone(at now: Date, reason: WatchGapLog.Reason) {
        guard isOn, capturing else { return }
        keepPart(seconds: capture.stop(), last: false)
        capturing = false
        partStartedAt = nil
        gaps.open(at: now, reason: reason)
    }

    /// Takes the microphone back. watchOS allows it only while the app is in front, so this
    /// throws in the background and the session keeps waiting.
    func resume(at now: Date) throws {
        guard isOn, !capturing else { return }
        if let cut = hourCut, WatchHourClock.isPast(cut, at: now) {
            // The wait ran across the hour: what came before is one recording, what comes now
            // is the next.
            if let open = recordingID { library.close(recordingID: open) }
            recordingID = nil
        }
        let opened = recordingID == nil
        let id = recordingID ?? openRecording(at: now)
        do {
            try capture.start(file: library.fileURL(recordingID: id, part: partNumber))
        } catch {
            if opened {
                library.discardIfEmpty(recordingID: id)
                recordingID = nil
                hourCut = nil
            }
            throw error
        }
        capturing = true
        gaps.close(at: now)
        beginPart(at: now)
    }

    /// The wearer marked this moment. False when nothing is being recorded.
    func mark() -> Bool {
        guard isOn, capturing else { return false }
        marks.append(capture.secondsInFile)
        return true
    }

    /// Whether the microphone went quiet: the session is on and should be capturing, and no
    /// audio came for longer than `quietSeconds`.
    func microphoneWentQuiet(at now: Date, quietSeconds: TimeInterval = WatchRecordingSession.quietSeconds) -> Bool {
        guard isOn, capturing else { return false }
        guard let last = capture.lastAudioAt else { return true }
        return now.timeIntervalSince(last) > quietSeconds
    }

    /// The microphone must not be silent for longer than this while the session is on.
    static let quietSeconds: TimeInterval = 2

    private func openRecording(at now: Date) -> String {
        let id = newID()
        library.begin(id: id, at: now)
        recordingID = id
        partNumber = 0
        hourCut = WatchHourClock.cut(after: now, calendar: calendar)
        return id
    }

    private func beginPart(at start: Date) {
        guard let hourCut else { return }
        let plan = WatchHourClock.part(from: start, cut: hourCut)
        partStartedAt = start
        partPlannedSeconds = plan.seconds
        partClosesHour = plan.closesHour
        marks = []
        capture.askWhenFileHolds(plan.seconds)
        onPartBegan(start)
    }

    private func keepPart(seconds: TimeInterval, last: Bool) {
        guard let recordingID, let partStartedAt else { return }
        let context = partContext()
        guard seconds >= 0.5 else {
            // Too short to hold anything: drop the file.
            try? FileManager.default.removeItem(at: library.fileURL(recordingID: recordingID, part: partNumber))
            if last { library.close(recordingID: recordingID) }
            return
        }
        gaps.recorded(seconds, at: partStartedAt)
        if let info = library.addPart(recordingID: recordingID, startedAt: partStartedAt, durationSeconds: seconds,
                                      last: last, model: model, context: context, marks: marks) {
            onPartKept?(info)
        }
        marks = []
        partNumber += 1
    }
}
