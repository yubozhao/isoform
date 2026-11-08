import SwiftUI
import UIKit

struct ChatView: View {
    let chatID: String
    var usesTabTitle = false
    @EnvironmentObject private var chat: ChatStore
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.undoManager) private var undoManager
    @Environment(\.locale) private var locale
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.tabChromeHeight) private var tabChromeHeight
    @State private var composerIdentity = UUID()

    private var draft: String { chat.composerDraft(in: chatID).text }
    private var draftBinding: Binding<String> {
        let identity = composerIdentity
        return Binding(get: { draft }, set: { text in
            // A replaced field may still deliver an old Undo callback before SwiftUI removes it.
            guard composerIdentity == identity else { return }
            chat.editComposerDraft(text, in: chatID)
        })
    }
    @State private var composerVisible = false
    @EnvironmentObject private var actions: ActionStore
    @EnvironmentObject private var router: NotificationRouter
    @FocusState private var composerFocused: Bool
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var scroll = ChatScrollTracker()
    @State private var latest = LatestControl.hidden
    @State private var deliveryExplanation: String?
    @State private var awayMore: ChatAwayCause?
    @State private var liveReplySeq: Int?
    @State private var shownFirstReadID: String?
    @State private var firstReadFocusID: String?
    @State private var firstReadFocusSignal = UUID()
    @State private var firstReadCorrectionSignal = UUID()
    @State private var firstReadCorrectionFocusSignal = UUID()

    private static let bottomID = "bottom"

    /// What the control over the composer offers while the reader is further up.
    private enum LatestControl: Equatable {
        case hidden
        case latest
        case newMessages
    }

    private var header: ChatHeaderView? {
        guard chat.openChatID == chatID else { return nil }
        return chat.transcript?.header
    }

    private var isRunning: Bool {
        Self.showsRunningState(header?.state, computerOnline: chat.hubOnline)
    }

    static func showsRunningState(_ state: ChatState?, computerOnline: Bool?) -> Bool {
        computerOnline == true && state?.isBusy == true
    }

    private var outgoing: [ChatOutgoingMessage] {
        chat.outgoingMessages(in: chatID)
    }

    /// P141: the newest message from this iPhone that the Mac has not stored yet. It is newer than every stored item,
    /// so it is the latest message of yours and the only one whose receipt shows then.
    private var unsentLatestOutgoingID: String? {
        guard let last = outgoing.last, last.seq == nil else { return nil }
        return last.id
    }

    /// P141: the seq of the newest stored message of yours on screen.
    private var latestStoredUserSeq: Int? {
        chat.transcript?.items.last(where: { $0.role == .user })?.seq
    }

    /// Nothing is in the chat yet, so the empty card is the last thing on screen and has no space after it (P153).
    private var showsEmptyCardOnly: Bool {
        if let transcript = chat.transcript, chat.openChatID == chatID {
            return Self.showsEmptyChat(transcript, outgoing: outgoing)
        }
        return chatID == ChatStore.mainChatAlias
    }

    /// Changes when a row or a message is added at the bottom.
    private var bottomMarker: String {
        let newest = chat.openChatID == chatID ? (chat.transcript?.newestSeq ?? 0) : 0
        return "\(newest)|\(outgoing.count)|\(outgoing.last?.id ?? "")"
    }

    var body: some View {
        ScrollViewReader { proxy in
            VStack(spacing: 0) {
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        if chat.repairNeeded {
                            ChatRepairCard(message: ChatRepairCard.refusedMessage)
                        } else {
                            banners
                            transcriptContent
                            outgoingContent
                        }
                        // The scroll target sits one row spacing below the last row, so the end of a transcript is as far above
                        // the composer as two messages are from each other (P161). The empty card has no space after it.
                        if !showsEmptyCardOnly {
                            Color.clear
                                .frame(height: 0)
                                .id(Self.bottomID)
                        }
                    }
                    .padding(.horizontal, YansuTheme.gutter)
                    .padding(.top, 8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background {
                        GeometryReader { geometry in
                            Color.clear.preference(key: ChatContentFrameKey.self, value: geometry.frame(in: .global))
                        }
                    }
                }
                .scrollDismissesKeyboard(.interactively)
                .simultaneousGesture(DragGesture().onChanged { _ in scroll.policy.readerDragged() })
                // The composer already lifts itself above the tab bar, so the bar's margin would count that space twice (P161).
                .tabBarScrollVisibility(bottomMargin: 0)
                .refreshable {
                    await chat.open(chatID: chatID)
                }
                .background {
                    GeometryReader { geometry in
                        // The background is laid out inside the safe area, so its bottom is the composer's top. Both
                        // frames are global: this reader is not inside the scroll view, so a named space would not match.
                        Color.clear.preference(key: ChatVisibleBottomKey.self, value: geometry.frame(in: .global).maxY)
                    }
                }
                .onPreferenceChange(ChatContentFrameKey.self) { frame in
                    scroll.contentFrame = frame
                    follow(scroll.layout(), proxy)
                }
                // The keyboard's rise moves the visible area in steps; once it has finished, a reader at the bottom is put
                // back at the newest message (P161).
                .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardDidShowNotification)) { _ in
                    follow(scroll.policy.keyboardRose(), proxy)
                }
                // The keyboard's frame also changes when it resizes, for example when the keyboard type changes while typing.
                .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardDidChangeFrameNotification)) { _ in
                    follow(scroll.policy.keyboardRose(), proxy)
                }
                .onPreferenceChange(ChatVisibleBottomKey.self) { bottom in
                    scroll.visibleBottom = bottom
                    follow(scroll.layout(), proxy)
                }
                .onChange(of: bottomMarker) { _, _ in
                    scroll.policy.newContentArrived()
                    showLatestControl()
                }
                .onAppear {
                    // Opening the chat, or coming back to it, starts at the newest message.
                    follow(scroll.policy.appeared(), proxy)
                    FirstLoadTrace.record(header == nil ? "chat.view.skeleton" : "chat.view.cached")
                    openPendingProactiveItem()
                }
                .onChange(of: router.pendingMomentID) { _, _ in openPendingProactiveItem() }
                .onChange(of: chat.transcript?.newestSeq, initial: true) { _, _ in
                    openPendingProactiveItem()
                    revealFirstReadIfNeeded(proxy)
                }
                .onChange(of: firstReadFocusSignal) { _, _ in
                    guard let id = firstReadFocusID else { return }
                    focusFirstRead(id, proxy)
                }
                .onChange(of: firstReadCorrectionSignal) { _, _ in
                    follow(scroll.policy.jumpToLatest(), proxy)
                }
                .onChange(of: firstReadCorrectionFocusSignal) { _, _ in
                    focusCorrection(proxy)
                }
                // Today's Show for a send the computer did not take: Chat scrolls to that message (PROTOCOL.md section 23).
                .task(id: router.chatMessageRequest) {
                    guard let id = router.chatMessageRequest else { return }
                    try? await Task.sleep(for: .milliseconds(300))
                    proxy.scrollTo(id, anchor: .center)
                    router.consumeChatMessage(id)
                }
                .overlay(alignment: .bottom) {
                    if latest != .hidden {
                        latestButton(proxy)
                    }
                }
                if !chat.repairNeeded {
                    composer(proxy)
                }
            }
        }
        .yansuScreen()
        .navigationTitle(usesTabTitle ? String(localized: "Chat") : (header?.displayTitle ?? String(localized: "Chat")))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .alert(String(localized: "Message delivery"), isPresented: Binding(
            get: { deliveryExplanation != nil }, set: { if !$0 { deliveryExplanation = nil } }
        )) {
            Button(String(localized: "OK"), role: .cancel) { deliveryExplanation = nil }
        } message: {
            Text(deliveryExplanation ?? "")
        }
        .sheet(item: $awayMore) { cause in
            ChatAwaySheet(cause: cause)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
        }
        .task(id: chatID) {
            // The list below stops listening while it is covered, so an open chat keeps its own listener.
            async let events: Void = chat.watchForEvents()
            await chat.open(chatID: chatID)
            await events
        }
        .onChange(of: chat.transcript?.asOf) { _, _ in
            FirstLoadTrace.record("chat.view.updated")
        }
        .onChange(of: chat.transcript?.items.last?.seq) { oldValue, newValue in
            guard oldValue != nil, let newValue,
                  let item = chat.transcript?.items.last(where: { $0.seq == newValue }),
                  item.role == .assistant else { return }
            liveReplySeq = newValue
        }
        .onChange(of: chat.returnedDrafts, initial: true) { _, _ in
            recoverDrafts()
        }
        .onChange(of: chat.repairNeeded) { _, _ in
            recoverDrafts()
        }
        .onChange(of: scenePhase) { _, _ in
            recoverDrafts()
        }
        .onAppear {
            composerVisible = true
            recoverDrafts()
        }
        .onDisappear {
            composerVisible = false
        }
    }

    private func recoverDrafts() {
        guard composerVisible, scenePhase == .active, !chat.repairNeeded else { return }
        _ = chat.takeReturnedDrafts(in: chatID, appendingTo: draft)
    }

    // MARK: - Scrolling

    private func revealFirstReadIfNeeded(_ proxy: ScrollViewProxy) {
        guard chat.openChatID == chatID, scroll.policy.pinned,
              let item = chat.transcript?.items.last,
              item.proactive?.kind == "first_read", item.id != shownFirstReadID else { return }
        shownFirstReadID = item.id
        focusFirstRead(item.id, proxy)
    }

    /// The card's field took focus: the portrait card, with its field, Send and any failure line, ends above the composer.
    private func focusCorrection(_ proxy: ScrollViewProxy) {
        scroll.policy.readFirstRead()
        DispatchQueue.main.async {
            proxy.scrollTo(FirstReadCard.portraitAnchor, anchor: .bottom)
        }
        // The keyboard finishes rising after focus, so the visible area changes again; settle once it has.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) {
            proxy.scrollTo(FirstReadCard.portraitAnchor, anchor: .bottom)
        }
    }

    private func focusFirstRead(_ id: String, _ proxy: ScrollViewProxy) {
        scroll.policy.readFirstRead()
        DispatchQueue.main.async {
            scroll.policy.readFirstRead()
            proxy.scrollTo(id, anchor: .top)
        }
        // SwiftUI may lay out the taller answer after the first scroll.
        // Do not move the reader if they have already started dragging.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) {
            guard scroll.policy.holdingFirstRead else { return }
            proxy.scrollTo(id, anchor: .top)
        }
    }

    private func follow(_ action: ChatScrollPolicy.Action, _ proxy: ScrollViewProxy) {
        // The empty chat has no transcript to follow: it opens at its top (#635, P153). A chat with no rows never scrolls
        // to its bottom, so its card is not cut off under the title bar.
        guard chatID != ChatStore.mainChatAlias, chat.transcript?.items.isEmpty == false else { return }
        showLatestControl()
        guard case .scrollToBottom(let animated) = action else { return }
        if animated {
            withAnimation(.easeOut(duration: 0.25)) {
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
            }
        } else {
            proxy.scrollTo(Self.bottomID, anchor: .bottom)
        }
    }

    private func showLatestControl() {
        let policy = scroll.policy
        let next: LatestControl = policy.pinned || (policy.holdingFirstRead && !policy.hasNewBelow)
            ? .hidden : (policy.hasNewBelow ? .newMessages : .latest)
        if latest != next { latest = next }
    }

    private func latestButton(_ proxy: ScrollViewProxy) -> some View {
        Button {
            follow(scroll.policy.jumpToLatest(), proxy)
        } label: {
            // New messages stands out in warm ink, as the terracotta text did; Paper's accent is too close to
            // the secondary grey to carry it.
            Label(latest == .newMessages ? "New messages" : "Latest", systemImage: "arrow.down")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(latest == .newMessages ? YansuTheme.onSolid : YansuTheme.secondary)
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(latest == .newMessages ? YansuTheme.solid : YansuTheme.surface, in: Capsule())
                .overlay {
                    if latest != .newMessages {
                        Capsule().strokeBorder(YansuTheme.border, lineWidth: 1)
                    }
                }
                .shadow(color: Color.black.opacity(0.06), radius: 6, y: 2)
                .frame(minHeight: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.bottom, 4)
        .transition(.opacity)
        .accessibilityIdentifier("chat.latest")
    }

    // MARK: - Banners and states

    @ViewBuilder
    private var banners: some View {
        if let transcript = chat.transcript, chat.openChatID == chatID {
            Text("As of \(BriefView.asOfText(transcript.asOf ?? Date()))")
                .font(.footnote)
                .foregroundStyle(YansuTheme.tertiary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        if let notice = chat.notice {
            NoticeBanner(message: notice, dismiss: chat.dismissNotice)
        }
        offlineBanner
        let waiting = chat.queuedMessageCount(in: chatID)
        if waiting > 1 {
            Text(String(localized: "\(waiting) messages waiting"))
                .font(.footnote)
                .foregroundStyle(YansuTheme.tertiary)
                .accessibilityIdentifier("chat.waitingCount")
        }
        if isRunning {
            HStack(spacing: 7) {
                Text(String(localized: "Isoform is working on this chat."))
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(YansuTheme.secondary)
                ChatWorkingDots(reduceMotion: reduceMotion)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityIdentifier("chat.running")
        }
    }

    @ViewBuilder
    private var offlineBanner: some View {
        let time = chat.transcript?.asOf.map(BriefView.asOfText) ?? "an earlier time"
        if chat.hubOnline == false {
            NoticeBanner(message: "Showing the chat from \(time). Your computer is offline.", systemImage: "wifi.slash")
        } else if chat.transcriptRefreshFailed {
            NoticeBanner(message: "Showing the chat from \(time). Isoform could not refresh it.",
                         systemImage: "arrow.triangle.2.circlepath")
        } else if chat.listRefreshFailed {
            NoticeBanner(message: "Isoform could not refresh your chats.",
                         systemImage: "arrow.triangle.2.circlepath")
        }
    }

    @ViewBuilder
    private var transcriptContent: some View {
        if let transcript = chat.transcript, chat.openChatID == chatID {
            if Self.showsEmptyChat(transcript, outgoing: outgoing) {
                // The empty chat opens at its top, under the banner, and its last line clears the composer (P153). The
                // card takes its natural height so its copy wraps whole at the largest text size.
                EmptyStateCard(title: "No messages yet.",
                               detail: "Write below to start. Isoform answers on your computer.")
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 16)
                    .accessibilityIdentifier("chat.emptyState")
            } else {
                ForEach(transcript.items) { item in
                    row(for: item)
                }
                // The Mac's pending permission is transient: it is shown after the stored rows and only while the
                // latest read still has it.
                if let pending = transcript.pendingPermission, let permission = pending.permission,
                   pending.isPermissionPrompt, !chat.isAnswered(permission.requestID) {
                    permissionCard(pending, permission: permission)
                }
            }
        } else if chatID == ChatStore.mainChatAlias {
            // No main chat listed yet: the composer is the whole screen, with its prompt (PROTOCOL.md 15.5a). The
            // top inset keeps the banner and its retry below the title bar.
            VStack(alignment: .leading, spacing: 12) {
                if case .failed(let message) = chat.listState {
                    NoticeBanner(message: message, systemImage: "exclamationmark.circle")
                    // Try again is a text link under its line, in the accent color (never a full-width button).
                    Button(String(localized: "Try again")) { Task { await chat.refreshList() } }
                        .buttonStyle(.plain)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("chat.list.retry")
                }
                // The card takes its natural height, so its copy wraps whole and never truncates (#635).
                EmptyStateCard(title: ChatCopy.askAnything, detail: "")
                    .fixedSize(horizontal: false, vertical: true)
            }
            // The transcript opens at its top: the banner sits just under the title bar, and the scroll area runs to the
            // composer with nothing extra below (#635).
            .padding(.top, 16)
        } else if case .failed(let message) = chat.transcriptState {
            NoticeBanner(message: message, systemImage: "exclamationmark.circle")
            EmptyStateCard(title: "This chat is not available yet.",
                           detail: "Pull down to check with your computer again.")
            Button(String(localized: "Try again")) { Task { await chat.open(chatID: chatID) } }
                .buttonStyle(YansuPrimaryButtonStyle())
                .accessibilityIdentifier("chat.transcript.retry")
        } else {
            ChatLoadingSkeleton()
        }
    }

    /// "No messages yet" only while nothing is stored, pending or on its way from this iPhone.
    static func showsEmptyChat(_ transcript: OpenChat, outgoing: [ChatOutgoingMessage]) -> Bool {
        transcript.items.isEmpty && transcript.pendingPermission == nil && outgoing.isEmpty
    }

    /// Messages from this iPhone that no row on screen shows yet, under the stored rows.
    @ViewBuilder
    private var outgoingContent: some View {
        let rows = chat.openChatID == chatID ? chat.transcript?.items : nil
        ForEach(ChatDelivery.drawnBelowRows(outgoing, rows: rows)) { message in
            outgoingBubble(message)
                .id(message.id)
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
        }
        .animation(reduceMotion ? .easeOut(duration: 0.2) : .spring(duration: 0.42),
                   value: outgoing.map(\.id))
    }

    // MARK: - Rows

    @ViewBuilder
    private func row(for item: ChatItemView) -> some View {
        if let report = item.report {
            ChatMessageReportRow(report: report)
        } else if item.kind == .proactive, let proactive = item.proactive, proactive.isWithdrawnThreadUpdate {
            // Nothing is left to answer, so the update is one quiet routine line, never a waiting card.
            Text(proactive.headline)
                .font(.footnote)
                .foregroundStyle(YansuTheme.tertiary)
                .frame(maxWidth: .infinity, alignment: .center)
                .multilineTextAlignment(.center)
                .accessibilityIdentifier("chat.withdrawnUpdate")
        } else if item.kind == .proactive, let proactive = item.proactive, proactive.kind == "first_read" {
            if !proactive.sentences.isEmpty {
                FirstReadCard(item: proactive, chatID: chatID, onPortraitChanged: {
                    firstReadFocusID = item.id
                    firstReadFocusSignal = UUID()
                }, onCorrectionSent: { firstReadCorrectionSignal = UUID() },
                onCorrectionFocus: { firstReadCorrectionFocusSignal = UUID() })
            }
        } else if item.kind == .proactive, let proactive = item.proactive {
            VStack(alignment: .leading, spacing: 10) {
                proactiveCard(proactive)
                ForEach(proactive.items) { child in
                    proactiveCard(child)
                }
            }
        } else if item.kind == .status {
            ChatStatusRow(status: item.status, fallback: item.text)
        } else if item.kind == .toolUse || item.kind == .toolResult || item.role == .tool {
            toolRow(item)
        } else if item.kind == .error {
            let message = ChatReplyPresentation(item.text, kind: .error)
            VStack(alignment: .leading, spacing: 8) {
                NoticeBanner(message: message.text.isEmpty ? "Isoform could not finish that step." : message.text,
                             systemImage: "exclamationmark.circle")
                ChatTruncationNote(note: message.truncationNote)
            }
        } else if item.role == .user {
            // A stored transcript row itself proves the computer received this message, even after local
            // receipt pruning. Only a retained receipt can additionally prove that the computer saw it.
            let period = chat.awayPeriod(containingSeq: item.seq, in: chatID)
            let wentOut = period?.seqs.first { $0.value == item.seq }.flatMap { period?.sentAt[$0.key] }
            VStack(spacing: 8) {
                userBubble(text: item.text,
                           delivery: outgoing.first { $0.seq == item.seq }?.deliveryStatus(computerOnline: chat.hubOnline) ?? .sent,
                           sentLabel: wentOut.map { ChatAwayCopy.time($0, locale: locale) },
                           showsReceipt: unsentLatestOutgoingID == nil && item.seq == latestStoredUserSeq)
                if let period, let first = period.messageIDs.first, period.seqs[first] == item.seq {
                    awayLine(period)
                }
            }
        } else if item.role == .system {
            let message = ChatReplyPresentation(item.text, kind: .systemMessage)
            VStack(spacing: 8) {
                Text(message.text)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.tertiary)
                ChatTruncationNote(note: message.truncationNote)
            }
            .frame(maxWidth: .infinity, alignment: .center)
            .multilineTextAlignment(.center)
        } else if let quiet = askedAtLine(for: item) {
            VStack(alignment: .leading, spacing: 6) {
                Text(quiet)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.tertiary)
                    .lineLimit(1)
                    .accessibilityIdentifier("chat.answer.askedAt")
                ChatAssistantReply(text: item.text, revealsLiveReply: liveReplySeq == item.seq)
            }
        } else {
            ChatAssistantReply(text: item.text, revealsLiveReply: liveReplySeq == item.seq)
        }
    }

    // MARK: - Waiting while the computer is away

    /// A message from this iPhone: waiting in the light tint with Undo, or with how far it got.
    @ViewBuilder
    private func outgoingBubble(_ message: ChatOutgoingMessage) -> some View {
        let delivery = message.deliveryStatus(computerOnline: chat.hubOnline)
        let period = chat.awayPeriod(containing: message.id, in: chatID)
        let expired = message.expiredWhileWaiting(computerOnline: chat.hubOnline, now: Date())
        VStack(spacing: 8) {
            if expired {
                waitingBubble(message, expired: true)
            } else if delivery == .waitingForComputer, chat.canUndoWaiting(message) {
                waitingBubble(message, expired: false)
            } else {
                userBubble(text: message.text, delivery: delivery,
                           retry: message.hasFailed ? { Task { await chat.retryFailed(message.id, in: chatID) } } : nil,
                           sentLabel: period?.sentAt[message.id].map { ChatAwayCopy.time($0, locale: locale) },
                           showsReceipt: message.id == unsentLatestOutgoingID)
            }
            if let period, period.messageIDs.first == message.id {
                awayLine(period)
            }
        }
    }

    private func waitingBubble(_ message: ChatOutgoingMessage, expired: Bool) -> some View {
        VStack(alignment: .trailing, spacing: 4) {
            HStack(alignment: .bottom) {
                Spacer(minLength: 48)
                ChatMessageText(text: message.text, kind: .message)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(YansuTheme.accentWaiting, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contextMenu {
                        Button {
                            UIPasteboard.general.string = message.text
                        } label: {
                            Label(String(localized: "Copy"), systemImage: "doc.on.doc")
                        }
                        Button {
                            undoWaiting(message)
                        } label: {
                            Label(String(localized: "Undo"), systemImage: "arrow.uturn.backward")
                        }
                    }
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("chat.message.waiting")
            HStack(spacing: 4) {
                if expired {
                    // A day with no answer: the relay let it go, so it did not send (PROTOCOL.md section 23).
                    Text(String(localized: "This waited a day, so it wasn't sent."))
                        .accessibilityIdentifier("chat.message.status")
                    Button(String(localized: "Send again")) { Task { await chat.sendExpiredAgain(message.id, in: chatID) } }
                        .buttonStyle(.plain)
                        .fontWeight(.semibold)
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("chat.message.sendAgain")
                } else {
                    Text(String(localized: "Waits for your computer"))
                        .accessibilityIdentifier("chat.message.status")
                    Text(verbatim: "·")
                        .accessibilityHidden(true)
                    Button(String(localized: "Undo")) { undoWaiting(message) }
                        .buttonStyle(.plain)
                        .fontWeight(.semibold)
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("chat.message.undo")
                }
            }
            .font(.caption)
            .foregroundStyle(YansuTheme.secondary)
            .padding(.trailing, 6)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }

    private func undoWaiting(_ message: ChatOutgoingMessage) {
        Task {
            if await chat.undoWaiting(message.id, in: chatID) { composerFocused = true }
        }
    }

    /// One line per stretch away, under its first message: why and what happens, then green once it is back.
    @ViewBuilder
    private func awayLine(_ period: ChatAwayPeriod) -> some View {
        if let back = period.backLine(locale: locale) {
            Text(back)
                .font(.footnote)
                .foregroundStyle(YansuTheme.success)
                .frame(maxWidth: .infinity, alignment: .center)
                .multilineTextAlignment(.center)
                .accessibilityIdentifier("chat.away.back")
        } else if !period.isOver {
            Button { awayMore = period.cause } label: {
                Text("\(period.lineText(now: Date(), settled: chat.awayPeriodAllNotSent(period, in: chatID), locale: locale)) \(Text(String(localized: "More")).fontWeight(.semibold).foregroundStyle(YansuTheme.accent))")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
                    .multilineTextAlignment(.center)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("chat.away.line")
        }
    }

    /// "You asked at 7:42 AM · {the start of the question}" over an answer, when several messages waited or the
    /// answer came more than an hour after its question.
    private func askedAtLine(for item: ChatItemView) -> String? {
        guard item.role == .assistant, let raw = item.askedAt, let stored = RelayTime.parse(raw) else { return nil }
        let question = chat.transcript?.items.last { $0.role == .user && $0.seq < item.seq }
        let period = question.flatMap { chat.awayPeriod(containingSeq: $0.seq, in: chatID) }
        let asked = question.flatMap { period?.typedAt(seq: $0.seq) } ?? stored
        guard ChatAwayCopy.showsAskedAt(askedAt: asked, answeredAt: item.atDate,
                                        severalWaited: period?.waitedMoreThanOne == true) else { return nil }
        return ChatAwayCopy.askedAt(asked, question: question?.text ?? "", locale: locale)
    }

    private func proactiveCard(_ item: ChatProactiveView) -> some View {
        ChatProactiveCard(item: item,
                          isSelected: chat.selectedProactive?.momentID == item.momentID &&
                              chat.selectedProactiveChatID == chatID,
                          onOpen: { openProactiveItem(item) },
                          performMute: {
                              await actions.performProactiveAction(chatID: chatID, momentID: item.momentID,
                                                                   actionID: "mute_kind",
                                                                   permissionRequest: false, locale: locale)
                          }) { action in
            let outcome = await actions.performProactiveAction(chatID: chatID, momentID: item.momentID,
                                                               actionID: action.id,
                                                               permissionRequest: item.permission != nil, locale: locale)
            switch outcome {
            case .applied, .refused:
                // Action revisions keep their seq, so refresh the newest page rather than the after cursor.
                Task { await chat.refreshAfterProactiveAction(chatID: chatID) }
            case .pending, .cancelled: break
            }
            return outcome
        }
    }

    private func openProactiveItem(_ item: ChatProactiveView) {
        chat.openProactiveItem(item, chatID: chatID)
        Task { await actions.recordProactiveOpened(chatID: chatID, momentID: item.momentID) }
    }

    private func openPendingProactiveItem() {
        guard let momentID = router.pendingMomentID,
              chat.openChatID == chatID,
              chat.transcriptState == .loaded else { return }
        if let item = ChatProactiveLookup.find(momentID: momentID, in: chat.transcript?.items ?? []) {
            openProactiveItem(item)
        } else if let answer = chat.transcript?.items.first(where: { $0.stableID == momentID }) {
            // An answer notification opens Chat at the first answer.
            firstReadFocusID = answer.id
            firstReadFocusSignal = UUID()
        }
        router.consumePendingMomentID(momentID)
    }

    /// A message from the user. One sent from this iPhone says under it how far it has got.
    /// P141: only the latest message of yours shows its receipt. A retry for a failed one always shows.
    private func userBubble(text: String, delivery: ChatDeliveryStatus, retry: (() -> Void)? = nil,
                            sentLabel: String? = nil, showsReceipt: Bool = true) -> some View {
        VStack(alignment: .trailing, spacing: 4) {
            HStack(alignment: .bottom) {
                Spacer(minLength: 48)
                ChatMessageText(text: text, kind: .message)
                    .textSelection(.enabled)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(YansuTheme.surfaceUser, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .overlay {
                        RoundedRectangle(cornerRadius: 18, style: .continuous)
                            .strokeBorder(YansuTheme.border, lineWidth: 1)
                    }
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("chat.message.user")
            HStack(spacing: 10) {
                if showsReceipt {
                    Group {
                        if let sentLabel {
                            Text(sentLabel)
                        } else if let explanation = delivery.explanation {
                            Button(delivery.label) { deliveryExplanation = explanation }
                                .buttonStyle(.plain)
                        } else {
                            Text(delivery.label)
                        }
                    }
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("chat.message.status")
                }
                if let retry, delivery == .failed {
                    // The words are back in the box; this sends them as an ordinary message.
                    Button(String(localized: "Try again"), action: retry)
                        .buttonStyle(.plain)
                        .fontWeight(.semibold)
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("chat.message.retry")
                }
            }
            .font(.caption)
            .padding(.trailing, 6)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }

    private func toolRow(_ item: ChatItemView) -> some View {
        let message = ChatReplyPresentation(item.text, kind: item.kind == .toolUse ? .toolSummary : .toolOutput)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "wrench.and.screwdriver")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.tertiary)
                    .accessibilityHidden(true)
                Text(item.toolName.isEmpty ? "Tool" : item.toolName)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(YansuTheme.secondary)
                if !message.text.isEmpty {
                    Text(message.text)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.tertiary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                }
                Spacer(minLength: 0)
            }
            ChatTruncationNote(note: message.truncationNote)
        }
        .padding(.horizontal, 4)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("chat.tool")
    }

    private func permissionCard(_ item: ChatItemView, permission: ChatPermissionView) -> some View {
        let busy = chat.isBusy("permission:\(permission.requestID)")
        return YansuCard {
            VStack(alignment: .leading, spacing: 14) {
                Text("Isoform needs your permission")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                if !item.text.isEmpty {
                    ChatMessageText(text: item.text, kind: .permissionPrompt)
                }
                VStack(spacing: 10) {
                    ForEach(Array(permission.options.enumerated()), id: \.element.id) { index, option in
                        Button(option.label) {
                            Task { await chat.answerPermission(item, optionID: option.id) }
                        }
                        .buttonStyle(index == 0 ? AnyButtonStyle(YansuPrimaryButtonStyle())
                                                : AnyButtonStyle(YansuSecondaryButtonStyle()))
                        .disabled(busy)
                        .accessibilityIdentifier("chat.permission.option")
                    }
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.permission.card")
    }

    // MARK: - Composer

    private func composer(_ proxy: ScrollViewProxy) -> some View {
        let sendable = !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return VStack(alignment: .leading, spacing: 8) {
            if isRunning {
                Text(String(localized: "Isoform is still answering. You can send your next message when it finishes."))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.tertiary)
            }
            if !chat.composerDraft(in: chatID).offerReferences.isEmpty || chat.composerDraft(in: chatID).briefReference != nil {
                Button(ChatProvenanceCopy.text("Discard draft", "放弃草稿", locale: locale)) {
                    if chat.discardComposer(in: chatID) {
                        resetComposerUndo()
                        recoverDrafts()
                    }
                }
                .font(.footnote)
                .accessibilityIdentifier("chat.discardDraft")
            }
            if dynamicTypeSize.isAccessibilitySize {
                // At accessibility sizes the field keeps its own row; the mic and Send sit under it.
                VStack(alignment: .leading, spacing: 8) {
                    composerField
                    HStack(spacing: 10) {
                        HoldToTalk(text: draftBinding)
                        Spacer(minLength: 0)
                        if isRunning {
                            Button("Stop") {
                                Task { await chat.cancelTurn() }
                            }
                            .buttonStyle(ComposerButtonStyle(prominent: false))
                            .fixedSize()
                            .accessibilityIdentifier("chat.stop")
                        }
                        sendButton(proxy)
                    }
                }
            } else {
                HStack(alignment: .bottom, spacing: 10) {
                    composerField
                    HoldToTalk(text: draftBinding)
                    if isRunning {
                        Button("Stop") {
                            Task { await chat.cancelTurn() }
                        }
                        .buttonStyle(ComposerButtonStyle(prominent: false))
                        .fixedSize()
                        .accessibilityIdentifier("chat.stop")
                    }
                    sendButton(proxy)
                }
            }
        }
        .padding(.horizontal, YansuTheme.gutter)
        // The floating tab bar covers the bottom of a tab page, so the composer stands above it. The bar stays while
        // the field has focus (P159), so the composer sits above the bar and the keyboard.
        .padding(.bottom, 10 + tabChromeHeight)
        .background(YansuTheme.background)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.composerBar")
    }

    private var composerField: some View {
        // Our own prompt, in the same stack as the field, so it wraps at the largest sizes instead of truncating to
        // "Message Isof…". The stack is the field's box, so the box grows with the prompt and the text.
        ZStack(alignment: .topLeading) {
            TextField("", text: draftBinding, axis: .vertical)
                .accessibilityLabel(String(localized: "Message Isoform"))
                .id(composerIdentity)
                .font(.body)
                .lineLimit(1...5)
                .focused($composerFocused)
                .composerKeyboardDone { composerFocused = false }
                .accessibilityIdentifier("chat.composer")
            if draftBinding.wrappedValue.isEmpty {
                Text("Message Isoform")
                    .font(.body)
                    .foregroundStyle(YansuTheme.tertiary)
                    .fixedSize(horizontal: false, vertical: true)
                    .allowsHitTesting(false)
            }
        }
        // 22 pt of text plus 11 pt each side makes the box 44 pt, the same as Send, so the box's top is the row's top.
        .yansuFieldBox()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.composerBox")
    }

    private func sendButton(_ proxy: ScrollViewProxy) -> some View {
        Button("Send") {
            Task { await send(proxy) }
        }
        .buttonStyle(ComposerButtonStyle(prominent: true))
        .fixedSize()
        .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isRunning)
        .accessibilityIdentifier("chat.send")
    }

    /// The message leaves the composer at once and shows as Sending; a refused one comes back to the composer.
    private func send(_ proxy: ScrollViewProxy) async {
        guard !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        // Undo must never resurrect idea text into the fresh, unassociated composer after a send.
        resetComposerUndo()
        follow(scroll.policy.jumpToLatest(), proxy)
        if await chat.sendComposer(in: chatID, onEnqueued: { resetComposerUndo() }) {
            composerFocused = false
            recoverDrafts()
        }
    }

    private func resetComposerUndo() {
        undoManager?.removeAllActions()
        composerIdentity = UUID()
    }

}

/// Holds the scroll policy outside SwiftUI state, so the layout reports that arrive on every scroll frame do not
/// redraw the chat; only a change to the Latest control does.
private final class ChatScrollTracker {
    var policy = ChatScrollPolicy()
    var contentFrame: CGRect?
    var visibleBottom: CGFloat?

    func layout() -> ChatScrollPolicy.Action {
        guard let contentFrame, let visibleBottom, contentFrame.height > 0 else { return .none }
        return policy.layout(contentTop: contentFrame.minY, contentHeight: contentFrame.height,
                             visibleBottom: visibleBottom)
    }
}

// Views that do not set a key still take part in `reduce` with the default value, so the keys are optional and
// a missing value never replaces a measured one.
private struct ChatContentFrameKey: PreferenceKey {
    static var defaultValue: CGRect?

    static func reduce(value: inout CGRect?, nextValue: () -> CGRect?) {
        value = nextValue() ?? value
    }
}

private struct ChatVisibleBottomKey: PreferenceKey {
    static var defaultValue: CGFloat?

    static func reduce(value: inout CGFloat?, nextValue: () -> CGFloat?) {
        value = nextValue() ?? value
    }
}

/// Uses the transcript's spacing and warm cards while the first page is being read.
struct ChatLoadingSkeleton: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            ForEach(0..<3) { index in
                HStack {
                    if index == 1 { Spacer(minLength: 44) }
                    YansuCard {
                        VStack(alignment: .leading, spacing: 10) {
                            RoundedRectangle(cornerRadius: 4)
                                .fill(YansuTheme.border)
                                .frame(width: index == 1 ? 90 : 130, height: 12)
                            RoundedRectangle(cornerRadius: 4)
                                .fill(YansuTheme.border.opacity(0.6))
                                .frame(height: 12)
                        }
                    }
                    if index != 1 { Spacer(minLength: 24) }
                }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Checking with your computer.")
        .accessibilityIdentifier("chat.loading")
    }
}

struct ChatAssistantReply: View {
    let text: String
    var revealsLiveReply = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var visibleCharacters = 0

    var body: some View {
        HStack(alignment: .bottom) {
            ChatMessageText(text: displayedText, kind: .reply)
                .textSelection(.enabled)
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 18, style: .continuous)
                        .strokeBorder(YansuTheme.border, lineWidth: 1)
                }
            Spacer(minLength: 48)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(Text(verbatim: text))
        .accessibilityIdentifier("chat.message.assistant")
        .task(id: "\(text)|\(revealsLiveReply)") {
            guard revealsLiveReply, !reduceMotion else {
                visibleCharacters = text.count
                return
            }
            visibleCharacters = 0
            while visibleCharacters < text.count {
                let start = text.index(text.startIndex, offsetBy: visibleCharacters)
                let next = text[start...].firstIndex(where: { $0.isWhitespace })
                    .map { text.distance(from: text.startIndex, to: text.index(after: $0)) }
                    ?? text.count
                visibleCharacters = next
                do { try await Task.sleep(for: .milliseconds(30)) }
                catch { visibleCharacters = text.count; return }
            }
        }
    }

    private var displayedText: String {
        guard revealsLiveReply, !reduceMotion else { return text }
        return String(text.prefix(visibleCharacters))
    }
}

private struct ChatWorkingDots: View {
    let reduceMotion: Bool

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: reduceMotion)) { timeline in
            HStack(spacing: 3) {
                ForEach(0..<3) { index in
                    let phase = (timeline.date.timeIntervalSinceReferenceDate * 2.4 +
                                 Double(index) * 0.65).truncatingRemainder(dividingBy: 2)
                    let opacity = reduceMotion ? 0.65 : 0.35 + (phase < 1 ? phase : 2 - phase) * 0.55
                    Circle()
                        .fill(YansuTheme.secondary.opacity(opacity))
                        .frame(width: 4, height: 4)
                }
            }
            .accessibilityHidden(true)
        }
    }
}

