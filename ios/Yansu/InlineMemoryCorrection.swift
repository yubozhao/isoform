import SwiftUI

/// Scrolls the scroll view that holds an inline correction to the element with the given id. The scroll view sets it.
private struct CorrectionRevealKey: EnvironmentKey {
    static let defaultValue: ((String) -> Void)? = nil
}

extension EnvironmentValues {
    var correctionReveal: ((String) -> Void)? {
        get { self[CorrectionRevealKey.self] }
        set { self[CorrectionRevealKey.self] = newValue }
    }
}

/// Keeps a correction beside the learned item until it is sent or cancelled.
struct InlineMemoryCorrection: View {
    /// Where the scroll view puts the card: a little above the middle, so the field, Send and Cancel sit above the
    /// keyboard and its input bar.
    static let revealAnchor = UnitPoint(x: 0.5, y: 0.6)

    /// The card's own id, from its item, so a reveal scrolls to this card and no other.
    let anchorID: String
    let cancel: () -> Void
    let submit: (String) async -> Bool
    @EnvironmentObject private var router: NotificationRouter
    @State private var reason: String
    @State private var sending = false
    @State private var failed: Bool
    @FocusState private var focused: Bool
    @Environment(\.correctionReveal) private var reveal

    init(anchor: String, initialReason: String = "", initiallyFailed: Bool = false,
         cancel: @escaping () -> Void, submit: @escaping (String) async -> Bool) {
        self.anchorID = anchor
        self.cancel = cancel
        self.submit = submit
        _reason = State(initialValue: initialReason)
        _failed = State(initialValue: initiallyFailed)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            // The computer's prompt, and a blank answer is allowed: it means "stop treating it as confirmed".
            TextField(String(localized: "What's off? You can leave it blank."), text: $reason,
                      prompt: Text(String(localized: "What's off? You can leave it blank.")).foregroundColor(YansuTheme.secondary),
                      axis: .vertical)
                .focused($focused)
                .padding(10)
                .background(YansuTheme.composer, in: RoundedRectangle(cornerRadius: 10))
                .overlay { RoundedRectangle(cornerRadius: 10).strokeBorder(YansuTheme.composerBorder) }
                .accessibilityIdentifier("brief.correction.field")
            if failed {
                HStack(spacing: 6) {
                    Text(String(localized: "Couldn't save your answer."))
                        .accessibilityIdentifier("brief.correction.error")
                    Button(String(localized: "Try again")) { sendCorrection() }
                        .fontWeight(.semibold)
                        .foregroundStyle(YansuTheme.accent)
                        .disabled(sending)
                        .accessibilityIdentifier("brief.correction.retry")
                }
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
            }
            HStack(spacing: 8) {
                // One word, so it never wraps beside Cancel (P150).
                Button("Send") { sendCorrection() }
                    .buttonStyle(YansuPrimaryButtonStyle())
                    .disabled(sending)
                    .accessibilityIdentifier("brief.correction.send")
                    .fixedSize()
                Button("Cancel", action: cancel)
                    .buttonStyle(YansuSecondaryButtonStyle())
                    .disabled(sending)
                    .accessibilityIdentifier("brief.correction.cancel")
            }
        }
        .id(anchorID)
        .onAppear { router.isCorrecting = true; focused = true }
        .onDisappear { router.isCorrecting = false }
        .onChange(of: focused) { _, isFocused in if isFocused { revealAboveKeyboard() } }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillChangeFrameNotification)) { _ in
            if focused { revealAboveKeyboard() }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.correction.inline")
    }

    private func sendCorrection() {
        sending = true
        failed = false
        Task {
            // The person's words, trimmed at the ends; a blank answer is sent as blank (the computer saves nothing).
            if await submit(FirstReadCorrectionRules.words(from: reason)) { cancel() } else { failed = true }
            sending = false
        }
    }

    /// The field took focus, or the keyboard changed size. The keyboard finishes rising after focus, so the visible
    /// area changes again: reveal once now and once it has settled, as the first-read card does.
    private func revealAboveKeyboard() {
        guard let reveal else { return }
        DispatchQueue.main.async { reveal(anchorID) }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) { reveal(anchorID) }
    }
}
