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
    /// The card's id, and where the scroll view puts it: a little above the middle, so the field, Send and Cancel
    /// sit above the keyboard and its input bar.
    static let anchorID = "memory.correction.anchor"
    static let revealAnchor = UnitPoint(x: 0.5, y: 0.6)

    let cancel: () -> Void
    let submit: (String) async -> Bool
    @EnvironmentObject private var router: NotificationRouter
    @State private var reason: String
    @State private var sending = false
    @State private var failed: Bool
    @FocusState private var focused: Bool
    @Environment(\.correctionReveal) private var reveal

    init(initialReason: String = "", initiallyFailed: Bool = false, cancel: @escaping () -> Void,
         submit: @escaping (String) async -> Bool) {
        self.cancel = cancel
        self.submit = submit
        _reason = State(initialValue: initialReason)
        _failed = State(initialValue: initiallyFailed)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("What is not right").font(.footnote.weight(.medium)).foregroundStyle(YansuTheme.secondary)
            TextField(String(localized: "What is not right"), text: $reason, axis: .vertical)
                .focused($focused)
                .padding(10)
                .background(YansuTheme.composer, in: RoundedRectangle(cornerRadius: 10))
                .overlay { RoundedRectangle(cornerRadius: 10).strokeBorder(YansuTheme.composerBorder) }
                .accessibilityIdentifier("brief.correction.field")
            if failed { Text("Didn't save. Try again.").font(.footnote).foregroundStyle(YansuTheme.secondary) }
            HStack(spacing: 8) {
                // One word, so it never wraps beside Cancel (P150).
                Button("Send") {
                    sending = true
                    failed = false
                    Task {
                        if await submit(reason) { cancel() } else { failed = true }
                        sending = false
                    }
                }
                .buttonStyle(YansuPrimaryButtonStyle())
                .disabled(sending || reason.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityIdentifier("brief.correction.send")
                .fixedSize()
                Button("Cancel", action: cancel)
                    .buttonStyle(YansuSecondaryButtonStyle())
                    .disabled(sending)
                    .accessibilityIdentifier("brief.correction.cancel")
            }
        }
        .id(Self.anchorID)
        .onAppear { router.isCorrecting = true; focused = true }
        .onDisappear { router.isCorrecting = false }
        .onChange(of: focused) { _, isFocused in if isFocused { revealAboveKeyboard() } }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillChangeFrameNotification)) { _ in
            if focused { revealAboveKeyboard() }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.correction.inline")
    }

    /// The field took focus, or the keyboard changed size. The keyboard finishes rising after focus, so the visible
    /// area changes again: reveal once now and once it has settled, as the first-read card does.
    private func revealAboveKeyboard() {
        guard let reveal else { return }
        DispatchQueue.main.async { reveal(Self.anchorID) }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.35) { reveal(Self.anchorID) }
    }
}
