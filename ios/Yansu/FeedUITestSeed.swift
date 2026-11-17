#if DEBUG && targetEnvironment(simulator)
import Foundation

/// Cached briefs for the seeded Feed UI test states: "feed" has a morning and an evening brief today and two earlier
/// days; "feed-empty" has only the earlier days, so today shows its empty state.
enum FeedUITestSeed {
    /// Today's brief with its text and clicks, as the computer writes them (backlog item 58): one button, one row
    /// with a reason and a question, and the opener "Talk about this brief" asks the computer to say.
    static func conversationBrief(now: Date) -> MobileBriefV1 {
        let chinese = Locale.current.language.languageCode?.identifier == "zh"
        var brief = MobileBriefV1(version: 1, briefId: "ui-conversation-brief", slot: "morning",
                                  localDate: FeedAssembly.localDate(for: now), timezone: TimeZone.current.identifier,
                                  asOf: stamp(now), narrativeStatus: "ok",
                                  narrative: [BriefParagraph(text: chinese ? "今天可以先整理项目笔记。" : "Start with the project notes today."),
                                              BriefParagraph(text: chinese ? "Dana 还在等你回复 Q3 演示稿。" : "Dana is still waiting on your reply about the Q3 deck.")],
                                  conversationKey: "ui-conversation-version")
        brief.evidence = [BriefEvidence(ref: "e1", sourceKind: "principal", sourceId: "p-dana", label: "Dana",
                                        observedAt: stamp(now.addingTimeInterval(-3600)))]
        var dana = BriefItem(id: "people-1", title: chinese ? "回复 Dana" : "Reply to Dana",
                             detail: chinese ? "关于 Q3 演示稿" : "About the Q3 deck", refs: ["e1"])
        dana.key = "people:principal:p-dana"
        dana.attentionReason = chinese ? "她从周二起就在等" : "She has been waiting since Tuesday"
        dana.attentionRank = 1
        brief.sections.people = [dana]
        brief.cards = [
            MobileBriefCard(id: "card-plan", kind: "action", mode: "send", label: chinese ? "拟一份计划" : "Draft a plan",
                            message: "Draft a plan for the project notes from this brief.", refs: ["e1"]),
            MobileBriefCard(id: "q-project-notes", kind: "question", mode: "send",
                            label: chinese ? "查看项目笔记" : "Review the project notes",
                            message: "What changed in the project notes?", refs: ["e1"], paragraph: 0),
            MobileBriefCard(id: "card-dana", kind: "item", mode: "send",
                            message: "What should I tell Dana about the Q3 deck?", refs: ["e1"], itemKey: dana.key),
            MobileBriefCard(id: "opener", kind: "opener", mode: "speak_first",
                            message: chinese ? "今天早上可以先整理项目笔记。你想从哪里开始？" : "Your morning starts with the project notes. What would you like to begin with?"),
            MobileBriefCard(id: "opener:yes", kind: "opener_answer", mode: "send",
                            label: chinese ? "从这里开始" : "Start here", message: "Help me start with this. What should I do first?", refs: ["e1"]),
            MobileBriefCard(id: "opener:other", kind: "opener_answer", mode: "send",
                            label: chinese ? "看看其他选择" : "Show me another option", message: "What else could I start with?", refs: ["e1"]),
        ]
        return brief
    }

