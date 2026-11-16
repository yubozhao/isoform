#if DEBUG && targetEnvironment(simulator)
import Foundation

/// The stub of B2 and B3 for the simulator, until a computer serves `/work`. Names and content are invented, as on
/// design's board (phone-today artboard 2). `YANSU_UITEST_WORK=1` turns it on; `=done` answers everything.
enum WorkUITestSeed {
    static var isOn: Bool { ProcessInfo.processInfo.environment["YANSU_UITEST_WORK"] != nil }
    static var allAnswered: Bool { ProcessInfo.processInfo.environment["YANSU_UITEST_WORK"] == "done" }

    /// The seeded brief's id, so the brief's own items sit in the brief in full.
    @MainActor static var briefID: String?

    @MainActor static func page(now: Date = Date()) -> WorkPage {
        func at(_ minutes: Double) -> String { RelayTime.format(now.addingTimeInterval(-minutes * 60)) }
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
                     answer: WorkAnswer(action: "yes", at: at(10), line: "Reply to Priya: in your Mail drafts",
                                        undoUntil: tomorrow), createdAt: at(200)),
        ]
        if allAnswered {
            items = items.map { item in
                guard item.isOpen else { return item }
                var answered = item
                answered.state = "answered"
                answered.answer = WorkAnswer(action: "yes", at: at(5), line: "\(item.work.title): kept", undoUntil: tomorrow)
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
        PhoneAgent(id: "codex", name: "Codex", kind: "code", reach: "connected_computer", connected: true),
        PhoneAgent(id: "claude-code", name: "Claude Code", kind: "code", reach: "on_computer"),
        PhoneAgent(id: "cursor", name: "Cursor", kind: "code", reach: "on_computer"),
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
}
#endif
