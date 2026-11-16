import Foundation

// Sealed notices (PROTOCOL.md section 14). The push carries the ciphertext in `yansu.ct`, a fixed fallback
// body the relay can read, and the category and thread. The notification service extension opens the notice
// and sets the sealed title and body; the app reads the plaintext from `yansu.notice`.
// The decision category (`YANSU_DECISION`, with Approve and Decline) left with the Desk. A notice that still
// names it does not open, so it keeps the relay's fallback text.
enum NoticeCategory: String, CaseIterable, Sendable {
    case moment = "YANSU_MOMENT"
    case brief = "YANSU_BRIEF"
    /// Phone-only categories the extension sets when a notice can be answered from the notification (PROTOCOL.md
    /// 9.2): meeting prep with Not needed, finished work with Useful. The sealed plaintext keeps YANSU_MOMENT.
    case prep = "YANSU_PREP"
    case result = "YANSU_RESULT"
}

/// The answers a notification can give without opening the app (PROTOCOL.md 9.2), and the category that shows them.
enum NoticeWorkAnswer: String, Sendable {
    case notNeeded = "not_needed"
    case useful

    /// The phone category for a notice whose plaintext names a work item and carries this answer's action.
    static func category(for plaintext: NoticePlaintext) -> NoticeCategory? {
        guard plaintext.workItemID != nil else { return nil }
        let ids = Set(plaintext.actions.map(\.id))
        switch plaintext.kind {
        case "meeting_prep" where ids.contains(NoticeWorkAnswer.notNeeded.rawValue): return .prep
        case "work_finished" where ids.contains(NoticeWorkAnswer.useful.rawValue): return .result
        default: return nil
        }
    }

    /// The B3 answer body: Not needed is a No with that reason, Useful is Useful; the key is the notice's own.
    func body(noticeID: String, answeredAt: String) -> [String: String] {
        switch self {
        case .notNeeded:
            return ["idempotencyKey": "notice:\(noticeID):not_needed", "action": "no", "reason": "not_needed",
                    "surface": "notification", "answeredAt": answeredAt]
        case .useful:
            return ["idempotencyKey": "notice:\(noticeID):useful", "action": "useful",
                    "surface": "notification", "answeredAt": answeredAt]
        }
    }
}

enum NoticeRoute: Equatable, Sendable {
    case chat(momentID: String?)
    case brief
}

enum NoticeRouting {
    /// Blocked and finished work open Chat, where one-time work reports back. A work notice names the work, not a
    /// chat, so it opens the chat. Every other notice, including a kind this build does not know, opens the Feed.
    static func route(kind: String, momentID: String? = nil) -> NoticeRoute {
        switch kind {
        case "brief_ready", "weekly_recap", "brief_missed": return .brief
        case "work_blocked", "work_finished": return .chat(momentID: momentID)
        case "feed", "goals": return .brief
        case "": return .brief
        default:
            guard let momentID, NoticeIdentifier.isSafe(momentID) else { return .brief }
            return .chat(momentID: momentID)
        }
    }
}

enum NoticeReceiptState: String, CaseIterable, Sendable {
    case delivered
    case opened
    case acted
    case dismissed
}

enum NoticeIdentifier {
    /// Ids the relay route accepts in a path: 1 to 128 printable ASCII bytes with no slash, question mark,
    /// hash or percent sign.
    static func isSafe(_ value: String) -> Bool {
        guard !value.isEmpty, value.utf8.count <= 128 else { return false }
        return value.utf8.allSatisfy { byte in
            (0x21...0x7E).contains(byte) && ![0x2F, 0x3F, 0x23, 0x25].contains(byte)
        }
    }
}

struct NoticeAction: Codable, Equatable, Sendable {
    let id: String
    let label: String
    let auth: Bool
}

extension NoticeAction {
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = (try? container.decodeIfPresent(String.self, forKey: .id)) ?? ""
        label = (try? container.decodeIfPresent(String.self, forKey: .label)) ?? ""
        auth = (try? container.decodeIfPresent(Bool.self, forKey: .auth)) ?? false
    }
}

