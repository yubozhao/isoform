import SwiftUI

/// Review a prepared reply in place, using the same draft and action as its task thread.
private struct TodayDraftReviewSheet: View {
    let task: MobileTask
    @ObservedObject var tasks: TasksStore
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var brief: BriefStore
    @State private var sendingSince: Date?
    @State private var pendingBody: String?
    @State private var hold: Task<Void, Never>?
    @State private var failed = false

    private var detail: MobileTaskDetail? {
        guard case .loaded(let value) = tasks.detailLoadState, value.id == task.id else { return nil }
        return value
    }

    private var pending: PendingWrite? {
        tasks.pendingDraftSends.first { $0.todayDraftSend?.taskID == task.id }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text(task.title).font(.title3.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                    if let draft = detail?.draft {
                        if let pending, let send = pending.todayDraftSend, send.failed != true,
                           (!tasks.computerReachable || Date().timeIntervalSince(pending.createdAt) >= 5) {
                            HStack {
                                Text(send.dispatchedAt == nil ? String(localized: "today.draft.waiting") : String(localized: "today.draft.checking"))
                                    .foregroundStyle(YansuTheme.secondary)
                                Spacer()
                                if send.dispatchedAt == nil {
                                    Button(String(localized: "Undo")) { Task { await tasks.undoDraftSend(pending.key) } }
                                        .accessibilityIdentifier("today.draft.undo")
                                }
                            }
                            .font(.subheadline)
                            .accessibilityElement(children: .contain)
                            .accessibilityIdentifier("today.draft.pending")
                        } else {
                            TaskDraftCard(draft: draft, person: draft.recipients?.to.first ?? draft.to,
                                          pendingSince: pending?.todayDraftSend?.failed == true ? nil : (pending?.createdAt ?? sendingSince),
                                          failed: failed || pending?.todayDraftSend?.failed == true,
                                          canSend: detail?.actions?.contains("send") == true && sendingSince == nil,
                                          send: { body in sendDraft(body, revision: draft.revision, name: draft.name) }, undo: undoSend,
                                          notNow: nil, failedText: pending?.todayDraftSend?.text ?? pendingBody)
                        }
                        if draft.channel == "mail" { PhoneSourceLink(kind: .mail) }
                    } else if case .failed = tasks.detailLoadState {
                        Button(String(localized: "Try again")) { Task { _ = await tasks.loadDetail(id: task.id) } }
                    } else {
                        ProgressView()
                    }
                }
                .padding(YansuTheme.gutter)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(YansuTheme.background.ignoresSafeArea())
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(String(localized: "Done")) { dismiss() }
                        .accessibilityIdentifier("today.review.done")
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("today.review.sheet")
        .task { _ = await tasks.loadDetail(id: task.id) }
    }

    private func sendDraft(_ body: String?, revision: String?, name: String?) {
        pendingBody = body
        guard let revision, !revision.isEmpty else {
            if brief.hubOnline == false { failed = true; return }
            holdSend(body)
            return
        }
        Task { failed = !(await tasks.queueDraftSend(taskID: task.id, revision: revision, text: body, person: name)) }
    }

    private func holdSend(_ body: String?) {
        failed = false
        pendingBody = body
        sendingSince = Date()
        hold = Task { @MainActor in
            do { try await Task.sleep(for: .seconds(5)) } catch { return }
            let sent = await tasks.act(id: task.id, action: "send", text: body)
            sendingSince = nil
            failed = !sent
        }
    }

    private func undoSend() {
        if let pending { Task { await tasks.undoDraftSend(pending.key) }; return }
        hold?.cancel()
        hold = nil
        sendingSince = nil
    }
}

