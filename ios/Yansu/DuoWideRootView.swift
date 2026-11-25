import SwiftUI

struct DuoWideRootView: View {
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var today: TodayStore
    @EnvironmentObject private var briefs: BriefStore
    @EnvironmentObject private var chatAvailability: ChatAvailabilityStore
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.locale) private var locale

    @ObservedObject var navigation: DuoNavigation
    @ObservedObject var watchRecordings: WatchRecordingsModel = .shared
    let metrics: DuoMetrics

    @State private var fullScreen = false
    @Namespace private var todayCardNamespace

    private var selectedTab: DuoTab {
        DuoTab.shown(for: router.selectedTab, showsWatch: watchRecordings.showsWatchTab,
                     showsChat: chatAvailability.chatShown)
    }

    private var hasSelection: Bool {
        navigation.path(for: selectedTab).count > 1 ||
            (selectedTab == .chat && chat.selectedProactive != nil)
    }

    private var todayItemID: String? {
        guard case .item(let id) = navigation.path(for: .today).last else { return nil }
        return id
    }

    private var selectedTopicID: String? {
        switch navigation.path(for: .chat).last {
        case .thread(let id): return id
        case .draft(let id) where id != "main": return id
        default: return nil
        }
    }

    var body: some View {
        GeometryReader { geometry in
            let currentMetrics = DuoLayout.metrics(
                width: geometry.size.width,
                height: geometry.size.height,
                horizontalSizeClassIsRegular: horizontalSizeClass == .regular,
                reportedFoldX: metrics.foldX
            )
            let frames = DuoLayout.frames(for: selectedTab, metrics: currentMetrics,
                                          hasSelection: hasSelection, fullScreen: fullScreen)
            ZStack(alignment: .topLeading) {
                if fullScreen {
                    paneContent
                        .frame(width: max(0, geometry.size.width - DuoLayout.rightPaneToolbarInset))
                        .frame(maxHeight: .infinity, alignment: .topLeading)
                        .transition(reduceMotion ? .opacity : .scale(scale: 0.92).combined(with: .opacity))
                        .accessibilityIdentifier("duo.fullScreen")
                } else {
                    // The fold splits the page: the left pane runs to it, the right pane from it to
                    // the rail, and nothing crosses it (phone-duo v1.0.0 rule 1).
                    HStack(alignment: .top, spacing: 0) {
                        leftPane
                            .frame(width: frames.left.width)
                            .frame(maxHeight: .infinity, alignment: .top)
                            .clipped()
                        paneContent
                            .frame(width: frames.right.width)
                            .frame(maxHeight: .infinity, alignment: .topLeading)
                            .clipped()
                    }
                    .transition(.opacity)
                }

                // The rail is the app's only chrome: the person button, then the tabs (rule 4).
                VStack(spacing: 14) {
                    OwnerProfileButton()
                        .environmentObject(pairing)
                        .environmentObject(router)
                    // One page needs no tabs (PhoneTabs).
                    if PhoneTabs.showsTabBar(DuoTab.rail(showsWatch: watchRecordings.showsWatchTab,
                                                         showsChat: chatAvailability.chatShown).map(\.routerTab)) {
                        tabCapsule
                    }
                    Spacer(minLength: 0)
                }
                .padding(.top, DuoLayout.paneTop)
                .padding(.bottom, 20)
                .frame(width: DuoLayout.rightPaneToolbarInset)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)

                let frame = DuoMotion.dockedPlayer(
                    size: CGSize(width: 52, height: 52),
                    in: CGRect(origin: .zero, size: geometry.size),
                    foldX: currentMetrics.foldX
                )
                DuoDockedBriefPlayer(player: environment.audioPlayer, onScreen: environment.briefOnScreen) {
                    guard let id = environment.audioPlayer.brief?.identifier else { return }
                    router.openBrief(id, landsOnSpoken: true)
                }
                .frame(width: frame.width, height: frame.height)
                .position(x: frame.midX, y: frame.midY)
                .accessibilityIdentifier("duo.brief.player")

                // The band behind the status bar is solid in the page's color, so nothing scrolled
                // reads under the clock (rule 6).
                Color.clear
                    .frame(maxWidth: .infinity)
                    .frame(height: 0)
                    .background(YansuTheme.background.ignoresSafeArea(edges: .top))
                    .accessibilityHidden(true)
            }
            .environment(\.duoFullScreen, DuoFullScreen(isOn: fullScreen) {
                withAnimation(reduceMotion ? .easeInOut(duration: 0.15) : .easeInOut(duration: 0.5)) { fullScreen.toggle() }
            })
            .coordinateSpace(name: DuoLayout.coordinateSpaceName)
            .background(YansuTheme.background)
            .animation(reduceMotion ? .easeInOut(duration: 0.15) : .spring(duration: 0.5),
                       value: router.selectedTab)
            .animation(reduceMotion ? .easeInOut(duration: 0.15) : .spring(duration: 0.48),
                       value: hasSelection)
            .onChange(of: router.selectedTab) { _, tab in
                fullScreen = false
                switch tab {
                case .today: break
                // Watch is a tab in the rail and opens in the main area, never as a sheet
                // (phone-watch v1.1.0 rule 2). With no watch there is no tab, and Today stays.
                case .watch:
                    if !watchRecordings.showsWatchTab { router.selectedTab = .today }
                case .chat:
                    // With Chat off there is no Chat tab, and a link to it lands on Today (scope cut A6).
                    guard chatAvailability.chatShown else { router.selectedTab = .today; break }
                    if navigation.path(for: .chat).isEmpty {
                        navigation.replaceCurrent(with: .topics, on: .chat)
                    }
                    if environment.refreshesTopics {
                        Task { await environment.topics.refresh() }
                    }
                // The Tasks tab went (phone-today rule 19); the router lands its links on Today.
                case .tasks, .settings: break
                }
            }
            .onChange(of: watchRecordings.showsWatchTab) { _, shows in
                // The watch was unpaired while its tab was open.
                if !shows, router.selectedTab == .watch { router.selectedTab = .today }
            }
            .onChange(of: chatAvailability.chatShown) { _, shows in
                // Chat was switched off on the computer while its tab was open.
                if !shows, router.selectedTab == .chat { router.selectedTab = .today }
            }
            .onAppear(perform: reconcileTodaySelection)
            .onChange(of: briefs.cachedBriefs) { _, _ in reconcileTodaySelection() }
            .onChange(of: briefs.repairNeeded) { _, _ in reconcileTodaySelection() }
            .onChange(of: pairing.isPaired) { _, _ in reconcileTodaySelection() }
            .onChange(of: chat.draft(in: chat.openChatID ?? "")) { _, draft in
                guard !draft.isEmpty, selectedTab == .chat,
                      navigation.path(for: .chat).last != .draft("main") else { return }
                navigation.push(.draft("main"), on: .chat)
            }
        }
    }

    private func reconcileTodaySelection() {
        let today = FeedAssembly.localDate(for: Date())
        let days = FeedAssembly.days(from: environment.brief.cachedBriefs, today: today)
        let cardIDs = Set(days.first(where: { $0.localDate == today })?.cards.map(\.id) ?? [])
        navigation.reconcileTodaySelection(isPaired: pairing.isPaired,
                                           repairNeeded: environment.brief.repairNeeded,
                                           cardIDs: cardIDs)
    }

    private var leftPane: some View {
        Group {
            switch selectedTab {
            case .today:
                if let todayItemID {
                    FeedView(feed: environment.feed, topics: environment.topics,
                             onSelect: selectTodayItem, onClose: { navigation.close(on: .today) },
                             selectedID: todayItemID, cardsOnly: true, cardNamespace: todayCardNamespace,
                             foldX: metrics.foldX, windowWidth: metrics.width)
                        .transition(reduceMotion ? .opacity : .move(edge: .leading).combined(with: .opacity))
                } else {
                    yourDayPane
                        .transition(reduceMotion ? .opacity : .move(edge: .leading).combined(with: .opacity))
                }
            case .watch:
                // Always in view: the title, Now, where the hours are, the watch's settings
                // (phone-watch v1.1.0 rule 5). The title scrolls with them (phone-duo rule 6).
                NavigationStack {
                    WatchTabView(recordings: watchRecordings, sections: .side, wideTitle: RootView.tabTitle(.watch, locale: locale))
                        .toolbar(.hidden, for: .navigationBar)
                }
                .accessibilityIdentifier("duo.watch.side")
            case .chat:
                if chat.selectedProactive != nil {
                    ChatTabView(showsProactiveSheet: false)
                        .environmentObject(environment.chat)
                        .environmentObject(pairing)
                        .environmentObject(router)
                        .environmentObject(environment)
                        .accessibilityElement(children: .contain)
                        .accessibilityIdentifier("duo.chat.thread")
                } else {
                    topicsPane
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var paneContent: some View {
        Group {
            switch selectedTab {
            case .today:
                if let todayItemID {
                    DuoOpenedPage(back: String(localized: "Today"), title: "", onBack: { navigation.close(on: .today) }) {
                        FeedView(feed: environment.feed, topics: environment.topics,
                                 onSelect: selectTodayItem, onClose: { navigation.close(on: .today) },
                                 selectedID: todayItemID, showsSelectedItem: true,
                                 cardNamespace: todayCardNamespace, foldX: metrics.foldX,
                                 windowWidth: metrics.width)
                    }
                    .accessibilityIdentifier("duo.today.item.\(todayItemID)")
                } else {
                    // No title and no bar in the right pane while it shows the tab's own page: the
                    // title is the left pane's, the person button the rail's (phone-duo rules 3 and 4).
                    NavigationStack {
                        FeedView(feed: environment.feed, topics: environment.topics,
                                 onSelect: selectTodayItem, cardNamespace: todayCardNamespace,
                                 foldX: metrics.foldX, windowWidth: metrics.width,
                                 onPrepareInTopic: prepareTopicComposer)
                            .toolbar(.hidden, for: .navigationBar)
                    }
                    .accessibilityIdentifier("duo.today.feed")
                }
            case .watch:
                // Today's hours, conversations, earlier days; a conversation opens here with
                // "Watch" as its back button and the left pane stays (rule 6).
                // No title and no bar while it shows the tab's own page (phone-duo rule 3).
                NavigationStack {
                    WatchTabView(recordings: watchRecordings, sections: .main, wide: true)
                        .toolbar(.hidden, for: .navigationBar)
                }
                .accessibilityIdentifier("duo.watch.page")
            case .chat:
                if let item = chat.selectedProactive,
                   let chatID = chat.selectedProactiveChatID {
                    ChatProactiveItemView(item: item, chatID: chatID) {
                        chat.closeProactiveItem()
                    }
                    .environmentObject(environment)
                    .accessibilityIdentifier("duo.chat.proactive.item")
                } else if case .thread(let id) = navigation.path(for: .chat).last,
                   let route = topicRoute(id: id) {
                    DuoOpenedPage(back: String(localized: "Chat"), title: route.name, onBack: { navigation.close(on: .chat) }) {
                        TopicView(route: route, topics: environment.topics)
                    }
                } else if case .draft(let id) = navigation.path(for: .chat).last, id != "main",
                          let route = topicRoute(id: id) {
                    DuoOpenedPage(back: String(localized: "Chat"), title: route.name, onBack: { navigation.close(on: .chat) }) {
                        TopicView(route: route, topics: environment.topics)
                    }
                } else {
                    ChatTabView()
                        .environmentObject(environment.chat)
                        .environmentObject(pairing)
                        .environmentObject(router)
                        .accessibilityElement(children: .contain)
                        .accessibilityIdentifier("duo.chat.thread")
                }
            }
        }
    }

    private var yourDayPane: some View {
        VStack(alignment: .leading, spacing: 18) {
            DuoPaneTitle(String(localized: "Today"))
            if let brief = today.newestBrief() {
                VStack(alignment: .leading, spacing: 10) {
                    Text(brief.titleText).font(.headline).foregroundStyle(YansuTheme.ink)
                    Text(brief.narrative.first?.text ?? "")
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                        .lineLimit(6)
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
            }
            Spacer(minLength: 0)
        }
        .padding(.top, DuoLayout.paneTop)
        .padding(.horizontal, DuoLayout.paneInset)
        // The pane is a container: its title and card keep their own identifiers.
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("duo.today.yourDay")
    }

    private func selectTodayItem(_ id: String) {
        guard navigation.path(for: .today).last != .item(id) else { return }
        withAnimation(reduceMotion ? .easeOut(duration: 0.2) : .spring(duration: 0.48)) {
            navigation.push(.item(id), on: .today)
        }
    }

    private func prepareTopicComposer(id: String, message: String) -> Bool {
        guard let topic = environment.topics.topic(id), environment.topics.support != .unsupported else { return false }
        environment.topics.saveDraft(TopicStore.Draft(text: message), for: topic.id)
        navigation.setPushedPath([.draft(topic.id)], on: .chat)
        router.selectedTab = .chat
        return true
    }

    private func topicRoute(id: String) -> TopicRoute? {
        guard let topic = environment.topics.topic(id) else { return nil }
        return TopicRoute(id: topic.id, name: topic.name)
    }

    private var topicsPane: some View {
        VStack(alignment: .leading, spacing: 12) {
            DuoPaneTitle(String(localized: "Chat"))
            Text(String(localized: "Topics"))
                .font(.footnote.weight(.semibold))
                .foregroundStyle(YansuTheme.tertiary)
            if (today.snapshot?.topics ?? []).isEmpty {
                Text(String(localized: "No topics yet"))
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
            } else {
                ScrollView {
                    VStack(spacing: 8) {
                        ForEach(today.snapshot?.topics ?? []) { topic in
                            Button {
                                withAnimation(reduceMotion ? .easeOut(duration: 0.2) : .spring(duration: 0.48)) {
                                    navigation.push(.thread(topic.id), on: .chat)
                                }
                            } label: {
                                HStack {
                                    Text(topic.name)
                                        .font(.subheadline.weight(.medium))
                                        .foregroundStyle(YansuTheme.ink)
                                        .lineLimit(2)
                                    Spacer(minLength: 4)
                                }
                                .padding(12)
                                .background(selectedTopicID == topic.id
                                            ? YansuTheme.accentWash : YansuTheme.surface,
                                            in: RoundedRectangle(cornerRadius: 12))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.top, DuoLayout.paneTop)
        .padding(.horizontal, DuoLayout.paneInset)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("duo.chat.topics")
    }

    private var tabCapsule: some View {
        VStack(spacing: 2) {
            ForEach(DuoTab.rail(showsWatch: watchRecordings.showsWatchTab, showsChat: chatAvailability.chatShown),
                    id: \.self) { tab in
                switch tab {
                case .today: tabButton(.today, symbol: "sun.max", title: String(localized: "Today"), name: "today")
                // The mark in the watch's pose, as on the narrow bar (rule 3).
                case .watch: tabButton(.watch, symbol: "applewatch", title: RootView.tabTitle(.watch, locale: locale), name: "watch")
                case .chat: tabButton(.chat, symbol: "bubble.left", title: String(localized: "Chat"), name: "chat")
                }
            }
        }
        .padding(5)
        .background(.ultraThinMaterial, in: Capsule())
        .overlay(Capsule().stroke(YansuTheme.border, lineWidth: 1))
        .shadow(color: YansuTheme.ink.opacity(0.12), radius: 12, y: 4)
    }

    private func tabButton(_ tab: DuoTab, symbol: String, title: String, name: String) -> some View {
        let isSelected = selectedTab == tab
        return Button {
            withAnimation(reduceMotion ? .easeInOut(duration: 0.15) : .spring(duration: 0.45)) {
                router.selectedTab = tab.routerTab
            }
        } label: {
            VStack(spacing: 3) {
                ZStack(alignment: .topTrailing) {
                    if tab == .watch {
                        WatchPoseMark(state: watchItem, selected: isSelected, size: 20)
                    } else {
                        Image(systemName: symbol)
                    }
                    if tab == .chat && chatNeedsAttention {
                        Circle()
                            .fill(YansuTheme.accent)
                            .frame(width: 6, height: 6)
                            .offset(x: 4, y: -3)
                    }
                }
                .font(.system(size: 16, weight: isSelected ? .semibold : .regular))
                Text(title)
                    .font(.system(size: 8, weight: isSelected ? .semibold : .regular))
                    .lineLimit(1)
            }
            .foregroundStyle(isSelected ? YansuTheme.accent : YansuTheme.secondary)
            .frame(width: 48, height: 48)
            .background {
                if isSelected {
                    Capsule()
                        .fill(YansuTheme.accent.opacity(0.12))
                        .matchedGeometryEffect(id: "duo.tab.lens", in: tabLens)
                }
            }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(title)
        .accessibilityIdentifier("duo.tab.\(name)")
    }

    @Namespace private var tabLens

    private var watchItem: WatchItemState {
        WatchItemState.of(connection: watchRecordings.connection, status: watchRecordings.watchStatus,
                          rows: watchRecordings.rows, outcome: watchRecordings.outcome)
    }

    private var chatNeedsAttention: Bool {
        environment.chat.chats.contains(where: { $0.state == .waiting }) ||
            environment.chat.transcript?.pendingPermission?.isPermissionPrompt == true
    }
}

/// The wide layout's mini player: a docked circle with the brief's progress. It shows while Play started a brief that
/// is not on screen, and opens the brief at the paragraph being spoken.
private struct DuoDockedBriefPlayer: View {
    @ObservedObject var player: BriefAudioPlayer
    @ObservedObject var onScreen: BriefOnScreen
    let open: () -> Void

    private var shown: Bool {
        guard player.started, let brief = player.brief else { return false }
        return !onScreen.isShowing(brief.identifier)
    }

    private var progress: CGFloat {
        guard player.duration > 0 else { return 0 }
        return min(1, max(0, player.position / player.duration))
    }

    var body: some View {
        if shown {
            Button(action: open) {
                ZStack {
                    Circle().stroke(YansuTheme.border, lineWidth: 3)
                    Circle()
                        .trim(from: 0, to: progress)
                        .stroke(YansuTheme.accent, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                    Image(systemName: "headphones")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(YansuTheme.ink)
                }
                .padding(5)
                .background(.ultraThinMaterial, in: Circle())
                .overlay(Circle().stroke(YansuTheme.border, lineWidth: 1))
                .shadow(color: YansuTheme.ink.opacity(0.14), radius: 10, y: 4)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "Open your brief"))
        }
    }
}
