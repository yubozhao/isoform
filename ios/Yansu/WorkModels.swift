import Foundation

// Your work: the one list where everything Isoform puts to the person on the phone is judged (phone-today v1.0.0
// section 2, phone-map v1.0.0 section 4). The wire shapes are backend items B2 (`GET /work`) and B3
// (`POST /work/{id}/answer`), agreed with the phone-connection session. Until a computer advertises `work` in
// `GET /status`, the phone shows the rows it already has (Needs you, running and done tasks) in the same places.

/// One item of Your work (B2, agreed v1: side-session note phone-redesign-backend-shapes.md).
struct WorkItem: Codable, Equatable, Hashable, Identifiable, Sendable {
    /// Shared with the computer: `brief:<briefId>:<itemKey>`, `topic:…`, `task:…`, `followup:…`, `prep:…`, and so on.
    let id: String
    /// `done` (work Isoform did), `offered` (work it would do), `claim`, `result` (back from your agent) or `check`.
    let kind: String
    /// The word on an Up next row: `draft`, `prep`, `check`, `offered`, `claim` or `result`.
    var row: String? = nil
    /// `open`, `answered`, `with_agent` or `back`.
    var state: String
    /// `brief`, `topic`, `watch`, `prep`, `agent_of_you` or `agent`.
    let source: String
    var ref: WorkRef? = nil
    /// The first line's parts, composed on the computer; joined with " · ".
    let first: [WorkFirstPart]
    /// The one sentence of what Isoform noticed, shown in bold.
    let noticed: String
    let work: WorkBody
    var yes: WorkYes? = nil
    var why: [WorkLabel]? = nil
    /// What the work is made from, as chips in Edit.
    var gets: [WorkLabel]? = nil
    var reasons: [String]? = nil
    var answer: WorkAnswer? = nil
    var agent: WorkAgent? = nil
    let createdAt: String
    /// The computer's order; the clients group by state.
    var order: Int? = nil
    /// One agent asking another (`kind` `request`, phone-agent v1.2.0 §1): who asks whom, and what would move.
    var request: WorkRequest? = nil
    /// When a request ends by itself (RFC 3339).
    var expiresAt: String? = nil
    /// A result's files (v1.2.0 §2): where each lives, and whether the phone may read it.
    var files: [WorkFile]? = nil
    /// Work taken back from an agent, offered again (v1.2.1 rule 5).
    var takenBack: WorkTakenBack? = nil

    var createdDate: Date? { RelayTime.parse(createdAt) }
    var isOpen: Bool { state == "open" || state == "back" }
    /// "For your 10:00 with Lena · tomorrow · from the brief". Parts with no text are times the phone words for the
    /// reader's day and clock (`key` `when` or `clock`, with `at`).
    var firstLine: String { firstLine(now: Date()) }

    func firstLine(now: Date, calendar: Calendar = .current, locale: Locale = .current) -> String {
        first.compactMap { part -> String? in
            if !part.text.isEmpty { return part.text }
            guard let at = part.at.flatMap(RelayTime.parse) else { return nil }
            return part.key == "clock" ? BriefClock.text(at, locale: locale) : YourWork.dayWord(at, now: now, calendar: calendar, locale: locale)
        }.joined(separator: " · ")
    }

    /// What Yes does, with the opening time the computer sends apart from the words.
    var yesLine: String? {
        guard let yes else { return nil }
        guard let at = yes.at.flatMap(RelayTime.parse), yes.does == "keep" else { return yes.line }
        return yes.line + " " + String.localizedStringWithFormat(String(localized: "work.yes.opensAt"), BriefClock.text(at, locale: .current))
    }
    var briefID: String? { ref?.briefId }
}

struct WorkAgentName: Codable, Equatable, Hashable, Sendable {
    var agentId: String? = nil
    let name: String
}

struct WorkRequest: Codable, Equatable, Hashable, Sendable {
    let from: WorkAgentName
    let to: WorkAgentName
    /// "What would move", in the grey box.
    var moves: String? = nil
}

struct WorkFile: Codable, Equatable, Hashable, Identifiable, Sendable {
    let id: String
    let name: String
    /// "branch halcyon/test-plan": always said, with "on your computer".
    var `where`: String? = nil
    /// Text the phone may open read-only; anything else opens on the computer.
    var readable: Bool? = nil
    var bytes: Int? = nil
}