    static func yourWeekBrief(now: Date) -> MobileBriefV1 {
        var brief = conversationBrief(now: now)
        brief.briefId = "ui-your-week"
        brief.slot = "evening"
        brief.yourWeek = MobileBriefWeek(
            start: "2026-10-19",
            end: "2026-10-25",
            lead: "You made steady progress on the launch plan.",
            time: [
                MobileBriefWeekTime(topicId: "launch", name: "Launch plan", hours: 6),
                MobileBriefWeekTime(other: true, hours: 3),
            ],
            done: [MobileBriefWeekDone(id: "done-launch", kind: "file", title: "Finished the launch outline")],
            open: [MobileBriefWeekOpen(itemKey: "open-launch", cardId: "week-open-card", title: "Review the launch plan",
                                       label: "Review the plan")],
            learned: [MobileBriefWeekLearned(id: "learned-focus", kind: "memory", text: "You prefer focused mornings.", memoryId: "mem-7")],
            ask: MobileBriefWeekAsk(text: "Plan Monday", message: "Help me plan Monday morning.",
                                    cardId: "week-ask-card",
                                    hold: MobileBriefWeekHold(start: "2026-10-19T09:00:00-07:00",
                                                              end: "2026-10-19T12:00:00-07:00", label: "Planning"))
        )
        brief.cards.append(contentsOf: [
            MobileBriefCard(id: "week-open-card", kind: "item", mode: "send", label: "Review the plan",
                            message: "Review the launch plan from this week."),
            MobileBriefCard(id: "week-ask-card", kind: "question", mode: "send", label: "Plan Monday",
                            message: "Help me plan Monday morning."),
        ])
        if ProcessInfo.processInfo.environment["YANSU_UITEST_WEEK_TOPIC"] == "1" {
            brief.yourWeek?.ask = MobileBriefWeekAsk(text: "Start next week on the launch plan?",
                                                      message: "Help me plan next week for the launch.", day: "2026-10-26", topicId: "launch")
        }
        return brief
    }

    static func briefs(today includesToday: Bool, now: Date) -> [MobileBriefV1] {
        let calendar = Calendar.current
        let day = { (offset: Int) -> Date in calendar.date(byAdding: .day, value: -offset, to: now) ?? now }
        var briefs = [
            brief(on: day(1), slot: "morning", hour: 7, items: [
                ("moved", item("Filed the expense report", "Receipts attached for September", "history", "h-41")),
                ("meetings", item("Planning with Priya", "10:00 to 10:45, notes in the team folder", "calendar", "ev-12")),
                ("learned", item("You review pull requests after lunch", "Seen on four days this week", "learning", "l-3")),
            ]),
            brief(on: day(3), slot: "morning", hour: 7, items: [
                ("doing", item("roadmap-2027.numbers", "Edited in Numbers", "file", "f-77")),
                ("watching", item("news.ycombinator.com", "9 visits", "web", "site:news.ycombinator.com")),
            ]),
        ]
        guard includesToday else { return briefs }
        briefs.append(brief(on: day(0), slot: "morning", hour: 7, items: [
            ("moved", item("3pm with Maya moved", "Now Thursday. Her calendar still shows Tuesday.", "calendar", "ev-1")),
            ("meetings", item("Design review with Sam", "11:00 to 11:30. Last week's notes are in Drafts.", "calendar", "ev-2",
                              url: "https://example.com/meet/design-review")),
            ("people", item("Dana is waiting on a reply", "Since Tuesday, about the Q3 deck", "principal", "p-dana")),
            ("deliverables", item("The Q3 outline is ready", "Drafted from your notes. Open it on your computer.", "deliverable", "d-9")),
            ("watching", item("Wails 2.12.1 changes drag regions",
                              "You spent time on Wails drag regions yesterday; the release changes how drag areas are read",
                              "find", "find-1", url: "https://example.com/wails/releases")),
            ("watching", item("github.com", "14 visits", "web", "site:github.com")),
            ("doing", item("launch-plan.key", "Edited in Keynote", "file", "f-12")),
            ("schedules", item("Weekly expense summary ran", "Finished at 8:02 AM with 3 new receipts", "schedule_run", "s-4")),
            ("learned", item("You prefer short replies in the morning", "Learned from your chats this week", "memory", "mem-7")),
            ("openLoops", item("Send the contract draft to Ana", "Waiting on Ana", "purpose", "pu-1")),
        ]))
        // The evening brief repeats a record, which the Feed shows once.
        briefs.append(brief(on: day(0), slot: "evening", hour: 18, items: [
            ("watching", item("github.com", "21 visits", "web", "site:github.com")),
            ("learned", item("You prefer short replies in the morning", "Learned from your chats this week", "memory", "mem-7")),
        ]))
        return briefs
    }

