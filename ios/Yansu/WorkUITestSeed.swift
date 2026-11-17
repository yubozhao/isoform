#if DEBUG && targetEnvironment(simulator)
import Foundation

/// The stub of B2 and B3 for the simulator, until a computer serves `/work`. Names and content are invented, as on
/// design's board (phone-today artboard 2). `YANSU_UITEST_WORK=1` turns it on; `=done` answers everything.
enum WorkUITestSeed {
    static var isOn: Bool { ProcessInfo.processInfo.environment["YANSU_UITEST_WORK"] != nil }
    static var allAnswered: Bool { ProcessInfo.processInfo.environment["YANSU_UITEST_WORK"] == "done" }
    /// `=agents` adds an agent's request, a result with files, a taken-back item and a request that ran out
    /// (phone-agent v1.2.0 and v1.2.1).
    static var agents: Bool { ProcessInfo.processInfo.environment["YANSU_UITEST_WORK"] == "agents" }

    /// The seeded brief's id, so the brief's own items sit in the brief in full.
    @MainActor static var briefID: String?

    @MainActor static func page(now: Date = Date()) -> WorkPage {
        func at(_ minutes: Double) -> String { RelayTime.format(now.addingTimeInterval(-minutes * 60)) }
        // Answered today, whatever the clock: just after midnight "10 minutes ago" is yesterday.
        func today(_ minutes: Double) -> String {
            RelayTime.format(max(now.addingTimeInterval(-minutes * 60), Calendar.current.startOfDay(for: now)))
        }
        let tomorrow = RelayTime.format(Calendar.current.startOfDay(for: now.addingTimeInterval(86_400)).addingTimeInterval(7 * 3600))
        var items = [
            WorkItem(id: "work-prep-lena", kind: "done", row: "prep", state: "open",
                     source: "brief", first: [WorkFirstPart(text: "For your 10:00 with Lena"), WorkFirstPart(text: "tomorrow"), WorkFirstPart(text: "from the brief")],
                     noticed: "Lena will ask whether the import can take the full customer file.",
                     work: WorkBody(title: "Prep for Lena · ready",
                                    text: "Where the fix stands, the two failing tests, and the test plan you promised her on Thursday."),
                     yes: WorkYes(does: "keep", line: "Yes keeps it with the meeting. It opens at 9:55 AM."),
                     why: [WorkLabel(label: "Lena moved the review to 10:00 and asked about the full file in her email.")],
                     gets: [WorkLabel(label: "Lena's email, 4:12 PM"), WorkLabel(label: "The Halcyon branch")], createdAt: at(50)),
            WorkItem(id: "work-test-plan", kind: "offered", row: "offered", state: "open",
                     source: "topic", first: [WorkFirstPart(text: "Halcyon import"), WorkFirstPart(text: "a project"), WorkFirstPart(text: "from your topics")],
                     noticed: "The import has no test plan for full customer files.",
                     work: WorkBody(title: "Write the test plan",
                                    text: "Cases for the 40,000-row file and the two failing date formats.",
                                    minutes: 10),
                     yes: WorkYes(does: "keep", line: "Yes hands it to Claude, your agent. You see exactly what goes first."),
                     createdAt: at(40)),
            WorkItem(id: "work-reply-sam", kind: "done", row: "draft", state: "open",
                     source: "watch", first: [WorkFirstPart(text: "Asked of you"), WorkFirstPart(text: "Sam"), WorkFirstPart(text: "2:05 PM, from your watch")],
                     noticed: "You told Sam you'd send the pricing page tonight.",
                     work: WorkBody(title: "Reply to Sam · draft",
                                    text: "Hi Sam, here's the pricing page we went through at lunch: the three tiers, with the team price on the second."),
                     yes: WorkYes(does: "keep", line: "Yes puts it in your Mail drafts. You send it."),
                     gets: [WorkLabel(label: "Your lunch with Sam, 2:05 PM"), WorkLabel(label: "pricing-v3.pdf"),
                            WorkLabel(label: "Sam's email address")], createdAt: at(30)),
            WorkItem(id: "work-check-priya", kind: "check", row: "check", state: "open",
                     source: "agent_of_you", first: [WorkFirstPart(text: "Still open?"), WorkFirstPart(text: "from her email, Friday")],
                     noticed: "Is Priya still waiting on the Thursday slides?",
                     work: WorkBody(title: "Thursday slides for Priya"),
                     yes: WorkYes(does: "keep", line: "Right?"), createdAt: at(20)),
            WorkItem(id: "work-q4-outline", kind: "offered", row: "offered", state: "with_agent",
                     source: "brief", first: [WorkFirstPart(text: "Back from Claude"), WorkFirstPart(text: "you said yes at 4:10 PM")],
                     noticed: "The Q4 plan needs an outline before Tuesday.",
                     work: WorkBody(title: "Q4 plan outline"),
                     agent: WorkAgent(name: "Claude", since: at(90)), createdAt: at(120)),
            WorkItem(id: "work-reply-priya", kind: "done", row: "draft", state: "answered",
                     source: "brief", first: [WorkFirstPart(text: "From the brief")],
                     noticed: "Priya asked to move Thursday's review.",
                     work: WorkBody(title: "Reply to Priya"),
                     answer: WorkAnswer(action: "yes", at: today(10), line: "Reply to Priya: in your Mail drafts",
                                        undoUntil: tomorrow), createdAt: at(200)),
        ]
        if agents {
            let ends = RelayTime.format(Calendar.current.startOfDay(for: now.addingTimeInterval(86_400)).addingTimeInterval(16 * 3600 + 600))
            items.insert(contentsOf: [
                WorkItem(id: "work-request", kind: "request", row: "request", state: "open", source: "agent",
                         first: [], noticed: "",
                         work: WorkBody(title: "Codex asks Muse to collect the three customers' date formats."),
                         reasons: [], createdAt: at(15),
                         request: WorkRequest(from: WorkAgentName(agentId: "codex", name: "Codex"),
                                              to: WorkAgentName(agentId: "muse", name: "Muse"),
                                              moves: "The importer's last failing test, once Codex has the formats."),
                         expiresAt: ends),
                WorkItem(id: "work-result-plan", kind: "result", row: "result", state: "back", source: "agent",
                         first: [WorkFirstPart(text: "Back from Claude"), WorkFirstPart(text: "you said yes at 4:10 PM")],
                         noticed: "Claude wrote the test plan for the full customer file.",
                         work: WorkBody(title: "Halcyon test plan", text: "Twelve cases, with the two failing date formats first."),
                         createdAt: at(12),
                         files: [WorkFile(id: "f1", name: "test-plan.md", where: "branch halcyon/test-plan", readable: true),
                                 WorkFile(id: "f2", name: "fixtures.zip", where: "branch halcyon/test-plan", readable: false)]),
            ], at: 0)
            items.append(WorkItem(id: "work-taken", kind: "offered", row: "offered", state: "open", source: "topic",
                                  first: [WorkFirstPart(text: "Pricing page"), WorkFirstPart(text: "from your topics")],
                                  noticed: "The pricing page still shows last year's team price.",
                                  work: WorkBody(title: "Update the team price"), createdAt: at(5),
                                  takenBack: WorkTakenBack(agentId: "claude", agentName: "Claude", at: at(3))))
            items.append(WorkItem(id: "work-ran-out", kind: "request", row: "request", state: "answered", source: "agent",
                                  first: [], noticed: "", work: WorkBody(title: "Codex asks Muse for the release notes."),
                                  answer: WorkAnswer(action: "ran_out", at: at(60), line: "Codex's request to Muse ran out · nothing moved"),
                                  createdAt: at(1500),
                                  request: WorkRequest(from: WorkAgentName(name: "Codex"), to: WorkAgentName(name: "Muse"))))
            // The computer's order: the request first, then the result.
            for index in items.indices { items[index].order = index }
        }
        if allAnswered {
            items = items.map { item in
                guard item.isOpen else { return item }
                var answered = item
                answered.state = "answered"
                answered.answer = WorkAnswer(action: "yes", at: today(5), line: "\(item.work.title): kept", undoUntil: tomorrow)
                return answered
            }
        }
        if let briefID {
            for index in items.indices where items[index].source == "brief" {
                items[index].ref = WorkRef(briefId: briefID, paragraph: index == 0 ? 0 : nil)
            }
        }
        return WorkPage(version: "ui-1", toAnswer: items.filter(\.isOpen).count, items: items)
    }
}

