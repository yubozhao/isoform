import SwiftUI

/// The feedback a brief item offers in its own row (P152). The brief no longer repeats each item in a separate
/// Cards section; Useful and Not for me are the row's, with the same holds, undo and pending states the card had.
enum BriefItemAction: Equatable, Sendable {
    case useful
    case notForMe
}

/// The feedback chips and the held, pending or folded status of one brief item. A remembered item offers no
/// chips: its "Not quite" is the row's memory correction (BriefContentView), as before.
struct BriefItemActions: View {
    let card: FeedCardModel
    let canGiveFeedback: Bool
    /// Useful went with Chat (scope cut A6: "Not right is the one correction").
    var offersUseful = true
    @ObservedObject var feed: FeedStore
    let today: TodayStore
    let briefStore: BriefStore
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var holds = DecisionHoldCoordinator()
    @State private var held: Set<String> = []

    var body: some View {
        Group {
            if let hold = holds.holds.first(where: { $0.id == card.key }) {
                statusLine(hold.label) {
                    if holds.undo(hold.id) { held.remove(hold.id) }
                }
            } else if let pending = feed.pendingToday.first(where: { $0.todayFeedback?.cardKey == card.key }) {
                statusLine(String(localized: "Waits for your computer"),
                           undo: pending.todayFeedback?.dispatchedAt == nil ? { Task { await feed.undoToday(pending.key) } } : nil)
            } else if let choice = feed.choices[card.key], choice.action == .notRelevant {
                statusLine(choice.delivery == .pending ? String(localized: "Waits for your computer") : String(localized: "Fewer like this"))
            } else if !Self.actions(for: card, canGiveFeedback: canGiveFeedback, offersUseful: offersUseful).isEmpty {
                HStack(spacing: 8) {
                    if offersUseful {
                        Button(String(localized: "Useful")) { Task { await hold(.like) } }
                            .buttonStyle(YansuChipButtonStyle())
                            .disabled(feed.choices[card.key] != nil)
                            .accessibilityIdentifier("feed.like")
                    }
                    Button(String(localized: "Not for me")) { Task { await hold(.notRelevant) } }
                        .buttonStyle(YansuChipButtonStyle())
                        .disabled(feed.choices[card.key] != nil)
                        .accessibilityIdentifier("feed.notRight")
                }
            }
        }
        .onDisappear { Task { await holds.flushAll() } }
        .onChange(of: scenePhase) { _, phase in
            if DecisionHoldCoordinator.shouldFlush(when: phase) { Task { await holds.flushAll() } }
        }
    }

    /// The actions a row offers for its card: none without feedback permission, and none for a remembered item.
    static func actions(for card: FeedCardModel, canGiveFeedback: Bool, offersUseful: Bool = true) -> [BriefItemAction] {
        guard canGiveFeedback, card.memoryID == nil else { return [] }
        return offersUseful ? [.useful, .notForMe] : [.notForMe]
    }

    /// The feed card a brief item is drawn from, matched by its content (a one-time work item's chat link is removed
    /// on the card, so that form is tried too).
    static func card(for item: BriefItem, in cards: [FeedCardModel]) -> FeedCardModel? {
        if let match = cards.first(where: { $0.item == item }) { return match }
        var shown = item
        shown.url = nil
        return cards.first { $0.item == shown }
    }

    private func statusLine(_ label: String, undo: (() -> Void)? = nil) -> some View {
        HStack(spacing: 4) {
            Text(verbatim: label)
            if let undo {
                Text(verbatim: "·")
                Button("Undo", action: undo).accessibilityIdentifier("brief.feedback.undo")
            }
        }
        .font(.footnote)
        .foregroundStyle(YansuTheme.secondary)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.feedback.folded")
    }

    private func hold(_ action: FeedFeedbackAction) async {
        guard let approval = await feed.authorizeFeedback(action, for: card, reason: nil) else { return }
        if today.hubUnreachable || briefStore.hubOnline == false {
            let version = today.snapshot?.briefs.first { $0.id == card.briefID }?.bodyVersion
            _ = await feed.queueToday(approval, bodyVersion: version)
            return
        }
        held.insert(card.key)
        let label = action == .notRelevant ? String(localized: "Fewer like this") : String(localized: "Useful")
        holds.begin(.init(id: card.key, label: label,
                          detail: card.item.title ?? String(localized: "Untitled"))) {
            _ = await feed.commitFeedback(approval)
            held.remove(card.key)
        }
    }
}