struct ChatMessageReportRow: View {
    let report: ChatMessageReport
    @Environment(\.locale) private var locale

    var body: some View {
        Text(report.presentation(locale: locale))
            .font(.footnote)
            .foregroundStyle(YansuTheme.tertiary)
            .frame(maxWidth: .infinity, alignment: .center)
            .multilineTextAlignment(.center)
            .accessibilityIdentifier("chat.messageSendReport")
    }
}

/// A durable message with a live, owner-checked action. Refusals leave the original choices in place.
struct ChatProactiveCard: View {
    let item: ChatProactiveView
    var isSelected = false
    var showsDetails = false
    var showsStatus = true
    var showsActions = true
    var onOpen: () -> Void = {}
    var performMute: (() async -> ProactiveActionOutcome)?
    let perform: (ChatProactiveAction) async -> ProactiveActionOutcome
    @Environment(\.locale) private var locale
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var pairing: PairingStore
    @State private var busy = false
    @State private var result: String?
    @State private var appliedState: String?
    @State private var showsEvidence = false
    @State private var showsDraft = false
    @Environment(\.openTaskThread) private var openTaskThread

    private var chrome: ChatPresentationLanguage { ChatPresentationLanguage(locale: locale) }
    private var language: String { chrome.code }
    /// Briefs whose words are on the phone: Chat offers Play and Read all only for these.
    private var availableBriefIDs: Set<String> {
        Set(environment.brief.cachedBriefs.filter(BriefCache.isCurrentCandidate).compactMap(\.briefId))
    }

