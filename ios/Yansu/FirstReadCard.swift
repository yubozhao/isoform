import SwiftUI

/// The checked portrait and its explicit answers (Phone Threads section 6, and the first read correction P163).
struct FirstReadCard: View {
    let item: ChatProactiveView
    let chatID: String
    let onPortraitChanged: () -> Void
    let onCorrectionSent: () -> Void
    /// The field took focus, or the answer failed while it had focus: Chat keeps the card above the composer (P163).
    var onCorrectionFocus: () -> Void = {}
    /// The scroll target for the portrait card, so Chat can settle it above the composer.
    static let portraitAnchor = "firstRead.portraitAnchor"
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var router: NotificationRouter
    @Environment(\.locale) private var locale
    @State private var confirmed = false
    @State private var confirmedAt: Date?
    @State private var correcting = false
    @State private var correction = ""
    @State private var phase: FirstReadCorrectionPhase = .none
    @State private var answeredAt: Date?
    @State private var waitingSince: Date?
    @State private var sendTask: Task<Void, Never>?
    @State private var busy = false
    @State private var undoRequested = false
    @State private var error = false
    @State private var retry: (() -> Void)?
    @State private var timerID = UUID()
    @FocusState private var fieldFocused: Bool

    private var language: String { ChatPresentationLanguage(locale: locale).code }
    private var helps: [String] { item.localized[language]?.helps ?? item.helps }
    private var showUndo: Bool { confirmedAt.map { Date().timeIntervalSince($0) < 5 } ?? false }
    /// The journal entries for this card's correction are keyed by this target.
    private var target: String { "first-read/\(item.momentID)" }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            YansuCard {
                VStack(alignment: .leading, spacing: 12) {
                    label(String(localized: "What I see so far"))
                        .accessibilityIdentifier("firstRead.label")
                    Text(verbatim: item.sentences.joined(separator: " "))
                        .font(.body)
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("firstRead.sentences")
                    if confirmed {
                        VStack(alignment: .leading, spacing: 7) {
                            HStack(spacing: 7) {
                                Image(systemName: "checkmark").accessibilityHidden(true)
                                Text(String(localized: "You said that's you"))
                                    .accessibilityIdentifier("firstRead.confirmed")
                                if showUndo || (busy && !undoRequested) {
                                    Text("·").accessibilityHidden(true)
                                    Button(String(localized: "Undo")) { undo() }
                                        .accessibilityIdentifier("firstRead.undo")
                                }
                            }
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(YansuTheme.accent)
                            Text(String(localized: "Your portrait in About you starts from this."))
                                .font(.subheadline)
                                .foregroundStyle(YansuTheme.secondary)
                        }
                    } else if phase.showsAnswer {
                        correctionAnswer
                    } else if correcting {
                        VStack(alignment: .leading, spacing: 10) {
                            TextField(String(localized: "What's off? You can leave it blank."), text: $correction,
                                      prompt: Text(String(localized: "What's off? You can leave it blank.")).foregroundColor(YansuTheme.secondary),
                                      axis: .vertical)
                                .textFieldStyle(.plain)
                                .yansuFieldBox()
                                .focused($fieldFocused)
                                .accessibilityIdentifier("firstRead.correction")
                            Button(String(localized: "Send")) { sendCorrection() }
                                .buttonStyle(FirstReadButtonStyle(prominent: true))
                                .disabled(busy)
                                .accessibilityIdentifier("firstRead.send")
                        }
                    } else {
                        HStack(spacing: 8) {
                            Button(String(localized: "That's me")) { confirm() }
                                .buttonStyle(FirstReadButtonStyle(prominent: false))
                                .disabled(busy)
                                .accessibilityIdentifier("firstRead.confirm")
                            Button(String(localized: "Not quite")) {
                                correcting = true
                                onPortraitChanged()
                            }
                                .buttonStyle(FirstReadButtonStyle(prominent: false))
                                .disabled(busy)
                                .accessibilityIdentifier("firstRead.notQuite")
                        }
                    }
                    if error {
                        HStack(spacing: 6) {
                            Text(String(localized: "Couldn't save your answer."))
                                .accessibilityIdentifier("firstRead.error")
                            Button(String(localized: "Try again")) { retry?() }
                                .fontWeight(.semibold)
                                .foregroundStyle(YansuTheme.accent)
                                .accessibilityIdentifier("firstRead.retry")
                        }
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                // With the field open, the card ends above the composer when Chat scrolls it into place.
                .padding(.bottom, correcting || error ? 16 : 0)
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("firstRead.portrait")
            .id(FirstReadCard.portraitAnchor)

            if helps.count >= 2 {
                YansuCard {
                    VStack(alignment: .leading, spacing: 12) {
                        label(String(localized: "How I'll help"))
                        ForEach(Array(helps.enumerated()), id: \.offset) { index, line in
                            HStack(alignment: .top, spacing: 10) {
                                // With fewer than three facts their positions no longer
                                // identify their sources, so use a neutral mark.
                                Image(systemName: helps.count == 3 ? ["envelope", "sun.max", "terminal"][min(index, 2)] : "circle")
                                    .foregroundStyle(YansuTheme.accent)
                                    .accessibilityHidden(true)
                                Text(verbatim: line)
                                    .foregroundStyle(YansuTheme.ink)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("firstRead.help")
            }
            if item.offerCount > 0 {
                YansuCard {
                    VStack(alignment: .leading, spacing: 10) {
                        label(String(localized: "Up next"))
                        Text(item.offerCount == 1
                             ? String(localized: "One thing I can take off your plate")
                             : String.localizedStringWithFormat(String(localized: "%d things I can take off your plate"), item.offerCount))
                            .font(.headline)
                            .foregroundStyle(YansuTheme.ink)
                        Text(String(localized: "In Tasks › Up next, each with what I saw. Nothing starts until you say Do it."))
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                        Button(String(localized: "See them")) { router.openUpNext() }
                            .buttonStyle(FirstReadButtonStyle(prominent: true))
                            .accessibilityIdentifier("firstRead.seeThem")
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("firstRead.offers")
            }
        }
        .onChange(of: fieldFocused) { _, focused in if focused { onCorrectionFocus() } }
        .onChange(of: error) { _, failed in if failed && fieldFocused { onCorrectionFocus() } }
        .onAppear {
            syncFromItem()
            Task { await restoreHeldCorrection() }
        }
        .onChange(of: item.state) { _, _ in if !busy { syncFromItem() } }
        .task(id: timerID) {
            guard let at = confirmedAt else { return }
            let delay = max(0, 5 - Date().timeIntervalSince(at))
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            confirmedAt = nil
        }
        .task(id: answeredAt) {
            guard let at = answeredAt else { return }
            let delay = max(0, FirstReadCorrectionRules.undoWindow - Date().timeIntervalSince(at))
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            answeredAt = nil
        }
        .task(id: waitingSince) {
            guard let since = waitingSince else { return }
            let delay = max(0, TaskSendWait.relayRetention - Date().timeIntervalSince(since))
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled, case .waiting(let words) = phase else { return }
            // The relay has let it go: nothing is sent, and the card asks the person again.
            sendTask?.cancel()
            removeJournaledCorrection()
            waitingSince = nil
            busy = false
            phase = .expired(words: words)
        }
    }

    private func label(_ value: String) -> some View {
        Text(value)
            .font(.caption.weight(.semibold))
            .foregroundStyle(YansuTheme.secondary)
            .textCase(.uppercase)
    }

    /// The words the person sent, then what follows them (P163). Shown only after Not quite and Send.
    @ViewBuilder private var correctionAnswer: some View {
        VStack(alignment: .leading, spacing: 7) {
            switch phase {
            case .waiting(let words):
                saidLine(words, undo: nil)
                HStack(spacing: 4) {
                    Text(String(localized: "Waits for your computer"))
                        .accessibilityIdentifier("firstRead.waiting")
                    Text(verbatim: "·").accessibilityHidden(true)
                    Button(String(localized: "Undo")) { withdrawWaiting() }
                        .fontWeight(.semibold)
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("firstRead.undo")
                }
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
            case .answered(let words):
                let open = FirstReadCorrectionRules.undoOpen(answeredAt: answeredAt, now: Date())
                if let words {
                    saidLine(words, undo: open ? { undoCorrection(words: words) } : nil)
                }
                Text(String(localized: words == "" ? "Your portrait in About you won't start from this."
                                                   : "Your portrait in About you starts from your words."))
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("firstRead.portraitLine")
            case .expired(let words):
                saidLine(words, undo: nil)
                HStack(spacing: 6) {
                    Text(String(localized: "This waited a day, so it wasn't sent."))
                    Button(String(localized: "Send again")) { resend(words: words) }
                        .fontWeight(.semibold)
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("firstRead.sendAgain")
                }
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
            case .none, .failed:
                EmptyView()
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("firstRead.correctionAnswer")
    }

    /// "You said: “words”" with an optional Undo after it. An empty answer reads as "not quite you".
    @ViewBuilder private func saidLine(_ words: String, undo: (() -> Void)?) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 4) {
            if words.isEmpty {
                Text(String(localized: "You said it's not quite you"))
                    .accessibilityIdentifier("firstRead.said")
            } else {
                Text(String(localized: "You said:"))
                Text(verbatim: "“\(words)”")
                    .accessibilityLabel(Text(verbatim: words))
                    .accessibilityIdentifier("firstRead.said")
            }
            if let undo {
                Text(verbatim: "·").accessibilityHidden(true)
                Button(String(localized: "Undo"), action: undo)
                    .fontWeight(.semibold)
                    .foregroundStyle(YansuTheme.accent)
                    .accessibilityIdentifier("firstRead.undo")
            }
        }
        .font(.subheadline.weight(.semibold))
        .foregroundStyle(YansuTheme.accent)
    }

    private func syncFromItem() {
        confirmed = item.state == "acted"
        if let millis = item.firstReadConfirmedAt, confirmed {
            confirmedAt = Date(timeIntervalSince1970: Double(millis) / 1000)
            timerID = UUID()
        }
        // The computer has the correction once the card says corrected. After a relaunch the words are not
        // kept on the phone, so only the portrait line shows.
        if item.state == "corrected" {
            switch phase {
            case .none:
                phase = .answered(words: nil)
            case .waiting(let words):
                phase = .answered(words: words)
                waitingSince = nil
                answeredAt = Date()
            default:
                break
            }
        }
    }

    private func answer(_ action: String, text: String = "") async -> JournaledOutcome<FirstReadAnswer> {
        let writer = JournaledWriter(journal: environment.journal,
                                     makeKey: { UUID().uuidString.lowercased() }, now: { Date() })
        return await writer.send(.chatProactiveAction, target: target, choice: action,
                                 fingerprint: WriteFingerprint.make([item.momentID, action, text]),
                                 correctionWords: action == "correct_portrait" ? text : nil) { key in
            try await environment.firstReadHub.answerFirstRead(momentID: item.momentID, action: action, text: text, idempotencyKey: key)
        }
    }

    private func confirm() {
        guard !busy else { return }
        busy = true
        error = false
        retry = nil
        undoRequested = false
        confirmed = true
        confirmedAt = Date()
        timerID = UUID()
        onPortraitChanged()
        Task {
            guard case .sent(let result) = await answer("confirm_portrait") else {
                confirmed = false
                error = true
                retry = { confirm() }
                onPortraitChanged()
                busy = false
                return
            }
            if undoRequested {
                let undone = await answer("undo_portrait")
                if case .sent = undone { confirmed = false } else { confirmed = true }
                confirmedAt = nil
                onPortraitChanged()
            } else {
                confirmedAt = result.confirmedAt.map { Date(timeIntervalSince1970: Double($0) / 1000) }
                timerID = UUID()
            }
            busy = false
            await chat.refreshAfterProactiveAction(chatID: chatID)
        }
    }

    private func undo() {
        if busy {
            undoRequested = true
            confirmed = false
            onPortraitChanged()
            return
        }
        busy = true
        error = false
        retry = { undo() }
        Task {
            if case .sent = await answer("undo_portrait") {
                confirmed = false
                confirmedAt = nil
                onPortraitChanged()
            } else {
                error = true
            }
            busy = false
            await chat.refreshAfterProactiveAction(chatID: chatID)
        }
    }

    private func sendCorrection() {
        // A blank Not quite is a valid answer (P163 v1.0.1): the computer saves nothing.
        guard !busy else { return }
        resend(words: FirstReadCorrectionRules.words(from: correction))
    }

    /// Sends the correction words to the computer and moves the card to the phase the answer gives.
    private func resend(words: String) {
        guard !busy else { return }
        busy = true
        error = false
        retry = nil
        correcting = false
        phase = .waiting(words: words)
        waitingSince = Date()
        sendTask = Task { await runCorrection(words: words) }
    }

    private func runCorrection(words: String) async {
        let outcome = await answer("correct_portrait", text: words)
        guard !Task.isCancelled else { return }
        busy = false
        guard let result = FirstReadCorrectionRules.result(outcome) else { return }
        waitingSince = nil
        phase = FirstReadCorrectionRules.phase(after: result, words: words)
        switch result {
        case .answered:
            answeredAt = Date()
            correction = ""
            onCorrectionSent()
        case .held:
            // The journal keeps the write, so it goes when the computer is back.
            break
        case .failed:
            // The field comes back with the words still in it.
            correction = words
            correcting = true
            error = true
            retry = { resend(words: words) }
        }
        await chat.refreshAfterProactiveAction(chatID: chatID)
    }

    /// Undo while the computer has not answered: nothing is sent, and the words go back into the field.
    private func withdrawWaiting() {
        guard case .waiting(let words) = phase else { return }
        sendTask?.cancel()
        removeJournaledCorrection()
        waitingSince = nil
        busy = false
        phase = .none
        correction = words
        correcting = true
        onPortraitChanged()
    }

    /// Undo after the computer answered: the words go back into the field, as the That's me undo does.
    private func undoCorrection(words: String) {
        guard !busy else { return }
        busy = true
        error = false
        retry = { undoCorrection(words: words) }
        Task {
            if case .sent = await answer("undo_portrait") {
                phase = .none
                answeredAt = nil
                correction = words
                correcting = true
                onPortraitChanged()
            } else {
                error = true
            }
            busy = false
            await chat.refreshAfterProactiveAction(chatID: chatID)
        }
    }

    /// A correction still held for the computer comes back with its words after a relaunch (P163).
    private func restoreHeldCorrection() async {
        guard phase == .none, !confirmed, item.state != "corrected" else { return }
        let entries = await environment.journal.entries()
        guard let restored = FirstReadCorrectionRules.restoredWaiting(from: entries, target: target, now: Date()) else { return }
        phase = restored.phase
        if case .expired = restored.phase {
            removeJournaledCorrection()
        } else {
            waitingSince = restored.sentAt
        }
    }

    private func removeJournaledCorrection() {
        let journal = environment.journal
        let target = target
        Task {
            let keys = await journal.entries().filter { $0.target == target }.map(\.key)
            await journal.remove(keys: keys)
        }
    }
}

private struct FirstReadButtonStyle: ButtonStyle {
    let prominent: Bool
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .padding(.horizontal, 16)
            .padding(.vertical, 9)
            .foregroundStyle(prominent ? YansuTheme.onSolid : YansuTheme.ink)
            .background(prominent ? YansuTheme.solid : YansuTheme.surface, in: Capsule())
            .overlay { Capsule().strokeBorder(prominent ? YansuTheme.solid : YansuTheme.border, lineWidth: 1) }
            .opacity(configuration.isPressed ? 0.7 : 1)
    }
}
