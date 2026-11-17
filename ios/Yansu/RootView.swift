import Combine
import LocalAuthentication
import SwiftUI

struct RootView: View {
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var router: NotificationRouter
    @ObservedObject private var watchRecordings = WatchRecordingsModel.shared
    @EnvironmentObject private var push: PushRegistration
    @EnvironmentObject private var firstRun: FirstRunStore
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var onScreen: BriefOnScreen
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.locale) private var locale
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @AppStorage("faceIDToOpen") private var faceIDToOpen = false
    @State private var isLocked = false
    @StateObject private var duoNavigation = DuoNavigation()
    /// Whether the mini player is shown: Play started a brief and that brief is not on screen.
    @State private var showsMiniPlayer = false
    /// The keyboard is on screen. Only the Chat tab reads it: at accessibility sizes the bar steps aside there (P162).
    @State private var keyboardShown = false

    static func tabTitle(_ tab: NotificationRouter.Tab, locale: Locale) -> String {
        let copy = ChatPresentationLanguage(locale: locale)
        switch tab {
        case .today: return copy.text(en: String(localized: "Today tab", locale: locale), zhCN: "今日")
        case .watch: return copy.text(en: "Watch", zhCN: "手表")
        case .chat: return copy.text(en: "Chat", zhCN: "聊天")
        case .tasks: return copy.text(en: "Tasks", zhCN: "任务")
        case .settings: return copy.text(en: "Settings", zhCN: "设置")
        }
    }

    var body: some View {
        Group {
            if firstRun.shouldPresent(hasPairing: pairing.paired != nil,
                                      needsPairingRecovery: environment.vault.loadError != nil) {
                FirstRunView(firstRun: firstRun)
            } else {
                tabs
            }
        }
        .onAppear {
            if pairing.paired != nil || environment.vault.loadError != nil { firstRun.complete() }
            if faceIDToOpen {
                isLocked = true
                Task { await unlock() }
            }
        }
        .sheet(item: $router.sheet, onDismiss: {
            if router.sheet == nil { environment.pairing.cancel() }
        }) { sheet in
            Group {
                switch sheet {
                case .settings:
                    NavigationStack {
                        SettingsView()
                            .toolbar {
                                ToolbarItem(placement: .confirmationAction) {
                                    Button(String(localized: "Done")) { router.sheet = nil }
                                        .accessibilityIdentifier("settings.done")
                                }
                            }
                    }
                case .pairing:
                    PairingView(startScanning: router.startsPairingWithScanner)
                }
            }
            .environmentObject(environment)
            .environmentObject(router)
            .environmentObject(environment.brief)
            .environmentObject(environment.pairing)
            .environmentObject(environment.actions)
            .environmentObject(environment.notifications)
            .environmentObject(environment.keepAwake)
            .environmentObject(push)
        }
        .sheet(item: $router.briefRoute) { route in
            NavigationStack {
                BriefDetailView(entry: HistoryEntry(id: route.briefID, slot: nil, localDate: nil, asOf: nil,
                                                    narrativeStatus: nil, isCached: true),
                                player: environment.audioPlayer, landsOnSpoken: route.landsOnSpoken,
                                presentedAsSheet: true)
            }
            .environmentObject(environment)
            .environmentObject(environment.brief)
            .environmentObject(environment.pairing)
            .environmentObject(environment.chat)
            .environmentObject(environment.actions)
            .environmentObject(environment.today)
            .environmentObject(router)
            .environmentObject(onScreen)
        }
        .onReceive(Publishers.CombineLatest3(environment.audioPlayer.$started, environment.audioPlayer.$state,
                                             onScreen.$counts).receive(on: RunLoop.main)) { _ in
            showsMiniPlayer = miniPlayerIsWanted
        }
        .onReceive(Publishers.CombineLatest(environment.today.$snapshot, environment.brief.$current)
            .receive(on: RunLoop.main)) { snapshot, brief in
            router.resolveCurrentBrief(snapshot?.currentBrief?.id ?? brief?.identifier)
        }
        .task {
            if pairing.isPaired {
                environment.today.startListening()
            }
            async let snapshot: Void = environment.today.refresh()
            if environment.refreshesBriefs { await environment.brief.refresh() }
            await snapshot
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .active:
                environment.today.startListening()
                Task { await environment.today.refresh() }
                Task {
                    await environment.push.resumeIfOptedIn()
                    await environment.notifications.refresh()
                    await environment.noticesRead.poll()
                    await environment.keepAwake.refresh()
                    if router.selectedTab == .today {
                        await environment.tasks.refresh()
                        await environment.ideas.refresh()
                    }
                }
                if environment.refreshesBriefs { Task { await environment.brief.refresh() } }
                if faceIDToOpen, isLocked { Task { await unlock() } }
            case .background:
                environment.today.stopListening()
                if faceIDToOpen { isLocked = true }
            default:
                break
            }
        }
        .onChange(of: router.topicFilter) { _, route in
            if route != nil { duoNavigation.setPushedPath([], on: .chat) }
        }
        .onChange(of: router.briefPushes) { _, _ in
            Task { await environment.today.refresh() }
        }
        .onOpenURL { url in
            if PairingInvitation.hasInvitationPrefix(url.absoluteString) {
                firstRun.complete()
                router.openPairing()
                environment.pairing.begin(invitation: url.absoluteString)
                return
            }
            router.openDeepLink(url, currentBriefID: environment.today.snapshot?.currentBrief?.id ?? environment.brief.current?.identifier)
        }
        .overlay(alignment: .bottom) {
            if let name = router.pairedToast {
                Label(String.localizedStringWithFormat(String(localized: "setup.paired"), name), systemImage: "checkmark")
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(YansuTheme.onSolid)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 10)
                    .background(YansuTheme.ink, in: Capsule())
                    .padding(.bottom, 110)
                    .transition(.opacity)
                    .accessibilityIdentifier("setup.pairedToast")
                    .task {
                        try? await Task.sleep(for: .seconds(2))
                        withAnimation { router.pairedToast = nil }
                    }
            }
        }
        .overlay {
            if isLocked {
                AppLockView(unlock: { Task { await unlock() } })
                    .transition(.opacity)
                    .zIndex(1)
            }
        }
        .tint(YansuTheme.accent)
        #if DEBUG && targetEnvironment(simulator)
        .preferredColorScheme(Self.libraryUITestColorScheme)
        #endif
    }

    private var tabs: some View {
        GeometryReader { geometry in
            let hasDuoUITestSize = ProcessInfo.processInfo.environment["YANSU_UITEST_DUO_SIZE"] == "890x626"
            let layoutWidth = hasDuoUITestSize ? 890 : geometry.size.width
            let layoutHeight = hasDuoUITestSize ? 626 : geometry.size.height
            let metrics = DuoLayout.metrics(
                width: layoutWidth,
                height: layoutHeight,
                horizontalSizeClassIsRegular: horizontalSizeClass == .regular || hasDuoUITestSize
            )
            Group {
                if metrics.isWide {
                    if hasDuoUITestSize {
                        DuoWideRootView(navigation: duoNavigation, metrics: metrics)
                            .environmentObject(environment.chat)
                            .frame(width: layoutWidth, height: layoutHeight)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
                    } else {
                        DuoWideRootView(navigation: duoNavigation, metrics: metrics)
                            .environmentObject(environment.chat)
                    }
                } else {
                    narrowTabs
                }
            }
            .animation(reduceMotion ? .easeOut(duration: 0.2) : .easeInOut(duration: 0.5),
                       value: metrics.isWide)
        }
    }

    private var narrowTabs: some View {
        TabView(selection: $router.selectedTab) {
                NavigationStack(path: navigationPath(.today)) {
                    FeedView(feed: environment.feed, topics: environment.topics,
                             onPrepareInTopic: prepareTopicComposer)
                        .onAppear { router.openThreads.removeAll() }
                        .navigationTitle(String(localized: "Today"))
                        .navigationBarTitleDisplayMode(.large)
                        .navigationDestination(for: DuoStep.self) { step in
                            if case .item(let id) = step {
                                FeedView(feed: environment.feed, topics: environment.topics,
                                         onClose: { duoNavigation.close(on: .today) },
                                         selectedID: id, showsSelectedItem: true)
                            }
                        }
                }
                .tabItem { Label(Self.tabTitle(.today, locale: locale), systemImage: "sun.max") }
                .tag(NotificationRouter.Tab.today)
                .toolbar(.hidden, for: .tabBar)

                // Watch is a tab only while this phone has an Apple Watch paired.
                if watchRecordings.connection.hasWatch {
                    NavigationStack {
                        WatchTabView(recordings: watchRecordings)
                            .navigationBarTitleDisplayMode(.large)
                            .tabRootBar()
                            .onAppear { router.openThreads.removeAll() }
                    }
                    .tabItem { Label(Self.tabTitle(.watch, locale: locale), systemImage: "applewatch") }
                    .tag(NotificationRouter.Tab.watch)
                    .toolbar(.hidden, for: .tabBar)
                }

                NavigationStack(path: navigationPath(.chat)) {
                    ChatTabView()
                        .navigationTitle(String(localized: "Chat"))
                        .navigationBarTitleDisplayMode(.large)
                        .tabRootBar()
                        .taskThreads(environment.tasks, from: .chat)
                        .onAppear { router.openThreads.removeAll() }
                        .navigationDestination(for: DuoStep.self) { step in
                            switch step {
                            case .thread(let id):
                                if let topic = environment.topics.topic(id) {
                                    TopicView(route: TopicRoute(id: topic.id, name: topic.name),
                                              topics: environment.topics)
                                }
                            case .draft(let id):
                                if id == "main" {
                                    ChatTabView()
                                } else if let topic = environment.topics.topic(id) {
                                    TopicView(route: TopicRoute(id: topic.id, name: topic.name),
                                              topics: environment.topics)
                                }
                            default:
                                EmptyView()
                            }
                        }
                }
                .tabItem { Label(Self.tabTitle(.chat, locale: locale), systemImage: "bubble.left") }
                .tag(NotificationRouter.Tab.chat)
                .toolbar(.hidden, for: .tabBar)

            }
        .toolbar(.hidden, for: .tabBar)
        // The watch was unpaired while its tab was open: the tab is gone, so Today shows.
        .onChange(of: watchRecordings.connection.hasWatch) { _, hasWatch in
            if !hasWatch, router.selectedTab == .watch { router.selectedTab = .today }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            VStack(spacing: 7) {
                if showsMiniPlayer {
                    BriefMiniPlayer(player: environment.audioPlayer, open: openPlayingBrief)
                        .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                }
                // The bar stays while the Chat composer has focus, so the other tabs are always one tap away.
                if !router.isCorrecting && router.openThreads.isEmpty && onScreen.readingCounts.isEmpty && !barStepsAside {
                    FloatingTabBar(router: router, chat: environment.chat, tasks: environment.tasks)
                        .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                }
            }
            .animation(reduceMotion ? .easeOut(duration: 0.25) : .spring(duration: 0.45),
                       value: showsMiniPlayer)
            .animation(reduceMotion ? .easeOut(duration: 0.25) : .spring(duration: 0.45),
                       value: router.isCorrecting)
            .animation(reduceMotion ? .easeOut(duration: 0.25) : .spring(duration: 0.45),
                       value: router.openThreads)
        }
        .environment(\.tabChromeHeight, tabChromeHeight)
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
            keyboardShown = true
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in
            keyboardShown = false
        }
    }

    /// P162: at accessibility text sizes the Chat tab (and the topics pushed from it) gives the bar's room to the
    /// keyboard while it is up; the keyboard's Done brings the bar back. Default sizes keep the bar while typing (P159).
    private var barStepsAside: Bool {
        keyboardShown && dynamicTypeSize.isAccessibilitySize && router.selectedTab == .chat
    }

    /// The bottom chrome drawn over the tab pages, computed from state rather than measured: a measured height
    /// written back into this view's state fed its own layout, which overflowed the main-thread stack in UI tests.
    /// The bar is 72 pt (56 pt compact) and stays while the Chat composer has focus, but steps aside for an open
    /// brief correction. The mini player is about 56 pt, with 7 pt between it and the bar.
    private var tabChromeHeight: CGFloat {
        let bar: CGFloat = router.isCorrecting || !router.openThreads.isEmpty || !onScreen.readingCounts.isEmpty || barStepsAside ? 0 : (router.isTabBarCompact ? 56 : 72)
        let player: CGFloat = showsMiniPlayer ? 56 + (bar > 0 ? 7 : 0) : 0
        return bar + player
    }

    /// The mini player shows once Play started a brief, unless the brief view or the Today card already shows it.
    private var miniPlayerIsWanted: Bool {
        let player = environment.audioPlayer
        guard player.started, let brief = player.brief else { return false }
        return !onScreen.isShowing(brief.identifier)
    }

    private func openPlayingBrief() {
        guard let id = environment.audioPlayer.brief?.identifier else { return }
        router.openBrief(id, landsOnSpoken: true)
    }

    private func navigationPath(_ tab: DuoTab) -> Binding<[DuoStep]> {
        Binding(
            get: { Array(duoNavigation.path(for: tab).dropFirst()) },
            set: { duoNavigation.setPushedPath($0, on: tab) }
        )
    }

    private func prepareTopicComposer(id: String, message: String) -> Bool {
        guard let topic = environment.topics.topic(id), environment.topics.support != .unsupported else { return false }
        environment.topics.saveDraft(TopicStore.Draft(text: message), for: topic.id)
        duoNavigation.setPushedPath([.draft(topic.id)], on: .chat)
        router.selectedTab = .chat
        return true
    }

    #if DEBUG && targetEnvironment(simulator)
    private static var libraryUITestColorScheme: ColorScheme? {
        let values = ProcessInfo.processInfo.environment
        guard ["library", "feed-conversation", "feed-topics", "feed-topics-no-chat-read",
               "tasks", "tasks-seeded", "settings"]
            .contains(values["YANSU_UITEST_SETTINGS_STATE"] ?? "") else { return nil }
        switch values["YANSU_UITEST_APPEARANCE"]?.lowercased() {
        case "light": return .light
        case "dark": return .dark
        default: return nil
        }
    }
    #endif

    private func unlock() async {
        do {
            if try await LAContext().evaluatePolicy(.deviceOwnerAuthentication,
                                                    localizedReason: String(localized: "Unlock Isoform")) {
                isLocked = false
            }
        } catch {
            isLocked = true
        }
    }
}

