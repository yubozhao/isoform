import Foundation

/// What came of a Done task (PROTOCOL.md 20.7b). The phone words it from its own catalog.
struct MobileTaskOutcome: Codable, Equatable, Hashable, Sendable {
    let kind: String
    let by: String
    let at: String
    var params: [String: String]? = nil

    var date: Date? { RelayTime.parse(at) }
    var byYou: Bool { by == "you" }
}

/// Where a task came from: Up next, a brief chip, Chat, your list or a routine.
struct MobileTaskOrigin: Codable, Equatable, Hashable, Sendable {
    let kind: String
    let at: String
    var label: String? = nil
    var slot: String? = nil
}

struct MobileTaskChoice: Codable, Equatable, Hashable, Identifiable, Sendable {
    let id: String
    let label: String
}

struct MobileTaskFile: Codable, Equatable, Hashable, Sendable {
    let name: String
}

/// One entry in a thread: Isoform's message, your reply or tap, a quiet line, or a file the task made.
struct MobileTaskEvent: Codable, Equatable, Hashable, Identifiable, Sendable {
    let id: String
    let kind: String
    let at: String
    var text: String? = nil
    var choices: [MobileTaskChoice]? = nil
    var chosenId: String? = nil
    var file: MobileTaskFile? = nil

    var date: Date? { RelayTime.parse(at) }
    /// Answer buttons stay only until one is tapped.
    var openChoices: [MobileTaskChoice] { chosenId == nil ? (choices ?? []) : [] }
}

struct MobileTaskReplyRequest: Encodable, Equatable, Sendable {
    let idempotencyKey: String
    let text: String
}

struct MobileTaskActionRequest: Encodable, Equatable, Sendable {
    let idempotencyKey: String
    let action: String
    var title: String? = nil
    var eventId: String? = nil
    var choiceId: String? = nil
    /// Send with your own words when you edited the draft.
    var text: String? = nil
    var draftRevision: String? = nil
    /// When a send was queued while the computer was away (PROTOCOL.md section 23). Absent for a live send.
    var queuedAt: String? = nil
}

/// A send that waited for the computer past its hour and needs the person's yes (PROTOCOL.md section 23).
struct TaskSendConfirmation: Equatable, Identifiable, Sendable {
    /// The journal key of a draft send, or empty for a send made from the thread.
    let key: String
    let taskID: String
    let text: String?
    let draftRevision: String?
    /// The person the reply is for, when the computer knows them.
    var person: String? = nil
    var id: String { key.isEmpty ? taskID : key }

    /// The prompt, naming the person when there is one: "the reply" otherwise.
    static func title(person: String?) -> Text {
        if let person = RecipientName.name(person) {
            return Text("Your computer is back. Send the reply to \(person) now?")
        }
        return Text("Your computer is back. Send the reply now?")
    }
}

enum TaskSendWait {
    /// A send queued longer ago than this asks again (PROTOCOL.md section 23).
    static let askAfter: TimeInterval = 60 * 60
    /// The relay keeps a waiting request this long; past it the send did not happen.
    static let relayRetention: TimeInterval = 24 * 60 * 60

    /// The queue time a send carries: when the person pressed Send, if it was queued (nil for a live send).
    static func queuedAt(_ date: Date?) -> String? {
        guard let date else { return nil }
        return ISO8601DateFormatter().string(from: date)
    }
}

/// Where a thread was opened from; its back button names it.
enum TaskThreadSource: Equatable, Sendable {
    case tasks, today, chat

    var backTitle: String {
        switch self {
        case .tasks: return String(localized: "Tasks")
        case .today: return String(localized: "Today")
        case .chat: return String(localized: "Chat")
        }
    }
}

/// The words of a thread. Times use the phone's locale; every sentence exists in English and Chinese.
enum TaskThreadCopy {
    enum Tone: Equatable { case needsYou, running, done, scheduled }

    static func clock(_ date: Date, locale: Locale) -> String {
        date.formatted(Date.FormatStyle(date: .omitted, time: .shortened).locale(locale))
    }

    /// "10:12 AM" today, "Yesterday, 4:20 PM", otherwise "Thursday, 4:20 PM".
    static func when(_ date: Date, now: Date = Date(), locale: Locale, calendar: Calendar = .current) -> String {
        let time = clock(date, locale: locale)
        if calendar.isDate(date, inSameDayAs: now) { return time }
        if calendar.isDateInYesterday(date) {
            return String.localizedStringWithFormat(String(localized: "Yesterday, %@"), time)
        }
        let day = date.formatted(Date.FormatStyle().weekday(.wide).locale(locale))
        return String.localizedStringWithFormat(String(localized: "%1$@, %2$@"), day, time)
    }