struct FeedView: View {
    @ObservedObject var feed: FeedStore
    @ObservedObject var topics: TopicStore
    var onSelect: ((String) -> Void)? = nil
    var onClose: (() -> Void)? = nil
    var selectedID: String? = nil
    var cardsOnly = false
    var showsSelectedItem = false
    var cardNamespace: Namespace.ID? = nil
    var foldX: CGFloat? = nil
    var windowWidth: CGFloat = 0
    var onPrepareInTopic: ((String, String) -> Bool)? = nil
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var brief: BriefStore
    @EnvironmentObject private var todayStore: TodayStore
    @EnvironmentObject private var tasks: TasksStore
    @EnvironmentObject private var work: WorkStore
    @EnvironmentObject private var push: PushRegistration
    @EnvironmentObject private var actions: ActionStore
    @EnvironmentObject private var notifications: NotificationSettingsStore
    @EnvironmentObject private var keepAwake: KeepAwakeStore
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var router: NotificationRouter
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.tabChromeHeight) private var tabChromeHeight
    @State private var evidence: EvidenceRequest?
    @State private var showingPastBriefs = false
    /// Whether the Today page has scrolled under its title bar (P160).
    @State private var pageScrolled = false
    @State private var reviewTask: MobileTask?
    @State private var correcting: FeedCardModel?
    @StateObject private var undoHolds = DecisionHoldCoordinator()
    @State private var heldFeedback: Set<String> = []
    @State private var feedbackHaptics = 0
    @State private var undoHaptics = 0
    @State private var refreshHaptics = 0
    @State private var isRefreshing = false
    @State private var showingOfflineNote = false
    @State private var showingComputer = false
    @State private var connectionCheckedAt: Date?
    /// Once visible, keep the placeholder on screen until its copy arrives, even after marking it shown.
    @State private var placeholderHubID: String?
    /// Bumped when the brief folds, so the card reads its fold again.
    @State private var foldRevision = 0
    @AppStorage("today.keepAwakeOfferDismissed") private var keepAwakeOfferDismissed = false
    @Environment(\.locale) private var locale
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var scopes: [String] { pairing.paired?.scopes ?? [] }
    private var canGiveFeedback: Bool { SettingsView.allowsFeedFeedback(scopes) }

    private func mayShowPlaceholder(hubID: String) -> Bool {
        placeholderHubID == hubID || !TodayBriefPlaceholder.wasShown(hubID: hubID)
    }

    var body: some View {
        content(now: Date())
        // The watch line: only when hours wait for the computer or failed; it opens the Watch tab.
        .safeAreaInset(edge: .top, spacing: 0) {
            WatchTodayLine(model: WatchRecordingsModel.shared) { router.selectedTab = .watch }
        }
        .overlay(alignment: .top) {
            if let notice = router.linkNotice {
                NoticeBanner(message: notice, dismiss: { router.linkNotice = nil })
                    .padding(.horizontal, YansuTheme.gutter)
                    .accessibilityIdentifier("today.linkNotice")
            }
        }
        .refreshable {
            await refresh(latest: true)
        }
        .task {
            guard pairing.isPaired else { return }
            await refresh(latest: false)
        }
        .yansuScreen()
        // The bar shows the Isoform mark; the title names this tab on a pushed screen's back button.
        .navigationTitle(String(localized: "Today"))
        .navigationBarTitleDisplayMode(.large)
        .tabRootBar()
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                WatchTodayMark(model: WatchRecordingsModel.shared) { router.selectedTab = .watch }
            }
        }
        .environment(\.titleBarScrolled, pageScrolled)
        .navigationDestination(isPresented: $showingPastBriefs) {
            PastBriefsView()
        }
        .navigationDestination(for: HistoryEntry.self) { entry in
            historyDetail(entry)
        }
        .taskThreads(environment.tasks, from: .today)
        .navigationDestination(for: TopicRoute.self) { route in
            TopicView(route: route, topics: topics)
        }
        .onChange(of: router.briefPushes) { _, _ in
            Task { await brief.refresh() }
        }
        .onChange(of: scenePhase) { _, phase in
            guard DecisionHoldCoordinator.shouldFlush(when: phase) else { return }
            Task { await undoHolds.flushAll() }
        }
        .sheet(item: $evidence) { request in
            EvidenceSheet(request: request)
        }
        .sheet(isPresented: $showingComputer) {
            NavigationStack {
                if let cause = connectionCause {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 18) {
                            VStack(alignment: .leading, spacing: 8) {
                                Text(awayFirstLine(cause))
                                    .font(.headline).foregroundStyle(YansuTheme.ink)
                                    .accessibilityIdentifier("today.connection.status")
                                Text("connection.copyAtThatTime").font(.footnote).foregroundStyle(YansuTheme.secondary)
                            }.frame(maxWidth: .infinity, alignment: .leading)
                                .padding(16).background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius))
                            if cause == .sleeping {
                                // The same words Today's away line shows (P147), with the waiting clause when something waits.
                                Text(ComputerSleepReason.directions(for: cause, reason: sleepReason, sentWaiting: environment.chat.hasUnsentMessages))
                                    .font(.body).foregroundStyle(YansuTheme.secondary)
                                    .accessibilityIdentifier("today.connection.steps")
                                Text("You can still ask and act. Isoform does it when your computer wakes.")
                                    .font(.body).foregroundStyle(YansuTheme.secondary)
                                    .accessibilityIdentifier("today.connection.canStillAct")
                            } else {
                                Text(TodayStore.ConnectionCause.directions(cause, lastConnected: todayStore.lastConnected)).font(.body).foregroundStyle(YansuTheme.secondary)
                                    .accessibilityIdentifier("today.connection.directions")
                            }
                            // Shown only when the computer says keep awake is off; an unknown setting hides it.
                            if cause == .sleeping && keepAwake.on == false && !keepAwakeOfferDismissed {
                                VStack(alignment: .leading, spacing: 8) {
                                    Text("connection.keepAwakeOffer").font(.footnote).foregroundStyle(YansuTheme.secondary)
                                    HStack {
                                        Button("Turn on") { Task { await keepAwake.set(true) } }
                                        Button("today.keepAwake.notNow") { keepAwakeOfferDismissed = true }
                                    }.foregroundStyle(YansuTheme.accent)
                                }.accessibilityIdentifier("today.connection.keepAwakeOffer")
                            }
                            todayPendingWrites
                            Button(String(localized: "connection.checkAgain")) {
                                Task { await todayStore.refresh(); connectionCheckedAt = Date() }
                            }.buttonStyle(YansuSecondaryButtonStyle())
                            if let checked = connectionCheckedAt {
                                Text(String.localizedStringWithFormat(String(localized: "connection.checkedAt"), todayTime(checked)))
                                    .font(.footnote).foregroundStyle(YansuTheme.secondary)
                            }
                        }.frame(maxWidth: .infinity, alignment: .leading).padding(YansuTheme.gutter)
                    }.yansuScreen()
                        .navigationTitle(String(localized: "Your computer"))
                        .toolbar { ToolbarItem(placement: .confirmationAction) { Button(String(localized: "Done")) { showingComputer = false } } }
                } else {
                    Text("connection.back").font(.headline).foregroundStyle(YansuTheme.ink)
                        .navigationTitle(String(localized: "Your computer"))
                }
            }
            .accessibilityIdentifier("today.connection.sheet")
        }
        .onChange(of: connectionCause) { old, new in
            guard old != nil, new == nil, showingComputer else { return }
            Task { try? await Task.sleep(for: .milliseconds(1500)); if connectionCause == nil { showingComputer = false } }
        }
        .sheet(item: $reviewTask) { task in
            TodayDraftReviewSheet(task: task, tasks: environment.tasks)
        }
        .sheet(item: $correcting) { card in
            ReasonSheet(title: String(localized: "Not right"),
                        detail: String(localized: "Tell Isoform what is wrong with this. It stops using that memory once your computer confirms."),
                        prompt: String(localized: "What is not right"),
                        confirmTitle: String(localized: "Send to your computer"),
                        required: true,
                        notice: feed.notice,
                        dismissNotice: feed.dismissNotice) { reason in
                await holdFeedback(.notRight, for: card, reason: reason)
            }
        }
        .sensoryFeedback(.success, trigger: feedbackHaptics)
        .sensoryFeedback(.impact(weight: .light), trigger: undoHaptics)
        .sensoryFeedback(.selection, trigger: refreshHaptics)
    }

    /// The briefs and the topics are read side by side: a computer that is slow to list topics never holds the
    /// briefs back.
    private func refresh(latest: Bool) async {
        let (brief, feed, topics) = (brief, feed, topics)
        let readsBriefs = latest || environment.refreshesBriefs
        let readsTopics = latest || environment.refreshesTopics
        isRefreshing = true
        defer {
            isRefreshing = false
            if latest { refreshHaptics += 1 }
        }
        await withTaskGroup(of: Void.self) { group in
            if latest { group.addTask { await environment.today.refresh() } }
            group.addTask { await environment.work.refresh() }
            if environment.refreshesTasks { group.addTask { await environment.tasks.refresh() } }
            if readsTopics { group.addTask { await topics.refresh() } }
            if readsBriefs {
                group.addTask {
                    if latest { await brief.refresh() }
                    await brief.refreshHistory()
                    await feed.retryPending()
                }
            }
        }
    }

    @ViewBuilder
    private func content(now: Date) -> some View {
        let today = FeedAssembly.localDate(for: now)
        let days = FeedAssembly.days(from: brief.cachedBriefs, today: today)
        let cards = days.first { $0.localDate == today }?.cards ?? []
        if showsSelectedItem, let selectedID, let card = cards.first(where: { $0.id == selectedID }) {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    DuoFoldSafeControl(foldX: foldX, windowWidth: windowWidth,
                                       width: 72, height: 40) {
                        Button(String(localized: "Close")) { onClose?() }
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(YansuTheme.accent)
                            .accessibilityIdentifier("duo.today.item.close")
                    }
                    matchedCard(card, now: now, actionsAtTop: true, isSource: true)
                }
                .padding(YansuTheme.gutter)
            }
            .accessibilityIdentifier("duo.today.item")
        } else if cardsOnly {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    sectionLabel("Cards")
                    ForEach(cards) { card in
                        Button { onSelect?(card.id) } label: {
                            matchedCard(card, now: now, isSource: true, showsActions: false,
                                        selectableOnTap: false,
                                        usesGeometryEffect: selectedID != card.id)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("duo.today.card.\(card.id)")
                    }
                }
                .padding(YansuTheme.gutter)
            }
            .accessibilityIdentifier("duo.today.cards")
        } else {
            ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    if pairing.isPaired {
                        if let hubID = pairing.paired?.hubID, todayStore.snapshot == nil,
                           brief.current == nil, !brief.repairNeeded, mayShowPlaceholder(hubID: hubID) {
                            todaySkeleton(hubID: hubID)
                                .transition(.opacity)
                        } else {
                            pairedContent(now: now)
                                .transition(reduceMotion ? .opacity : .offset(y: 8).combined(with: .opacity))
                        }
                    } else if pairing.recoveryMessage != nil {
                        // A lost pairing never blanks Today (phone-setup rule 11): one amber line with Pair, and the
                        // brief this phone saved stays under it.
                        PairingLostLine(retriesSavedPairing: true)
                        if let saved = todayStore.newestBrief() {
                            TodayBriefCardView(current: saved, hubID: "", player: environment.audioPlayer,
                                               canMakeBrief: false, now: now, onPastBriefs: { showingPastBriefs = true })
                        }
                    } else {
                        PairPromptCard(
                            title: String(localized: "Read your computer's daily brief here."),
                            detail: String(localized: "Pair this phone with the Isoform app on your computer. Everything is encrypted between this phone and your computer.")
                        )
                    }
                }
                .padding(.horizontal, YansuTheme.gutter)
                .padding(.top, 16)
                .padding(.bottom, 48 + tabChromeHeight)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .environment(\.correctionReveal) { id in proxy.scrollTo(id, anchor: InlineMemoryCorrection.revealAnchor) }
            .task(id: router.upNextRequest) {
                // "See them" and anything else that named Up next: Your work holds it now (phone-today rule 19).
                guard let request = router.upNextRequest else { return }
                try? await Task.sleep(for: .milliseconds(300))
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) {
                    proxy.scrollTo("today.yourWork.anchor", anchor: .top)
                }
                if router.upNextRequest == request { router.upNextRequest = nil }
            }
            .task(id: router.heldTaskRowRequest) {
                guard let id = router.heldTaskRowRequest else { return }
                // Today lays out after the tab opens; scroll once it has.
                try? await Task.sleep(for: .milliseconds(300))
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) {
                    proxy.scrollTo("task.row.\(id)", anchor: .center)
                }
                router.consumeHeldTaskRow(id)
            }
            .onChange(of: router.pendingTodayDay, initial: true) { _, pending in
                guard pending, todayStore.snapshot != nil || brief.current != nil else { return }
                DispatchQueue.main.async {
                    proxy.scrollTo("today.day.anchor", anchor: .top)
                    router.pendingTodayDay = false
                }
            }
            .onChange(of: todayStore.snapshot?.generatedAt) { _, _ in
                guard router.pendingTodayDay, todayStore.snapshot != nil else { return }
                DispatchQueue.main.async {
                    proxy.scrollTo("today.day.anchor", anchor: .top)
                    router.pendingTodayDay = false
                }
            }
            .onChange(of: brief.current?.identifier) { _, id in
                guard router.pendingTodayDay, id != nil else { return }
                DispatchQueue.main.async {
                    proxy.scrollTo("today.day.anchor", anchor: .top)
                    router.pendingTodayDay = false
                }
            }
            .tabBarScrollVisibility()
            .titleBarScrollTracking($pageScrolled)
            .animation(reduceMotion ? .easeOut(duration: 0.18) : .easeOut(duration: 0.25), value: todayStore.snapshot == nil)
            .animation(reduceMotion ? .easeOut(duration: 0.18) : .easeOut(duration: 0.25), value: brief.current?.identifier)
            }
        }
    }

    @ViewBuilder
    private func matchedCard(_ card: FeedCardModel, now: Date, actionsAtTop: Bool = false,
                             isSource: Bool, showsActions: Bool = true,
                             selectableOnTap: Bool = true,
                             usesGeometryEffect: Bool = true) -> some View {
        let view = feedCard(card, now: now, actionsAtTop: actionsAtTop, showsActions: showsActions,
                            selectableOnTap: selectableOnTap)
        if usesGeometryEffect, let cardNamespace {
            view.matchedGeometryEffect(id: "today.\(card.id)", in: cardNamespace, isSource: isSource)
        } else {
            view
        }
    }

    // MARK: Paired

    private func feedCard(_ card: FeedCardModel, now: Date, actionsAtTop: Bool = false,
                          showsActions: Bool = true, selectableOnTap: Bool = true) -> FeedCard {
        FeedCard(card: card, choice: feed.choices[card.key], canGiveFeedback: canGiveFeedback,
                 canSend: brief.hubOnline != false, now: now,
                 like: { Task { await holdFeedback(.like, for: card) } },
                 notRight: { notRight(card) },
                 showSources: {
                     evidence = EvidenceRequest(title: card.item.title ?? String(localized: "Sources"),
                                                sources: card.sources)
                 },
                 isSelected: selectedID == card.id,
                 actionsAtTop: actionsAtTop,
                 showsActions: showsActions,
                 onSelect: selectableOnTap ? { onSelect?(card.id) } : nil)
    }

    @ViewBuilder
    private func pairedContent(now: Date) -> some View {
        if brief.repairNeeded {
            // The pairing lost its rights on the computer: the same one line, and Today stays as saved (rule 11).
            PairingLostLine()
        }
        Group {
            todaySummary(now: now)
            let _ = brief.logFetchOutcomes(at: "render")
            if let signOut = todayStore.snapshot?.assistantSignOut {
                // R75: one line under the date. The sign-in happens on the computer, so there is no button.
                Text(String(localized: "\(signOut.displayName) is signed out on your computer. Briefs are shorter until you sign in there."))
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("today.assistantSignedOut")
            }
            if let current = todayStore.newestBrief(), let hubID = pairing.paired?.hubID {
                if !Calendar.current.isDate(current.asOfDate ?? .distantPast, inSameDayAs: now) {
                    todayFetchLine(state: brief.todayFetchState(localDate: FeedAssembly.localDate(for: now)))
                }
                let _ = foldRevision
                TodayBriefCardView(current: current, hubID: hubID, player: environment.audioPlayer,
                                   topics: topics, onPrepareInTopic: onPrepareInTopic,
                                   canMakeBrief: scopes.contains("brief:refresh"), now: now,
                                   onPastBriefs: { showingPastBriefs = true },
                                   folded: TodayBriefFold.isFolded(current.identifier))
                if push.showsPermissionExplanation {
                    NotificationAskCard()
                }
                if !Calendar.current.isDate(current.asOfDate ?? .distantPast, inSameDayAs: now),
                   let line = TodayAwaitingBrief.nextBriefLine(in: todayStore.snapshot, now: now, locale: locale) {
                    Text(line)
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("today.nextBrief")
                }
            } else {
                TodayAwaitingBriefCard(computerName: pairing.paired?.hubName ?? String(localized: "your computer"),
                                       snapshot: todayStore.snapshot, now: now)
            }
            // Your work: the one list, under the brief. It loads on its own and never moves the brief (rank 2).
            YourWorkSection(work: work, now: now, onAnswered: foldNewestBrief) {
                legacyYourWork
            }
            .id("today.yourWork.anchor")
            heldReplies
            if work.supported == true {
                // Tasks waiting on the person that Your work doesn't list stay on Today (review 2026-11-15).
                todayNeedsYou(YourWork.waitingOutside(tasks.waitingItems, items: work.items), title: String(localized: "Needs you"))
            }
            if let notice = actions.todayNotice { NoticeBanner(message: notice, dismiss: { actions.todayNotice = nil }) }
            if let notice = feed.todayNotice { NoticeBanner(message: notice, dismiss: { feed.todayNotice = nil }) }
            todayPendingWrites
            Color.clear.frame(height: 0).id("today.day.anchor")
        }
    }

    private func todaySkeleton(hubID: String) -> some View {
        FirstCopyShapes(identifier: "feed.skeleton")
            .accessibilityLabel(String(localized: "Your brief will appear here."))
            .onAppear {
                placeholderHubID = hubID
                TodayBriefPlaceholder.markShown(hubID: hubID)
            }
    }

    /// Today's away times use Chat's day wording, so one goodbye time reads the same on both screens: the time alone
    /// today, "yesterday at" the day before, the date after that.
    private func todayTime(_ date: Date) -> String {
        ChatAwayCopy.since(date, now: Date(), locale: locale, calendar: .current).trimmingCharacters(in: .whitespaces)
    }

    /// The Computer sheet's first line, also Today's away line (P133). One function, so the two can't drift.
    // The past-brief destination, kept out of the body so the type checker can still read it.
    private func historyDetail(_ entry: HistoryEntry) -> some View {
        BriefDetailView(entry: entry, player: environment.audioPlayer,
                        fallbackLead: todayStore.snapshot?.briefs.first { $0.id == entry.id }?.lead)
    }

    /// One line for the sends that went a day unanswered, in Chat or Tasks. Show opens the oldest of them.
    private var expiredNotice: (text: String, open: () -> Void)? {
        var items = environment.chat.expiredWaitingMessages(now: Date()).map {
            HeldSendNotice.Item(place: .chat(messageID: $0.id), sentAt: $0.sentAt, person: nil)
        }
        items += tasks.expiredDraftSends.compactMap { write in
            guard let draft = write.todayDraftSend else { return nil }
            return HeldSendNotice.Item(place: .task(taskID: draft.taskID), sentAt: write.createdAt, person: draft.person)
        }
        guard let notice = HeldSendNotice.make(items) else { return nil }
        return (notice.text, {
            switch notice.oldest {
            case .chat(let messageID): router.showChatMessage(messageID)
            case .task(let taskID): router.showHeldTaskRow(taskID)
            }
        })
    }

    /// Replies the computer did not take in a day (PROTOCOL.md section 23): each says so, with Send again. They sat on
    /// their Tasks row until the Tasks tab went (phone-today rule 19); Today's Show scrolls here now.
    @ViewBuilder
    private var heldReplies: some View {
        let held = tasks.expiredDraftSends.compactMap { write in write.todayDraftSend.map { (write, $0) } }
        if !held.isEmpty {
            VStack(spacing: 0) {
                ForEach(Array(held.enumerated()), id: \.element.0.key) { index, pair in
                    let (write, draft) = pair
                    VStack(alignment: .leading, spacing: 6) {
                        Text(verbatim: heldTitle(draft.taskID))
                            .font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            Text(String(localized: "This waited a day, so it wasn't sent."))
                                .font(.footnote).foregroundStyle(YansuTheme.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                                .accessibilityIdentifier("tasks.held.line")
                            Button(String(localized: "Send again")) { Task { await tasks.sendExpiredAgain(write) } }
                                .buttonStyle(.plain)
                                .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.accent)
                                .accessibilityIdentifier("tasks.sendAgain.\(draft.taskID)")
                        }
                    }
                    .padding(16)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("tasks.held.\(draft.taskID)")
                    .id("task.row.\(draft.taskID)")
                    if index < held.count - 1 { Divider().overlay(YansuTheme.borderSubtle).padding(.leading, 16) }
                }
            }
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
        }
    }

    private func heldTitle(_ taskID: String) -> String {
        let known = (tasks.page?.tasks ?? []) + tasks.waitingItems.compactMap(\.task)
        return known.first { $0.id == taskID }?.title ?? String(localized: "today.heldReply")
    }

    /// The write the phone holds for this Needs you card, if any.
    private func queuedWrite(for need: TodayNeed) -> PendingWrite? {
        guard let id = need.card?.proactive?.momentID else { return nil }
        return actions.pendingToday.first { $0.todayReplay?.momentID == id }
    }

    /// Held a day with no answer: the relay let it go, so the card needs the person again.
    private func isExpiredHeld(_ write: PendingWrite) -> Bool {
        Date().timeIntervalSince(write.createdAt) >= TaskSendWait.relayRetention
    }

    private func awayFirstLine(_ cause: TodayStore.ConnectionCause) -> String {
        TodayStore.ConnectionCause.firstLine(cause, reason: sleepReason, sleptAt: sleptAt.map(todayTime), asOf: todayStore.asOf.map(todayTime),
                                             lastConnected: todayStore.lastConnected.map(todayTime))
    }

    private var connectionCause: TodayStore.ConnectionCause? {
        #if DEBUG && targetEnvironment(simulator)
        if let seed = ProcessInfo.processInfo.environment["YANSU_UITEST_CONNECTION_CAUSE"],
           let cause = TodayStore.ConnectionCause(rawValue: seed) { return cause }
        #endif
        return todayStore.connectionCause
    }

    /// When the computer went to sleep: the relay goodbye's time. Nil when no goodbye has a time.
    private var sleptAt: Date? {
        #if DEBUG && targetEnvironment(simulator)
        if let seed = ProcessInfo.processInfo.environment["YANSU_UITEST_GOODBYE_AT"], let date = RelayTime.parse(seed) { return date }
        #endif
        return todayStore.connectionSince
    }

    private var sleepReason: ComputerSleepReason? {
        #if DEBUG && targetEnvironment(simulator)
        if let seed = ProcessInfo.processInfo.environment["YANSU_UITEST_GOODBYE_REASON"] {
            return ComputerSleepReason(rawValue: seed)
        }
        #endif
        return todayStore.connectionReason.flatMap(ComputerSleepReason.init(rawValue:))
    }

    private func todaySummary(now: Date) -> some View {
        let date: String = {
            if locale.language.languageCode?.identifier == "zh" {
                let formatter = DateFormatter()
                formatter.locale = locale
                formatter.dateFormat = "M月d日 EEEE"
                return formatter.string(from: now)
            }
            return now.formatted(.dateTime.weekday(.wide).month(.wide).day().locale(locale))
        }()
        let cause = connectionCause
        return VStack(alignment: .leading, spacing: 8) {
            // The date line always stays (P133). The away line sits under it, in the sheet's own words, and opens the sheet.
            // The date under the title; the count to answer is Your work's own (phone-today rule 8).
            Text(verbatim: date)
            .font(.subheadline)
            .foregroundStyle(YansuTheme.secondary)
            .accessibilityIdentifier("feed.summary")
            if let cause {
                Button { showingComputer = true } label: {
                    HStack(spacing: 5) {
                        Circle().fill(YansuTheme.tertiary).frame(width: 5, height: 5)
                        Text(awayFirstLine(cause))
                        Image(systemName: "chevron.right").font(.caption2)
                    }.font(.subheadline).foregroundStyle(YansuTheme.secondary)
                }.buttonStyle(.plain).accessibilityIdentifier("today.connection.line")
            }
            if let expired = expiredNotice {
                // A send the computer never answered for a day: the line says so, and Show opens it (PROTOCOL.md section 23).
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(expired.text)
                        .font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("today.expired.line")
                    Button(String(localized: "Show"), action: expired.open)
                        .buttonStyle(.plain)
                        .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("today.expired.show")
                }
            }
            if let cause, showingOfflineNote, todayStore.asOf != nil {
                Text(ComputerSleepReason.directions(for: cause, reason: sleepReason, sentWaiting: environment.chat.hasUnsentMessages))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if cause == .sleeping {
                    Text("You can still ask and act. Isoform does it when your computer wakes.")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("today.connection.canStillAct")
                }
                if cause == .sleeping && keepAwake.on == false && !keepAwakeOfferDismissed {
                    Text("To reach it while it's plugged in, keep it awake for your phone.")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                    HStack {
                        Button("Turn on") { Task { await keepAwake.set(true) } }
                        Button("today.keepAwake.notNow") { keepAwakeOfferDismissed = true }
                    }
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .accessibilityIdentifier("today.keepAwakeOffer")
                } else if keepAwake.pending && keepAwake.on == true {
                    Text("Turns on when your computer is back.")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
            }
        }
        .onChange(of: cause, initial: true) { _, value in
            showingOfflineNote = value != nil
        }
    }

    /// Today's brief is missing (#631): while the phone tries, it says so; once the tries stop it keeps saying so, with
    /// Try again, until today's brief arrives.
    @ViewBuilder
    private func todayFetchLine(state: BriefStore.TodayFetchState) -> some View {
        let computer = pairing.paired?.hubName ?? String(localized: "your computer")
        switch state {
        case .none:
            EmptyView()
        case .retrying:
            Text(String.localizedStringWithFormat(String(localized: "Couldn't get today's brief from %@ yet. Trying again."), computer))
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
                .accessibilityIdentifier("today.fetchFailing")
        case .stopped:
            VStack(alignment: .leading, spacing: 10) {
                Text(String.localizedStringWithFormat(String(localized: "Couldn't get today's brief from %@."), computer))
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("today.fetchStopped")
                // Try again is a text link under its line, in the accent color, never a full-width button.
                Button(String(localized: "Try again")) { Task { await todayStore.refresh() } }
                    .buttonStyle(.plain)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .accessibilityIdentifier("today.fetchRetry")
            }
            // Opening Today again starts a fresh try while the line says it stopped (#631).
            .task { await todayStore.refresh() }
        }
    }

    private func foldNewestBrief() {
        guard let id = todayStore.newestBrief()?.identifier else { return }
        TodayBriefFold.fold(id)
        foldRevision += 1
    }

    /// Your work on a computer without B2: the rows it already sends (Needs you, Done for you), in Your work's place
    /// and words, so nothing the Tasks tab showed is lost while the computer catches up.
    @ViewBuilder
    private var legacyYourWork: some View {
        // A queued card that is on its way to the computer no longer needs the person.
        let count = max(0, tasks.waitingCount - tasks.waitingItems.filter { queuedWrite(for: $0).map { !isExpiredHeld($0) } ?? false }.count)
        VStack(alignment: .leading, spacing: 12) {
            YourWorkHeader(count: count)
            todayNeedsYou
            todayDoneForYou
            if count == 0 && tasks.doneForToday(now: Date(), briefMadeAt: todayStore.snapshot?.latestBrief?.madeDate).isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("work.empty").font(.body.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                    Text("work.emptyNext").font(.subheadline).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("work.empty")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("today.yourWork")
    }

    private var todayNeedsYou: some View {
        todayNeedsYou(tasks.waitingItems, title: String(localized: "work.upNext"))
    }

    @ViewBuilder
    private func todayNeedsYou(_ needs: [TodayNeed], title: String) -> some View {
        let rows = Array(needs.prefix(YourWork.dailyLimit))
        if !rows.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                SectionLabel(title: title)
                    .padding(.horizontal, 4)
                VStack(spacing: 0) {
                    ForEach(Array(rows.enumerated()), id: \.element.id) { index, need in
                        if let task = need.task {
                            if task.status == "draft_ready" {
                                Button { reviewTask = task } label: {
                                    todayTaskRow(task)
                                }
                                .buttonStyle(.plain)
                                .accessibilityIdentifier("today.review.\(task.id)")
                            } else {
                                NavigationLink {
                                    TaskThreadView(task: task, tasks: tasks, source: .today)
                                } label: { todayTaskRow(task) }
                                .buttonStyle(.plain)
                                .accessibilityIdentifier("tasks.row.\(task.id)")
                            }
                        } else if need.card?.proactive != nil {
                            PhoneWaitingProactiveRow(need: need)
                        }
                        if index < rows.count - 1 { Divider().overlay(YansuTheme.borderSubtle).padding(.leading, 36) }
                    }
                }
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("today.needsYou")
        }
    }

    private func todayTaskRow(_ task: MobileTask) -> some View {
        HStack(alignment: .center, spacing: 12) {
            Circle().fill(YansuTheme.warning).frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 4) {
                Text(task.title).font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                if let source = todayTaskSource(task) {
                    Text(source).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Text(String(localized: "Review"))
                .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                .padding(.horizontal, 12).padding(.vertical, 6)
                .overlay(Capsule().stroke(YansuTheme.border))
        }
        .padding(16)
        .contentShape(Rectangle())
    }

    private func todayTaskSource(_ task: MobileTask) -> String? {
        let source: String?
        switch task.source {
        case "mail": source = String(localized: "today.fromMail")
        case "slack": source = String(localized: "today.fromSlack")
        case "up_next": source = String(localized: "today.fromUpNext")
        case "brief_chip": source = String(localized: "today.fromBrief")
        case "chat": source = String(localized: "today.fromChat")
        case "list": source = String(localized: "today.fromList")
        case "routine": source = String(localized: "today.fromRoutine")
        default: source = nil
        }
        if let source, let at = RelayTime.parse(task.createdAt) {
            return String.localizedStringWithFormat(String(localized: "today.source.withTime"), source, todayTime(at))
        }
        return source
    }

    @ViewBuilder
    private var todayDoneForYou: some View {
        let rows = tasks.doneForToday(now: Date(), briefMadeAt: todayStore.snapshot?.latestBrief?.madeDate)
        if !rows.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                SectionLabel(title: String(localized: "work.doneToday"))
                    .padding(.horizontal, 4)
                VStack(spacing: 0) {
                    ForEach(Array(rows.enumerated()), id: \.element.id) { index, task in
                        TaskRow(task: task, directTasks: tasks, source: .today, grouped: true)
                        if index < rows.count - 1 { Divider().overlay(YansuTheme.borderSubtle).padding(.leading, 56) }
                    }
                }
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("today.doneForYou")
        }
    }

    /// The needs-you rows still shown on the phone: those without a write in flight.
    static func needsYouRows(_ needs: [TodayNeed], pendingIDs: Set<String>) -> [TodayNeed] {
        needs.filter { !pendingIDs.contains($0.card?.proactive?.momentID ?? "") }
    }

    @ViewBuilder
    private var todayPendingWrites: some View {
        // A queued card stays on its Needs you row (queuedWrite), so only the other held writes show here.
        let corrections = actions.pendingToday.filter { $0.todayCorrection != nil }
        if !corrections.isEmpty || !feed.pendingToday.isEmpty ||
            !notifications.pendingToday.isEmpty || todayStore.pendingFresh != nil {
            VStack(alignment: .leading, spacing: 12) {
                sectionLabel("Waits for your computer")
                if let pending = todayStore.pendingFresh {
                    HStack {
                        Text("Make my brief now").font(.subheadline)
                        Spacer()
                        if pending.todayRefreshDispatchedAt == nil {
                            Button("Undo") { Task { await todayStore.undoFresh() } }
                        }
                    }
                }
                ForEach(corrections, id: \.key) { write in
                    HStack {
                        Text("Not right").font(.subheadline)
                        Spacer()
                        if write.todayCorrection?.dispatchedAt == nil {
                            Button("Undo") { Task { await actions.undoToday(write.key) } }
                        }
                    }
                }
                ForEach(feed.pendingToday, id: \.key) { write in
                    if let feedback = write.todayFeedback {
                        HStack {
                            Text(verbatim: feedback.title).font(.subheadline)
                            Spacer()
                            if feedback.dispatchedAt == nil { Button("Undo") { Task { await feed.undoToday(write.key) } } }
                        }
                    }
                }
                ForEach(notifications.pendingToday, id: \.idempotencyKey) { change in
                    HStack {
                        Text("Notification settings").font(.subheadline)
                        Spacer()
                        if change.dispatched == false { Button("Undo") { notifications.undoToday(change.idempotencyKey) } }
                    }
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("feed.pending")
        }
    }

    private func sectionLabel(_ title: LocalizedStringKey) -> some View {
        Text(title)
            .font(.caption.weight(.semibold))
            .tracking(0.8)
            .textCase(.uppercase)
            .foregroundStyle(YansuTheme.secondary)
            .accessibilityAddTraits(.isHeader)
    }

    // MARK: Actions

    private func notRight(_ card: FeedCardModel) {
        if card.memoryID != nil {
            guard SettingsView.allowsMemoryCorrection(scopes) else {
                feed.notice = String(localized: "Choose Pair again in Settings to correct what Isoform remembers.")
                return
            }
            feed.dismissNotice()
            correcting = card
        } else {
            Task { await holdFeedback(.notRight, for: card) }
        }
    }

    private func holdFeedback(_ action: FeedFeedbackAction, for card: FeedCardModel,
                              reason: String? = nil) async -> Bool {
        guard let approval = await feed.authorizeFeedback(action, for: card, reason: reason) else { return false }
        withAnimation(.spring(duration: 0.45)) {
            heldFeedback.insert(card.key)
            undoHolds.begin(.init(id: card.key, label: String(localized: "Sent to your computer"),
                                  detail: card.item.title ?? String(localized: "Untitled"))) {
                let succeeded = await feed.commitFeedback(approval)
                withAnimation(reduceMotion ? .easeOut(duration: 0.25) : .easeOut(duration: 0.35)) {
                    heldFeedback.remove(card.key)
                }
                if succeeded { feedbackHaptics += 1 }
            }
        }
        return true
    }

    private func undoFeedback(_ id: String) {
        guard undoHolds.undo(id) else { return }
        withAnimation(.spring(duration: 0.45)) { heldFeedback.remove(id) }
        undoHaptics += 1
    }
}

/// The first copy from the computer has no text or progress indicator.
struct FirstCopyShapes: View {
    var tasks = false
    var identifier = "firstCopy.shapes"

    private func line(_ width: CGFloat? = nil, height: CGFloat = 12) -> some View {
        RoundedRectangle(cornerRadius: 6).fill(YansuTheme.chip).frame(width: width, height: height)
    }

    private func card<Content: View>(@ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 12, content: content)
            .padding(18)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
            .overlay(RoundedRectangle(cornerRadius: 18).stroke(YansuTheme.border, lineWidth: 1))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            line(150)
            if tasks {
                line(90)
                card {
                    line(96, height: 11)
                    line(nil, height: 14)
                    line()
                    line(230)
                    HStack {
                        Spacer()
                        Capsule().fill(YansuTheme.chip).frame(width: 84, height: 34)
                        Capsule().fill(YansuTheme.chip).frame(width: 84, height: 34)
                    }
                }
                ForEach(0..<2) { _ in
                    line(100)
                    card {
                        ForEach(0..<2) { _ in
                            HStack(spacing: 14) {
                                Circle().fill(YansuTheme.chip).frame(width: 22, height: 22)
                                VStack(alignment: .leading, spacing: 7) {
                                    line(180)
                                    line(116, height: 10)
                                }
                            }
                        }
                    }
                }
            } else {
                ForEach(0..<3) { _ in
                    card {
                        line(96, height: 11)
                        line(nil, height: 14)
                        line(240)
                    }
                }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityIdentifier(identifier)
    }
}

/// One sentence about what the user wants to see in Today. It stays on this iPhone in this release
/// and is shown under the box.
struct FeedPromptBox: View {
    @ObservedObject var feed: FeedStore
    @State private var draft = ""
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                Image(systemName: "sparkle.magnifyingglass")
                    .foregroundStyle(YansuTheme.tertiary)
                    .accessibilityHidden(true)
                TextField("What would you like to see in Today?", text: $draft, axis: .vertical)
                    .lineLimit(1...3)
                    .font(.body)
                    .foregroundStyle(YansuTheme.ink)
                    .focused($focused)
                    .submitLabel(.done)
                    .onSubmit(save)
                    .accessibilityIdentifier("feed.prompt")
                if !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Button(action: save) {
                        Image(systemName: "arrow.up")
                            .font(.footnote.weight(.bold))
                            .foregroundStyle(YansuTheme.onSolid)
                            .frame(width: 28, height: 28)
                            .background(YansuTheme.solid, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Save")
                    .accessibilityIdentifier("feed.prompt.save")
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                    .strokeBorder(YansuTheme.border, lineWidth: 1)
            }
            if let interest = feed.interest {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text("Following: \(interest)")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("feed.interest")
                    Spacer(minLength: 4)
                    Button("Clear") {
                        feed.setInterest("")
                    }
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(YansuTheme.tertiary)
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("feed.interest.clear")
                }
                .padding(.horizontal, 4)
            }
        }
    }

    private func save() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        feed.setInterest(text)
        draft = ""
        focused = false
    }
}