struct WorkTakenBack: Codable, Equatable, Hashable, Sendable {
    var agentId: String? = nil
    let agentName: String
    let at: String
}

struct WorkRef: Codable, Equatable, Hashable, Sendable {
    var briefId: String? = nil
    var itemKey: String? = nil
    var topicId: String? = nil
    var proposalId: String? = nil
    var conversationId: String? = nil
    var saidAt: String? = nil
    var eventId: String? = nil
    var handoffId: String? = nil
    var jobId: String? = nil
    var passId: String? = nil
    var taskId: String? = nil
    var offerId: String? = nil
    /// The paragraph a brief item sits under, when the computer sends it.
    var paragraph: Int? = nil
}

struct WorkFirstPart: Codable, Equatable, Hashable, Sendable {
    let text: String
    var ref: String? = nil
    /// `when` (a day) or `clock` (a time) for a part the phone words from `at`.
    var key: String? = nil
    var at: String? = nil
}

struct WorkBody: Codable, Equatable, Hashable, Sendable {
    let title: String
    var text: String? = nil
    /// On work it offers: how long it would take, shown as "About 10 min".
    var minutes: Int? = nil

    var estimate: String? {
        minutes.map { String.localizedStringWithFormat(String(localized: "work.minutes"), $0) }
    }
}

/// What Yes does: `keep`, `mail_draft`, `agent`, `right` or `open`, and its line in the computer's words.
struct WorkYes: Codable, Equatable, Hashable, Sendable {
    let does: String
    let line: String
    var at: String? = nil
    var agent: String? = nil
    /// `code` or `else`: which of the person's two agents gets offered work (B4).
    var kind: String? = nil
}

struct WorkLabel: Codable, Equatable, Hashable, Sendable {
    var id: String? = nil
    let label: String
    var ref: String? = nil
    var removable: Bool? = nil
}

struct WorkEdited: Codable, Equatable, Hashable, Sendable {
    var before: String? = nil
    var after: String? = nil
}

struct WorkAnswer: Codable, Equatable, Hashable, Sendable {
    let action: String
    var reason: String? = nil
    var edited: WorkEdited? = nil
    let at: String
    var surface: String? = nil
    /// The done line: "Reply to Priya: in your Mail drafts".
    let line: String
    var undoUntil: String? = nil
    /// Phone-local: kept on the phone until the computer can be reached. Never on the wire from the computer.
    var waits: Bool? = nil
    /// A Yes recorded without sending: "{title}: yes · not sent", with Send in its row (26.1).
    var notSent: Bool? = nil

    var undoDate: Date? { undoUntil.flatMap(RelayTime.parse) }
}

struct WorkAgent: Codable, Equatable, Hashable, Sendable {
    let name: String
    /// Omitted by the computer for some running work (a topic's proposal): never required (review 2026-11-15).
    var since: String? = nil
    var sinceDate: Date? { since.flatMap(RelayTime.parse) }
}

struct WorkPage: Codable, Equatable, Sendable {
    var v: Int? = 1
    var version: String? = nil
    var generatedAt: String? = nil
    var toAnswer: Int
    var backFromAgents: Int? = nil
    var undoUntil: String? = nil
    var items: [WorkItem]
}

/// One answer from the phone (B3). Sent once, in order, with its key; kept on the phone while the computer is away.
struct WorkAnswerRequest: Codable, Equatable, Sendable {
    let idempotencyKey: String
    let action: String
    var reason: String? = nil
    var before: String? = nil
    var after: String? = nil
    var surface = "phone"
    var answeredAt: String? = nil
    var queuedAt: String? = nil
    /// Grows per phone; the computer refuses an answer older than the last it applied to that item.
    var seq: Int? = nil
    /// False on the first tap of a Yes on offered work: recorded, nothing sent until Send (26.1).
    var send: Bool? = nil
    /// What it gets, removed in the hand-off, and the agent picked for this one hand-off.
    var removed: [String]? = nil
    var agentId: String? = nil
    /// Open on your computer: the file to open (`open_file`, v1.2.0 rule 3).
    var file: String? = nil
}

struct WorkAnswerResult: Decodable, Equatable, Sendable {
    let item: WorkItem
}