    static func tone(_ task: MobileTask) -> Tone {
        switch task.group {
        case "needs_you": return .needsYou
        case "running": return .running
        case "scheduled": return .scheduled
        default: return .done
        }
    }

    /// The current step's number in a running task, counting from one.
    static func step(_ detail: MobileTaskDetail) -> (current: Int, total: Int)? {
        guard !detail.steps.isEmpty else { return nil }
        let current = detail.steps.firstIndex { $0.state == "current" }.map { $0 + 1 }
            ?? detail.steps.firstIndex { $0.state == "pending" }.map { $0 + 1 }
            ?? detail.steps.count
        return (current, detail.steps.count)
    }

    /// The status line under a thread's title: its label and the detail after the dot.
    static func status(task: MobileTask, detail: MobileTaskDetail?, now: Date = Date(),
                       locale: Locale) -> (label: String, detail: String?) {
        switch tone(task) {
        case .needsYou:
            let updated = task.updatedDate.map {
                String.localizedStringWithFormat(String(localized: "updated %@"), when($0, now: now, locale: locale))
            }
            return (String(localized: "thread.status.needsYou"), updated)
        case .running:
            if let detail, let step = step(detail) {
                return (String.localizedStringWithFormat(String(localized: "Running · step %1$d of %2$d"),
                                                         step.current, step.total), nil)
            }
            if let progress = task.progress, progress.total > 0 {
                return (String.localizedStringWithFormat(String(localized: "Running · step %1$d of %2$d"),
                                                         min(progress.done + 1, progress.total), progress.total), nil)
            }
            return (String(localized: "Running · starting"), nil)
        case .scheduled:
            guard let date = task.updatedDate else { return (String(localized: "Scheduled"), nil) }
            return (String.localizedStringWithFormat(String(localized: "Scheduled · %@"),
                                                     upcoming(date, now: now, locale: locale)), nil)
        case .done:
            let date = task.outcome?.date ?? task.updatedDate ?? now
            return (String.localizedStringWithFormat(String(localized: "Done · %@"), when(date, now: now, locale: locale)), nil)
        }
    }

    /// The gray line that opens a thread, or nil when the computer did not record where it came from.
    static func origin(_ origin: MobileTaskOrigin?, now: Date = Date(), locale: Locale) -> String? {
        guard let origin, let at = RelayTime.parse(origin.at) else { return nil }
        let label = origin.label?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        switch origin.kind {
        case "up_next":
            return String.localizedStringWithFormat(String(localized: "From Up next · you tapped Do it at %@"),
                                                    clock(at, locale: locale))
        case "brief_chip":
            guard !label.isEmpty else { return nil }
            let slot = origin.slot.flatMap(RelayTime.parse) ?? at
            return String.localizedStringWithFormat(String(localized: "From your %1$@ brief · %2$@"),
                                                    clock(slot, locale: locale), label)
        case "chat":
            return String.localizedStringWithFormat(String(localized: "You asked in Chat · %@"),
                                                    when(at, now: now, locale: locale))
        case "list":
            return String.localizedStringWithFormat(String(localized: "From your list · %@"), clock(at, locale: locale))
        case "routine":
            let slot = origin.slot.flatMap(RelayTime.parse) ?? at
            let day = slot.formatted(Date.FormatStyle().weekday(.wide).locale(locale))
            return String.localizedStringWithFormat(String(localized: "Your %1$@ routine · %2$@"),
                                                    day, clock(slot, locale: locale))
        default:
            return nil
        }
    }

    /// One sentence saying what came of a task, or nil when there is none: a Done row never says only "Done".
    static func outcome(_ outcome: MobileTaskOutcome?, now: Date = Date(), locale: Locale) -> String? {
        guard let outcome else { return nil }
        func param(_ key: String) -> String? {
            let value = outcome.params?[key]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return value.isEmpty ? nil : value
        }
        let time = outcome.date.map { clock($0, locale: locale) }
        switch outcome.kind {
        case "marked_done":
            return String(localized: "You marked it done.")
        case "completed":
            return param("summary")
        case "sent":
            guard let person = param("person"), let thing = param("thing") else { return nil }
            return String.localizedStringWithFormat(String(localized: "%1$@ has %2$@."), person, thing)
        case "replied":
            guard let person = param("person") else { return nil }
            return String.localizedStringWithFormat(String(localized: "%@ has your reply."), person)
        case "you_did":
            guard let app = param("app"), let time else { return nil }
            return String.localizedStringWithFormat(String(localized: "You replied from %1$@ at %2$@."), app, time)
        case "already_done":
            guard let did = param("did"), let time else { return nil }
            return String.localizedStringWithFormat(String(localized: "Already done: you %1$@ at %2$@."), did, time)
        case "hold_placed":
            let when: String?
            if let start = param("start"), let end = param("end") {
                when = PhoneHoldCopy.when(PhoneHoldSlot(start: start, end: end), now: now, locale: locale)
            } else {
                when = param("when")
            }
            guard let when else { return nil }
            return String.localizedStringWithFormat(String(localized: "On your calendar: %@."), when)
        case "routine_sent":
            guard let audience = param("audience"), let date = outcome.date else { return nil }
            return String.localizedStringWithFormat(String(localized: "%1$@ has it. Sent %2$@."),
                                                    audience, when(date, now: now, locale: locale))
        default:
            return nil
        }
    }