    /// The four paragraphs of the words test (handoff 2026-10-28, section 2): the greeting joined into the first, so
    /// the card opens on substance. Each paragraph starts at its own time in the test's audio (wordsAudioParagraphs).
    static let wordsParagraphs = [
        "Good morning. 3 meetings today, and Priya is waiting on you.",
        "The launch review starts at 3 PM, and the deck still needs one more pass.",
        "Two replies from the design team are waiting in your inbox.",
        "Tomorrow is quiet until the afternoon, so the day is yours to shape.",
    ]

    static func wordsAudioParagraphs() -> [BriefAudioParagraph] {
        wordsParagraphs.indices.map { BriefAudioParagraph(source: "narrative", index: $0, startSeconds: Double($0) * 7.5) }
    }

    /// The morning brief of the words test: its four paragraphs, and sections to check them against.
    static func wordsBrief(now: Date) -> MobileBriefV1 {
        // "waiting" needs a brief older than the slot that just came due (two minutes ago). Before 7:40 AM today's
        // morning brief would be in the future, so that moment takes yesterday's.
        let day = moment == "waiting" && Calendar.current.component(.hour, from: now) < 8
            ? now.addingTimeInterval(-86_400) : now
        var brief = brief(on: day, slot: "morning", hour: 7, items: [
            ("meetings", item("Design review with Sam", "11:00 to 11:30. Last week's notes are in Drafts.", "calendar", "ev-2")),
            ("people", item("Dana is waiting on a reply", "Since Tuesday, about the Q3 deck", "principal", "p-dana")),
            ("deliverables", item("The Q3 outline is ready", "Drafted from your notes. Open it on your computer.", "deliverable", "d-9")),
        ])
        brief.narrative = wordsParagraphs.map { BriefParagraph(text: $0) }
        // "Talk about this brief" frames: a refusal or an older computer is asked about a brief with no reference.
        if ["refused", "too-old"].contains(ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_TALK"]) {
            brief.conversationKey = nil
        }
        return brief
    }

    /// A brief made later, because the person asked for one. It arrives while the words test is playing.
    static func newerBrief(after words: MobileBriefV1) -> MobileBriefV1 {
        let asOf = (words.asOfDate ?? Date()).addingTimeInterval(60 * 60)
        let localDate = FeedAssembly.localDate(for: asOf)
        return MobileBriefV1(briefId: "\(localDate)-on-request", slot: "morning", localDate: localDate,
                             asOf: stamp(asOf), kind: "on_request", narrativeStatus: "ok",
                             narrative: [BriefParagraph(text: "A newer brief, made because you asked for one.")])
    }

    private static func stamp(_ date: Date) -> String {
        ISO8601DateFormatter().string(from: date)
    }

    private struct Seed {
        let title: String
        let detail: String
        let sourceKind: String
        let sourceID: String
        let url: String?
    }

    private static func item(_ title: String, _ detail: String, _ sourceKind: String, _ sourceID: String,
                             url: String? = nil) -> Seed {
        Seed(title: title, detail: detail, sourceKind: sourceKind, sourceID: sourceID, url: url)
    }

