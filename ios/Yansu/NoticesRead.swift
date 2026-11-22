import Foundation
import UserNotifications

// Read anywhere, cleared everywhere (notify A; PROTOCOL.md 9.4). A notice read on the computer, the watch or this
// phone leaves every device: the computer sends a silent `withdraw` push, and the phone also asks
// GET /notices/read?since= when it opens, which is the guarantee (the push reaches phones only after the gateway is
// redeployed). Ids are a moment notice's `yansu.id`, and `brief:<briefId>` for the brief.

struct NoticeReadEntry: Codable, Equatable, Sendable {
    let id: String
    let at: String
}

struct NoticeReadList: Codable, Equatable, Sendable {
    var read: [NoticeReadEntry]
}

enum NoticeWithdraw {
    /// A silent `withdraw` push: `yansu {"kind":"withdraw","hub","ids":[…]}`.
    static func parse(_ userInfo: [AnyHashable: Any]) -> (hub: String, ids: [String])? {
        guard let yansu = userInfo["yansu"] as? [String: Any], yansu["kind"] as? String == "withdraw",
              let hub = yansu["hub"] as? String, let ids = yansu["ids"] as? [String] else { return nil }
        let safe = ids.filter(NoticeIdentifier.isSafe).prefix(50)
        return safe.isEmpty ? nil : (hub, Array(safe))
    }

    /// The notice ids a delivered or pending notification stands for: its moment notice id, and `brief:<id>` for the
    /// brief (a remote brief push or the phone's own brief notice).
    static func ids(of userInfo: [AnyHashable: Any]) -> Set<String> {
        var ids = Set<String>()
        if let yansu = userInfo["yansu"] as? [String: Any], let id = yansu["id"] as? String { ids.insert(id) }
        if let brief = userInfo["localBriefID"] as? String { ids.insert("brief:" + brief) }
        if let yansu = userInfo["yansu"] as? [String: Any], let brief = yansu["briefId"] as? String {
            ids.insert("brief:" + brief)
        }
        return ids
    }

    /// Takes the read notices off this phone: delivered ones from the list, and pending local ones (a brief notice
    /// waiting for its slot) so they never ring.
    static func withdraw(_ ids: Set<String>, center: UNUserNotificationCenter = .current()) async {
        guard !ids.isEmpty else { return }
        let delivered = await center.deliveredNotifications()
            .filter { !Self.ids(of: $0.request.content.userInfo).isDisjoint(with: ids) }
            .map(\.request.identifier)
        if !delivered.isEmpty { center.removeDeliveredNotifications(withIdentifiers: delivered) }
        let pending = await center.pendingNotificationRequests()
            .filter { !Self.ids(of: $0.content.userInfo).isDisjoint(with: ids) }
            .map(\.identifier)
        if !pending.isEmpty { center.removePendingNotificationRequests(withIdentifiers: pending) }
    }
}

protocol NoticesReadAPI: Sendable {
    func noticesReadAvailable() async throws -> Bool
    func noticesRead(since: String?) async throws -> NoticeReadList
    func markNoticesRead(_ ids: [String]) async throws
}

extension HubChannel: NoticesReadAPI {
    func noticesReadAvailable() async throws -> Bool { try await abilities().noticesRead == true }

    func noticesRead(since: String?) async throws -> NoticeReadList {
        try await perform(.noticesRead(since: since)).payload(NoticeReadList.self)
    }

    func markNoticesRead(_ ids: [String]) async throws {
        let body = try JSONEncoder().encode(["ids": ids])
        _ = try await perform(.write(path: "/notices/read", body: String(decoding: body, as: UTF8.self)))
    }
}

/// The poll and the phone's own reads, per paired computer.
@MainActor
final class NoticesReadStore {
    private let api: NoticesReadAPI
    private let defaults: UserDefaults
    private let hubID: () -> String?
    private let withdraw: (Set<String>) async -> Void
    private var supported: Bool?
    /// Briefs already marked read from this phone, so Today showing one again doesn't post again.
    private var marked = Set<String>()

    init(api: NoticesReadAPI, defaults: UserDefaults = .standard, hubID: @escaping () -> String?,
         withdraw: @escaping (Set<String>) async -> Void = { await NoticeWithdraw.withdraw($0) }) {
        self.api = api
        self.defaults = defaults
        self.hubID = hubID
        self.withdraw = withdraw
    }

    private func sinceKey(_ hub: String) -> String { "notices.read.since." + hub }

    /// Reads what was read elsewhere since the last poll and takes it off this phone.
    func poll() async {
        guard let hub = hubID(), await isSupported() else { return }
        let since = defaults.string(forKey: sinceKey(hub))
        guard let list = try? await api.noticesRead(since: since) else { return }
        await withdraw(Set(list.read.map(\.id)))
        // Oldest first: the last one is where the next poll starts.
        if let last = list.read.last?.at { defaults.set(last, forKey: sinceKey(hub)) }
    }

    /// Today showed the brief: it's read here, so it leaves the computer, the watch and the lock screen.
    func briefShown(_ briefID: String) async {
        let id = "brief:" + briefID
        guard NoticeIdentifier.isSafe(id), !marked.contains(id), await isSupported() else { return }
        marked.insert(id)
        await withdraw([id])
        do { try await api.markNoticesRead([id]) } catch { marked.remove(id) }
    }

    func clear() {
        supported = nil
        marked.removeAll()
    }

    private func isSupported() async -> Bool {
        if let supported { return supported }
        let value = (try? await api.noticesReadAvailable()) ?? false
        supported = value
        return value
    }
}