    /// "Today, 6:00 PM", "Tomorrow, 7:30 AM", otherwise "Thursday, 4:20 PM": when something scheduled runs.
    static func upcoming(_ date: Date, now: Date = Date(), locale: Locale, calendar: Calendar = .current) -> String {
        let time = clock(date, locale: locale)
        if calendar.isDate(date, inSameDayAs: now) {
            return String.localizedStringWithFormat(String(localized: "Today, %@"), time)
        }
        if let tomorrow = calendar.date(byAdding: .day, value: 1, to: now), calendar.isDate(date, inSameDayAs: tomorrow) {
            return String.localizedStringWithFormat(String(localized: "Tomorrow, %@"), time)
        }
        let day = date.formatted(Date.FormatStyle().weekday(.wide).locale(locale))
        return String.localizedStringWithFormat(String(localized: "%1$@, %2$@"), day, time)
    }

    static func scheduledTitle(_ item: MobileScheduledTask) -> String {
        switch (item.kind, item.slot) {
        case ("brief", "morning"): return String(localized: "Morning brief")
        case ("brief", _): return String(localized: "Evening brief")
        default: return item.title ?? ""
        }
    }

    /// The line under a row's title. Needs you leads with its copper state; Running names who runs it and the step.
    static func rowLine(_ task: MobileTask, now: Date = Date(), locale: Locale) -> (lead: String?, rest: String) {
        switch task.group {
        case "needs_you":
            let time = task.updatedDate.map { when($0, now: now, locale: locale) }
            return (TasksCopy.state(task), time.map { " · " + $0 } ?? "")
        case "running":
            if task.status == "drafting" { return (nil, String(localized: "Isoform is drafting it on your computer")) }
            let runner: String
            switch task.runner {
            case "claude_code": runner = String(localized: "Claude Code on your computer")
            case "codex": runner = String(localized: "Codex on your computer")
            default: runner = String(localized: "On your computer")
            }
            guard let progress = task.progress, progress.total > 0 else { return (nil, runner) }
            let step = String.localizedStringWithFormat(String(localized: "row.step %1$d of %2$d"),
                                                        min(progress.done + 1, progress.total), progress.total)
            return (nil, runner + " · " + step)
        case "scheduled":
            return (nil, task.updatedDate.map { upcoming($0, now: now, locale: locale) } ?? "")
        default:
            return (nil, outcome(task.outcome, now: now, locale: locale) ?? TasksCopy.detail(task, now: now, locale: locale))
        }
    }

    static func deckKind(_ kind: String?) -> String? {
        switch kind {
        case "reply": return String(localized: "deck.kind.reply")
        case "your_day": return String(localized: "deck.kind.yourDay")
        case "claude_code": return String(localized: "Claude Code")
        default: return nil
        }
    }

    static func sourceName(_ source: String) -> String {
        switch source {
        case "mail": return String(localized: "source.mail")
        case "calendar": return String(localized: "source.calendar")
        case "claude_code": return String(localized: "Claude Code")
        case "codex": return String(localized: "Codex")
        case "call": return String(localized: "source.call")
        default: return String(localized: "source.screen")
        }
    }

    static func sourceIcon(_ source: String) -> String {
        switch source {
        case "mail": return "envelope"
        case "calendar": return "calendar"
        case "claude_code", "codex": return "chevron.left.forwardslash.chevron.right"
        case "call": return "phone"
        default: return "macwindow"
        }
    }