    private static func brief(on day: Date, slot: String, hour: Int, items: [(String, Seed)]) -> MobileBriefV1 {
        let calendar = Calendar.current
        let asOf = calendar.date(bySettingHour: hour, minute: 40, second: 0, of: day) ?? day
        let localDate = FeedAssembly.localDate(for: asOf)
        var brief = MobileBriefV1(version: 1, briefId: "\(localDate)-\(slot)", slot: slot, localDate: localDate,
                                  timezone: TimeZone.current.identifier, asOf: stamp(asOf),
                                  hubName: "Seeded Mac", narrativeStatus: "ok",
                                  // The opening is a sentence the computer would write, never an item's title (P126).
                                  narrative: [BriefParagraph(text: slot == "evening"
                                                             ? "Your day is wrapping up, and here is what moved."
                                                             : "Here is what moved since yesterday.")])
        for (index, entry) in items.enumerated() {
            let (section, seed) = entry
            let ref = "e\(index + 1)"
            let at = asOf.addingTimeInterval(-Double(index + 1) * 25 * 60)
            brief.evidence.append(BriefEvidence(ref: ref, sourceKind: seed.sourceKind, sourceId: seed.sourceID,
                                                label: seed.title, observedAt: stamp(at)))
            var item = BriefItem(id: "\(section)-\(index + 1)", title: seed.title, detail: seed.detail,
                                 at: stamp(at), refs: [ref], url: seed.url)
            item.key = FeedAssembly.itemKey(section: section, sourceKind: seed.sourceKind, sourceID: seed.sourceID)
            switch section {
            case "moved": brief.sections.moved.append(item)
            case "meetings": brief.sections.meetings.append(item)
            case "people": brief.sections.people.append(item)
            case "deliverables": brief.sections.deliverables.append(item)
            case "watching": brief.sections.watching.append(item)
            case "doing": brief.sections.doing.append(item)
            case "schedules": brief.sections.schedules.append(item)
            case "learned": brief.sections.learned.append(item)
            default: brief.sections.openLoops.append(item)
            }
        }
        return brief
    }
}
// A separate seed keeps the Feed's existing layout fixtures unchanged.
extension FeedUITestSeed {
    static func libraryBriefs(now: Date) -> [MobileBriefV1] {
        let yesterday = Calendar.current.date(byAdding: .day, value: -1, to: now) ?? now
        var saved = brief(on: yesterday, slot: "evening", hour: 18, items: [
            ("deliverables", item("Launch outline", "Created by Isoform", "deliverable", "launch-outline")),
            ("moved", item("Prepare the launch outline", "The outline is ready for review.", "history", "launch-work")),
            ("doing", item("Private working notes", "Edited by you", "file", "observed-file")),
        ])
        saved.narrativeStatus = "ok"
        saved.narrative = [BriefParagraph(text: "The launch plan is ready. Review the outline before tomorrow.")]
        saved.sections.moved[0].kind = "one_time_work"
        saved.sections.moved[0].status = "needs_review"
        saved.sections.commitments = [BriefItem(title: "Review the launch plan", chatMessageId: "main-chat-report")]
        // Keep a correctable memory in the brief so the Library's read-only detail gate is testable.
        saved.evidence.append(BriefEvidence(ref: "memory", sourceKind: "memory", sourceId: "memory-seed"))
        saved.sections.learned = [BriefItem(title: "Keep reviews short", refs: ["memory"])]
        return [saved]
    }
}

struct LibraryUITestHistory: LibraryHistoryAPI {
    func libraryDecisions() async throws -> [LibraryRecord] {
        [LibraryRecord(id: "seed-decision", title: "Keep the launch focused", detail: "The request was answered on your computer.",
                       status: "Answered", date: Date())]
    }
}
// An in-process Today response keeps UI fixtures independent of a relay.
struct TodayUITestHub: TodayHubAPI {
    let snapshot: MobileTodaySnapshot?
    let offline: Bool

    func meetingPrep(id: String) async throws -> TodayMeetingPrep {
        guard !offline, id == "meeting-ui", ProcessInfo.processInfo.environment["YANSU_UITEST_MEETING_FAILURE"] != "1" else {
            throw HubRequestError.noAnswer
        }
        return TodayMeetingPrep(meetingId: id, meetingTitle: "Launch review", personName: "Priya",
                                youOwe: "Send the launch outline", theyAsked: "Confirm the date", lastTime: "Reviewed the plan")
    }