/// Answers the stub's list and takes its answers, as a computer with B2 and B3 would.
actor WorkUITestHub: WorkHubAPI {
    private var seeded: WorkPage?
    private var current: WorkPage {
        get async {
            if let seeded { return seeded }
            let page = await WorkUITestSeed.page()
            seeded = page
            return page
        }
    }

    func workAvailable() async throws -> Bool { true }
    func workSendAvailable() async throws -> Bool { true }
    func work() async throws -> WorkPage { await current }

    func answerWork(id: String, request: WorkAnswerRequest) async throws -> WorkItem {
        var page = await current
        guard let index = page.items.firstIndex(where: { $0.id == id }),
              let action = WorkAction(rawValue: request.action) else { throw HubRequestError.rejected(status: 404, message: "gone") }
        let updated = YourWork.answered(page.items[index], action: action,
                                        reason: request.reason.flatMap(WorkReason.init(rawValue:)), at: Date(), waits: false)
        page.items[index] = updated
        seeded = page
        return updated
    }
}
#endif

#if DEBUG && targetEnvironment(simulator)
/// The stub of GET /agents and its writes (PROTOCOL.md 27), with the nine agents and no choice for everything else.
actor AgentsUITestHub: AgentsHubAPI {
    private var current = PhoneAgents(choices: .init(code: "codex", else: ""), agents: [
        PhoneAgent(id: "codex", name: "Codex", kind: "code", reach: "connected_computer", connected: true,
                   lastWorkedAt: Date().addingTimeInterval(-7200).timeIntervalSince1970 * 1000),
        PhoneAgent(id: "claude-code", name: "Claude Code", kind: "code", reach: "on_computer"),
        PhoneAgent(id: "cursor", name: "Cursor", kind: "code", reach: "on_computer",
                   disconnectedAt: Date().addingTimeInterval(-3 * 86_400).timeIntervalSince1970 * 1000),
        PhoneAgent(id: "claude", name: "Claude", kind: "else", reach: "its_app"),
        PhoneAgent(id: "chatgpt", name: "ChatGPT", kind: "else", reach: "its_app"),
        PhoneAgent(id: "grok", name: "Grok", kind: "else", reach: "its_app"),
        PhoneAgent(id: "gemini", name: "Gemini", kind: "else", reach: "its_app"),
        PhoneAgent(id: "muse", name: "Muse", kind: "else", reach: "connected", connected: true),
        PhoneAgent(id: "instinct", name: "Instinct", kind: "else", reach: "by_text"),
    ])

    func phoneAgents() async throws -> PhoneAgents { current }

    func chooseAgent(_ request: PhoneAgentChoiceRequest) async throws -> PhoneAgents {
        if request.kind == "code" { current.choices.code = request.agentId } else { current.choices.else = request.agentId }
        return current
    }

    func handOff(_ request: PhoneHandOffRequest) async throws -> PhoneHandOffResult {
        PhoneHandOffResult(recordId: "ui-1", agentId: request.agentId ?? "")
    }

    func handoffs(limit: Int, before: String?) async throws -> AgentHandoffsPage {
        AgentHandoffsPage(handoffs: [
            AgentHandoffEntry(id: "h1", agentId: "claude-code", agentName: "Claude Code", kind: "code", method: "task_link",
                              text: "Fix the failing release build and open a PR.", sent: "Fix the failing release build",
                              sentAt: RelayTime.format(Date().addingTimeInterval(-900)), surface: "phone",
                              source: .init(kind: "work_item", id: "w1")),
            AgentHandoffEntry(id: "h2", agentId: "chatgpt", agentName: "ChatGPT", kind: "else", method: "paste",
                              text: "Write the launch checklist.", sentAt: RelayTime.format(Date().addingTimeInterval(-5400)),
                              surface: "computer", source: .init(kind: "topic_proposal")),
        ])
    }
}
#endif

#if DEBUG && targetEnvironment(simulator)
/// The stub of GET /answers and DELETE /answers/{id}, with invented answers.
actor AnswersUITestHub: AnswersHubAPI {
    private var page = AnswersPage(answers: [
        AnswerEntry(id: "a1", item: "brief:b:prep", kind: "work", title: "Prep for Lena", action: "yes",
                    at: RelayTime.format(Date().addingTimeInterval(-600)), surface: "phone", source: "brief"),
        AnswerEntry(id: "a2", item: "topic:t:p", kind: "work", title: "Write the test plan", action: "no", reason: "not_now",
                    at: RelayTime.format(Date().addingTimeInterval(-3600)), surface: "computer", source: "topic"),
        AnswerEntry(id: "a3", item: "prep:e", kind: "work", title: "Prep for the design review", action: "no",
                    reason: "not_needed", at: RelayTime.format(Date().addingTimeInterval(-7200)), surface: "notification",
                    source: "prep", undone: true),
    ])

    func answers(limit: Int, before: String?) async throws -> AnswersPage { page }
    func forgetAnswer(id: String) async throws { page.answers.removeAll { $0.id == id } }
}
#endif