/// The card an unpaired tab shows: what pairing gives, and the button that opens Pairing in Settings.
struct PairPromptCard: View {
    let title: String
    /// The body text. A saved pairing that cannot be read has no body: its "Try again" link stands in its place (P140).
    var detail: String? = nil
    var retriesSavedPairing = false
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var environment: AppEnvironment

    var body: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 16) {
                HStack(alignment: .center, spacing: 14) {
                    YansuMark(size: 48)
                    Text(title)
                        .font(.headline)
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if let detail {
                    Text(detail)
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                } else if retriesSavedPairing {
                    // Re-reads the saved pairing; a file that still cannot be read leaves this card as it is.
                    Button("Try again") { environment.retrySavedPairing() }
                        .buttonStyle(.plain)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("pairing.tryAgain")
                }
                Button("Pair with your computer") {
                    router.openPairing()
                }
                .buttonStyle(YansuPrimaryButtonStyle())
            }
        }
    }
}

/// "This phone needs pairing again to get new briefs." and Pair, one amber line above the brief (phone-setup rule 11).
struct PairingLostLine: View {
    var retriesSavedPairing = false
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var environment: AppEnvironment

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: "exclamationmark.triangle")
                .font(.subheadline)
                .foregroundStyle(YansuTheme.warning)
            Text("setup.lost")
                .font(.subheadline)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            if retriesSavedPairing {
                // Re-reads the saved pairing first; a file that still cannot be read leaves the line as it is.
                Button(String(localized: "Try again")) { environment.retrySavedPairing() }
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.accent)
                    .accessibilityIdentifier("pairing.tryAgain")
            }
            Button(String(localized: "setup.lostPair")) { router.openPairing(scanCode: true) }
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(YansuTheme.accent)
                .accessibilityIdentifier("today.pairAgain")
        }
        .buttonStyle(.plain)
        .padding(14)
        .background(YansuTheme.warningWash, in: RoundedRectangle(cornerRadius: 14))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("today.pairingLost")
    }
}

