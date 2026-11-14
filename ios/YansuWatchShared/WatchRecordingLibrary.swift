import Foundation

// The watch's own record of what it recorded: an index file beside the audio parts. A part's
// audio is deleted from the watch once the phone has it; the index row stays so the list can
// show where the recording is. Compiled into the phone's tests too, so the logic is checked
// without a watch simulator.

struct WatchRecordingPart: Codable, Equatable, Sendable {
    let part: Int
    let fileName: String
    let startedAt: Date
    let durationSeconds: Double
    var last: Bool
    /// The phone confirmed it holds this part; the file is gone from the watch.
    var sent: Bool
}

struct WatchRecordingEntry: Codable, Equatable, Identifiable, Sendable {
    let id: String
    let startedAt: Date
    var parts: [WatchRecordingPart]
    /// The recording was stopped (its last part is written).
    var finished: Bool
    var remote: WatchRecordingRemoteState?

    var durationSeconds: Double { parts.reduce(0) { $0 + $1.durationSeconds } }
    var allSent: Bool { finished && !parts.isEmpty && parts.allSatisfy(\.sent) }

    /// What the list shows for this recording.
    var display: WatchRecordingDisplay {
        if let remote, allSent || remote > .onPhone { return .remote(remote) }
        if !finished { return .recording }
        if allSent { return .remote(.onPhone) }
        let sent = parts.filter(\.sent).count
        return sent == 0 ? .onWatch : .sending(sent: sent, total: parts.count)
    }
}

enum WatchRecordingDisplay: Equatable, Sendable {
    case recording
    case onWatch
    case sending(sent: Int, total: Int)
    case remote(WatchRecordingRemoteState)
}

final class WatchRecordingLibrary: @unchecked Sendable {
    static let keptEntries = 30

    let directory: URL
    private let lock = NSLock()
    private var entries: [WatchRecordingEntry] = []
    private let fileManager: FileManager

    init(directory: URL, fileManager: FileManager = .default) {
        self.directory = directory
        self.fileManager = fileManager
        try? fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        if let data = try? Data(contentsOf: indexURL),
           let stored = try? JSONDecoder().decode([WatchRecordingEntry].self, from: data) {
            entries = stored
        }
    }

    private var indexURL: URL { directory.appendingPathComponent("index.json") }

    static func fileName(recordingID: String, part: Int) -> String { "\(recordingID)-\(part).m4a" }

    func fileURL(recordingID: String, part: Int) -> URL {
        directory.appendingPathComponent(Self.fileName(recordingID: recordingID, part: part))
    }

    /// Newest first.
    func all() -> [WatchRecordingEntry] {
        lock.withLock { entries.sorted { $0.startedAt > $1.startedAt } }
    }

    func begin(id: String, at: Date) {
        lock.withLock {
            guard !entries.contains(where: { $0.id == id }) else { return }
            entries.append(WatchRecordingEntry(id: id, startedAt: at, parts: [], finished: false, remote: nil))
            saveLocked()
        }
    }

    /// Records a finished part whose audio is already at fileURL(recordingID:part:), and returns
    /// what to tell the phone. Nil when the recording is unknown or the part number is out of range.
    @discardableResult
    func addPart(recordingID: String, startedAt: Date, durationSeconds: Double, last: Bool,
                 model: String? = nil) -> WatchRecordingPartInfo? {
        lock.withLock {
            guard let index = entries.firstIndex(where: { $0.id == recordingID }), !entries[index].finished,
                  durationSeconds > 0 else { return nil }
            let part = entries[index].parts.count
            guard part < WatchRecordingWire.maxParts else { return nil }
            // The 64th part must close the recording: there is no number for another.
            let closes = last || part == WatchRecordingWire.maxParts - 1
            entries[index].parts.append(WatchRecordingPart(
                part: part, fileName: Self.fileName(recordingID: recordingID, part: part), startedAt: startedAt,
                durationSeconds: durationSeconds, last: closes, sent: false))
            if closes { entries[index].finished = true }
            saveLocked()
            return WatchRecordingPartInfo(recordingID: recordingID, part: part, last: closes, startedAt: startedAt,
                                          durationSeconds: durationSeconds, model: model)
        }
    }