/// The actions and reasons of the shared item (phone-map rules 5 and 7).
enum WorkAction: String, CaseIterable, Sendable {
    case yes, no, notMine = "not_mine", edit, useful, notUseful = "not_useful", undo
    /// The hand-off's Send, after a Yes recorded with `send: false` (PROTOCOL.md 26.1).
    case send
    /// Opens a result's file on the computer; not kept in the ledger.
    case openFile = "open_file"
}

enum WorkReason: String, CaseIterable, Sendable {
    case alreadyDone = "already_done", notNow = "not_now", notUseful = "not_useful", myself
    case notNeeded = "not_needed", wrongFacts = "wrong_facts", notHowIdSayIt = "not_how_id_say"
    case wasTrueOnce = "was_true_once", onlySometimes = "only_sometimes", notAtAll = "not_at_all"

    /// The reasons No offers for an item's kind (phone-map rule 7). None is needed.
    static func choices(for item: WorkItem) -> [WorkReason] {
        // The computer's reasons win when it sends them (B2); the kind's own list stands in otherwise.
        if item.kind == "request" { return [] }
        if let sent = item.reasons?.compactMap(WorkReason.init(rawValue:)), !sent.isEmpty { return sent }
        return choices(for: item.kind)
    }

    static func choices(for kind: String) -> [WorkReason] {
        switch kind {
        case "done": return [.notNeeded, .wrongFacts, .notHowIdSayIt, .myself]
        case "claim": return [.wasTrueOnce, .onlySometimes, .notAtAll]
        // An agent's request takes Yes or No and nothing more (v1.2.0 rule 1).
        case "request": return []
        default: return [.alreadyDone, .notNow, .notUseful, .myself]
        }
    }

    var title: String {
        switch self {
        case .alreadyDone: return String(localized: "work.reason.alreadyDone")
        case .notNow: return String(localized: "work.reason.notNow")
        case .notUseful: return String(localized: "work.reason.notUseful")
        case .myself: return String(localized: "work.reason.myself")
        case .notNeeded: return String(localized: "work.reason.notNeeded")
        case .wrongFacts: return String(localized: "work.reason.wrongFacts")
        case .notHowIdSayIt: return String(localized: "work.reason.notHowIdSayIt")
        case .wasTrueOnce: return String(localized: "work.reason.wasTrueOnce")
        case .onlySometimes: return String(localized: "work.reason.onlySometimes")
        case .notAtAll: return String(localized: "work.reason.notAtAll")
        }
    }
}

/// Your work's groups, decided from the list alone so every state is tested without a view (phone-today rules 8 to 16).
enum YourWork {
    /// The most items a day (redesign v1.0.0, rule 15).
    static let dailyLimit = 10
    /// The Up next rows under the open item.
    static let upNextLimit = 3

    struct Groups: Equatable {
        /// Open items in order; the first waits open.
        var open: [WorkItem]
        var withAgent: [WorkItem]
        var doneToday: [WorkItem]

        var toAnswer: Int { open.count }
        var upNext: [WorkItem] { Array(open.dropFirst().prefix(YourWork.upNextLimit)) }
        var isEmpty: Bool { open.isEmpty && withAgent.isEmpty && doneToday.isEmpty }
    }

    /// The order: the brief's items first, in the brief's order; a result back on top of what's left; then what came
    /// since, newest first, with prep due soonest at the top. The computer sends brief items in the brief's order, so
    /// their relative order is kept as sent.
    static func ordered(_ items: [WorkItem]) -> [WorkItem] {
        // The computer's order wins when it sends one (B2); the rules below stand in for a list without it.
        if items.allSatisfy({ $0.order != nil }) {
            return items.enumerated().sorted { ($0.element.order!, $0.offset) < ($1.element.order!, $1.offset) }.map(\.element)
        }
        let indexed = Array(items.enumerated())
        func rank(_ item: WorkItem) -> Int {
            if item.source == "brief" { return 0 }
            if item.kind == "result" || item.state == "back" { return 1 }
            if item.source == "prep" { return 2 }
            return 3
        }
        return indexed.sorted { lhs, rhs in
            let (l, r) = (rank(lhs.element), rank(rhs.element))
            if l != r { return l < r }
            if l == 0 { return lhs.offset < rhs.offset }
            let (ld, rd) = (lhs.element.createdDate ?? .distantPast, rhs.element.createdDate ?? .distantPast)
            if l == 2, ld != rd { return ld < rd }
            if ld != rd { return ld > rd }
            return lhs.offset < rhs.offset
        }.map(\.element)
    }

