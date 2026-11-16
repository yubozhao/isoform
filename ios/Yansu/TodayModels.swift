import Foundation

// The phone's bounded, locally retained projection of the Mac's Today page.
struct MobileTodaySnapshot: Codable, Equatable, Sendable {
    var generatedAt: String
    var latestBrief: TodayBriefIndex?
    var briefs: [TodayBriefIndex]
    var needsYou: [TodayNeed]
    var needsYouCount: Int
    var day: TodayDay
    var topics: [MobileTopic]
    var briefRequest: TodayBriefRequest?
    var schedule: MobileBriefSchedule
    var keepAwake: Bool? = nil
    var instantPush: Bool? = nil
    var briefSchedule: [ScheduledPhoneBrief]? = nil
    var noticePacing: TodayNoticePacing? = nil
    /// The one current brief, by id and version (PROTOCOL.md 20.7a). Nil on a Mac before 1.0.
    var currentBrief: TodayBriefRef? = nil
    var tasksVersion: String? = nil
    /// An opaque change token for Your work (B2): a new one tells the phone to read `/work` again.
    var workVersion: String? = nil
    /// The brief's status (B1, PROTOCOL.md 8.2): the current brief, a build in progress, the next one.
    var briefStatus: MobileBriefStatus? = nil
    /// Set only while the Mac confirms the chosen assistant is signed out (R75). No button on the phone.
    var assistantSignOut: TodayAssistantSignOut? = nil

    var generatedDate: Date? { RelayTime.parse(generatedAt) }
}

struct MobileBriefStatus: Codable, Equatable, Sendable {
    struct Current: Codable, Equatable, Sendable {
        let id: String
        var version: String? = nil
        var kind: String? = nil
        var madeAt: String? = nil
        var audio: Bool? = nil
    }
    struct Making: Codable, Equatable, Sendable {
        var kind: String? = nil
        var slot: String? = nil
        var startedAt: String? = nil
        var readyAt: String? = nil
    }
    struct Next: Codable, Equatable, Sendable {
        var kind: String? = nil
        var slot: String? = nil
        var dueAt: String? = nil
    }
    var current: Current? = nil
    var making: Making? = nil
    var next: Next? = nil
}

/// The Mac's confirmed sign-out for the chosen assistant (PROTOCOL.md 20.7a).
struct TodayAssistantSignOut: Codable, Equatable, Sendable {
    var assistant: String

    /// The assistant's product name, as the Mac words it.
    var displayName: String { assistant == "codex" ? "Codex" : "Claude Code" }
}

struct TodayNoticePacing: Codable, Equatable, Sendable {
    var activeToday: Int
    var lastQuietBreak: String?
}

struct TodayBriefIndex: Codable, Equatable, Sendable, Identifiable {
    var id: String
    var slot: String
    var localDate: String
    var scheduledAt: String?
    var madeAt: String
    var catchUp: Bool?
    var durationSeconds: Double?
    var lead: String
    var bodyVersion: String? = nil
    /// morning, evening, week or on_request (PROTOCOL.md 8.1). Nil on a Mac before 1.0.
    var kind: String? = nil
    /// The SHA-256 hex of the brief's package. Nil on a Mac before 1.0, where bodyVersion stands in.
    var version: String? = nil

    var madeDate: Date? { RelayTime.parse(madeAt) }
}

struct TodayBriefRef: Codable, Equatable, Sendable {
    var id: String
    var version: String
}

struct TodayNeed: Codable, Equatable, Sendable, Identifiable {
    struct Card: Codable, Equatable, Sendable {
        var proactive: ChatProactiveView?
    }
    var id: String
    var chatId: String?
    var card: Card?
    var task: MobileTask?
    var noticeId: String? = nil
    var createdAt: String? = nil
    var kind: String? = nil
    var priority: String? = nil
    var expiresAt: String? = nil
    var destinationKind: String? = nil
    var destinationId: String? = nil
}

struct TodayDay: Codable, Equatable, Sendable {
    struct Event: Codable, Equatable, Sendable {
        var title: String
        var start: String
        var end: String
        var id: String? = nil
        var kind: String? = nil
    }
    struct Range: Codable, Equatable, Sendable {
        var start: String
        var end: String
    }
    var next: Event?
    var free: [Range]
    var calendarConnected: Bool
}

struct TodayMeetingPrep: Decodable, Equatable, Sendable {
    let meetingId: String
    let meetingTitle: String
    let personName: String
    let youOwe: String?
    let theyAsked: String?
    let lastTime: String?
}

struct TodayBriefRequest: Codable, Equatable, Sendable {
    var state: String
    var readyAt: String
    var readyDate: Date? { RelayTime.parse(readyAt) }
    func futureReadyDate(at now: Date) -> Date? {
        guard let readyDate, readyDate > now else { return nil }
        return readyDate
    }
}

struct MobileTodayEvent: Decodable, Sendable {
    var event: String
    var version: UInt64
    var snapshot: MobileTodaySnapshot?
}

struct MobileTodayResponse: Decodable {
    var snapshot: MobileTodaySnapshot?
    var version: UInt64?
    var parts: Int?

    private enum CodingKeys: String, CodingKey { case chunked, version, parts }
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        if (try? container.decode(Bool.self, forKey: .chunked)) == true {
            version = try container.decode(UInt64.self, forKey: .version)
            parts = try container.decode(Int.self, forKey: .parts)
            snapshot = nil
        } else {
            snapshot = try MobileTodaySnapshot(from: decoder)
            version = nil
            parts = nil
        }
    }
}

struct MobileTodayPart: Decodable {
    var version: UInt64
    var part: Int
    var data: String
}