    /// Closes a recording that was stopped while no part was being written (right after a cut, or
    /// while a call held the microphone). Its newest part becomes the last one when it is still
    /// on the watch; when it already left, the computer closes the recording itself after its
    /// wait (PROTOCOL.md 24.2). A recording with no parts is dropped.
    func close(recordingID: String) {
        lock.withLock {
            guard let index = entries.firstIndex(where: { $0.id == recordingID }), !entries[index].finished else { return }
            if entries[index].parts.isEmpty {
                entries.remove(at: index)
            } else {
                let newest = entries[index].parts.count - 1
                if !entries[index].parts[newest].sent { entries[index].parts[newest].last = true }
                entries[index].finished = true
            }
            saveLocked()
        }
    }

    /// A recording stopped before any audio was kept is dropped.
    func discardIfEmpty(recordingID: String) {
        lock.withLock {
            guard let index = entries.firstIndex(where: { $0.id == recordingID }), entries[index].parts.isEmpty else { return }
            entries.remove(at: index)
            saveLocked()
        }
    }

    /// The phone holds this part: delete its audio from the watch.
    func markSent(recordingID: String, part: Int) {
        lock.withLock {
            guard let index = entries.firstIndex(where: { $0.id == recordingID }),
                  let partIndex = entries[index].parts.firstIndex(where: { $0.part == part }) else { return }
            entries[index].parts[partIndex].sent = true
            try? fileManager.removeItem(at: directory.appendingPathComponent(entries[index].parts[partIndex].fileName))
            saveLocked()
        }
    }

    /// Parts still on the watch, oldest first, for (re)sending.
    func unsentParts() -> [(info: WatchRecordingPartInfo, url: URL)] {
        lock.withLock {
            entries.sorted { $0.startedAt < $1.startedAt }.flatMap { entry in
                entry.parts.filter { !$0.sent }.map { part in
                    (WatchRecordingPartInfo(recordingID: entry.id, part: part.part, last: part.last,
                                            startedAt: part.startedAt, durationSeconds: part.durationSeconds),
                     directory.appendingPathComponent(part.fileName))
                }
            }
        }
    }

    /// States from the phone. A state never moves backwards.
    func apply(states: [String: WatchRecordingRemoteState]) {
        lock.withLock {
            var changed = false
            for index in entries.indices {
                guard let state = states[entries[index].id] else { continue }
                if let current = entries[index].remote, current >= state { continue }
                entries[index].remote = state
                changed = true
            }
            if changed { saveLocked() }
        }
    }

    /// After a launch: a recording the app was killed in the middle of is closed. A part file
    /// that was being written is kept when `duration` can read it, and dropped otherwise.
    func recoverInterrupted(duration: (URL) -> Double) -> [WatchRecordingPartInfo] {
        lock.withLock {
            var recovered: [WatchRecordingPartInfo] = []
            for index in entries.indices where !entries[index].finished {
                let entry = entries[index]
                let part = entry.parts.count
                let url = directory.appendingPathComponent(Self.fileName(recordingID: entry.id, part: part))
                let seconds = fileManager.fileExists(atPath: url.path) ? duration(url) : 0
                if seconds > 0, part < WatchRecordingWire.maxParts {
                    let started = entry.parts.last.map { $0.startedAt.addingTimeInterval($0.durationSeconds) } ?? entry.startedAt
                    entries[index].parts.append(WatchRecordingPart(part: part, fileName: url.lastPathComponent,
                                                                   startedAt: started, durationSeconds: seconds,
                                                                   last: true, sent: false))
                    recovered.append(WatchRecordingPartInfo(recordingID: entry.id, part: part, last: true,
                                                            startedAt: started, durationSeconds: seconds))
                } else {
                    try? fileManager.removeItem(at: url)
                }
                entries[index].finished = true
            }
            entries.removeAll { $0.parts.isEmpty }
            saveLocked()
            return recovered
        }
    }

    private func saveLocked() {
        // Old rows whose audio already left the watch make room; a recording with audio still
        // here is never dropped.
        let sorted = entries.sorted { $0.startedAt > $1.startedAt }
        if sorted.count > Self.keptEntries {
            let keep = Set(sorted.prefix(Self.keptEntries).map(\.id))
            entries.removeAll { !keep.contains($0.id) && $0.allSent }
        }
        guard let data = try? JSONEncoder().encode(entries) else { return }
        try? data.write(to: indexURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
}