    static func groups(_ items: [WorkItem], now: Date, calendar: Calendar = .current) -> Groups {
        let open = Array(ordered(items.filter(\.isOpen)).prefix(dailyLimit))
        let withAgent = items.filter { $0.state == "with_agent" }
            .sorted { ($0.agent?.sinceDate ?? .distantPast) > ($1.agent?.sinceDate ?? .distantPast) }
        let done = items.filter { item in
            guard item.state == "answered", let answer = item.answer, let at = RelayTime.parse(answer.at) else { return false }
            return calendar.isDate(at, inSameDayAs: now)
        }.sorted { (RelayTime.parse($0.answer?.at ?? "") ?? .distantPast) > (RelayTime.parse($1.answer?.at ?? "") ?? .distantPast) }
        return Groups(open: open, withAgent: withAgent, doneToday: done)
    }

    /// "today", "tomorrow", "yesterday" or the weekday, for a first line's `when` part.
    static func dayWord(_ date: Date, now: Date, calendar: Calendar = .current, locale: Locale = .current) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return String(localized: "work.day.today") }
        if let next = calendar.date(byAdding: .day, value: 1, to: now), calendar.isDate(date, inSameDayAs: next) {
            return String(localized: "work.day.tomorrow")
        }
        if let previous = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(date, inSameDayAs: previous) {
            return String(localized: "work.day.yesterday")
        }
        return date.formatted(.dateTime.weekday(.wide).locale(locale))
    }

    /// A result back from your agent is judged Useful or Not useful, once (phone-map rule 9).
    static func asksUseful(_ item: WorkItem) -> Bool {
        item.kind == "result" || item.state == "back"
    }

    /// The rows the computer still sends as waiting on the person (Needs you) that Your work doesn't list: a blocked
    /// task stays reachable from Today once the Tasks tab is gone (review 2026-11-15).
    static func waitingOutside(_ needs: [TodayNeed], items: [WorkItem]) -> [TodayNeed] {
        let listed = Set(items.map(\.id))
        return needs.filter { need in
            if let task = need.task { return !listed.contains("task:" + task.id) && !listed.contains(task.id) }
            return !listed.contains(need.id)
        }
    }

    /// Whether Undo still shows on a done line: until tomorrow's build, which the computer names as `undoUntil`.
    static func canUndo(_ answer: WorkAnswer, now: Date) -> Bool {
        guard let until = answer.undoDate else { return false }
        return now < until
    }

    /// The phone's own done line for an answer it just gave, before the computer's words come back.
    static func localLine(for item: WorkItem, action: WorkAction, reason: WorkReason?) -> String {
        let title = item.work.title
        switch action {
        case .yes, .edit, .useful:
            return String.localizedStringWithFormat(String(localized: "work.done.yes"), title)
        case .no, .notUseful:
            if let reason { return String.localizedStringWithFormat(String(localized: "work.done.noReason"), title, reason.title.lowercased()) }
            return String.localizedStringWithFormat(String(localized: "work.done.no"), title)
        case .notMine:
            return String.localizedStringWithFormat(String(localized: "work.done.notMine"), title)
        case .undo, .openFile:
            return title
        case .send:
            return String.localizedStringWithFormat(String(localized: "work.done.yes"), title)
        }
    }

    /// The item as it shows the moment it is answered on the phone, before the computer confirms.
    static func answered(_ item: WorkItem, action: WorkAction, reason: WorkReason?, at: Date, waits: Bool) -> WorkItem {
        var next = item
        if action == .undo {
            next.state = "open"
            next.answer = nil
            return next
        }
        // The computer decides whether a Yes on offered work is with your agent; its item replaces this one.
        next.state = "answered"
        let undo = Calendar.current.startOfDay(for: at.addingTimeInterval(86_400)).addingTimeInterval(7 * 3600)
        next.answer = WorkAnswer(action: action.rawValue, reason: reason?.rawValue, at: RelayTime.format(at),
                                 line: localLine(for: item, action: action, reason: reason),
                                 undoUntil: RelayTime.format(undo), waits: waits)
        return next
    }
}