/// The sealed plaintext (PROTOCOL.md section 14.3). Unknown fields are ignored and optional fields may be absent.
struct NoticePlaintext: Codable, Equatable, Sendable {
    let version: Int
    let id: String
    let kind: String
    let title: String
    let body: String?
    let category: String
    let actions: [NoticeAction]
    let subjectID: String?
    let momentID: String?
    let destinationKind: String?
    let destinationID: String?
    /// The Your work item a meeting_prep or work_finished notice answers (PROTOCOL.md 9.2), on a phone that reads /work.
    let workItemID: String?
    let createdAt: String
    let expiresAt: String?

    enum CodingKeys: String, CodingKey {
        case version = "v"
        case id
        case kind
        case title
        case body
        case category
        case actions
        case subjectID = "subjectId"
        case momentID = "momentId"
        case destinationKind
        case destinationID = "destinationId"
        case workItemID = "workItemId"
        case createdAt
        case expiresAt
    }
}

extension NoticePlaintext {
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        version = (try? container.decodeIfPresent(Int.self, forKey: .version)) ?? 0
        id = try container.decode(String.self, forKey: .id)
        kind = try container.decode(String.self, forKey: .kind)
        title = try container.decode(String.self, forKey: .title)
        body = try? container.decodeIfPresent(String.self, forKey: .body)
        category = (try? container.decodeIfPresent(String.self, forKey: .category)) ?? ""
        actions = (try? container.decodeIfPresent([NoticeAction].self, forKey: .actions)) ?? []
        let subject: String? = try? container.decodeIfPresent(String.self, forKey: .subjectID)
        subjectID = subject.flatMap { $0.isEmpty ? nil : $0 }
        let moment: String? = try? container.decodeIfPresent(String.self, forKey: .momentID)
        momentID = moment.flatMap { NoticeIdentifier.isSafe($0) ? $0 : nil }
        let kind: String? = try? container.decodeIfPresent(String.self, forKey: .destinationKind)
        let target: String? = try? container.decodeIfPresent(String.self, forKey: .destinationID)
        if let kind, ["brief", "task", "chat"].contains(kind), let target, NoticeIdentifier.isSafe(target) {
            destinationKind = kind
            destinationID = target
        } else {
            destinationKind = nil
            destinationID = nil
        }
        let work: String? = try? container.decodeIfPresent(String.self, forKey: .workItemID)
        workItemID = work.flatMap { !$0.isEmpty && $0.utf8.count <= 256 && !$0.contains("/") ? $0 : nil }
        createdAt = (try? container.decodeIfPresent(String.self, forKey: .createdAt)) ?? ""
        let expires: String? = try? container.decodeIfPresent(String.self, forKey: .expiresAt)
        expiresAt = expires.flatMap { $0.isEmpty ? nil : $0 }
    }
}

/// What the push itself says about the notice. `ct` is present only for sealed notices.
struct NoticePush: Equatable, Sendable {
    /// The key the app and the extension use for the decrypted JSON.
    static let noticeUserInfoKey = "yansu.notice"

    let kind: String
    let id: String
    let hub: String
    let dev: String
    let ct: String?

    /// Recognises a sealed or fallback notice by its `yansu` block. Legacy pushes carry no notice id and return nil.
    static func parse(_ userInfo: [AnyHashable: Any]) -> NoticePush? {
        guard let yansu = userInfo["yansu"] as? [String: Any],
              let kind = yansu["kind"] as? String,
              let id = yansu["id"] as? String,
              NoticeIdentifier.isSafe(id) else { return nil }
        return NoticePush(kind: kind, id: id,
                          hub: (yansu["hub"] as? String) ?? "",
                          dev: (yansu["dev"] as? String) ?? "",
                          ct: yansu["ct"] as? String)
    }

    /// The decrypted notice the extension attached for the app, when there is one.
    static func plaintext(in userInfo: [AnyHashable: Any]) -> NoticePlaintext? {
        guard let json = userInfo[noticeUserInfoKey] as? String else { return nil }
        return try? JSONDecoder().decode(NoticePlaintext.self, from: Data(json.utf8))
    }
}

/// The body of POST /v1/notices/{noticeId}/receipts.
struct NoticeReceiptBody: Codable, Equatable, Sendable {
    let state: String
    let action: String
    let at: String
}