    /// The brief this card is about, when its words are on the phone. Chat shows the brief card for it.
    private var briefCardBrief: MobileBriefV1? {
        guard let briefID = item.briefID else { return nil }
        return environment.brief.cachedBriefs.first { $0.identifier == briefID && BriefCache.isCurrentCandidate($0) }
    }
    private var current: ChatProactiveView {
        var value = item
        value.state = appliedState ?? item.state
        return value
    }

    var body: some View {
            let copy = item.copy(language: language)
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top, spacing: 10) {
                if let headline = copy.headline.nilIfEmpty {
                    Text(headline)
                        .font(.body.weight(.medium))
                        .foregroundStyle(YansuTheme.ink)
                        .accessibilityAddTraits(.isHeader)
                }
                Spacer(minLength: 0)
                if item.actionIDs.contains("mute_kind"),
                   ChatProactiveAction.knownIDs.contains("mute_kind"), performMute != nil {
                    Menu {
                        Button(String(localized: "Mute this kind"), systemImage: "bell.slash") {
                            muteKind()
                        }
                    } label: {
                        Image(systemName: "ellipsis")
                            .frame(width: 36, height: 36)
                            .contentShape(Rectangle())
                    }
                    .accessibilityLabel(String(localized: "More options"))
                    .accessibilityIdentifier("chat.proactive.more")
                }
            }
            if let body = copy.body.nilIfEmpty {
                ChatMessageText(text: body, kind: .reply)
                    .textSelection(.enabled)
            }
            if showsDetails { detailFields }
            if let briefCard = briefCardBrief, let hubID = pairing.paired?.hubID {
                ChatBriefCard(brief: briefCard, hubID: hubID, player: environment.audioPlayer)
            }
            if let permission = item.permission {
                VStack(alignment: .leading, spacing: 6) {
                    Text(permission.toolName)
                        .font(.footnote.weight(.semibold))
                    Text(permission.details)
                        .font(.footnote)
                        .fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                }
                .foregroundStyle(YansuTheme.secondary)
            }
            if let draft = item.readableDraft {
                if showsDetails {
                    ChatProactiveDraftContent(draft: draft, identifier: "chat.proactive.draft")
                } else {
                    Button(showsDraft ? String(localized: "Hide the draft") : String(localized: "Read the draft")) {
                        showsDraft.toggle()
                    }
                    .buttonStyle(YansuSecondaryButtonStyle())
                    .disabled(busy)
                    .accessibilityIdentifier("chat.proactive.readDraft")
                    if showsDraft {
                        ChatProactiveDraftContent(draft: draft, identifier: "chat.proactive.draft")
                    }
                }
            }
            if showsActions { actionRow }
            if showsStatus {
                Text(busy ? chrome.text(en: "Checking with your computer...", zhCN: "正在向电脑确认...")
                          : current.statusLabel(language: language))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.tertiary)
                    .accessibilityLabel(chrome.text(en: "Status: \(statusText)", zhCN: "状态：\(statusText)"))
                    .accessibilityIdentifier("chat.proactive.state")
            }
            if let result {
                Text(result)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("chat.proactive.result")
            }
            evidenceLine
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(isSelected ? YansuTheme.accentWash : YansuTheme.surface,
                    in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .strokeBorder(isSelected ? YansuTheme.accent : YansuTheme.border, lineWidth: isSelected ? 2 : 1)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.proactive.card")
        .onChange(of: item.state) { _, _ in appliedState = nil }
    }

    private var statusText: String {
        busy ? chrome.text(en: "Checking with your computer", zhCN: "正在向电脑确认")
             : current.statusLabel(language: language)
    }

    @ViewBuilder
    private var detailFields: some View {
        if let personName = item.personName?.nilIfEmpty {
            Text(personName)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(YansuTheme.secondary)
        }
        if let place = item.place {
            let label = [place.app, place.window].compactMap { value in
                value?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty
            }.joined(separator: " · ")
            if !label.isEmpty {
                Text(label)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
            }
            if let at = place.at.flatMap(RelayTime.parse) {
                Text(at.formatted(date: .abbreviated, time: .shortened))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
        let events = ([item.event].compactMap { $0 } + item.events).filter { event in
            event.start.flatMap(Self.eventTime) != nil || event.end.flatMap(Self.eventTime) != nil
                || event.place?.nilIfEmpty != nil || (event.minutes ?? 0) > 0
                || event.isPrivate == false && (event.title?.nilIfEmpty != nil || event.people?.nilIfEmpty != nil)
        }
        ForEach(Array(events.enumerated()), id: \.offset) { index, event in
            VStack(alignment: .leading, spacing: 4) {
                if event.isPrivate == false, let title = event.title?.nilIfEmpty {
                    Text(title)
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(YansuTheme.ink)
                }
                let time = [event.start, event.end].compactMap { $0.flatMap(Self.eventTime) }.joined(separator: " – ")
                if !time.isEmpty {
                    Text(time)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                if let place = event.place?.nilIfEmpty {
                    Text(place)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                if let minutes = event.minutes, minutes > 0 {
                    Text(String(format: String(localized: "mainChat.card.eventMinutes", locale: locale),
                                locale: locale, Int64(minutes)))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                if event.isPrivate == false, let people = event.people?.nilIfEmpty {
                    Text(people)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
            }
            .accessibilityIdentifier("chat.proactive.event.\(index)")
        }
        if let work = item.work {
            if let summary = work.summary.nilIfEmpty {
                Text(summary)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
            }
            if let result = work.result.nilIfEmpty {
                Text(result)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
    }

    private static func eventTime(_ value: String) -> String? {
        guard let date = RelayTime.parse(value) else { return nil }
        return date.formatted(date: .abbreviated, time: .shortened)
    }

    @ViewBuilder
    private var actionRow: some View {
        let slots = ChatCardButtons.make(for: item, availableBriefIDs: availableBriefIDs,
                                         excludingPanelButtons: showsDetails)
        // The brief card has its own Play and Read all, so the card's Play and read buttons step aside for it.
        let hasBriefCard = briefCardBrief != nil && pairing.paired?.hubID != nil
        let choices: [ChatCardButton] = {
            var list = [slots.main, slots.second].compactMap { $0 }.filter {
                !(hasBriefCard && ($0.behavior == .play || $0.behavior == .expand))
            }
            // Once the draft is read, its reply goes out from the card (the send is the filled button).
            if showsDraft, item.readableDraft != nil {
                list.insert(ChatCardButton(id: "send", labelKey: "sendReply", labelValue: nil, behavior: .answer,
                                           actionID: "send"), at: 0)
            }
            return list
        }()
        if !choices.isEmpty {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 10) { actionButtons(choices) }
                VStack(alignment: .leading, spacing: 10) { actionButtons(choices) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func actionButtons(_ choices: [ChatCardButton]) -> some View {
        let primary = ChatCardPrimary.index(in: choices)
        return ForEach(Array(choices.enumerated()), id: \.element.id) { index, button in
            Button(button.label(locale: locale)) { handle(button) }
                .buttonStyle(index == primary ? AnyButtonStyle(YansuPrimaryButtonStyle())
                                              : AnyButtonStyle(YansuSecondaryButtonStyle()))
                .fixedSize(horizontal: true, vertical: false)
                .disabled(busy || (button.behavior == .answer && !current.canAct))
                .accessibilityLabel(button.label(locale: locale))
                .accessibilityIdentifier("chat.proactive.button.\(button.id)")
        }
    }

    private func handle(_ button: ChatCardButton) {
        switch button.behavior {
        case .panel, .expand:
            onOpen()
        case .answer:
            guard let actionID = button.actionID else { return }
            if actionID == "review", let work = item.work, let openTaskThread,
               Identifiers.isSafePathID(work.handoffId) {
                // Review opens the task's thread, never a prompt in Chat (Phone Threads §1).
                openTaskThread(.placeholder(work: work, title: item.headline))
                return
            }
            Task { await apply(ChatProactiveAction(id: actionID, label: button.label(locale: locale))) }
        case .calendar:
            guard let start = ChatCardButtons.eventStart(item).flatMap(RelayTime.parse),
                  let url = URL(string: "calshow:\(start.timeIntervalSinceReferenceDate)") else { return }
            UIApplication.shared.open(url)
        case .play:
            guard let briefID = item.briefID,
                  let brief = environment.brief.cachedBriefs.first(where: { $0.briefId == briefID }),
                  let hubID = pairing.paired?.hubID else { return }
            Task { await environment.audioPlayer.toggle(brief: brief, hubID: hubID) }
        case .correct, .place, .activity, .file:
            break
        }
    }

    private func muteKind() {
        guard !busy, let performMute else { return }
        busy = true
        result = nil
        Task {
            let outcome = await performMute()
            busy = false
            await applyOutcome(outcome, actionID: "mute_kind")
        }
    }

    private func apply(_ action: ChatProactiveAction) async {
        guard !busy, current.canAct else { return }
        busy = true
        result = nil
        let outcome = await perform(action)
        busy = false
        await applyOutcome(outcome, actionID: action.id)
    }

    @MainActor
    private func applyOutcome(_ outcome: ProactiveActionOutcome, actionID: String) {
        switch outcome {
        case .applied(let state):
            appliedState = state
            if actionID == "review", item.work != nil {
                result = chrome.text(en: "The result is ready in this chat.", zhCN: "结果已在此聊天中准备好。")
            } else {
                result = current.statusLabel(language: language)
            }
        case .refused(let message):
            // A send the computer refused keeps the words on the card to send again.
            result = actionID == "send" ? String(localized: "Not sent. Try again.") : message
        case .pending(let message):
            result = message
        case .cancelled:
            result = nil
        }
    }

    @ViewBuilder
    private var evidenceLine: some View {
        if let first = item.evidence.first {
            if showsDetails {
                let sources = item.evidence.map { $0.sourceLabel(language: language) }
                Text(chrome.text(en: "From \(sources.joined(separator: ", "))",
                                 zhCN: "来源：\(sources.joined(separator: "、"))"))
                    .font(.caption)
                    .foregroundStyle(YansuTheme.secondary)
            } else {
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    // P142: the foot reads "From Isoform · 10:48 AM", the time only, the way Today's times read.
                    if let at = first.at.flatMap(RelayTime.parse) {
                        Text(chrome.text(en: "From \(first.sourceLabel(language: language))",
                                         zhCN: "来自\(first.sourceLabel(language: language))") + " · " + BriefClock.text(at, locale: locale))
                    } else {
                        Text(chrome.text(en: "From \(first.sourceLabel(language: language))",
                                         zhCN: "来自\(first.sourceLabel(language: language))"))
                    }
                    Spacer(minLength: 0)
                    Button(showsEvidence ? chrome.text(en: "Hide", zhCN: "收起")
                                         : chrome.text(en: "Why", zhCN: "原因")) { showsEvidence.toggle() }
                        .foregroundStyle(YansuTheme.accent)
                        .frame(minHeight: 44)
                        .accessibilityLabel(showsEvidence ? chrome.text(en: "Hide sources", zhCN: "收起来源")
                                                         : chrome.text(en: "Why am I seeing this?", zhCN: "为什么显示这条信息？"))
                }
                if showsEvidence {
                    let sources = item.evidence.map { $0.sourceLabel(language: language) }.joined(separator: ", ")
                    Text(chrome.text(en: "Isoform used these sources on your computer: \(sources). Read the source details there.",
                                     zhCN: "Isoform 使用了电脑上的这些来源：\(sources)。请在电脑上查看来源详情。"))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .font(.caption)
            .foregroundStyle(YansuTheme.secondary)
            }
        }
    }
}

private extension String {
    var nilIfEmpty: String? {
        let value = trimmingCharacters(in: .whitespacesAndNewlines)
        return value.isEmpty ? nil : value
    }
}

struct ChatProactiveDraftContent: View {
    let draft: ChatProactiveDraft
    let identifier: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(String(localized: "To \(draft.to)"))
                .font(.footnote.weight(.medium))
                .foregroundStyle(YansuTheme.secondary)
            if !draft.subject.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                Text(String(localized: "Subject \(draft.subject)"))
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(YansuTheme.secondary)
            }
            Text(verbatim: draft.text)
                .font(.body)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(identifier)
    }
}

/// Routine updates occupy one quiet line; expanding them never changes the Mac's state.
struct ChatStatusRow: View {
    let status: ChatStatusView?
    let fallback: String
    @Environment(\.locale) private var locale
    @State private var expanded = false

    private var chrome: ChatPresentationLanguage { ChatPresentationLanguage(locale: locale) }
    private var language: String { chrome.code }

    var body: some View {
        VStack(spacing: 8) {
            if let status, !status.entries.isEmpty {
                Button { expanded.toggle() } label: {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Text(status.summary(language: language, fallback: fallback))
                        Image(systemName: expanded ? "chevron.up" : "chevron.down")
                            .font(.caption2)
                            .accessibilityHidden(true)
                    }
                    .frame(maxWidth: .infinity, minHeight: 44)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(status.summary(language: language, fallback: fallback))
                .accessibilityValue(expanded ? chrome.text(en: "Expanded", zhCN: "已展开")
                                              : chrome.text(en: "Collapsed", zhCN: "已收起"))
                .accessibilityHint(expanded ? chrome.text(en: "Hide status details", zhCN: "收起状态详情")
                                             : chrome.text(en: "Show status details", zhCN: "展开状态详情"))
                .accessibilityIdentifier("chat.status")
                if expanded {
                    ForEach(status.entries) { entry in
                        Text(entry.displayText(language: language))
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            } else {
                let message = ChatReplyPresentation(fallback, kind: .systemMessage)
                Text(message.text).accessibilityIdentifier("chat.status")
                ChatTruncationNote(note: message.truncationNote)
            }
        }
        .font(.footnote)
        .foregroundStyle(YansuTheme.tertiary)
        .frame(maxWidth: .infinity, alignment: .center)
        .multilineTextAlignment(.center)
    }
}

/// A message's words, with the note a phone-shortened one carries. The Topic screen draws its rows with it too.
struct ChatMessageText: View {
    let text: String
    let kind: ChatReplyPresentation.Kind

    var body: some View {
        let message = ChatReplyPresentation(text, kind: kind)
        VStack(alignment: .leading, spacing: 8) {
            Text(message.text)
                .font(.body)
                // P141: Isoform's words are the primary text color; blue is for links and actions only.
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
            ChatTruncationNote(note: message.truncationNote)
        }
    }
}

private struct ChatTruncationNote: View {
    let note: String?

    var body: some View {
        if let note {
            Text(note)
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("chat.message.truncation")
        }
    }
}

/// The Mac caps tool summaries at 240 UTF-8 bytes and other text at 4096, backing up at most three
/// bytes to a scalar boundary.
struct ChatReplyPresentation {
    enum Kind: CaseIterable {
        case reply, message, permissionPrompt, systemMessage, error, toolSummary, toolOutput

        var wireByteLimit: Int { self == .toolSummary ? 240 : 4096 }

        var truncationNote: String {
            switch self {
            case .reply:
                return "This reply was shortened for this phone. Read the full reply in Isoform on your computer."
            case .message:
                return "This message was shortened for this phone. Read the full message in Isoform on your computer."
            case .permissionPrompt:
                return "This permission request was shortened for this phone. Read the full request in Isoform on your computer."
            case .systemMessage:
                return "This system message was shortened for this phone. Read the full message in Isoform on your computer."
            case .error:
                return "This error was shortened for this phone. Read the full error in Isoform on your computer."
            case .toolSummary:
                return "This tool summary was shortened for this phone. Read the full details in Isoform on your computer."
            case .toolOutput:
                return "This tool output was shortened for this phone. Read the full output in Isoform on your computer."
            }
        }
    }

    let text: String
    let truncationNote: String?

    init(_ text: String, kind: Kind = .reply) {
        let marker = " [truncated]"
        if ((kind.wireByteLimit - 3)...kind.wireByteLimit).contains(text.utf8.count), text.hasSuffix(marker) {
            self.text = String(text.dropLast(marker.count))
            truncationNote = kind.truncationNote
        } else {
            self.text = text
            truncationNote = nil
        }
    }
}

/// Small pill buttons for the composer: the Send button is filled, Stop is outlined.
private struct ComposerButtonStyle: ButtonStyle {
    let prominent: Bool
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body.weight(.semibold))
            .padding(.horizontal, 16)
            .frame(minHeight: 44)
            .foregroundStyle(prominent ? YansuTheme.onSolid : YansuTheme.ink)
            .background(prominent ? YansuTheme.solid : YansuTheme.surface, in: Capsule())
            .overlay {
                if !prominent {
                    Capsule().strokeBorder(YansuTheme.border, lineWidth: 1)
                }
            }
            .opacity(!isEnabled ? 0.4 : configuration.isPressed ? 0.75 : 1)
    }
}

/// Lets the permission card choose between the two button styles with one type.
private struct AnyButtonStyle: ButtonStyle {
    private let make: (ButtonStyleConfiguration) -> AnyView

    init<Style: ButtonStyle>(_ style: Style) {
        make = { configuration in AnyView(style.makeBody(configuration: configuration)) }
    }

    func makeBody(configuration: Configuration) -> some View {
        make(configuration)
    }
}

/// More, under the thread line: why the message waits and what to do.
private struct ChatAwaySheet: View {
    let cause: ChatAwayCause
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(String(localized: "Waiting for your computer"))
                .font(.headline)
                .foregroundStyle(YansuTheme.ink)
            Text(cause.shortLine)
                .foregroundStyle(YansuTheme.ink)
            Text(cause.directions)
                .foregroundStyle(YansuTheme.secondary)
            Text(String(localized: "Your messages wait on this phone, in order, as long as it takes. Nothing expires. Undo takes one back and puts its words in the box."))
                .foregroundStyle(YansuTheme.secondary)
            Spacer(minLength: 0)
            Button(String(localized: "OK")) { dismiss() }
                .buttonStyle(.borderedProminent)
                .frame(maxWidth: .infinity)
                .accessibilityIdentifier("chat.away.sheet.ok")
        }
        .font(.subheadline)
        .padding(24)
        .accessibilityIdentifier("chat.away.sheet")
    }
}
