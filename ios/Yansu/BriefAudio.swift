import AVFoundation
import CryptoKit
import Foundation
import MediaPlayer
import SwiftUI

// The spoken brief (PROTOCOL.md section 8 `audio`, section 17 attachments). The Mac stores the audio
// beside the brief and, when a paired phone can read briefs, uploads a sealed copy to the relay. The
// phone prefetches that copy, opens it with the key that travelled inside the sealed brief,
// keeps the file in its cache and plays it locally.

struct BriefAudio: Codable, Equatable, Sendable {
    /// `ok`, `downloading` (a voice is still installing), or absent on an older brief (PROTOCOL.md section 3).
    var status: String? = nil
    var mime: String? = nil
    var bytes: Int? = nil
    var durationSeconds: Double? = nil
    var attachment: BriefAudioAttachment? = nil
    /// The recording's SHA-256 (section 8). A remade recording of the same brief has a new one.
    var sha256: String? = nil
    /// Where each spoken paragraph starts (PROTOCOL.md 8.1). Nil for a reading that is not the written paragraphs.
    var paragraphs: [BriefAudioParagraph]? = nil

    /// The computer is making this brief's recording right now (audio status `rendering`, PROTOCOL.md section 8). A
    /// voice that is still installing is `downloading` and is not a recording being made.
    var isRecording: Bool {
        status == "rendering"
    }

    /// A finished recording the computer can hand over. An unknown status never means there is something to play.
    var isFinished: Bool {
        (status == nil || status == "ok") && !(mime ?? "").isEmpty && (bytes ?? 0) > 0 && (durationSeconds ?? 0) > 0
    }
}

/// One spoken paragraph: which list it belongs to (`narrative` or `weeklyRecap`), its index in that list, and when
/// it starts in the audio.
struct BriefAudioParagraph: Codable, Equatable, Sendable {
    var source: String
    var index: Int
    var startSeconds: Double
}

extension BriefAudio {
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        status = try? container.decodeIfPresent(String.self, forKey: .status)
        mime = try container.decodeIfPresent(String.self, forKey: .mime)
        bytes = try container.decodeIfPresent(Int.self, forKey: .bytes)
        durationSeconds = try container.decodeIfPresent(Double.self, forKey: .durationSeconds)
        attachment = try container.decodeIfPresent(BriefAudioAttachment.self, forKey: .attachment)
        sha256 = try? container.decodeIfPresent(String.self, forKey: .sha256)
        paragraphs = try? container.decodeIfPresent([BriefAudioParagraph].self, forKey: .paragraphs)
    }

    /// The file extension the player writes, from the Mac's declared type.
    var fileExtension: String {
        mime == "audio/mp4" ? "m4a" : "wav"
    }

    /// "1:35" for the play button; nil when the Mac did not say.
    var durationLabel: String? {
        durationSeconds.flatMap(Self.label)
    }

    /// "1:35" for a length in seconds; nil when it is not a plausible length.
    static func label(_ seconds: TimeInterval) -> String? {
        guard seconds.isFinite, seconds > 0, seconds < 604800 else { return nil }
        let total = Int(seconds.rounded())
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}

/// Maps the audio's position to the paragraph being spoken, and a paragraph to where it starts (design section 3).
enum BriefAudioTimeline {
    /// The written paragraph (its index in `narrative`) being spoken at `seconds`: the last one that has started.
    static func narrativeParagraph(at seconds: TimeInterval, in paragraphs: [BriefAudioParagraph]?) -> Int? {
        let timings = narrativeTimings(paragraphs)
        guard let first = timings.first else { return nil }
        return timings.last(where: { $0.startSeconds <= seconds })?.index ?? first.index
    }

    /// Where a written paragraph starts in the audio; nil when it has no timing.
    static func start(ofNarrative index: Int, in paragraphs: [BriefAudioParagraph]?) -> TimeInterval? {
        narrativeTimings(paragraphs).first { $0.index == index }?.startSeconds
    }

    /// The written paragraphs' timings in the order they are spoken. A timing that is malformed is dropped.
    static func narrativeTimings(_ paragraphs: [BriefAudioParagraph]?) -> [BriefAudioParagraph] {
        (paragraphs ?? [])
            .filter { $0.source == "narrative" && $0.index >= 0 && $0.startSeconds.isFinite && $0.startSeconds >= 0 }
            .sorted { $0.startSeconds < $1.startSeconds }
    }

    /// The first words of a paragraph, for the lock screen.
    static func openingWords(_ text: String, count: Int = 8) -> String {
        text.split(whereSeparator: \.isWhitespace).prefix(count).joined(separator: " ")
    }
}

/// What this phone remembers about one brief's playback. Kept whether or not its audio is still on the phone.
struct BriefPlaybackMemory: Equatable, Sendable {
    let seconds: TimeInterval
    let duration: TimeInterval
    let played: Bool

    /// How much of the brief was heard, from 0 to 1, for the ring in Past briefs.
    var progress: Double {
        duration > 0 ? min(1, max(0, seconds / duration)) : 0
    }
}

/// The playback speeds the brief offers.
enum BriefPlaybackSpeed: Double, CaseIterable, Sendable {
    case normal = 1
    case faster = 1.25
    case fastest = 1.5

    var label: String { String(format: "%g×", rawValue) }
    var next: BriefPlaybackSpeed {
        let all = Self.allCases
        return all[(all.firstIndex(of: self).map { $0 + 1 } ?? 0) % all.count]
    }
}

/// Phone-only playback state that outlives the audio clip: the chosen speed and the brief last playing, with when
/// it was playing. A restart brings the brief back only when it was playing in the last 30 minutes.
struct BriefPlaybackRecall: Codable, Equatable, Sendable {
    var speed: Double? = nil
    var briefID: String? = nil
    var hubID: String? = nil
    var playingAt: Date? = nil
}

struct BriefAudioAttachment: Codable, Equatable, Sendable {
    var id: String? = nil
    var key: String? = nil
    var nonce: String? = nil
}

extension BriefAudioAttachment {
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decodeIfPresent(String.self, forKey: .id)
        key = try container.decodeIfPresent(String.self, forKey: .key)
        nonce = try container.decodeIfPresent(String.self, forKey: .nonce)
    }
}

enum BriefAudioError: LocalizedError, Equatable {
    case notAvailable
    case unreadable
    case computerOffline
    case rendering
    case voiceNotInstalled
    case versionChanged
    case tooLarge

    var errorDescription: String? {
        switch self {
        case .notAvailable:
            return "Audio is not available on your computer yet."
        case .unreadable:
            return "Isoform could not open the spoken brief on this phone."
        case .computerOffline:
            return "Your computer is offline."
        case .rendering:
            return "Audio is still rendering on your computer."
        case .voiceNotInstalled:
            return "A voice is not installed on your computer."
        case .versionChanged:
            return "Refresh the brief to hear the latest version from your computer."
        case .tooLarge:
            return "This audio is too large to save on your phone."
        }
    }
}

// Opens a sealed attachment: ChaCha20-Poly1305 with the key and nonce from the brief and the AAD
// "yansu-attachment-v1|<hub>|<attachmentId>" (PROTOCOL.md 17.1). Any failure keeps the text brief.
enum BriefAudioCrypto {
    static func aad(hub: String, attachmentID: String) -> String {
        "yansu-attachment-v1|\(hub)|\(attachmentID)"
    }

    static func open(_ sealed: Data, attachment: BriefAudioAttachment, hub: String) throws -> Data {
        guard let id = attachment.id, Identifiers.isSafePathID(id),
              let keyText = attachment.key, let nonceText = attachment.nonce else {
            throw BriefAudioError.unreadable
        }
        let key = try RelayCrypto.base64URLDecode(keyText)
        let nonce = try RelayCrypto.base64URLDecode(nonceText)
        guard key.count == 32, nonce.count == 12 else { throw BriefAudioError.unreadable }
        do {
            return try RelayCrypto.unseal(sealed, key: key, nonce: nonce, aad: aad(hub: hub, attachmentID: id))
        } catch {
            throw BriefAudioError.unreadable
        }
    }
}

// The version identifies the rendered text, not the wire schema version. The computer verifies it
// before returning the sealed attachment, so a refreshed brief never plays an older recording.
struct BriefAudioResource: Codable, Equatable, Sendable {
    let briefId: String
    let version: String
    let audio: BriefAudio