    func today() async throws -> MobileTodaySnapshot {
        // An offline fixture is a phone the relay reports the computer away from, the one case Today calls asleep.
        if offline { throw RelayClientError.offline }
        guard let snapshot else { throw HubRequestError.noAnswer }
        return snapshot
    }
    func pollTodayEvents() async throws -> [MobileTodayEvent] {
        try await Task.sleep(for: .seconds(900))
        return []
    }
    func requestTodayBrief(key: String) async throws -> BriefRefreshResult {
        // The "failed" moment is a computer that refused to make the brief the person asked for.
        if FeedUITestSeed.moment == "failed" { return BriefRefreshResult(status: "failed", nextAllowedAt: nil) }
        throw HubRequestError.noAnswer
    }
}

struct FeedTasksUITestHub: TasksHubAPI {
    let snapshot: MobileTodaySnapshot?

    func tasks() async throws -> MobileTasksPage {
        let needs = snapshot?.needsYou ?? []
        return MobileTasksPage(tasks: [], counts: MobileTaskCounts(needsYou: needs.count, running: 0, doneThisWeek: 0),
                               waitingItems: needs, waitingCount: needs.count)
    }
    func task(id: String) async throws -> MobileTaskDetail { throw HubRequestError.noAnswer }
    func stopTask(id: String, request: MobileTaskStopRequest) async throws -> MobileTask { throw HubRequestError.noAnswer }
}

extension FeedUITestSeed {
    /// A Today card moment for screenshots of the words brief (handoff 2026-10-28, section 2): "making", "waiting"
    /// (a scheduled brief is due while the computer is asleep) or "failed".
    static var moment: String? { ProcessInfo.processInfo.environment["YANSU_UITEST_BRIEF_MOMENT"] }

    static func snapshot(state: String, now: Date) throws -> MobileTodaySnapshot? {
        guard state.hasPrefix("feed"), state != "feed-no-history" else { return nil }
        var briefs: [MobileBriefV1]
        switch state {
        case "feed-conversation": briefs = [conversationBrief(now: now)] + libraryBriefs(now: now)
        case "feed-your-week": briefs = [yourWeekBrief(now: now)] + libraryBriefs(now: now)
        case let value where value.hasPrefix("feed-topics"):
            briefs = [TopicUITestSeed.brief(now: now)] + libraryBriefs(now: now)
        case "feed-words": briefs = [wordsBrief(now: now)]
        // No brief at all yet (a paired phone just before the first one): Today says when it comes.
        case "feed-first-brief": briefs = []
        default: briefs = self.briefs(today: state != "feed-empty", now: now)
        }
        briefs.sort { ($0.asOfDate ?? .distantPast) > ($1.asOfDate ?? .distantPast) }
        let entries = briefs.map { brief in
            let scheduled = Calendar.current.date(bySettingHour: brief.slot == "evening" ? 19 : 7,
                                                  minute: brief.slot == "evening" ? 30 : 0, second: 0,
                                                  of: brief.asOfDate ?? now) ?? now
            return TodayBriefIndex(id: brief.identifier, slot: brief.slot ?? "morning", localDate: brief.localDate ?? "",
                            scheduledAt: stamp(scheduled), madeAt: brief.asOf ?? stamp(now),
                            catchUp: state == "feed-catchup" && brief.identifier == briefs.first?.identifier,
                            durationSeconds: brief.audio?.durationSeconds,
                            lead: brief.narrative.first?.text ?? "Your day is ready.")
        }
        let needs = try ChatUITestSeed.proactiveCards(now: now).prefix(2).compactMap { row -> TodayNeed? in
            guard let card = row.proactive else { return nil }
            return TodayNeed(id: card.momentID, chatId: ChatUITestSeed.mainChatID,
                             card: TodayNeed.Card(proactive: card), task: nil)
        }
        let topics = state.hasPrefix("feed-topics") || state == "feed-review" ? Array(try TopicUITestSeed.topics(now: now).prefix(2)) : []
        return MobileTodaySnapshot(generatedAt: stamp(now), latestBrief: entries.first, briefs: entries,
                                   needsYou: needs, needsYouCount: needs.count,
                            day: TodayDay(next: TodayDay.Event(title: "Launch review",
                                                                            start: stamp(now.addingTimeInterval(3600)),
                                                                            end: stamp(now.addingTimeInterval(5400)),
                                                                            id: state == "feed-review" ? "meeting-ui" : nil,
                                                                            kind: "meeting"),
                                                 free: [TodayDay.Range(start: stamp(now),
                                                                       end: stamp(now.addingTimeInterval(3600)))],
                                                 calendarConnected: true),
                                   topics: topics,
                                   briefRequest: moment == "making"
                                       ? TodayBriefRequest(state: "making", readyAt: stamp(now.addingTimeInterval(3 * 60))) : nil,
                                   schedule: MobileBriefSchedule(morning: "07:00",
                                                                 evening: moment == "waiting" ? clock(now.addingTimeInterval(-2 * 60)) : "19:30"),
                                   briefSchedule: nextMorningBrief(now: now))
    }