/// Notifications are asked once, under the brief, by saying what they are (phone-setup rule 8). Turn on shows the
/// system's prompt; Not now asks again only from Settings › Notifications.
struct NotificationAskCard: View {
    @EnvironmentObject private var push: PushRegistration
    @State private var working = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: "bell").foregroundStyle(YansuTheme.accent)
                Text("setup.notifTitle").font(.body.weight(.semibold)).foregroundStyle(YansuTheme.ink)
            }
            Text("setup.notifBody")
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
            WorkWeightedRow(weights: [1.3, 1], spacing: 8) {
                Button {
                    working = true
                    Task { await push.acceptPermissionExplanation(); working = false }
                } label: {
                    Text("setup.notifOn").font(.body.weight(.semibold)).frame(maxWidth: .infinity, minHeight: 44)
                        .foregroundStyle(YansuTheme.onSolid)
                        .background(YansuTheme.solid, in: RoundedRectangle(cornerRadius: 12))
                }
                .disabled(working)
                .accessibilityIdentifier("push.permission.accept")
                Button { push.declinePermissionExplanation() } label: {
                    Text("setup.notifLater").font(.body).frame(maxWidth: .infinity, minHeight: 44)
                        .foregroundStyle(YansuTheme.ink)
                        .overlay { RoundedRectangle(cornerRadius: 12).stroke(YansuTheme.borderStrong) }
                }
                .accessibilityIdentifier("push.permission.decline")
            }
            .buttonStyle(.plain)
        }
        .padding(16)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("today.notificationAsk")
    }
}
