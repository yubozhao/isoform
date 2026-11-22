import SwiftUI

/// The Chat tab is the chat, always: the phone shows no list of chats and starts none (2026-10-16). Other chats
/// on the Mac stay data the phone reads for the chat's identity. Before the chat is known the tab says
/// why: not paired, a pairing to renew, or the Mac still being asked.
struct ChatTabView: View {
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var environment: AppEnvironment
    var showsProactiveSheet = true

    private var chatScopeGranted: Bool {
        SettingsView.allowsChat(pairing.paired?.scopes ?? [])
    }

    /// The chat the Mac lists (PROTOCOL.md 15.4), or the one this iPhone remembers from its last list.
    private var mainChatID: String? {
        chat.chats.first { $0.kind == .main }?.id
    }

    var body: some View {
        Group {
            if !pairing.isPaired {
                statusPage { unpairedContent }
            } else if !chatScopeGranted {
                statusPage {
                    ChatRepairCard(message: ChatCopy.legacyPairing)
                }
            } else if chat.repairNeeded {
                statusPage { ChatRepairCard(message: ChatRepairCard.refusedMessage) }
            } else if let route = router.topicFilter {
                TopicView(route: route, topics: environment.topics, embedded: true)
                    .id(route.id)
            } else if let mainChatID {
                ChatView(chatID: mainChatID, usesTabTitle: true)
                    .id(mainChatID)
            } else {
                // Paired with no main chat listed yet: the composer is still here, and a send goes to the computer's
                // main chat (PROTOCOL.md 15.5a).
                ChatView(chatID: ChatStore.mainChatAlias, usesTabTitle: true)
                    .id(ChatStore.mainChatAlias)
                    .refreshable {
                        await chat.refreshList()
                    }
            }
        }
        .navigationTitle(String(localized: "Chat"))
        .navigationBarTitleDisplayMode(.large)
        // Chat's title is the word alone, in both layouts; What Isoform sees opens from Settings › Your data
        // (phone-duo v1.1.0 rules 1 and 4).
        .sheet(item: Binding(
            get: { showsProactiveSheet ? chat.selectedProactive : nil },
            set: { if $0 == nil { chat.closeProactiveItem() } }
        )) { item in
            ChatProactiveItemView(item: item, chatID: chat.selectedProactiveChatID ?? "") {
                chat.closeProactiveItem()
            }
            .environmentObject(environment)
            .presentationDetents([.medium, .large])
            .presentationDragIndicator(.visible)
        }
        .task(id: pairing.paired?.deviceID) {
            guard pairing.isPaired, chatScopeGranted else { return }
            // The remembered chat shows at once, from its cached rows; the list confirms it, or finds it on a
            // first launch.
            chat.prepareMainChat()
            await chat.refreshList()
        }
    }

    private func statusPage<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                content()
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.top, 8)
            .padding(.bottom, 32)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .yansuScreen()
        .toolbar(.hidden, for: .tabBar)
    }

    private var unpairedContent: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 16) {
                HStack(alignment: .center, spacing: 14) {
                    YansuMark(size: 48)
                    Text(ChatCopy.introduction)
                        .font(.headline)
                        .foregroundStyle(YansuTheme.ink)
                }
                Text(ChatCopy.pairingDetail)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button(ChatCopy.pair) {
                    router.openPairing()
                }
                .buttonStyle(YansuPrimaryButtonStyle())
                .accessibilityIdentifier("chat.pair")
            }
        }
    }
}

/// Asks for Pair again in Settings: for a pairing from before Chat existed, or
/// one the relay no longer accepts.
struct ChatRepairCard: View {
    static let refusedMessage = ChatCopy.pairingRefused

    let message: String
    @EnvironmentObject private var router: NotificationRouter

    var body: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 12) {
                Text(ChatCopy.pairAgain)
                    .font(.headline)
                    .foregroundStyle(YansuTheme.ink)
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button(ChatCopy.openSettings) {
                    router.selectedTab = .settings
                }
                .buttonStyle(YansuPrimaryButtonStyle())
            }
        }
        .accessibilityIdentifier("chat.repair")
    }
}
