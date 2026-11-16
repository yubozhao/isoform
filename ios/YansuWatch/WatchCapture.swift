import AVFoundation
import Foundation

// The watch's microphone, opened once and kept open (WatchRecordingSession says why).
//
// An audio engine taps the microphone. Every buffer is converted to 16 kHz mono and written into
// the file that is current at that moment, as AAC. Changing the file (`rotate`) swaps one
// reference under a lock: the engine never stops, and a buffer goes into the old file or the new
// one, never into neither. Each file is closed as soon as the next one is current, so what is on
// the watch is complete up to the running part.
final class WatchCapture: WatchCaptureDriving, @unchecked Sendable {
    enum Failure: Error {
        /// The microphone gave no usable format (it is held elsewhere, or not there).
        case noInput
        case noConverter
    }

    static let sampleRate: Double = 16_000
    private static let settings: [String: Any] = [
        AVFormatIDKey: kAudioFormatMPEG4AAC,
        AVSampleRateKey: sampleRate,
        AVNumberOfChannelsKey: 1,
        AVEncoderBitRateKey: 24_000,
        AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
    ]

    /// Called on the main queue once the file holds what was asked for.
    var onFileHolds: (() -> Void)?
    /// Called on the main queue when writing fails (storage is full).
    var onWriteFailed: (() -> Void)?

    private let engine = AVAudioEngine()
    private let lock = NSLock()
    private var file: AVAudioFile?
    private var converter: AVAudioConverter?
    private var framesInFile: AVAudioFramePosition = 0
    private var askAtFrames: AVAudioFramePosition?
    private var lastAudio: Date?
    private var running = false
    private var writeFailures = 0

    var secondsInFile: TimeInterval {
        lock.withLock { Double(framesInFile) / Self.sampleRate }
    }

    var lastAudioAt: Date? { lock.withLock { running ? lastAudio : nil } }

    func askWhenFileHolds(_ seconds: TimeInterval) {
        lock.withLock { askAtFrames = AVAudioFramePosition(seconds * Self.sampleRate) }
    }

    func start(file url: URL) throws {
        let input = engine.inputNode
        let inputFormat = input.outputFormat(forBus: 0)
        guard inputFormat.sampleRate > 0, inputFormat.channelCount > 0 else { throw Failure.noInput }
        let opened = try Self.open(url)
        guard let converter = AVAudioConverter(from: inputFormat, to: opened.processingFormat) else {
            throw Failure.noConverter
        }
        lock.withLock {
            self.file = opened
            self.converter = converter
            framesInFile = 0
            askAtFrames = nil
            lastAudio = Date()
            writeFailures = 0
            running = true
        }
        input.removeTap(onBus: 0)
        input.installTap(onBus: 0, bufferSize: 4096, format: inputFormat) { [weak self] buffer, _ in
            self?.take(buffer)
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            lock.withLock {
                self.file = nil
                self.converter = nil
                running = false
            }
            try? FileManager.default.removeItem(at: url)
            throw error
        }
    }

    func rotate(to next: URL) throws -> TimeInterval {
        let opened = try Self.open(next)
        let (closed, seconds): (AVAudioFile?, TimeInterval) = lock.withLock {
            let previous = file
            let seconds = Double(framesInFile) / Self.sampleRate
            file = opened
            framesInFile = 0
            askAtFrames = nil
            return (previous, seconds)
        }
        // Released outside the lock: closing writes the file's index, and the tap must not wait.
        Self.finish(closed)
        return seconds
    }

    func stop() -> TimeInterval {
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        let (closed, seconds): (AVAudioFile?, TimeInterval) = lock.withLock {
            let previous = file
            let seconds = Double(framesInFile) / Self.sampleRate
            file = nil
            converter = nil
            framesInFile = 0
            askAtFrames = nil
            running = false
            return (previous, seconds)
        }
        Self.finish(closed)
        return seconds
    }

    private static func open(_ url: URL) throws -> AVAudioFile {
        try? FileManager.default.removeItem(at: url)
        return try AVAudioFile(forWriting: url, settings: settings, commonFormat: .pcmFormatFloat32, interleaved: false)
    }

    private static func finish(_ file: AVAudioFile?) {
        guard let file else { return }
        if #available(watchOS 11.0, *) { file.close() }
    }

    /// On the audio thread: convert, write into the current file, note the time.
    private func take(_ buffer: AVAudioPCMBuffer) {
        lock.lock()
        defer { lock.unlock() }
        guard running, let file, let converter else { return }
        let ratio = Self.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 64
        guard let out = AVAudioPCMBuffer(pcmFormat: file.processingFormat, frameCapacity: capacity) else { return }
        var given = false
        var conversionError: NSError?
        converter.convert(to: out, error: &conversionError) { _, status in
            if given {
                status.pointee = .noDataNow
                return nil
            }
            given = true
            status.pointee = .haveData
            return buffer
        }
        lastAudio = Date()
        guard conversionError == nil, out.frameLength > 0 else { return }
        do {
            try file.write(from: out)
            framesInFile += AVAudioFramePosition(out.frameLength)
            writeFailures = 0
        } catch {
            writeFailures += 1
            if writeFailures == 3, let onWriteFailed {
                DispatchQueue.main.async(execute: onWriteFailed)
            }
            return
        }
        if let ask = askAtFrames, framesInFile >= ask {
            askAtFrames = nil
            if let onFileHolds { DispatchQueue.main.async(execute: onFileHolds) }
        }
    }
}