    static func versionKey(for brief: MobileBriefV1) -> String {
        "\(brief.identifier)|\(brief.asOf ?? "")"
    }
}

extension MobileBriefV1 {
    // The computer preserves generation identity when its read filter withdraws synthesis.
    // Missing audio alone is also used while rendering and is not a withdrawal.
    var audioIsWithdrawn: Bool { audio == nil && narrativeStatus == "unavailable" }
}

// Relay sequence numbers are comparable only inside one pairing. A relay refetch of a
// withdrawal does not replace audio authorized by the computer after that same receipt.
struct BriefAudioReceipt: Codable, Equatable, Sendable {
    let deviceID: String
    var pairingID: String? = nil
    let sequence: UInt64

    func samePairing(as other: BriefAudioReceipt) -> Bool {
        deviceID == other.deviceID && pairingID == other.pairingID
    }

    func covers(_ other: BriefAudioReceipt) -> Bool {
        samePairing(as: other) && sequence >= other.sequence
    }
}

// One decrypted clip per brief, in the app container with protected files and no backup. A brief's clip is kept for
// 7 days. The cache holds at most `maximumClips` clips and `maximumTotalBytes` of audio, and the oldest clip goes
// first. Each manifest keeps only playback metadata, in clips.json beside the clips, keyed like them; the
// attachment's transport key and nonce are never written here.
final class BriefAudioCache: @unchecked Sendable {
    static let shared = BriefAudioCache()
    static let maximumAudioBytes = 16 * 1024 * 1024
    /// Audio is kept for 7 days; the brief's text is kept longer (BriefCache.textRetentionInterval).
    static let retentionInterval: TimeInterval = 7 * 24 * 60 * 60
    /// Four briefs a day for a week, with room for on-request briefs.
    static let maximumClips = 28
    static let maximumTotalBytes = 256 * 1024 * 1024
    /// Where each version was left is kept for the text's 30 days, at most 60 versions (two briefs a day).
    static let positionRetentionInterval: TimeInterval = 30 * 24 * 60 * 60
    static let positionLimit = 60

    struct Entry: Equatable {
        let url: URL
        let audio: BriefAudio
        let position: TimeInterval
    }

    private struct Manifest: Codable, Equatable {
        let key: String
        let audio: BriefAudio
        let storedAt: Date
        var position: TimeInterval
        var withdrawn: Bool? = nil
        var reauthorizedAfterWithdrawal: Bool? = nil
        var withdrawalReceipt: BriefAudioReceipt? = nil
    }

    private struct Withdrawal: Codable {
        let receipt: BriefAudioReceipt
        let observedAt: Date
    }

    private struct ResumePosition: Codable, Equatable {
        let seconds: TimeInterval
        let updatedAt: Date
        var duration: TimeInterval? = nil
        var played: Bool? = nil
    }

    private let directory: URL
    private let removeItem: (URL) throws -> Void
    private let now: () -> Date
    private let maximumClips: Int
    private let maximumTotalBytes: Int
    private let lock = NSLock()
    private var clearPending = false
    private var clipsURL: URL { directory.appendingPathComponent("clips.json") }
    /// The one manifest of a cache from before 7-day clips. It is read once and then replaced by clips.json.
    private var legacyManifestURL: URL { directory.appendingPathComponent("current.json") }
    private var positionsURL: URL { directory.appendingPathComponent("positions.json") }
    private var withdrawalsURL: URL { directory.appendingPathComponent("withdrawals.json") }
    private var recallURL: URL { directory.appendingPathComponent("recall.json") }

    init(directory: URL = BriefAudioCache.defaultDirectory,
         removeItem: @escaping (URL) throws -> Void = { try FileManager.default.removeItem(at: $0) },
         now: @escaping () -> Date = { Date() },
         maximumClips: Int = BriefAudioCache.maximumClips,
         maximumTotalBytes: Int = BriefAudioCache.maximumTotalBytes) {
        self.directory = directory.resolvingSymlinksInPath().standardizedFileURL
        self.removeItem = removeItem
        self.now = now
        self.maximumClips = maximumClips
        self.maximumTotalBytes = maximumTotalBytes
        _ = pruneLocked()
    }

    static var defaultDirectory: URL {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Isoform", isDirectory: true)
            .appendingPathComponent("brief-audio", isDirectory: true)
    }