/// The mini player (design section 4): the brief Play started, its title and where it is. Tapping it opens the brief
/// at the paragraph being spoken. After the audio ends it says "Played" for a few seconds, then goes. Swiping it down
/// dismisses it; while paused it stays until then or until something else plays.
private struct BriefMiniPlayer: View {
    @ObservedObject var player: BriefAudioPlayer
    let open: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var playing: Bool { player.state == .playing }

    var body: some View {
        HStack(spacing: 12) {
            Button {
                guard let brief = player.brief, let hubID = player.hubID else { return }
                Task { await player.toggle(brief: brief, hubID: hubID) }
            } label: {
                Image(systemName: playing ? "pause.fill" : "play.fill")
                    .font(.system(size: 14, weight: .semibold))
                    .contentTransition(.symbolEffect(.replace))
                    .animation(reduceMotion ? nil : .easeInOut(duration: 0.25), value: playing)
                    .frame(width: 32, height: 32)
                    .background(YansuTheme.accent.opacity(0.12), in: Circle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(playing ? String(localized: "Pause your brief") : String(localized: "Hear your brief"))
            .accessibilityIdentifier("player.mini.toggle")
            Button(action: open) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: player.brief?.titleText ?? "")
                        .font(.subheadline.weight(.semibold))
                        .lineLimit(1)
                    if player.finished {
                        Text("Played")
                            .font(.caption)
                            .accessibilityIdentifier("player.mini.played")
                    } else {
                        Text(verbatim: BriefPlaybackTime.text(player.position))
                            .font(.caption.monospacedDigit())
                            .contentTransition(.numericText())
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint(String(localized: "Opens the brief at the paragraph being spoken"))
            .accessibilityIdentifier("player.mini.open")
        }
        .foregroundStyle(YansuTheme.ink)
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(.ultraThinMaterial, in: Capsule())
        .overlay(Capsule().stroke(YansuTheme.border, lineWidth: 1))
        .shadow(color: YansuTheme.ink.opacity(0.1), radius: 10, y: 4)
        .padding(.horizontal, 48)
        .contentShape(Rectangle())
        .gesture(DragGesture(minimumDistance: 20).onEnded { value in
            if value.translation.height > 30 { player.dismiss() }
        })
        .task(id: player.finished) {
            guard player.finished else { return }
            try? await Task.sleep(for: .seconds(3))
            guard !Task.isCancelled, player.finished else { return }
            player.dismiss()
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("player.mini")
    }
}

private struct AppLockView: View {
    let unlock: () -> Void

    var body: some View {
        VStack(spacing: 18) {
            Image(systemName: "lock.fill")
                .font(.largeTitle)
                .foregroundStyle(YansuTheme.accent)
            Text("Isoform is locked")
                .font(.title2.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
            Button(String(localized: "Unlock Isoform"), action: unlock)
                .buttonStyle(YansuPrimaryButtonStyle())
                .frame(maxWidth: 220)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(YansuTheme.background.ignoresSafeArea())
        .accessibilityIdentifier("app.lock")
    }
}
