import Foundation

@MainActor
final class NotificationRouter: ObservableObject {
    /// Settings is not a tab: selecting `.settings` opens its sheet for older callers.
    enum Tab: Hashable {
        case today
        /// Shown only when this phone has an Apple Watch paired (phone-watch v1.0.0 rule 1).
        case watch
        case chat
        case tasks
        case settings
    }

    enum Sheet: String, Identifiable {
        case settings
        case pairing

        var id: String { rawValue }
    }

    @Published var isTabBarCompact = false
    /// True while a brief correction is open. The floating tab bar steps aside then, so Send and Cancel stay clear of it.
    @Published var isCorrecting = false
    /// Task threads on screen; the tab bar steps away while one is open (Phone Threads §1).
    @Published var openThreads: Set<UUID> = []
    @Published var selectedTab: Tab = .today {
        didSet {
            if selectedTab == .settings {
                selectedTab = oldValue == .settings ? .today : oldValue
                sheet = .settings
            }
            // The Tasks tab went (phone-today rule 19): anything that still names it lands on Today.
            if selectedTab == .tasks { selectedTab = .today }
            if selectedTab != oldValue { isTabBarCompact = false }
            if selectedTab != .today { pendingCurrentBrief = false }
        }
    }

    /// The sidebar opens on every tab root; Pairing from a button or an Isoform or legacy pairing link.
    @Published var sheet: Sheet?
    /// "{agent} is asking to join": the code sheet for that join (phone-agent v1.2.0 rule 11).
    @Published var joinCheck: AgentJoinRoute?
    private(set) var startsPairingWithScanner = false
    @Published private(set) var briefPushes = 0
    @Published private(set) var pendingMomentID: String?
    @Published private(set) var pendingTaskID: String?
    @Published var topicFilter: TopicRoute?
    @Published var upNextRequest: UUID?
    /// The brief view, presented over the current screen.
    @Published var briefRoute: BriefRoute?
    @Published var linkNotice: String?
    @Published var pendingTodayDay = false
    @Published private(set) var pendingCurrentBrief = false

    nonisolated static func isBriefPush(_ userInfo: [AnyHashable: Any]) -> Bool {
        pushKind(userInfo) == "brief"
    }

    /// A tap on a push without a notice opens Today: a brief push, or a `needs_you` push from an older Mac,
    /// whose Desk is gone (PROTOCOL.md section 9).
    nonisolated static func opensBrief(_ userInfo: [AnyHashable: Any]) -> Bool {
        isBriefPush(userInfo) || pushKind(userInfo) == "needs_you"
    }

    nonisolated static func pushKind(_ userInfo: [AnyHashable: Any]) -> String? {
        guard let yansu = userInfo["yansu"] as? [String: Any] else { return nil }
        return yansu["kind"] as? String
    }

    /// A new brief changes Today, so Today opens and refreshes.
    func briefPushReceived() {
        sheet = nil
        selectedTab = .today
        briefPushes += 1
    }

    /// A sealed notice opened from its banner or one of its buttons. Chat notices open Chat; briefs open Today.
    func noticeOpened(_ route: NoticeRoute) {
        switch route {
        case .chat(let momentID):
            topicFilter = nil
            sheet = nil
            pendingMomentID = momentID
            selectedTab = .chat
        case .brief:
            briefPushReceived()
        }
    }

    func consumePendingMomentID(_ momentID: String) {
        guard pendingMomentID == momentID else { return }
        pendingMomentID = nil
    }

    func openTodayNotice(kind: String?, id: String?) {
        guard let id, NoticeIdentifier.isSafe(id) else { briefPushReceived(); return }
        switch kind {
        case "task":
            sheet = nil
            pendingTaskID = id
            // The Tasks tab went (phone-today rule 19): its work is on Today, where the thread opens.
            selectedTab = .today
        case "chat":
            noticeOpened(.chat(momentID: id))
        case "agent_join":
            sheet = nil
            joinCheck = AgentJoinRoute(joinId: id)
        default:
            briefPushReceived()
        }
    }

    func consumePendingTaskID(_ id: String) {
        if pendingTaskID == id { pendingTaskID = nil }
    }

    /// Today's Show for a reply the computer did not take: Tasks opens and scrolls to that reply's row.
    @Published private(set) var heldTaskRowRequest: String?

    func showHeldTaskRow(_ taskID: String) {
        sheet = nil
        selectedTab = .today
        heldTaskRowRequest = taskID
    }

    func consumeHeldTaskRow(_ taskID: String) {
        if heldTaskRowRequest == taskID { heldTaskRowRequest = nil }
    }

    /// Today's Show for a Chat message the computer did not take: Chat opens and scrolls to that message.
    @Published private(set) var chatMessageRequest: String?

    func showChatMessage(_ messageID: String) {
        openMainChat()
        chatMessageRequest = messageID
    }

    func consumeChatMessage(_ messageID: String) {
        if chatMessageRequest == messageID { chatMessageRequest = nil }
    }

    func openUpNext() {
        sheet = nil
        upNextRequest = UUID()
        selectedTab = .today
    }

    /// Opens a non-pairing deep link, such as the link from the widget.
    func openDeepLink(_ url: URL, currentBriefID: String? = nil) {
        pendingCurrentBrief = false
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        let target = items.first(where: { $0.name == "id" })?.value
        let tab: Tab
        switch url.host?.lowercased() {
        case "brief":
            sheet = nil
            selectedTab = .today
            if let id = target ?? currentBriefID, NoticeIdentifier.isSafe(id) { openBrief(id) }
            else if target == nil { pendingCurrentBrief = true }
            else { linkNotice = String(localized: "Couldn't open that link.") }
            return
        case "feed", "desk", "today":
            pendingTodayDay = items.contains { $0.name == "section" && $0.value == "day" }
            tab = .today
        case "chat":
            if target != nil { openTodayNotice(kind: "chat", id: target); return }
            tab = .chat
        case "ideas", "tasks":
            if target != nil { openTodayNotice(kind: "task", id: target); return }
            tab = .today
        case "settings":
            openSettings()
            return
        case "goals":
            tab = .today
        default:
            sheet = nil
            selectedTab = .today
            linkNotice = String(localized: "Couldn't open that link.")
            return
        }
        if tab == .chat { topicFilter = nil }
        sheet = nil
        selectedTab = tab
    }

    /// Shows the chat, which is the Chat tab itself.
    func openTopic(_ route: TopicRoute) {
        sheet = nil
        topicFilter = route
        selectedTab = .chat
    }

    func openBrief(_ briefID: String, landsOnSpoken: Bool = false) {
        pendingCurrentBrief = false
        briefRoute = BriefRoute(briefID: briefID, landsOnSpoken: landsOnSpoken)
    }

    func resolveCurrentBrief(_ briefID: String?) {
        guard pendingCurrentBrief, let briefID, NoticeIdentifier.isSafe(briefID) else { return }
        openBrief(briefID)
    }

    func openMainChat() {
        topicFilter = nil
        sheet = nil
        selectedTab = .chat
    }

    func openSettings() {
        sheet = .settings
    }

    /// Opens Pairing, over Settings if it is open.
    /// "Paired with {computer}", for two seconds above the tabs after pairing (phone-setup rule 6).
    @Published var pairedToast: String?

    func openPairing(scanCode: Bool = false) {
        startsPairingWithScanner = scanCode
        sheet = .pairing
    }

    /// Pairing is done: its sheet closes and Today shows the first brief.
    func pairingFinished() {
        topicFilter = nil
        sheet = nil
        selectedTab = .today
    }
}
