import SwiftUI

/// One task, one view, from anywhere (Phone Threads §1): the title, a status line, the outcome when it is done,
/// where it came from, Isoform's messages and your replies in time order, the cards that do the work, and a reply
/// box whose words stay in this task.
struct TaskThreadView: View {
    let task: MobileTask
    @ObservedObject var tasks: TasksStore
    var source: TaskThreadSource = .tasks
    var wide = false
    var onClose: (() -> Void)?
    var foldX: CGFloat?
    var windowWidth: CGFloat = 0

    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var environment: AppEnvironment
    @Environment(\.dismiss) private var dismiss
    @Environment(\.locale) private var locale
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var reply = ""
    @State private var sending = false
    @State private var confirmsStop = false
    @State private var renaming = false
    @State private var newTitle = ""
    @State private var showsMade = false
    @State private var sendHaptics = 0
    /// Send waits five seconds with Undo before anything goes (click rule v1.1.0).
    @State private var sendingSince: Date?
    @State private var sendHold: Task<Void, Never>?
    @State private var sendFailed = false
    @State private var pendingBody: String?
    /// This thread's mark while it is on screen; the tab bar steps away while any is.
    @State private var presence = UUID()
    @FocusState private var replyFocused: Bool

    private var detail: MobileTaskDetail? {
        guard tasks.detailLoadState.taskID == task.id,
              case .loaded(let detail) = tasks.detailLoadState else { return nil }
        return detail
    }

    /// The freshest copy of the task: the thread once it loads, the list row before that.
    private var current: MobileTask {
        detail?.task ?? tasks.page?.tasks.first(where: { $0.id == task.id }) ?? task
    }

    private var actions: [String] {
        if let actions = detail?.actions { return actions }
        // An older computer offers only Stop, as before.
        return canStopLegacy ? ["stop"] : []
    }

    private var canStopLegacy: Bool {
        tasks.canStop && ["active", "in_progress", "blocked", "needs_review"].contains(current.status)
    }

    private var files: [MobileTaskEvent] { detail?.events.filter { $0.file != nil } ?? [] }