    /// The computer's next morning brief at 6:50 AM, the time the Today copy shows.
    private static func nextMorningBrief(now: Date) -> [ScheduledPhoneBrief]? {
        guard let next = Calendar.current.nextDate(after: now, matching: DateComponents(hour: 6, minute: 50),
                                                   matchingPolicy: .nextTime) else { return nil }
        return [ScheduledPhoneBrief(slot: "morning", at: stamp(next))]
    }

    private static func clock(_ date: Date) -> String {
        let parts = Calendar.current.dateComponents([.hour, .minute], from: date)
        return String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
    }
}
actor YourWeekDeclineUITestHub: YourWeekDeclineAPI {
    private let defaults: UserDefaults
    init(defaults: UserDefaults) { self.defaults = defaults }
    func weekDeclined(briefID: String, version: String) async throws -> Bool {
        defaults.bool(forKey: "seed.computer.week-declined")
    }
    func declineWeek(briefID: String, version: String, idempotencyKey: String) async throws -> Bool {
        defaults.set(true, forKey: "seed.computer.week-declined")
        return true
    }
}

actor FeedFeedbackUITestHub: FeedHubAPI {
    let failCorrection = ProcessInfo.processInfo.environment["YANSU_UITEST_FAIL_CORRECTION"] == "1"
    func sendFeedback(briefID: String, itemID: String, action: FeedFeedbackAction, reason: String?,
                      idempotencyKey: String) async throws -> BriefFeedbackPayload {
        if failCorrection && action == .notRight { throw HubRequestError.rejected(status: 500, message: "Could not save") }
        return BriefFeedbackPayload(recorded: true, effect: action == .notRight ? "memory_removed" : "recorded")
    }
}

actor KeepAwakeUITestHub: KeepAwakeAPI {
    var on: Bool
    let offline: Bool
    let instantPush: Bool

    init(on: Bool, offline: Bool, instantPush: Bool) {
        self.on = on
        self.offline = offline
        self.instantPush = instantPush
    }

    func keepAwakeStatus() async throws -> HubStatus {
        guard !offline else { throw HubRequestError.noAnswer }
        return HubStatus(hubName: nil, appVersion: nil, protocolVersion: nil, narrativeAvailable: nil,
                         lastBriefAt: nil, keepAwake: on, instantPush: instantPush)
    }
    func keepAwake() async throws -> KeepAwakeSetting {
        guard !offline else { throw HubRequestError.noAnswer }
        return KeepAwakeSetting(on: on, pairedPhones: 1, onPower: true)
    }
    func setKeepAwake(_ value: Bool) async throws -> KeepAwakeSetting {
        guard !offline else { throw HubRequestError.noAnswer }
        on = value
        return KeepAwakeSetting(on: on, pairedPhones: 1, onPower: true)
    }
}
#endif
