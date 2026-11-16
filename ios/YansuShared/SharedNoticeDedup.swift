import CryptoKit
import Foundation

/// The extension records a delivered APNs notice even if its banner is later dismissed.
/// Files contain no notice text and are scoped to the exact pairing.
enum SharedNoticeDedup {
    private static func verifiedTestID(_ id: String) -> String { "verified-test:" + id }

    @discardableResult
    static func recordVerifiedTest(hub: String, device: String, id: String, directory: URL? = defaultDirectory) -> Bool {
        record(hub: hub, device: device, id: verifiedTestID(id), directory: directory)
    }

    static func containsVerifiedTest(hub: String, device: String, id: String, directory: URL? = defaultDirectory) -> Bool {
        contains(hub: hub, device: device, id: verifiedTestID(id), directory: directory)
    }

    /// The computer's brief_ready notice for this brief reached this phone (PROTOCOL.md 9.1): the local "Your brief is
    /// ready" yields to it, so the brief is one notification, not two.
    private static func remoteBriefID(_ briefID: String) -> String { "remote-brief:" + briefID }

    @discardableResult
    static func recordRemoteBrief(hub: String, device: String, briefID: String, directory: URL? = defaultDirectory) -> Bool {
        record(hub: hub, device: device, id: remoteBriefID(briefID), directory: directory)
    }

    static func containsRemoteBrief(hub: String, device: String, briefID: String, directory: URL? = defaultDirectory) -> Bool {
        contains(hub: hub, device: device, id: remoteBriefID(briefID), directory: directory)
    }

    static var defaultDirectory: URL? {
        AppGroup.containerURL?.appendingPathComponent("Isoform/notice-dedup", isDirectory: true)
    }

    private static func file(hub: String, device: String, id: String, directory: URL) -> URL {
        let framed = [hub, device, id].map { "\($0.utf8.count):\($0)" }.joined(separator: "|")
        let digest = SHA256.hash(data: Data(framed.utf8)).map { String(format: "%02x", $0) }.joined()
        return directory.appendingPathComponent(digest)
    }

    @discardableResult
    static func record(hub: String, device: String, id: String, directory: URL? = defaultDirectory) -> Bool {
        guard !hub.isEmpty, !device.isEmpty, !id.isEmpty, let directory else { return false }
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try Data().write(to: file(hub: hub, device: device, id: id, directory: directory),
                             options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            return true
        } catch { return false }
    }

    static func contains(hub: String, device: String, id: String, directory: URL? = defaultDirectory) -> Bool {
        guard let directory else { return false }
        return FileManager.default.fileExists(atPath: file(hub: hub, device: device, id: id, directory: directory).path)
    }

    static func prune(directory: URL? = defaultDirectory, now: Date = Date()) {
        guard let directory, let files = try? FileManager.default.contentsOfDirectory(at: directory,
            includingPropertiesForKeys: [.contentModificationDateKey], options: [.skipsHiddenFiles]) else { return }
        for file in files {
            guard let date = try? file.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate,
                  now.timeIntervalSince(date) > 14 * 24 * 60 * 60 else { continue }
            try? FileManager.default.removeItem(at: file)
        }
    }
}