    /// A brief or routine from Scheduled: its thread is its time; nothing to load or reply to yet.
    private var isScheduled: Bool { task.group == "scheduled" && task.source != "list" }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    if wide { wideHeader }
                    header
                    content
                    Color.clear.frame(height: 1).id("thread.end")
                }
                .padding(.horizontal, YansuTheme.gutter + 4)
                .padding(.top, wide ? 20 : 4)
                .padding(.bottom, 24)
                .frame(maxWidth: wide ? 680 : .infinity, alignment: .leading)
                .frame(maxWidth: .infinity, alignment: wide ? .center : .leading)
            }
            .scrollDismissesKeyboard(.interactively)
            .accessibilityIdentifier("tasks.thread")
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if !isScheduled { replyBox(proxy) }
            }
            .onChange(of: detail?.events.count) { _, _ in
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) { proxy.scrollTo("thread.end", anchor: .bottom) }
            }
        }
        .background(YansuTheme.background.ignoresSafeArea())
        .navigationTitle("")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .toolbar {
            if !wide && !isScheduled {
                ToolbarItem(placement: .topBarTrailing) { moreMenu }
            }
        }
        .confirmationDialog(String(localized: "Stop this task? Isoform stops working on it on your computer."),
                            isPresented: $confirmsStop, titleVisibility: .visible) {
            Button(String(localized: "Stop"), role: .destructive) {
                Task { if await tasks.stop(id: task.id) { close() } }
            }
            Button(String(localized: "Cancel"), role: .cancel) {}
        }
        .alert(String(localized: "Rename"), isPresented: $renaming) {
            TextField(String(localized: "Title"), text: $newTitle)
            Button(String(localized: "Cancel"), role: .cancel) {}
            Button(String(localized: "Save")) {
                let title = newTitle.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !title.isEmpty else { return }
                Task { await tasks.act(id: task.id, action: "rename", title: title) }
            }
        }
        .sheet(isPresented: $showsMade) { madeSheet }
        .sensoryFeedback(.impact(weight: .light), trigger: sendHaptics)
        .onAppear { if !wide { router.openThreads.insert(presence) } }
        .onDisappear {
            router.openThreads.remove(presence)
            // Leaving during the five seconds does not cancel the send; it goes now.
            if sendingSince != nil, let hold = sendHold {
                hold.cancel()
                let id = task.id
                let tasks = tasks
                let body = pendingBody
                Task { @MainActor in _ = await tasks.act(id: id, action: "send", text: body) }
            }
        }
        .task(id: "\(task.id):\(tasks.detailResetGeneration)") {
            guard !isScheduled else { return }
            tasks.markOpened(current)
            _ = await tasks.loadDetail(id: task.id)
        }
        .task { await chat.watchForEvents() }
        .onReceive(chat.$lastTopicTurn) { event in
            guard let event else { return }
            Task { await tasks.receiveTurn(event, for: task.id) }
        }
    }

    // MARK: Top

    private var wideHeader: some View {
        HStack {
            DuoFoldSafeControl(foldX: foldX, windowWidth: windowWidth, width: 96, height: 40) {
                Button { close() } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "chevron.left").font(.body.weight(.semibold))
                        Text(source.backTitle)
                    }
                }
                .foregroundStyle(YansuTheme.accent)
                .accessibilityIdentifier("tasks.thread.back")
            }
            Spacer()
            moreMenu
        }
        .padding(.bottom, 12)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(threadTitle)
                .font(.system(size: 25, weight: .semibold))
                .tracking(-0.4)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("tasks.thread.title")
            statusLine
            if let outcome = TaskThreadCopy.outcome(current.outcome, locale: locale) {
                outcomeBox(outcome, byYou: current.outcome?.byYou == true)
                    .padding(.top, 8)
            }
        }
    }

    private var threadTitle: String { current.title }

    private var statusLine: some View {
        let status = TaskThreadCopy.status(task: current, detail: detail, locale: locale)
        let tone = TaskThreadCopy.tone(current)
        return HStack(spacing: 7) {
            Group {
                switch tone {
                case .needsYou:
                    Circle().fill(YansuTheme.warning).frame(width: 9, height: 9)
                case .running:
                    TaskProgressRing(progress: nil, reduceMotion: reduceMotion).frame(width: 15, height: 15)
                case .done:
                    Image(systemName: "checkmark.circle.fill").foregroundStyle(YansuTheme.tertiary)
                case .scheduled:
                    Image(systemName: "clock").foregroundStyle(YansuTheme.tertiary)
                }
            }
            .frame(width: 17, height: 17)
            Text(status.label)
                .fontWeight(.semibold)
                .foregroundStyle(tone == .needsYou ? YansuTheme.warning : tone == .running ? YansuTheme.accent : YansuTheme.secondary)
            if let line = status.detail {
                Text("· " + line).foregroundStyle(YansuTheme.secondary)
            }
        }
        .font(.subheadline)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("tasks.thread.status")
    }

    private func outcomeBox(_ text: String, byYou: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: "checkmark.circle.fill")
                .foregroundStyle(byYou ? YansuTheme.tertiary : YansuTheme.accent)
            Text(text)
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .font(.body)
        .padding(.horizontal, 16)
        .padding(.vertical, 13)
        .background(YansuTheme.surfaceWarm, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("tasks.thread.outcome")
    }

    // MARK: Body

    @ViewBuilder
    private var content: some View {
        if isScheduled {
            EmptyView()
        } else {
            threadContent
        }
    }

    @ViewBuilder
    private var threadContent: some View {
        if let origin = TaskThreadCopy.origin(detail?.origin, locale: locale) {
            Text(origin)
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.top, 22)
                .accessibilityIdentifier("tasks.thread.origin")
        }
        if let detail {
            VStack(alignment: .leading, spacing: 14) {
                ForEach(detail.events) { event in
                    eventView(event)
                        .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                }
                if showsSteps(detail) { stepsCard(detail) }
                if let draft = detail.draft { draftCard(draft) }
            }
            .padding(.top, 18)
            .animation(reduceMotion ? .easeOut(duration: 0.2) : .spring(duration: 0.4), value: detail.events)
        } else if tasks.detailLoadState.taskID == task.id, case .failed(_, let failure) = tasks.detailLoadState {
            VStack(alignment: .leading, spacing: 12) {
                NoticeBanner(message: failure.message)
                Button(String(localized: "Retry")) { Task { _ = await tasks.loadDetail(id: task.id) } }
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .accessibilityIdentifier("tasks.detail.retry")
            }
            .padding(.top, 22)
            .accessibilityIdentifier("tasks.detail.failure")
        } else {
            placeholder
        }
        if let notice = tasks.threadNotice {
            NoticeBanner(message: notice, dismiss: { tasks.threadNotice = nil })
                .padding(.top, 14)
                .accessibilityIdentifier("tasks.thread.notice")
        }
        if tasks.stopFailureID == task.id {
            HStack {
                Text(String(localized: "Didn't stop."))
                Button(String(localized: "Try again")) {
                    Task { if await tasks.stop(id: task.id) { close() } }
                }
                .fontWeight(.semibold)
            }
            .font(.footnote)
            .foregroundStyle(YansuTheme.warning)
            .padding(.top, 14)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("tasks.thread.stopFailure")
        }
    }

    /// Gray shapes of a thread while it loads: never a spinner on a tap.
    private var placeholder: some View {
        VStack(alignment: .leading, spacing: 14) {
            RoundedRectangle(cornerRadius: 6).fill(YansuTheme.chip).frame(width: 220, height: 12)
                .frame(maxWidth: .infinity)
            RoundedRectangle(cornerRadius: 22, style: .continuous).fill(YansuTheme.surface)
                .frame(height: 96).padding(.trailing, 60)
            RoundedRectangle(cornerRadius: 22, style: .continuous).fill(YansuTheme.surface)
                .frame(height: 56).padding(.trailing, 110)
        }
        .padding(.top, 22)
        .accessibilityHidden(true)
    }

    @ViewBuilder
    private func eventView(_ event: MobileTaskEvent) -> some View {
        switch event.kind {
        case "you":
            youBubble(event)
        case "line":
            Text(event.text ?? "")
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .accessibilityIdentifier("tasks.thread.line")
        case "file":
            if let file = event.file { fileChip(file) }
        default:
            isoformBubble(event)
        }
    }

    private func isoformBubble(_ event: MobileTaskEvent) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .bottom, spacing: 0) {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(Array(paragraphs(event.text).enumerated()), id: \.offset) { _, paragraph in
                        Text(paragraph)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .font(.body)
                .foregroundStyle(YansuTheme.ink)
                .textSelection(.enabled)
                .padding(.horizontal, 15)
                .padding(.vertical, 13)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(YansuTheme.border, lineWidth: 1)
                }
                Spacer(minLength: 40)
            }
            if let time = eventTime(event) {
                Text(time).font(.caption).foregroundStyle(YansuTheme.secondary).padding(.leading, 6)
            }
            let choices = event.openChoices
            if !choices.isEmpty {
                choiceButtons(event, choices)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tasks.thread.isoform")
    }

    private func choiceButtons(_ event: MobileTaskEvent, _ choices: [MobileTaskChoice]) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 10) { choiceRow(event, choices) }
            VStack(alignment: .leading, spacing: 10) { choiceRow(event, choices) }
        }
        .padding(.top, 4)
    }

    @ViewBuilder
    private func choiceRow(_ event: MobileTaskEvent, _ choices: [MobileTaskChoice]) -> some View {
        ForEach(choices) { choice in
            Button(choice.label) {
                sendHaptics += 1
                Task { await tasks.act(id: task.id, action: "choose", eventID: event.id, choiceID: choice.id) }
            }
            .buttonStyle(TaskChoiceButtonStyle())
            .disabled(detail?.hasThread != true)
            .accessibilityIdentifier("tasks.thread.choice.\(choice.id)")
        }
    }

    private func youBubble(_ event: MobileTaskEvent) -> some View {
        VStack(alignment: .trailing, spacing: 6) {
            HStack(spacing: 0) {
                Spacer(minLength: 56)
                Text(event.text ?? "")
                    .font(.body)
                    .foregroundStyle(YansuTheme.onSolid)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .padding(.horizontal, 15)
                    .padding(.vertical, 12)
                    .background(YansuTheme.solid,
                                in: UnevenRoundedRectangle(topLeadingRadius: 20, bottomLeadingRadius: 20,
                                                           bottomTrailingRadius: 6, topTrailingRadius: 20,
                                                           style: .continuous))
                    .opacity(event.id.hasPrefix("pending") ? 0.75 : 1)
            }
            if let time = eventTime(event) {
                Text(time).font(.caption).foregroundStyle(YansuTheme.secondary).padding(.trailing, 6)
            }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("tasks.thread.you")
    }

    private func fileChip(_ file: MobileTaskFile) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "doc").foregroundStyle(YansuTheme.accent)
            Text(file.name).foregroundStyle(YansuTheme.ink).lineLimit(1)
        }
        .font(.subheadline.weight(.medium))
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay { RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(YansuTheme.border, lineWidth: 1) }
        .accessibilityIdentifier("tasks.thread.file")
    }

    private func showsSteps(_ detail: MobileTaskDetail) -> Bool {
        !detail.steps.isEmpty && ["in_progress", "needs_review", "blocked"].contains(current.status)
    }

    private func stepsCard(_ detail: MobileTaskDetail) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 18) {
                ForEach(Array(detail.steps.enumerated()), id: \.offset) { _, step in
                    HStack(alignment: .firstTextBaseline, spacing: 13) {
                        stepMark(step.state)
                            .frame(width: 22, height: 22)
                            .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 6 }
                        Text(step.text)
                            .font(step.state == "current" ? .body.weight(.semibold) : .body)
                            .foregroundStyle(step.state == "pending" ? YansuTheme.secondary : YansuTheme.ink)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            .padding(16)
            if current.status == "in_progress" {
                Divider().overlay(YansuTheme.border)
                VStack(alignment: .leading, spacing: 3) {
                    Text(String(localized: "When it’s done"))
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(YansuTheme.ink)
                    Text(String(localized: "You review the result. Nothing is sent or merged without you."))
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(16)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .overlay { RoundedRectangle(cornerRadius: 22, style: .continuous).strokeBorder(YansuTheme.border, lineWidth: 1) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tasks.detail.steps")
    }

    @ViewBuilder
    private func stepMark(_ state: String) -> some View {
        switch state {
        case "done":
            Image(systemName: "checkmark.circle.fill")
                .font(.title3)
                .foregroundStyle(YansuTheme.onSolid, YansuTheme.solid)
        case "current":
            TaskProgressRing(progress: nil, reduceMotion: reduceMotion).frame(width: 19, height: 19)
        default:
            Circle()
                .stroke(YansuTheme.faint, style: StrokeStyle(lineWidth: 1.5, dash: [2.5, 2.5]))
                .frame(width: 19, height: 19)
        }
    }

    private func draftCard(_ draft: MobileTaskDraft) -> some View {
        TaskDraftCard(draft: draft, person: draft.recipients?.to.first ?? draft.to, pendingSince: sendingSince,
                      failed: sendFailed, canSend: detail?.actions?.contains("send") == true && sendingSince == nil,
                      send: { body in holdSend(body) }, undo: undoSend,
                      notNow: detail?.actions?.contains("not_now") == true ? { perform("not_now") } : nil)
    }

    private func holdSend(_ body: String?) {
        sendHaptics += 1
        sendFailed = false
        pendingBody = body
        withAnimation { sendingSince = Date() }
        sendHold = Task { @MainActor in
            do { try await Task.sleep(for: .seconds(5)) } catch { return }
            let sent = await tasks.act(id: task.id, action: "send", text: body)
            withAnimation {
                sendingSince = nil
                sendFailed = !sent
            }
        }
    }

    private func undoSend() {
        sendHold?.cancel()
        sendHold = nil
        withAnimation { sendingSince = nil }
    }

    // MARK: Reply and menu

    private func replyBox(_ proxy: ScrollViewProxy) -> some View {
        let sendable = !reply.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !sending
        return HStack(alignment: .bottom, spacing: 8) {
            TextField(String(localized: "Reply about this task"), text: $reply, axis: .vertical)
                .font(.body)
                .lineLimit(1...5)
                .focused($replyFocused)
                .padding(.leading, 18)
                .padding(.vertical, 13)
                .accessibilityIdentifier("tasks.thread.reply")
            HoldToTalk(text: $reply)
                .padding(.bottom, 4)
            if sendable {
                Button { send(proxy) } label: {
                    Image(systemName: "arrow.up")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(YansuTheme.onSolid)
                        .frame(width: 36, height: 36)
                        .background(YansuTheme.solid, in: Circle())
                }
                .buttonStyle(.plain)
                .padding(.trailing, 6)
                .padding(.bottom, 5)
                .transition(.scale.combined(with: .opacity))
                .accessibilityLabel(String(localized: "Send"))
                .accessibilityIdentifier("tasks.thread.send")
            }
        }
        .background(YansuTheme.composer, in: Capsule())
        .overlay { Capsule().strokeBorder(YansuTheme.composerBorder, lineWidth: 1) }
        .shadow(color: .black.opacity(0.05), radius: 10, y: 3)
        .animation(reduceMotion ? nil : .spring(duration: 0.3), value: sendable)
        .padding(.horizontal, YansuTheme.gutter)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .frame(maxWidth: wide ? 680 : .infinity)
        .frame(maxWidth: .infinity)
        .background {
            LinearGradient(colors: [YansuTheme.background.opacity(0), YansuTheme.background],
                           startPoint: .top, endPoint: .center)
                .ignoresSafeArea()
        }
    }

    private func send(_ proxy: ScrollViewProxy) {
        let text = reply
        reply = ""
        sending = true
        sendHaptics += 1
        Task {
            if !(await tasks.reply(id: task.id, text: text)) {
                reply = text
            }
            sending = false
        }
    }

    private var moreMenu: some View {
        Menu {
            ForEach(actions, id: \.self) { action in
                if let title = TaskThreadCopy.menuTitle(action) {
                    Button(role: action == "stop" ? .destructive : nil) { perform(action) } label: {
                        Label(title, systemImage: TaskThreadCopy.menuIcon(action))
                    }
                    .accessibilityIdentifier("tasks.thread.menu.\(action)")
                }
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.body.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
                .frame(width: 36, height: 36)
                .contentShape(Circle())
        }
        .disabled(actions.isEmpty)
        .accessibilityLabel(String(localized: "More"))
        .accessibilityIdentifier("tasks.thread.more")
    }

    private func perform(_ action: String) {
        switch action {
        case "stop":
            confirmsStop = true
        case "rename":
            newTitle = current.title
            renaming = true
        case "made":
            showsMade = true
        case "mark_done", "not_now":
            Task {
                if await tasks.act(id: task.id, action: action), action == "not_now" { close() }
            }
        default:
            break
        }
    }

    private var madeSheet: some View {
        NavigationStack {
            List(files) { event in
                if let file = event.file {
                    Label(file.name, systemImage: "doc")
                        .foregroundStyle(YansuTheme.ink)
                }
            }
            .navigationTitle(String(localized: "What this task made"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(String(localized: "Done")) { showsMade = false }
                }
            }
        }
        .presentationDetents([.medium])
    }

    private func close() {
        onClose?()
        dismiss()
    }

    private func eventTime(_ event: MobileTaskEvent) -> String? {
        guard !event.id.hasPrefix("pending"), let date = event.date else { return nil }
        return TaskThreadCopy.when(date, locale: locale)
    }

    private func paragraphs(_ text: String?) -> [String] {
        (text ?? "").components(separatedBy: "\n\n")
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
    }
}

/// An answer Isoform offers as a button. Tapping it posts the answer as your message.
struct TaskChoiceButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(YansuTheme.accent)
            .padding(.horizontal, 16)
            .frame(minHeight: 40)
            .background(YansuTheme.surface, in: Capsule())
            .overlay { Capsule().strokeBorder(YansuTheme.accent.opacity(0.45), lineWidth: 1) }
            .opacity(!isEnabled ? 0.45 : configuration.isPressed ? 0.7 : 1)
    }
}