    /// "From Mail · Tue 3:40 PM": where and when Isoform saw the fact.
    static func sourceLine(_ evidence: IdeaEvidence, now: Date = Date(), locale: Locale,
                           calendar: Calendar = .current) -> String {
        let name = sourceName(evidence.source)
        guard let date = RelayTime.parse(evidence.at) else {
            return String.localizedStringWithFormat(String(localized: "From %@"), name)
        }
        let time = clock(date, locale: locale)
        let when: String
        if calendar.isDate(date, inSameDayAs: now) {
            when = time
        } else {
            let day = date.formatted(Date.FormatStyle().weekday(.abbreviated).locale(locale))
            when = String.localizedStringWithFormat(String(localized: "%1$@ %2$@"), day, time)
        }
        return String.localizedStringWithFormat(String(localized: "From %1$@ · %2$@"), name, when)
    }

    static func menuTitle(_ action: String) -> String? {
        switch action {
        case "mark_done": return String(localized: "Mark as done")
        case "rename": return String(localized: "Rename")
        case "not_now": return String(localized: "Not now")
        case "made": return String(localized: "What this task made")
        case "stop": return String(localized: "Stop")
        case "find_time": return String(localized: "hold.findTime")
        default: return nil
        }
    }

    static func menuIcon(_ action: String) -> String {
        switch action {
        case "mark_done": return "checkmark.circle"
        case "rename": return "pencil"
        case "not_now": return "clock"
        case "made": return "doc"
        case "stop": return "stop"
        case "find_time": return "calendar.badge.plus"
        default: return "circle"
        }
    }
}

extension MobileTask {
    func titled(_ title: String) -> MobileTask {
        MobileTask(id: id, title: title, status: status, group: group, blockedReason: blockedReason, runner: runner,
                   progress: progress, createdAt: createdAt, updatedAt: updatedAt, outcome: outcome, unread: unread, openedAt: openedAt, source: source)
    }

    func withUnread(_ unread: Bool) -> MobileTask {
        var copy = self
        copy.unread = unread
        return copy
    }
}

import SwiftUI

private struct OpenTaskThreadKey: EnvironmentKey {
    static let defaultValue: ((MobileTask) -> Void)? = nil
}

extension EnvironmentValues {
    /// Opens a task's thread from a card on this page (a Chat card's Review, Today's Needs you).
    var openTaskThread: ((MobileTask) -> Void)? {
        get { self[OpenTaskThreadKey.self] }
        set { self[OpenTaskThreadKey.self] = newValue }
    }
}

/// Lets cards on a page open a task's thread as a push on that page's stack, with its back button named for it.
struct TaskThreadDestination: ViewModifier {
    @ObservedObject var tasks: TasksStore
    let source: TaskThreadSource
    @EnvironmentObject private var router: NotificationRouter
    @State private var task: MobileTask?

    func body(content: Content) -> some View {
        content
            .environment(\.openTaskThread) { task = $0 }
            // A task notice opens its thread on Today, now that the Tasks tab is gone (phone-today rule 19).
            .task(id: source == .today ? router.pendingTaskID : nil) { await openPendingTask() }
            .navigationDestination(item: $task) { task in
                TaskThreadView(task: task, tasks: tasks, source: source)
            }
            .alert(TaskSendConfirmation.title(person: tasks.sendConfirmation?.person),
                   isPresented: Binding(get: { tasks.sendConfirmation != nil },
                                        set: { if !$0 { tasks.sendConfirmation = nil } }),
                   presenting: tasks.sendConfirmation) { held in
                Button(String(localized: "Send")) { Task { await tasks.confirmHeldSend(held) } }
                Button(String(localized: "Not now"), role: .cancel) { Task { await tasks.setHeldSendAside(held) } }
            }
    }
}

extension TaskThreadDestination {
    @MainActor
    func openPendingTask() async {
        guard source == .today, let id = router.pendingTaskID else { return }
        if let found = tasks.page?.tasks.first(where: { $0.id == id }) {
            task = found
            router.consumePendingTaskID(id)
            return
        }
        if case .success(let detail)? = await tasks.loadDetail(id: id), router.pendingTaskID == id {
            task = detail.task
            router.consumePendingTaskID(id)
        }
    }
}

extension View {
    func taskThreads(_ tasks: TasksStore, from source: TaskThreadSource) -> some View {
        modifier(TaskThreadDestination(tasks: tasks, source: source))
    }
}

extension MobileTask {
    /// The row a work card stands for, until its thread loads from the computer.
    static func placeholder(work: ChatProactiveWork, title: String) -> MobileTask {
        let group: String
        switch work.status {
        case "in_progress": group = "running"
        case "completed": group = "done"
        default: group = "needs_you"
        }
        return MobileTask(id: work.handoffId, title: title, status: work.status, group: group,
                          blockedReason: nil, runner: nil, progress: nil, createdAt: "", updatedAt: "")
    }
}
