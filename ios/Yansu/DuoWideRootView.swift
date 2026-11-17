import SwiftUI

struct DuoWideRootView: View {
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var today: TodayStore
    @EnvironmentObject private var briefs: BriefStore
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @ObservedObject var navigation: DuoNavigation
    let metrics: DuoMetrics

    @State private var fullScreen = false
    @Namespace private var todayCardNamespace

    private var selectedTab: DuoTab {
        switch router.selectedTab {
        case .chat: return .chat
        default: return .today
        }
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
            ZStack(alignment: .trailing) {
                if fullScreen {
                    paneContent
                        .padding(.top, DuoLayout.headerToolbarClearance)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .transition(reduceMotion ? .opacity : .scale(scale: 0.92).combined(with: .opacity))
                        .accessibilityIdentifier("duo.fullScreen")
                } else {
                    HStack(alignment: .top, spacing: DuoLayout.gutter) {
                        leftPane
                            .frame(width: frames.left.width)
                            .frame(maxHeight: .infinity, alignment: .top)
                        paneContent
                            .padding(.top, DuoLayout.headerToolbarClearance)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    }
                    .padding(.trailing, 80)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
                    .transition(.opacity)
                }

                VStack(spacing: 14) {
                    tabCapsule
                    actionCapsule
                    Spacer(minLength: 0)
                }
                .padding(.trailing, 10)
                .padding(.top, 72)
                .padding(.bottom, 20)
                .frame(maxHeight: .infinity, alignment: .topTrailing)

                HStack(spacing: 8) {
                    OwnerProfileButton()
                        .environmentObject(pairing)
                        .environmentObject(router)
                }
                .padding(.top, 8)
                .padding(.trailing, 12)
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
            }
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
                // The wide layout has no Watch tab yet: the page opens as a sheet over Today, so the
                // mark and the watch line on Today still lead somewhere.
                case .watch:
                    router.selectedTab = .today
                    router.showsWatchSheet = true
                case .chat:
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
            .sheet(isPresented: $router.showsWatchSheet) {
                NavigationStack {
                    WatchTabView()
                        .navigationBarTitleDisplayMode(.inline)
                        .toolbar {
                            ToolbarItem(placement: .confirmationAction) {
                                Button(String(localized: "Done")) { router.showsWatchSheet = false }
                            }
                        }
                }
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
                    FeedView(feed: environment.feed, topics: environment.topics,
                             onSelect: selectTodayItem, onClose: { navigation.close(on: .today) },
                             selectedID: todayItemID, showsSelectedItem: true,
                             cardNamespace: todayCardNamespace, foldX: metrics.foldX,
                             windowWidth: metrics.width)
                        .accessibilityIdentifier("duo.today.item.\(todayItemID)")
                } else {
                    NavigationStack {
                        FeedView(feed: environment.feed, topics: environment.topics,
                                 onSelect: selectTodayItem, cardNamespace: todayCardNamespace,
                                 foldX: metrics.foldX, windowWidth: metrics.width,
                                 onPrepareInTopic: prepareTopicComposer)
                    }
                    .accessibilityIdentifier("duo.today.feed")
                }
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
                    VStack(alignment: .leading, spacing: 8) {
                        DuoFoldSafeControl(foldX: metrics.foldX, windowWidth: metrics.width,
                                           width: 72, height: 40) {
                            Button(String(localized: "Close")) { navigation.close(on: .chat) }
                                .foregroundStyle(YansuTheme.accent)
                                .accessibilityIdentifier("duo.chat.thread.close")
                        }
                        TopicView(route: route, topics: environment.topics)
                    }
                } else if case .draft(let id) = navigation.path(for: .chat).last, id != "main",
                          let route = topicRoute(id: id) {
                    VStack(alignment: .leading, spacing: 8) {
                        DuoFoldSafeControl(foldX: metrics.foldX, windowWidth: metrics.width,
                                           width: 72, height: 40) {
                            Button(String(localized: "Close")) { navigation.close(on: .chat) }
                                .foregroundStyle(YansuTheme.accent)
                                .accessibilityIdentifier("duo.chat.thread.close")
                        }
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
            Text(String(localized: "Today"))
                .font(.title2.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
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
        .padding(.top, 16)
        .padding(.horizontal, 16)
        .background(YansuTheme.surface.opacity(0.5))
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
            Text(String(localized: "Topics"))
                .font(.title2.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
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
        .padding(.top, 16)
        .padding(.horizontal, 12)
        .accessibilityIdentifier("duo.chat.topics")
    }

    private var tabCapsule: some View {
        VStack(spacing: 2) {
            tabButton(.today, symbol: "sun.max", title: String(localized: "Today"))
            tabButton(.chat, symbol: "bubble.left", title: String(localized: "Chat"))
        }
        .padding(5)
        .background(.ultraThinMaterial, in: Capsule())
        .overlay(Capsule().stroke(YansuTheme.border, lineWidth: 1))
        .shadow(color: YansuTheme.ink.opacity(0.12), radius: 12, y: 4)
    }

    private func tabButton(_ tab: DuoTab, symbol: String, title: String) -> some View {
        let isSelected = selectedTab == tab
        return Button {
            withAnimation(reduceMotion ? .easeInOut(duration: 0.15) : .spring(duration: 0.45)) {
                router.selectedTab = tab == .chat ? .chat : .today
            }
        } label: {
            VStack(spacing: 3) {
                ZStack(alignment: .topTrailing) {
                    Image(systemName: symbol)
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
        .accessibilityIdentifier("duo.tab.\(title.lowercased())")
    }

    @Namespace private var tabLens

    private var chatNeedsAttention: Bool {
        environment.chat.chats.contains(where: { $0.state == .waiting }) ||
            environment.chat.transcript?.pendingPermission?.isPermissionPrompt == true
    }

    private var actionCapsule: some View {
        HStack(spacing: 4) {
            if hasSelection {
                Button {
                    withAnimation(reduceMotion ? .easeInOut(duration: 0.15) : .easeInOut(duration: 0.5)) {
                        fullScreen.toggle()
                    }
                } label: {
                    Image(systemName: fullScreen ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                        .frame(width: 38, height: 38)
                }
                .accessibilityLabel(String(localized: fullScreen ? "Exit full screen" : "Full screen"))
                .accessibilityIdentifier("duo.fullScreen.toggle")
            }
            Menu {
                if hasSelection {
                    Button(String(localized: "Close")) {
                        navigation.close(on: selectedTab)
                    }
                } else {
                    Button(String(localized: "Settings")) { router.openSettings() }
                }
            } label: {
                Image(systemName: "ellipsis")
                    .frame(width: 38, height: 38)
            }
            .accessibilityLabel(String(localized: "More"))
        }
        .font(.system(size: 16, weight: .medium))
        .foregroundStyle(YansuTheme.ink)
        .padding(4)
        .background(.ultraThinMaterial, in: Capsule())
        .overlay(Capsule().stroke(YansuTheme.border, lineWidth: 1))
        .shadow(color: YansuTheme.ink.opacity(0.12), radius: 12, y: 4)
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