    private func key(brief: MobileBriefV1, hubID: String) -> String {
        let identity = "\(hubID)|\(BriefAudioResource.versionKey(for: brief))"
        return SHA256.hash(data: Data(identity.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    /// A Mac that remakes a brief's recording keeps its id and time but names a new SHA-256 (section 8). A clip is
    /// that brief's recording unless both name a hash and the hashes differ.
    static func sameRecording(_ clip: BriefAudio, _ named: BriefAudio?) -> Bool {
        guard let have = clip.sha256?.lowercased(), !have.isEmpty,
              let want = named?.sha256?.lowercased(), !want.isEmpty else { return true }
        return have == want
    }

    private func fileURL(key: String, audio: BriefAudio) -> URL {
        directory.appendingPathComponent(key).appendingPathExtension(audio.fileExtension)
    }

    func cached(brief: MobileBriefV1, hubID: String) -> Entry? {
        lock.lock()
        defer { lock.unlock() }
        let cacheKey = key(brief: brief, hubID: hubID)
        guard !clearPending, let manifest = readManifests()[cacheKey], manifest.withdrawn != true,
              !hasUnacknowledgedWithdrawal(manifest),
              manifest.storedAt > now().addingTimeInterval(-Self.retentionInterval),
              Self.sameRecording(manifest.audio, brief.audio) else { return nil }
        let url = fileURL(key: manifest.key, audio: manifest.audio)
        guard let size = (try? url.resourceValues(forKeys: [.fileSizeKey]))?.fileSize,
              size > 0, size <= Self.maximumAudioBytes,
              manifest.audio.bytes == size else {
            // A clip that no longer matches its manifest is discarded, with its manifest, as the cache is read.
            _ = pruneLocked()
            return nil
        }
        return Entry(url: url, audio: manifest.audio, position: max(0, manifest.position))
    }

    func invalidate(brief: MobileBriefV1, hubID: String) {
        lock.lock()
        defer { lock.unlock() }
        let cacheKey = key(brief: brief, hubID: hubID)
        var manifests = readManifests()
        guard var manifest = manifests[cacheKey] else { return }
        manifest.withdrawn = true
        manifest.reauthorizedAfterWithdrawal = nil
        manifest.withdrawalReceipt = latestWithdrawalReceiptLocked(key: cacheKey)
        manifest.position = 0
        manifests[cacheKey] = manifest
        do {
            // Revoke the cached authorization before removing bytes. A failed file removal
            // must not let a reopened cache restore playback from an older readable view.
            try writeManifests(manifests)
        } catch {
            clearPending = true
            // Unlinking the manifests can still succeed when protected file writes cannot.
            try? removeItem(clipsURL)
        }
        try? removeItem(fileURL(key: manifest.key, audio: manifest.audio))
        var positions = readPositions()
        positions.removeValue(forKey: cacheKey)
        try? writePositions(positions)
    }

    func withdrawalState(brief: MobileBriefV1, hubID: String) -> (withdrawn: Bool, reauthorized: Bool, receipt: BriefAudioReceipt?) {
        lock.lock()
        defer { lock.unlock() }
        guard let manifest = readManifests()[key(brief: brief, hubID: hubID)] else {
            return (false, false, nil)
        }
        let withdrawn = manifest.withdrawn == true || hasUnacknowledgedWithdrawal(manifest)
        return (withdrawn, !withdrawn && manifest.reauthorizedAfterWithdrawal == true, manifest.withdrawalReceipt)
    }

    // Keep a bounded receipt watermark even before any audio exists or while another clip is
    // selected. Only the manifest's later authorization can suppress a received withdrawal.
    func recordWithdrawal(_ receipt: BriefAudioReceipt, brief: MobileBriefV1, hubID: String) {
        lock.lock()
        defer { lock.unlock() }
        // Seal an outstanding revocation before bounded receipt eviction can forget it.
        do { try sealOutstandingWithdrawalsLocked() }
        catch { clearPending = true; return }
        let cacheKey = key(brief: brief, hubID: hubID)
        if latestWithdrawalReceiptLocked(key: cacheKey)?.covers(receipt) == true { return }
        var withdrawals = readWithdrawals()
        withdrawals[cacheKey] = Withdrawal(receipt: receipt, observedAt: now())
        do {
            try prepareDirectory()
            try writeWithdrawals(withdrawals)
        } catch {
            // A download must not persist an authorization whose receipt we could not save.
            clearPending = true
        }
    }

    func latestWithdrawalReceipt(brief: MobileBriefV1, hubID: String) -> BriefAudioReceipt? {
        lock.lock()
        defer { lock.unlock() }
        return latestWithdrawalReceiptLocked(key: key(brief: brief, hubID: hubID))
    }

    private func sealOutstandingWithdrawalsLocked() throws {
        var manifests = readManifests()
        var changed = false
        for (cacheKey, manifest) in manifests where hasUnacknowledgedWithdrawal(manifest) {
            var sealed = manifest
            sealed.withdrawn = true
            sealed.reauthorizedAfterWithdrawal = nil
            sealed.withdrawalReceipt = latestWithdrawalReceiptLocked(key: cacheKey)
            sealed.position = 0
            manifests[cacheKey] = sealed
            changed = true
        }
        if changed { try writeManifests(manifests) }
    }

    private func hasUnacknowledgedWithdrawal(_ manifest: Manifest) -> Bool {
        guard let receipt = readWithdrawals()[manifest.key]?.receipt else { return false }
        return manifest.withdrawalReceipt?.covers(receipt) != true
    }

    private func latestWithdrawalReceiptLocked(key: String) -> BriefAudioReceipt? {
        let recorded = readWithdrawals()[key]?.receipt
        let authorized = readManifests()[key]?.withdrawalReceipt
        if let recorded {
            return authorized?.covers(recorded) == true ? authorized : recorded
        }
        return authorized
    }

    /// `store` off the main actor: hashing and writing a clip of up to 16 MB there held up scrolling and taps
    /// while a brief's audio arrived.
    @discardableResult
    func storeInBackground(_ data: Data, brief: MobileBriefV1, hubID: String, audio: BriefAudio,
                           reauthorizedAfterWithdrawal: Bool = false,
                           withdrawalReceipt: BriefAudioReceipt? = nil) async throws -> URL {
        try await Task.detached(priority: .userInitiated) { [self] in
            try self.store(data, brief: brief, hubID: hubID, audio: audio,
                           reauthorizedAfterWithdrawal: reauthorizedAfterWithdrawal, withdrawalReceipt: withdrawalReceipt)
        }.value
    }

    @discardableResult
    func store(_ data: Data, brief: MobileBriefV1, hubID: String, audio: BriefAudio,
               reauthorizedAfterWithdrawal: Bool = false, withdrawalReceipt: BriefAudioReceipt? = nil) throws -> URL {
        guard !data.isEmpty, data.count <= Self.maximumAudioBytes else { throw BriefAudioError.tooLarge }
        guard audio.bytes == nil || audio.bytes == data.count,
              ["audio/wav", "audio/x-wav", "audio/mp4"].contains(audio.mime ?? "") else {
            throw BriefAudioError.unreadable
        }
        lock.lock()
        defer { lock.unlock() }
        guard !clearPending else { throw BriefAudioError.unreadable }
        try prepareDirectory()
        let cacheKey = key(brief: brief, hubID: hubID)
        let url = fileURL(key: cacheKey, audio: audio)
        var metadata = audio
        metadata.attachment = nil
        metadata.bytes = data.count
        // The clip is the recording the brief names, or it is not kept: text and audio never come from two versions.
        metadata.sha256 = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        guard Self.sameRecording(metadata, brief.audio) else { throw BriefAudioError.versionChanged }
        var manifests = readManifests()
        let previous = manifests[cacheKey]
        let position = readPositions()[cacheKey]?.seconds ?? previous?.position ?? 0
        try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        manifests[cacheKey] = Manifest(key: cacheKey, audio: metadata, storedAt: now(), position: position,
                                       reauthorizedAfterWithdrawal: reauthorizedAfterWithdrawal, withdrawalReceipt: withdrawalReceipt)
        do {
            try writeManifests(manifests)
        } catch {
            if previous == nil { try? FileManager.default.removeItem(at: url) }
            throw error
        }
        // Older clips past their 7 days or over the caps go now, with any file that belongs to no clip.
        _ = pruneLocked()
        return url
    }

    /// What this phone remembers about a brief's playback, or nil when it never played the brief here.
    func playbackMemory(brief: MobileBriefV1, hubID: String) -> BriefPlaybackMemory? {
        lock.lock()
        defer { lock.unlock() }
        guard !clearPending, let saved = readPositions()[key(brief: brief, hubID: hubID)] else { return nil }
        return BriefPlaybackMemory(seconds: saved.seconds, duration: saved.duration ?? 0, played: saved.played == true)
    }

    func savePosition(_ position: TimeInterval, duration: TimeInterval? = nil, played: Bool? = nil,
                      brief: MobileBriefV1, hubID: String) throws {
        guard position.isFinite else { return }
        lock.lock()
        defer { lock.unlock() }
        guard !clearPending else { return }
        let cacheKey = key(brief: brief, hubID: hubID)
        var manifests = readManifests()
        if var manifest = manifests[cacheKey] {
            guard manifest.withdrawn != true, !hasUnacknowledgedWithdrawal(manifest) else { return }
            manifest.position = max(0, position)
            manifests[cacheKey] = manifest
            try writeManifests(manifests)
        } else if latestWithdrawalReceiptLocked(key: cacheKey) != nil {
            return
        }
        var positions = readPositions()
        let previous = positions[cacheKey]
        let knownLength = duration.flatMap { $0.isFinite && $0 > 0 ? $0 : nil }
        positions[cacheKey] = ResumePosition(seconds: max(0, position), updatedAt: now(),
                                             duration: knownLength ?? previous?.duration,
                                             played: played ?? previous?.played)
        try writePositions(positions)
    }

    func readRecall() -> BriefPlaybackRecall? {
        lock.lock()
        defer { lock.unlock() }
        guard !clearPending, let data = try? Data(contentsOf: recallURL), data.count <= 4 * 1024 else { return nil }
        return try? JSONDecoder().decode(BriefPlaybackRecall.self, from: data)
    }

    func writeRecall(_ recall: BriefPlaybackRecall) throws {
        lock.lock()
        defer { lock.unlock() }
        guard !clearPending else { throw BriefAudioError.unreadable }
        try prepareDirectory()
        try JSONEncoder().encode(recall).write(
            to: recallURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    @discardableResult
    func clear() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        clearPending = true
        guard FileManager.default.fileExists(atPath: directory.path) else {
            clearPending = false
            return true
        }
        do {
            try removeItem(directory)
            clearPending = false
            return true
        } catch {
            // A failed delete must not let this instance restore the audio the user just removed.
            // AppEnvironment keeps its durable pairing cleanup marker until deletion succeeds.
            return false
        }
    }

    // Keeps the clips the 7-day window and both caps allow, and the withdrawals still in their window. Everything
    // else in the folder goes: an expired or oversize clip, a clip whose bytes no longer match its manifest, a file
    // no manifest names, and a cache from before 7-day clips' single manifest once it is moved to clips.json.
    private func pruneLocked() -> Bool {
        guard FileManager.default.fileExists(atPath: directory.path) else { return true }
        guard let entries = try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) else {
            return false
        }
        do { try sealOutstandingWithdrawalsLocked() }
        catch { return false }
        var keep: Set<String> = []
        let withdrawals = readWithdrawals()
        if !withdrawals.isEmpty {
            do { try writeWithdrawals(withdrawals); keep.insert(withdrawalsURL.lastPathComponent) }
            catch { return false }
        }
        let positions = readPositions()
        if !positions.isEmpty {
            do { try writePositions(positions); keep.insert(positionsURL.lastPathComponent) }
            catch { return false }
        }
        if FileManager.default.fileExists(atPath: recallURL.path) { keep.insert(recallURL.lastPathComponent) }
        let manifests = readManifests()
        let kept = retainedManifests(manifests, now: now())
        if kept != manifests || FileManager.default.fileExists(atPath: legacyManifestURL.path) {
            if kept.isEmpty {
                // Nothing left to name: the manifest file goes too.
                do {
                    try? FileManager.default.removeItem(at: legacyManifestURL)
                    if FileManager.default.fileExists(atPath: clipsURL.path) { try FileManager.default.removeItem(at: clipsURL) }
                } catch { return false }
            } else {
                do { try writeManifests(kept) }
                catch { return false }
            }
        }
        for manifest in kept.values where manifest.withdrawn != true {
            keep.insert(fileURL(key: manifest.key, audio: manifest.audio).lastPathComponent)
        }
        if FileManager.default.fileExists(atPath: clipsURL.path) { keep.insert(clipsURL.lastPathComponent) }
        var removed = true
        for entry in entries where !keep.contains(entry.lastPathComponent) {
            do { try FileManager.default.removeItem(at: entry) }
            catch { removed = false }
        }
        return removed
    }

    /// The manifests to keep, newest first among the clips: a clip stays while it is within 7 days, its bytes match,
    /// and it fits the count and byte caps. The oldest go first, so the first clip that does not fit ends the list.
    /// A withdrawal with no clip stays while its manifest is within the window.
    private func retainedManifests(_ manifests: [String: Manifest], now: Date) -> [String: Manifest] {
        let cutoff = now.addingTimeInterval(-Self.retentionInterval)
        let fresh = manifests.filter { cacheKey, manifest in
            cacheKey == manifest.key && cacheKey.count == 64 && cacheKey.allSatisfy { $0.isHexDigit && !$0.isUppercase } &&
                manifest.position.isFinite && manifest.storedAt > cutoff
        }
        var kept = fresh.filter { $0.value.withdrawn == true }
        let clips = fresh.values.filter { $0.withdrawn != true }.sorted { $0.storedAt > $1.storedAt }
        var total = 0
        var count = 0
        for clip in clips {
            let url = fileURL(key: clip.key, audio: clip.audio)
            guard let size = (try? url.resourceValues(forKeys: [.fileSizeKey]))?.fileSize,
                  size > 0, size <= Self.maximumAudioBytes, size == clip.audio.bytes else { continue }
            guard count < maximumClips, total + size <= maximumTotalBytes else { break }
            total += size
            count += 1
            kept[clip.key] = clip
        }
        return kept
    }

    // Remember at most 14 version positions for 14 days. Returning to an older brief can resume
    // after downloading it again, without retaining a second audio clip or a transport secret.
    private func readPositions() -> [String: ResumePosition] {
        guard let data = try? Data(contentsOf: positionsURL), data.count <= 16 * 1024,
              let positions = try? JSONDecoder().decode([String: ResumePosition].self, from: data) else { return [:] }
        return boundedPositions(positions)
    }

    private func boundedPositions(_ positions: [String: ResumePosition]) -> [String: ResumePosition] {
        let cutoff = now().addingTimeInterval(-Self.positionRetentionInterval)
        let valid = positions.filter { key, position in
            key.count == 64 && key.allSatisfy { $0.isHexDigit && !$0.isUppercase } &&
                position.seconds.isFinite && position.seconds >= 0 && position.updatedAt > cutoff
        }.sorted {
            $0.value.updatedAt == $1.value.updatedAt ? $0.key < $1.key : $0.value.updatedAt > $1.value.updatedAt
        }.prefix(Self.positionLimit)
        return Dictionary(uniqueKeysWithValues: valid.map { ($0.key, $0.value) })
    }

    private func writePositions(_ positions: [String: ResumePosition]) throws {
        try JSONEncoder().encode(boundedPositions(positions)).write(
            to: positionsURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    private func prepareDirectory() throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        var protectedDirectory = directory
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try protectedDirectory.setResourceValues(values)
        try FileManager.default.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                                              ofItemAtPath: directory.path)
    }

    private func readWithdrawals() -> [String: Withdrawal] {
        guard let data = try? Data(contentsOf: withdrawalsURL), data.count <= 16 * 1024,
              let withdrawals = try? JSONDecoder().decode([String: Withdrawal].self, from: data) else { return [:] }
        return boundedWithdrawals(withdrawals)
    }

    private func boundedWithdrawals(_ withdrawals: [String: Withdrawal]) -> [String: Withdrawal] {
        let cutoff = now().addingTimeInterval(-Self.retentionInterval)
        let valid = withdrawals.filter { key, withdrawal in
            key.count == 64 && key.allSatisfy { $0.isHexDigit && !$0.isUppercase } && withdrawal.observedAt > cutoff
        }.sorted {
            $0.value.observedAt == $1.value.observedAt ? $0.key < $1.key : $0.value.observedAt > $1.value.observedAt
        }.prefix(14)
        return Dictionary(uniqueKeysWithValues: valid.map { ($0.key, $0.value) })
    }

    private func writeWithdrawals(_ withdrawals: [String: Withdrawal]) throws {
        try JSONEncoder().encode(boundedWithdrawals(withdrawals)).write(
            to: withdrawalsURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    /// Every manifest by its clip key. A cache from before 7-day clips kept one manifest in current.json, which is
    /// read here until clips.json is written.
    private func readManifests() -> [String: Manifest] {
        if let data = try? Data(contentsOf: clipsURL), data.count <= 512 * 1024,
           let manifests = try? JSONDecoder().decode([String: Manifest].self, from: data) {
            return manifests
        }
        if let data = try? Data(contentsOf: legacyManifestURL), data.count < 16 * 1024,
           let legacy = try? JSONDecoder().decode(Manifest.self, from: data) {
            return [legacy.key: legacy]
        }
        return [:]
    }

    private func writeManifests(_ manifests: [String: Manifest]) throws {
        try JSONEncoder().encode(manifests).write(
            to: clipsURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        try? FileManager.default.removeItem(at: legacyManifestURL)
    }
}

// The engine seam keeps store tests deterministic without opening an audio session or playing sound.
@MainActor
protocol BriefAudioPlayback: AnyObject {
    var duration: TimeInterval { get }
    var currentTime: TimeInterval { get set }
    /// The playback speed. It applies only when the engine allows rate changes.
    var rate: Float { get set }
    func play() -> Bool
    func pause()
    func stop()
}

extension AVAudioPlayer: BriefAudioPlayback {}

// AppEnvironment owns this player. Leaving Feed or locking the phone preserves playback.
@MainActor
final class BriefAudioPlayer: NSObject, ObservableObject, AVAudioPlayerDelegate {
    enum State: Equatable {
        case idle
        case loading
        case ready
        case playing
        case paused
        case unavailable(BriefAudioError)
        case failed(String)
    }

    @Published private(set) var state: State = .idle
    @Published private(set) var position: TimeInterval = 0
    @Published private(set) var duration: TimeInterval = 0
    /// Bumped whenever audio is stored on or dropped from this phone, so the Today card rechecks it.
    @Published private(set) var audioRevision = 0
    /// Set once Play was pressed for `brief`, and cleared when the player is dismissed. The mini player shows only then.
    @Published private(set) var started = false
    /// Set when the audio ends. The mini player shows "Played" for a few seconds, and the brief is marked played.
    @Published private(set) var finished = false
    @Published private(set) var speed: BriefPlaybackSpeed = .normal
    private(set) var brief: MobileBriefV1?
    private(set) var hubID: String?
    /// Called when a brief's audio has been stored on this phone. The notice waits for it (LocalBriefNotices).
    var onAudioStored: (@MainActor (MobileBriefV1, String) -> Void)?

    private let cache: BriefAudioCache
    private let prefetchCache: BriefAudioCache?
    private let requestAudio: @MainActor (MobileBriefV1) async throws -> BriefAudioResource
    private let downloadAudio: @MainActor (BriefAudio) async throws -> Data
    private let makeEngine: @MainActor (URL) throws -> BriefAudioPlayback
    private let managesSystemPlayback: Bool
    private let authorizationReceipt: @MainActor () -> BriefAudioReceipt?
    private let resolveAuthorizationReceipt: @MainActor (MobileBriefV1) async -> BriefAudioReceipt?
    private var engine: BriefAudioPlayback?
    private var audio: BriefAudio?
    private var localURL: URL?
    private var generation: UInt64 = 0
    private var prefetchGeneration: UInt64 = 0
    private var prefetchTask: Task<Void, Never>?
    private var prefetchIdentity: String?
    private var prefetchAllowsHub = false
    private var timer: Timer?
    private var observers: [NSObjectProtocol] = []
    private var remoteTargets: [(MPRemoteCommand, Any)] = []
    private var resumeAfterInterruption = false
    private var lastSavedPosition: TimeInterval = 0
    private var requestedPosition: TimeInterval?
    private var reauthorizedAfterWithdrawal = false
    private var requestedWithdrawalReceipt: BriefAudioReceipt?
    private var waitingForPrefetch = false
    /// The briefs to fetch audio for, newest first, and the run that is fetching them (see fetchRecentAudio).
    private var recentAudioQueue: (briefs: [MobileBriefV1], hubID: String)?
    private var recentAudioTask: Task<Void, Never>?
    private var recentAudioGeneration: UInt64 = 0
    private var playbackTapTime: TimeInterval?
    private var awaitingFirstFrame = false
    private var firstFramePosition: TimeInterval = 0

    private func playbackTiming(_ stage: String) {
        guard let start = playbackTapTime else { return }
        NSLog("BriefAudio timing %@ %.1f ms", stage, (ProcessInfo.processInfo.systemUptime - start) * 1000)
    }

    init(cache: BriefAudioCache = .shared, prefetchCache: BriefAudioCache? = nil,
         requestAudio: @escaping @MainActor (MobileBriefV1) async throws -> BriefAudioResource,
         downloadAudio: @escaping @MainActor (BriefAudio) async throws -> Data,
         makeEngine: @escaping @MainActor (URL) throws -> BriefAudioPlayback = { url in
             let player = try AVAudioPlayer(contentsOf: url)
             player.enableRate = true
             return player
         },
         managesSystemPlayback: Bool = true,
         authorizationReceipt: @escaping @MainActor () -> BriefAudioReceipt? = { nil },
         resolveAuthorizationReceipt: @escaping @MainActor (MobileBriefV1) async -> BriefAudioReceipt? = { _ in nil }) {
        self.cache = cache
        self.prefetchCache = prefetchCache
        self.requestAudio = requestAudio
        self.downloadAudio = downloadAudio
        self.makeEngine = makeEngine
        self.managesSystemPlayback = managesSystemPlayback
        self.authorizationReceipt = authorizationReceipt
        self.resolveAuthorizationReceipt = resolveAuthorizationReceipt
        if let saved = cache.readRecall()?.speed, let chosen = BriefPlaybackSpeed(rawValue: saved) {
            speed = chosen
        }
        super.init()
    }

    deinit {
        timer?.invalidate()
        observers.forEach(NotificationCenter.default.removeObserver)
        remoteTargets.forEach { $0.0.removeTarget($0.1) }
    }

    func matches(brief: MobileBriefV1, hubID: String) -> Bool {
        self.hubID == hubID && self.brief.map { BriefAudioResource.versionKey(for: $0) } == BriefAudioResource.versionKey(for: brief)
    }

    func prepare(brief: MobileBriefV1, hubID: String) async {
        _ = await prepareSelection(brief: brief, hubID: hubID)
    }

    func prepareForSeeking(brief: MobileBriefV1, hubID: String) async {
        _ = await prepareSelection(brief: brief, hubID: hubID, localFirst: true)
    }

    // Fetch the latest audio without changing the selected player or stopping
    // an older brief. Relay metadata is usable only with a verified receipt;
    // an observed withdrawal still requires the Mac to reauthorize it.
    func prefetch(brief: MobileBriefV1, hubID: String, allowHubRequest: Bool = true) async {
        let identity = "\(hubID)|\(BriefAudioResource.versionKey(for: brief))"
        if prefetchIdentity == identity, let prefetchTask {
            let token = prefetchGeneration
            let upgrade = allowHubRequest && !prefetchAllowsHub
            await prefetchTask.value
            if upgrade, prefetchGeneration == token,
               cache.cached(brief: brief, hubID: hubID) == nil,
               prefetchCache?.cached(brief: brief, hubID: hubID) == nil {
                self.prefetchTask = nil
                prefetchIdentity = nil
                await prefetch(brief: brief, hubID: hubID, allowHubRequest: true)
            }
            await startWaitingPlayback(brief: brief, hubID: hubID)
            return
        }
        prefetchTask?.cancel()
        prefetchGeneration &+= 1
        let token = prefetchGeneration
        prefetchIdentity = identity
        prefetchAllowsHub = allowHubRequest
        let task = Task { [weak self] in
            guard let self else { return }
            await self.fetchAudio(brief: brief, hubID: hubID, allowHubRequest: allowHubRequest,
                                  isCurrent: { token == self.prefetchGeneration })
        }
        prefetchTask = task
        await task.value
        if prefetchGeneration == token { prefetchTask = nil; prefetchIdentity = nil }
        if prefetchGeneration == token { await startWaitingPlayback(brief: brief, hubID: hubID) }
    }

    private func startWaitingPlayback(brief: MobileBriefV1, hubID: String) async {
        guard waitingForPrefetch, matches(brief: brief, hubID: hubID), state == .loading,
              let cached = cache.cached(brief: brief, hubID: hubID) ?? prefetchCache?.cached(brief: brief, hubID: hubID) else { return }
        waitingForPrefetch = false
        self.brief = brief
        audio = cached.audio
        localURL = cached.url
        duration = finiteDuration(cached.audio.durationSeconds ?? 0)
        position = boundedPosition(cached.position)
        state = .ready
        await toggle()
    }

    /// Keeps the last 7 days of briefs playable offline: every brief in `briefs` from that window with no audio on this
    /// phone gets its audio, newest first, one at a time. It runs apart from the single prefetch above, so a Play
    /// tap or a newer brief never cancels it. A call while a run is in progress queues its list for that run.
    func fetchRecentAudio(_ briefs: [MobileBriefV1], hubID: String, now: Date = Date()) {
        recentAudioQueue = (Self.recent(briefs, now: now), hubID)
        guard recentAudioTask == nil else { return }
        recentAudioTask = Task { [weak self] in
            await self?.runRecentAudio()
            self?.recentAudioTask = nil
        }
    }

    /// Waits for the run started by fetchRecentAudio, if any. Tests and the UI test harness use it.
    func finishRecentAudio() async {
        await recentAudioTask?.value
    }

    private func runRecentAudio() async {
        while let next = recentAudioQueue {
            recentAudioQueue = nil
            let generation = recentAudioGeneration
            // A brief whose package carries no audio yet gets it in a later package (text first, audio right after).
            for brief in next.briefs where brief.audio != nil && !brief.audioIsWithdrawn
                && !hasAudioOnPhone(brief: brief, hubID: next.hubID) {
                guard !Task.isCancelled, generation == recentAudioGeneration else { break }
                await fetchAudio(brief: brief, hubID: next.hubID, allowHubRequest: true,
                                 isCurrent: { generation == self.recentAudioGeneration && !Task.isCancelled })
            }
        }
    }

    /// The briefs whose audio the 7 days keep: newest first, each brief once, and nothing older than the window.
    static func recent(_ briefs: [MobileBriefV1], now: Date) -> [MobileBriefV1] {
        let cutoff = now.addingTimeInterval(-BriefAudioCache.retentionInterval)
        var seen: Set<String> = []
        return briefs.sorted { ($0.asOfDate ?? .distantPast) > ($1.asOfDate ?? .distantPast) }
            .filter { ($0.asOfDate ?? .distantPast) > cutoff && seen.insert($0.identifier).inserted }
    }

    private func fetchAudio(brief: MobileBriefV1, hubID: String, allowHubRequest: Bool, isCurrent: @escaping @MainActor () -> Bool) async {
        let destination = prefetchCache ?? cache
        if let withdrawal = cache.latestWithdrawalReceipt(brief: brief, hubID: hubID),
           authorizationReceipt().map({ withdrawal.samePairing(as: $0) }) ?? true {
            destination.recordWithdrawal(withdrawal, brief: brief, hubID: hubID)
        }
        guard !brief.audioIsWithdrawn else { return }
        if cache.cached(brief: brief, hubID: hubID) != nil || destination.cached(brief: brief, hubID: hubID) != nil {
            audioRevision &+= 1
            return
        }
        do {
            let receipt = await resolveAuthorizationReceipt(brief)
            guard isCurrent(), !Task.isCancelled else { return }
            let withdrawal = cache.latestWithdrawalReceipt(brief: brief, hubID: hubID)
            let requestReceipt = authorizationReceipt()
            if let withdrawal, let requestReceipt, !withdrawal.samePairing(as: requestReceipt) {
                cache.recordWithdrawal(requestReceipt, brief: brief, hubID: hubID)
                destination.recordWithdrawal(requestReceipt, brief: brief, hubID: hubID)
            }
            let resource: BriefAudioResource
            let reauthorized: Bool
            if withdrawal == nil, receipt != nil, let audio = brief.audio, audio.attachment != nil {
                resource = BriefAudioResource(briefId: brief.identifier,
                                              version: BriefAudioResource.versionKey(for: brief), audio: audio)
                reauthorized = false
            } else {
                guard allowHubRequest else { return }
                resource = try await requestAudio(brief)
                reauthorized = withdrawal != nil
            }
            guard isCurrent(), !Task.isCancelled,
                  resource.briefId == brief.identifier,
                  resource.version == BriefAudioResource.versionKey(for: brief) else { return }
            let data = try await downloadAudio(resource.audio)
            guard isCurrent(), !Task.isCancelled else { return }
            let latestWithdrawal = cache.latestWithdrawalReceipt(brief: brief, hubID: hubID)
            if let latestWithdrawal {
                guard reauthorized, let requestReceipt, requestReceipt.covers(latestWithdrawal) else { return }
            }
            _ = try await destination.storeInBackground(data, brief: brief, hubID: hubID, audio: resource.audio,
                                reauthorizedAfterWithdrawal: reauthorized,
                                withdrawalReceipt: reauthorized ? requestReceipt : receipt)
            onAudioStored?(brief, hubID)
            audioRevision &+= 1
        } catch {
            // The text remains available and a later refresh can retry.
        }
    }

    // Called at brief receipt, even when Feed is offscreen or another brief is open.
    // Advancing generation also fences out metadata/download completions already in flight.
    func receive(brief: MobileBriefV1, hubID: String, fromCache: Bool = false, receipt: BriefAudioReceipt? = nil) {
        guard brief.audioIsWithdrawn else { return }
        let authorization = cache.withdrawalState(brief: brief, hubID: hubID)
        if let receipt {
            if authorization.reauthorized && authorization.receipt?.covers(receipt) == true { return }
            // A refetch also cannot cancel recovery already requested after this receipt.
            let pending = matches(brief: brief, hubID: hubID) ? requestedWithdrawalReceipt : nil
            if pending?.covers(receipt) == true { return }
            // Same-computer re-pairing keeps local briefs. An old pairing's cached projection
            // cannot revoke a recording freshly authorized in the current pairing.
            if fromCache, let cursor = authorizationReceipt(), !receipt.samePairing(as: cursor),
               (authorization.reauthorized && authorization.receipt?.samePairing(as: cursor) == true) ||
                pending?.samePairing(as: cursor) == true { return }
            cache.recordWithdrawal(receipt, brief: brief, hubID: hubID)
            prefetchCache?.recordWithdrawal(receipt, brief: brief, hubID: hubID)
        } else if fromCache && authorization.reauthorized { return }
        prefetchGeneration &+= 1
        prefetchTask?.cancel()
        prefetchTask = nil
        prefetchIdentity = nil
        if matches(brief: brief, hubID: hubID) {
            stop()
            self.brief = brief
            self.hubID = hubID
            state = .unavailable(.notAvailable)
        }
        cache.invalidate(brief: brief, hubID: hubID)
        prefetchCache?.invalidate(brief: brief, hubID: hubID)
        audioRevision &+= 1
    }

    // Bind the whole button action to its selection, including time spent awaiting metadata.
    // Checking the generation also rejects an old A action after selection changed A -> B -> A.
    func toggle(brief: MobileBriefV1, hubID: String, tappedAt: TimeInterval? = nil, localFirst: Bool = false) async {
        playbackTapTime = tappedAt ?? ProcessInfo.processInfo.systemUptime
        playbackTiming("handler")
        let token = await prepareSelection(brief: brief, hubID: hubID, localFirst: localFirst)
        guard token == generation, matches(brief: brief, hubID: hubID), !Task.isCancelled else { return }
        if localFirst, audio == nil {
            if state == .loading { waitingForPrefetch = true }
            return
        }
        await toggle()
    }

    private func prepareSelection(brief: MobileBriefV1, hubID: String, localFirst: Bool = false) async -> UInt64 {
        receive(brief: brief, hubID: hubID, fromCache: true)
        if let withdrawal = cache.latestWithdrawalReceipt(brief: brief, hubID: hubID),
           authorizationReceipt().map({ withdrawal.samePairing(as: $0) }) ?? true {
            prefetchCache?.recordWithdrawal(withdrawal, brief: brief, hubID: hubID)
        }
        if matches(brief: brief, hubID: hubID), state != .idle {
            if !localFirst || audio != nil || state == .loading { return generation }
        }
        stop()
        self.brief = brief
        self.hubID = hubID
        let token = generation
        state = .loading
        if brief.audioIsWithdrawn && !cache.withdrawalState(brief: brief, hubID: hubID).reauthorized {
            state = .unavailable(.notAvailable)
            return token
        }
        if let cached = cache.cached(brief: brief, hubID: hubID) ?? prefetchCache?.cached(brief: brief, hubID: hubID) {
            audio = cached.audio
            localURL = cached.url
            duration = finiteDuration(cached.audio.durationSeconds ?? 0)
            position = boundedPosition(cached.position)
            state = .ready
            return token
        }
        if prefetchIdentity == "\(hubID)|\(BriefAudioResource.versionKey(for: brief))", let prefetchTask {
            await prefetchTask.value
            guard token == generation, !Task.isCancelled else { return token }
        } else if localFirst {
            await prefetch(brief: brief, hubID: hubID, allowHubRequest: false)
            guard token == generation, !Task.isCancelled else { return token }
        }
        if let cached = cache.cached(brief: brief, hubID: hubID) ?? prefetchCache?.cached(brief: brief, hubID: hubID) {
            audio = cached.audio
            localURL = cached.url
            duration = finiteDuration(cached.audio.durationSeconds ?? 0)
            position = boundedPosition(cached.position)
            state = .ready
            return token
        }
        if localFirst { state = .loading }
        else { await fetchMetadata(brief: brief, token: token) }
        return token
    }

    // An unavailable row stays tappable: the computer may have finished rendering or come online.
    func toggle() async {
        if state == .playing { pause(); return }
        guard state != .loading, let brief, hubID != nil else { return }
        let token = generation
        if audio == nil {
            await fetchMetadata(brief: brief, token: token)
            guard token == generation, !Task.isCancelled, audio != nil else { return }
        }
        if let engine { playbackTiming("source ready"); start(engine); return }
        state = .loading
        do {
            if localURL == nil {
                guard let audio else { throw BriefAudioError.notAvailable }
                let plaintext = try await downloadAudio(audio)
                try Task.checkCancellation()
                guard token == generation, let hubID else { return }
                localURL = try await cache.storeInBackground(plaintext, brief: brief, hubID: hubID, audio: audio,
                                          reauthorizedAfterWithdrawal: reauthorizedAfterWithdrawal,
                                          withdrawalReceipt: requestedWithdrawalReceipt)
                onAudioStored?(brief, hubID)
                audioRevision &+= 1
                position = boundedPosition(requestedPosition ?? cache.cached(brief: brief, hubID: hubID)?.position ?? 0)
            }
            try Task.checkCancellation()
            guard token == generation, let localURL else { return }
            playbackTiming("source ready")
            let engine = try makeEngine(localURL)
            guard engine.duration.isFinite, engine.duration > 0 else { throw BriefAudioError.unreadable }
            self.engine = engine
            (engine as? AVAudioPlayer)?.delegate = self
            duration = engine.duration
            position = boundedPosition(position)
            engine.currentTime = position
            start(engine)
        } catch {
            guard token == generation else { return }
            if error is CancellationError { state = audio == nil ? .idle : .ready }
            else { record(error) }
        }
    }

    func pause() {
        resumeAfterInterruption = false
        engine?.pause()
        tick()
        persistPosition()
        if engine != nil { state = .paused }
        timer?.invalidate()
        timer = nil
        updateNowPlaying()
        #if os(iOS)
        if managesSystemPlayback {
            try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        }
        #endif
    }

    func seek(to seconds: TimeInterval) {
        guard seconds.isFinite else { return }
        position = boundedPosition(seconds)
        requestedPosition = position
        engine?.currentTime = position
        persistPosition()
        updateNowPlaying()
    }

    func stop() {
        generation &+= 1
        pause()
        recordRecall(playing: false)
        engine?.stop()
        engine = nil
        audio = nil
        localURL = nil
        brief = nil
        hubID = nil
        position = 0
        duration = 0
        lastSavedPosition = 0
        requestedPosition = nil
        reauthorizedAfterWithdrawal = false
        requestedWithdrawalReceipt = nil
        waitingForPrefetch = false
        resumeAfterInterruption = false
        started = false
        finished = false
        state = .idle
        removeSystemControls()
    }

    @discardableResult
    func resetForPairing(hubID: String?) -> Bool {
        prefetchGeneration &+= 1
        recentAudioGeneration &+= 1
        recentAudioQueue = nil
        prefetchTask?.cancel()
        prefetchTask = nil
        prefetchIdentity = nil
        stop()
        self.hubID = hubID
        let primaryCleared = cache.clear()
        let prefetchCleared = prefetchCache?.clear() ?? true
        return primaryCleared && prefetchCleared
    }

    @discardableResult
    func clearCache() -> Bool {
        prefetchGeneration &+= 1
        recentAudioGeneration &+= 1
        recentAudioQueue = nil
        prefetchTask?.cancel()
        prefetchTask = nil
        prefetchIdentity = nil
        stop()
        let primaryCleared = cache.clear()
        let prefetchCleared = prefetchCache?.clear() ?? true
        return primaryCleared && prefetchCleared
    }

    private func fetchMetadata(brief: MobileBriefV1, token: UInt64) async {
        state = .loading
        // Older cached history has no pairing scope. Resolve its envelope before metadata;
        // never assign an old sequence to a new pairing just because the brief matches.
        let resolved = await resolveAuthorizationReceipt(brief)
        guard token == generation, !Task.isCancelled else { return }
        if let resolved, let hubID { cache.recordWithdrawal(resolved, brief: brief, hubID: hubID) }
        let knownWithdrawal = hubID.map { cache.withdrawalState(brief: brief, hubID: $0) }
        let observed = hubID.flatMap { cache.latestWithdrawalReceipt(brief: brief, hubID: $0) }
        let followsWithdrawal = brief.audioIsWithdrawn || knownWithdrawal?.withdrawn == true ||
            knownWithdrawal?.reauthorized == true || observed != nil
        // Capture before requesting metadata: a successful response supersedes receipts
        // already accepted in this pairing, including briefs cached by an older app build.
        if let cursor = authorizationReceipt() {
            requestedWithdrawalReceipt = observed?.covers(cursor) == true ? observed : cursor
            if let observed, !observed.samePairing(as: cursor),
               let hubID {
                cache.recordWithdrawal(cursor, brief: brief, hubID: hubID)
            }
        } else {
            requestedWithdrawalReceipt = observed
        }
        state = .loading
        do {
            let resource = try await requestAudio(brief)
            try Task.checkCancellation()
            guard token == generation else { return }
            guard resource.briefId == brief.identifier,
                  resource.version == BriefAudioResource.versionKey(for: brief) else {
                throw BriefAudioError.versionChanged
            }
            guard let bytes = resource.audio.bytes, bytes > 0,
                  bytes <= BriefAudioCache.maximumAudioBytes else { throw BriefAudioError.tooLarge }
            audio = resource.audio
            reauthorizedAfterWithdrawal = followsWithdrawal
            duration = finiteDuration(resource.audio.durationSeconds ?? 0)
            state = .ready
        } catch {
            guard token == generation else { return }
            if error is CancellationError { state = .idle }
            else { record(error) }
        }
    }

    private func record(_ error: Error) {
        if let audioError = error as? BriefAudioError { state = .unavailable(audioError) }
        else { state = .failed(BriefAudioError.unreadable.errorDescription ?? "") }
    }

    private func start(_ engine: BriefAudioPlayback) {
        do {
            #if os(iOS)
            if managesSystemPlayback {
                try AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
                try AVAudioSession.sharedInstance().setActive(true)
            }
            #endif
            if position >= duration { seek(to: 0) }
            engine.rate = Float(speed.rawValue)
            guard engine.play() else { throw BriefAudioError.unreadable }
            state = .playing
            started = true
            finished = false
            playbackTiming("playing state")
            awaitingFirstFrame = true
            firstFramePosition = engine.currentTime
            persistPosition(played: false)
            installSystemControls()
            updateNowPlaying()
            timer?.invalidate()
            timer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
                Task { @MainActor [weak self] in self?.tick() }
            }
        } catch {
            pause()
            record(error)
        }
    }

    // MARK: Brief view and mini player

    /// What this phone remembers about a brief's playback, from either audio cache.
    func playbackMemory(brief: MobileBriefV1, hubID: String) -> BriefPlaybackMemory? {
        cache.playbackMemory(brief: brief, hubID: hubID) ?? prefetchCache?.playbackMemory(brief: brief, hubID: hubID)
    }

    /// Whether the brief's audio is on this phone, in either cache.
    func hasAudioOnPhone(brief: MobileBriefV1, hubID: String) -> Bool {
        cache.cached(brief: brief, hubID: hubID) != nil || prefetchCache?.cached(brief: brief, hubID: hubID) != nil
    }

    /// Play is dimmed when the audio is not on this phone and the computer cannot be reached to send it.
    func canStart(brief: MobileBriefV1, hubID: String, computerOnline: Bool?) -> Bool {
        hasAudioOnPhone(brief: brief, hubID: hubID) || computerOnline != false
    }

    /// The written paragraph being spoken, by its index in the brief's narrative. Only meaningful for the brief the
    /// player holds.
    var spokenParagraph: Int? {
        BriefAudioTimeline.narrativeParagraph(at: position, in: brief?.audio?.paragraphs)
    }

    /// Jumps to a written paragraph's start. Nothing happens when the paragraph has no timing.
    func seek(toParagraph index: Int) {
        guard let start = BriefAudioTimeline.start(ofNarrative: index, in: brief?.audio?.paragraphs) else { return }
        seek(to: start)
    }

    /// Back or ahead by a number of seconds, within the audio.
    func skip(by seconds: TimeInterval) {
        seek(to: position + seconds)
    }

    /// The brief view's "Play from here": starts this brief at a paragraph. A brief not yet loaded is loaded first.
    func play(brief: MobileBriefV1, hubID: String, fromParagraph index: Int) async {
        if matches(brief: brief, hubID: hubID), state == .playing {
            seek(toParagraph: index)
            return
        }
        await toggle(brief: brief, hubID: hubID)
        guard matches(brief: brief, hubID: hubID), state == .playing else { return }
        seek(toParagraph: index)
    }

    func setSpeed(_ value: BriefPlaybackSpeed) {
        speed = value
        engine?.rate = Float(value.rawValue)
        var recall = cache.readRecall() ?? BriefPlaybackRecall()
        recall.speed = value.rawValue
        try? cache.writeRecall(recall)
        updateNowPlaying()
    }

    /// Dismisses the player: stops it, saves where it stopped, and forgets it for a restart.
    func dismiss() {
        stop()
    }

    /// After a restart, the brief that was playing within the last 30 minutes comes back paused, where it stopped.
    /// Nothing starts by itself. `lookup` reads the brief from the phone's library.
    func restoreRecentSession(hubID: String?, now: Date = Date(), lookup: (String) -> MobileBriefV1?) {
        guard state == .idle, brief == nil, let hubID, let recall = cache.readRecall(), recall.hubID == hubID,
              let briefID = recall.briefID, let playingAt = recall.playingAt,
              now.timeIntervalSince(playingAt) <= 30 * 60, let restored = lookup(briefID) else { return }
        let memory = playbackMemory(brief: restored, hubID: hubID)
        brief = restored
        self.hubID = hubID
        position = max(0, memory?.seconds ?? 0)
        duration = finiteDuration(memory?.duration ?? restored.audio?.durationSeconds ?? 0)
        started = true
    }

    /// Writes the recall: the brief and when it was last playing while `playing`, or clears the brief.
    private func recordRecall(playing: Bool) {
        var recall = cache.readRecall() ?? BriefPlaybackRecall()
        recall.speed = speed.rawValue
        if playing, let brief, let hubID {
            recall.briefID = brief.identifier
            recall.hubID = hubID
            recall.playingAt = Date()
        } else {
            recall.briefID = nil
            recall.hubID = nil
            recall.playingAt = nil
        }
        try? cache.writeRecall(recall)
    }

    func tick() {
        guard let engine else { return }
        position = boundedPosition(engine.currentTime)
        if awaitingFirstFrame, position > firstFramePosition {
            playbackTiming("first frame observed")
            awaitingFirstFrame = false
        }
        if abs(position - lastSavedPosition) >= 5 { persistPosition() }
        updateNowPlaying()
    }

    private func persistPosition(played: Bool? = nil) {
        guard let brief, let hubID else { return }
        do {
            try cache.savePosition(position, duration: duration, played: played, brief: brief, hubID: hubID)
            try prefetchCache?.savePosition(position, duration: duration, played: played, brief: brief, hubID: hubID)
            lastSavedPosition = position
        } catch {
            // Playback stays usable while protected storage is temporarily unavailable.
        }
        if state == .playing { recordRecall(playing: true) }
    }

    private func finiteDuration(_ value: TimeInterval) -> TimeInterval {
        value.isFinite && value > 0 ? value : 0
    }

    private func boundedPosition(_ value: TimeInterval) -> TimeInterval {
        guard value.isFinite else { return 0 }
        return max(0, duration > 0 ? min(value, duration) : value)
    }

    func playbackFinished() {
        pause()
        seek(to: 0)
        persistPosition(played: true)
        recordRecall(playing: false)
        state = .ready
        finished = true
    }

    nonisolated func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        Task { @MainActor [weak self] in
            guard let self, self.engine as? AVAudioPlayer === player else { return }
            if flag { self.playbackFinished() }
            else { self.pause(); self.record(BriefAudioError.unreadable) }
        }
    }

    nonisolated func audioPlayerDecodeErrorDidOccur(_ player: AVAudioPlayer, error: Error?) {
        Task { @MainActor [weak self] in
            guard let self, self.engine as? AVAudioPlayer === player else { return }
            self.pause()
            self.record(BriefAudioError.unreadable)
        }
    }

    func interruptionBegan() {
        let wasPlaying = state == .playing
        pause()
        resumeAfterInterruption = wasPlaying
    }

    func interruptionEnded(shouldResume: Bool) {
        let resume = resumeAfterInterruption && shouldResume
        resumeAfterInterruption = false
        if resume, let engine { start(engine) }
    }

    func headphonesDisconnected() {
        resumeAfterInterruption = false
        pause()
    }

    private func installSystemControls() {
        guard managesSystemPlayback, remoteTargets.isEmpty else { return }
        let center = MPRemoteCommandCenter.shared()
        center.playCommand.isEnabled = true
        center.pauseCommand.isEnabled = true
        center.togglePlayPauseCommand.isEnabled = true
        center.changePlaybackPositionCommand.isEnabled = true
        center.skipForwardCommand.preferredIntervals = [15]
        center.skipBackwardCommand.preferredIntervals = [15]
        center.skipForwardCommand.isEnabled = true
        center.skipBackwardCommand.isEnabled = true
        remoteTargets.append((center.skipForwardCommand, center.skipForwardCommand.addTarget { [weak self] _ in
            Task { @MainActor [weak self] in self?.skip(by: 15) }
            return .success
        }))
        remoteTargets.append((center.skipBackwardCommand, center.skipBackwardCommand.addTarget { [weak self] _ in
            Task { @MainActor [weak self] in self?.skip(by: -15) }
            return .success
        }))
        remoteTargets.append((center.playCommand, center.playCommand.addTarget { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self, let engine = self.engine, self.state != .playing else { return }
                self.start(engine)
            }
            return .success
        }))
        remoteTargets.append((center.pauseCommand, center.pauseCommand.addTarget { [weak self] _ in
            Task { @MainActor [weak self] in self?.pause() }
            return .success
        }))
        remoteTargets.append((center.togglePlayPauseCommand, center.togglePlayPauseCommand.addTarget { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self, let engine = self.engine else { return }
                if self.state == .playing { self.pause() }
                else { self.start(engine) }
            }
            return .success
        }))
        remoteTargets.append((center.changePlaybackPositionCommand,
                              center.changePlaybackPositionCommand.addTarget { [weak self] event in
            guard let event = event as? MPChangePlaybackPositionCommandEvent else { return .commandFailed }
            let position = event.positionTime
            Task { @MainActor [weak self] in self?.seek(to: position) }
            return .success
        }))
        #if os(iOS)
        let notifications = NotificationCenter.default
        observers.append(notifications.addObserver(forName: AVAudioSession.interruptionNotification,
                                                    object: nil, queue: .main) { [weak self] notification in
            let type = (notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? NSNumber)?.uintValue
            let options = (notification.userInfo?[AVAudioSessionInterruptionOptionKey] as? NSNumber)?.uintValue ?? 0
            Task { @MainActor [weak self] in
                switch type {
                case AVAudioSession.InterruptionType.began.rawValue: self?.interruptionBegan()
                case AVAudioSession.InterruptionType.ended.rawValue:
                    self?.interruptionEnded(shouldResume: AVAudioSession.InterruptionOptions(rawValue: options).contains(.shouldResume))
                default: break
                }
            }
        })
        observers.append(notifications.addObserver(forName: AVAudioSession.routeChangeNotification,
                                                    object: nil, queue: .main) { [weak self] notification in
            let reason = (notification.userInfo?[AVAudioSessionRouteChangeReasonKey] as? NSNumber)?.uintValue
            if reason == AVAudioSession.RouteChangeReason.oldDeviceUnavailable.rawValue {
                Task { @MainActor [weak self] in self?.headphonesDisconnected() }
            }
        })
        #endif
    }

    private func updateNowPlaying() {
        guard managesSystemPlayback, engine != nil else { return }
        MPNowPlayingInfoCenter.default().nowPlayingInfo = nowPlayingInfo
    }

    /// The opening words of the written paragraph being spoken, or nil when there is none to show.
    private var spokenOpening: String? {
        guard let index = spokenParagraph, let paragraphs = brief?.narrative, paragraphs.indices.contains(index),
              let text = paragraphs[index].text else { return nil }
        let words = BriefAudioTimeline.openingWords(text)
        return words.isEmpty ? nil : words
    }

    /// What the lock screen shows: the brief's title, then the opening words of the paragraph being spoken.
    var nowPlayingInfo: [String: Any] {
        [
            MPMediaItemPropertyTitle: brief?.titleText ?? String(localized: "Brief"),
            MPMediaItemPropertyArtist: spokenOpening ?? "Isoform",
            MPMediaItemPropertyAlbumTitle: "Isoform",
            MPMediaItemPropertyPlaybackDuration: duration,
            MPNowPlayingInfoPropertyElapsedPlaybackTime: position,
            MPNowPlayingInfoPropertyPlaybackRate: state == .playing ? Double(speed.rawValue) : 0.0,
            MPNowPlayingInfoPropertyDefaultPlaybackRate: 1.0,
            MPNowPlayingInfoPropertyMediaType: MPNowPlayingInfoMediaType.audio.rawValue
        ]
    }

    private func removeSystemControls() {
        guard managesSystemPlayback else { return }
        remoteTargets.forEach { $0.0.removeTarget($0.1) }
        remoteTargets.removeAll()
        observers.forEach(NotificationCenter.default.removeObserver)
        observers.removeAll()
        MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
    }
}

/// "1:05" for a playback position or length. Shared by the brief view and the mini player.
enum BriefPlaybackTime {
    static func text(_ seconds: TimeInterval) -> String {
        let total = seconds.isFinite ? Int(min(max(0, seconds), 7 * 24 * 3600).rounded(.down)) : 0
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}
