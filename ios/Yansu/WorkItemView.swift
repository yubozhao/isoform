import SwiftUI

// The shared item (phone-map v1.0.0 section 4), used on Today, in the brief and in a conversation. Each answer is one
// tap; No can carry a reason, none is needed; an answered item is one line with Undo.

/// The open item: where it came from, what Isoform noticed, the work, what Yes does, and Yes, No, Not mine.
struct WorkItemCard: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    /// At accessibility sizes text wraps and never truncates (pass-access rule 7).
    private var wraps: Bool { dynamicTypeSize.isAccessibilitySize }
    let item: WorkItem
    /// "1 of 4" and the dots; nil hides them.
    var position: (index: Int, count: Int)? = nil
    var answer: (WorkAction) -> Void
    var edit: () -> Void
    var showWhy: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .center, spacing: 8) {
                Text(verbatim: item.firstLine)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .lineLimit(wraps ? nil : 2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("work.item.source")
                if item.kind != "claim" {
                    Button(String(localized: "work.edit"), action: edit)
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.accent)
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("work.item.edit")
                }
                Menu {
                    Button(String(localized: "work.menu.why"), action: showWhy)
                } label: {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(YansuTheme.secondary)
                        .frame(width: 32, height: 32)
                        .padding(.vertical, -8)
                }
                .accessibilityLabel(String(localized: "More options"))
            }
            VStack(alignment: .leading, spacing: 10) {
                Text(verbatim: item.noticed)
                    .font(.body.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                WorkBox(work: item.work)
                if let yes = item.yesLine {
                    Text(verbatim: yes)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .workItemAccessibility(item, answer: answer, edit: item.kind == "claim" ? nil : edit)
            .accessibilityIdentifier("work.item.noticed")
            if YourWork.asksUseful(item) {
                Text("work.usefulAsk").font(.footnote).foregroundStyle(YansuTheme.secondary)
                WorkWeightedRow(weights: [1, 1], spacing: 8) {
                    Button { answer(.useful) } label: {
                        Text("work.useful").font(.body.weight(.semibold)).frame(maxWidth: .infinity, minHeight: 46)
                            .foregroundStyle(YansuTheme.onSolid)
                            .background(YansuTheme.solid, in: RoundedRectangle(cornerRadius: 12))
                    }
                    .accessibilityIdentifier("work.useful")
                    Button { answer(.notUseful) } label: {
                        Text("work.notUseful").font(.body).frame(maxWidth: .infinity, minHeight: 46)
                            .foregroundStyle(YansuTheme.ink)
                            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 12))
                            .overlay { RoundedRectangle(cornerRadius: 12).stroke(YansuTheme.borderStrong) }
                    }
                    .accessibilityIdentifier("work.notUseful")
                }
                .buttonStyle(.plain)
            } else {
                WorkAnswerButtons(height: 46, answer: answer)
            }
            if let position, position.count > 1 {
                HStack {
                    Text(String.localizedStringWithFormat(String(localized: "work.position"), position.index + 1, position.count))
                        .accessibilityIdentifier("work.item.position")
                    Spacer()
                    HStack(spacing: 5) {
                        ForEach(0..<min(position.count, 10), id: \.self) { dot in
                            Circle()
                                .fill(dot == position.index ? YansuTheme.accent : YansuTheme.borderStrong)
                                .frame(width: 6, height: 6)
                        }
                    }
                    .accessibilityHidden(true)
                }
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
            }
        }
        .padding(16)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("work.item.\(item.id)")
    }
}

/// The grey box with the work: its title and text (work it did), or the title and what it would do (work it offers).
struct WorkBox: View {
    let work: WorkBody

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(verbatim: work.title)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
            if let text = work.text, !text.isEmpty {
                let line = [text, work.estimate].compactMap { $0 }.joined(separator: " ")
                Text(verbatim: line)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else if let estimate = work.estimate {
                Text(verbatim: estimate).font(.subheadline).foregroundStyle(YansuTheme.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 13)
        .padding(.vertical, 12)
        .background(YansuTheme.surfaceWarm, in: RoundedRectangle(cornerRadius: 12))
    }
}

/// Yes, No, Not mine: Yes filled and widest, No outlined, Not mine plain (board artboard 2).
struct WorkAnswerButtons: View {
    var height: CGFloat = 46
    var answer: (WorkAction) -> Void
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        // From the first accessibility size the answers stack full width, Yes first, 56 points each (pass-access rule 7).
        if dynamicTypeSize.isAccessibilitySize {
            VStack(spacing: 8) { buttons(height: 56) }.buttonStyle(.plain)
        } else {
            WorkWeightedRow(weights: [1.3, 1, 1], spacing: 8) { buttons(height: height) }.buttonStyle(.plain)
        }
    }

    @ViewBuilder
    private func buttons(height: CGFloat) -> some View {
            Button { answer(.yes) } label: {
                Text("work.yes").font(.body.weight(.semibold)).frame(maxWidth: .infinity, minHeight: height)
                    .foregroundStyle(YansuTheme.onSolid)
                    .background(YansuTheme.solid, in: RoundedRectangle(cornerRadius: 12))
            }
            .accessibilityIdentifier("work.yes")
            Button { answer(.no) } label: {
                Text("work.no").font(.body).frame(maxWidth: .infinity, minHeight: height)
                    .foregroundStyle(YansuTheme.ink)
                    .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 12))
                    .overlay { RoundedRectangle(cornerRadius: 12).stroke(YansuTheme.borderStrong) }
            }
            .accessibilityIdentifier("work.no")
            Button { answer(.notMine) } label: {
                Text("work.notMine").font(.body).frame(maxWidth: .infinity, minHeight: height)
                    .foregroundStyle(YansuTheme.secondary)
            }
            .accessibilityIdentifier("work.notMine")
    }
}

extension View {
    /// An item read whole by VoiceOver (where it came from, the sentence, the work, what Yes does) and answered with
    /// its actions (pass-access rule 9). The buttons stay for touch.
    func workItemAccessibility(_ item: WorkItem, answer: @escaping (WorkAction) -> Void, edit: (() -> Void)?) -> some View {
        let label = [item.firstLine, item.noticed, item.work.title, item.work.text, item.yesLine]
            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: ". ")
        return self
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text(verbatim: label))
            .accessibilityActions {
                if YourWork.asksUseful(item) {
                    Button(String(localized: "work.useful")) { answer(.useful) }
                    Button(String(localized: "work.notUseful")) { answer(.notUseful) }
                } else {
                    Button(String(localized: "work.yes")) { answer(.yes) }
                    Button(String(localized: "work.no")) { answer(.no) }
                    Button(String(localized: "work.notMine")) { answer(.notMine) }
                    if let edit { Button(String(localized: "work.edit"), action: edit) }
                }
            }
    }
}

/// After No: "No. Any reason? (optional)", the reasons as chips, Undo and Skip (board artboard 2, frame 4).
struct WorkReasonPicker: View {
    let item: WorkItem
    var pick: (WorkReason) -> Void
    var undo: () -> Void
    var skip: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(verbatim: item.work.title)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
            HStack(spacing: 6) {
                Image(systemName: "xmark").font(.caption.weight(.bold)).foregroundStyle(YansuTheme.secondary)
                Text("work.reasons.ask").font(.subheadline).foregroundStyle(YansuTheme.ink)
                Text("work.reasons.optional").font(.subheadline).foregroundStyle(YansuTheme.secondary)
            }
            WorkChipFlow(spacing: 8) {
                ForEach(WorkReason.choices(for: item), id: \.self) { reason in
                    Button { pick(reason) } label: {
                        Text(verbatim: reason.title)
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.ink)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 8)
                            .background(YansuTheme.chip, in: Capsule())
                            .overlay { Capsule().stroke(YansuTheme.chipBorder) }
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("work.reason.\(reason.rawValue)")
                }
            }
            HStack {
                Button(String(localized: "Undo"), action: undo).accessibilityIdentifier("work.reasons.undo")
                Spacer()
                Button(String(localized: "work.reasons.skip"), action: skip).accessibilityIdentifier("work.reasons.skip")
            }
            .font(.subheadline)
            .foregroundStyle(YansuTheme.accent)
            .buttonStyle(.plain)
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("work.reasons")
    }
}

/// One line per answer: a check, what happened, "waits for your computer" while it does, and Undo.
struct WorkDoneLine: View {
    let item: WorkItem
    let now: Date
    /// "Yes · not sent": Send in its row reopens the hand-off (phone-agent v1.0.0 rule 3).
    var send: (() -> Void)? = nil
    var undo: () -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: "checkmark")
                .font(.system(size: 10, weight: .bold))
                .foregroundStyle(YansuTheme.success)
                .frame(width: 20, height: 20)
                .background(YansuTheme.successWash, in: Circle())
            Group {
                if item.answer?.waits == true {
                    Text(verbatim: item.answer?.line ?? item.work.title)
                        + Text(verbatim: " · ").foregroundStyle(YansuTheme.secondary)
                        + Text("work.waits").foregroundStyle(YansuTheme.secondary)
                } else {
                    Text(verbatim: item.answer?.line ?? item.work.title)
                }
            }
            .font(.subheadline)
            .foregroundStyle(YansuTheme.ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            if let send {
                Button(String(localized: "work.send"), action: send)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("work.done.send")
            }
            if let answer = item.answer, YourWork.canUndo(answer, now: now) || answer.waits == true {
                Button(String(localized: "Undo"), action: undo)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.accent)
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("work.done.undo")
            }
        }
        .padding(.vertical, 12)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("work.done.\(item.id)")
    }
}

/// An Up next row: what it is, where it came from, and a word for its kind.
struct WorkUpNextRow: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    /// At accessibility sizes text wraps and never truncates (pass-access rule 7).
    private var wraps: Bool { dynamicTypeSize.isAccessibilitySize }
    let item: WorkItem

    var body: some View {
        HStack(spacing: 10) {
            Circle().fill(YansuTheme.accent).frame(width: 7, height: 7)
            VStack(alignment: .leading, spacing: 3) {
                Text(verbatim: item.work.title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(wraps ? nil : 1)
                Text(verbatim: item.firstLine)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .lineLimit(wraps ? nil : 1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if let kind = Self.kindWord(item.row) {
                Text(verbatim: kind)
                    .font(.caption.weight(.medium))
                    .foregroundStyle(YansuTheme.secondary)
            }
            Image(systemName: "chevron.right")
                .font(.caption.weight(.semibold))
                .foregroundStyle(YansuTheme.secondary)
        }
        .padding(.vertical, 12)
        .contentShape(Rectangle())
    }

    static func kindWord(_ row: String?) -> String? {
        switch row {
        case "draft": return String(localized: "work.kind.draft")
        case "prep": return String(localized: "work.kind.prep")
        case "check": return String(localized: "work.kind.check")
        case "offered": return String(localized: "work.kind.offered")
        default: return nil
        }
    }
}

/// A With your agent row: "Q4 plan outline · Claude · since 4:10 PM".
struct WorkAgentRow: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    /// At accessibility sizes text wraps and never truncates (pass-access rule 7).
    private var wraps: Bool { dynamicTypeSize.isAccessibilitySize }
    let item: WorkItem
    @Environment(\.locale) private var locale

    var body: some View {
        HStack(spacing: 10) {
            Text(verbatim: String((item.agent?.name ?? "A").prefix(1)))
                .font(.caption.weight(.bold))
                .foregroundStyle(YansuTheme.accent)
                .frame(width: 26, height: 26)
                .background(YansuTheme.accentWash, in: Circle())
            VStack(alignment: .leading, spacing: 3) {
                Text(verbatim: item.work.title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(wraps ? nil : 1)
                if let agent = item.agent {
                    Text(String.localizedStringWithFormat(String(localized: "work.agent.since"), agent.name,
                                                          agent.sinceDate.map { BriefClock.text($0, locale: locale) } ?? ""))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 12)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("work.agent.\(item.id)")
    }
}

/// Edit, the richest answer: the work full screen and editable, What it gets as chips. Use it on work it did, Hand it
/// over on work it offers, Save on a claim. The change is kept word for word, before and after.
struct WorkEditSheet: View {
    let item: WorkItem
    var save: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var text: String

    init(item: WorkItem, save: @escaping (String) -> Void) {
        self.item = item
        self.save = save
        _text = State(initialValue: item.work.text ?? item.work.title)
    }

    private var confirmTitle: String {
        switch item.kind {
        case "offered": return String(localized: "work.edit.handOver")
        case "claim": return String(localized: "work.edit.save")
        default: return String(localized: "work.edit.useIt")
        }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    TextEditor(text: $text)
                        .font(.body)
                        .frame(minHeight: 220)
                        .scrollContentBackground(.hidden)
                        .padding(10)
                        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 14))
                        .accessibilityIdentifier("work.edit.text")
                    if let gets = item.gets, !gets.isEmpty {
                        SectionLabel(title: String(localized: "work.edit.gets"))
                        WorkChipFlow(spacing: 8) {
                            ForEach(gets, id: \.self) { piece in
                                Text(verbatim: piece.label)
                                    .font(.subheadline)
                                    .foregroundStyle(YansuTheme.ink)
                                    .padding(.horizontal, 12)
                                    .padding(.vertical, 8)
                                    .background(YansuTheme.chip, in: Capsule())
                                    .overlay { Capsule().stroke(YansuTheme.chipBorder) }
                            }
                        }
                    }
                    Text("work.edit.kept")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                .padding(YansuTheme.gutter)
            }
            .yansuScreen()
            .navigationTitle(Text(verbatim: item.work.title))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button(String(localized: "Cancel")) { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button(confirmTitle) { save(text); dismiss() }
                        .accessibilityIdentifier("work.edit.confirm")
                }
            }
        }
    }
}

/// A row whose children share its width by weight: Yes is 1.3 of No and Not mine (board artboard 2).
struct WorkWeightedRow: Layout {
    var weights: [CGFloat]
    var spacing: CGFloat = 8

    private func widths(_ total: CGFloat, count: Int) -> [CGFloat] {
        let used = (0..<count).map { $0 < weights.count ? weights[$0] : 1 }
        let sum = used.reduce(0, +)
        let room = max(0, total - spacing * CGFloat(max(0, count - 1)))
        return used.map { room * $0 / sum }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? 320
        let sizes = widths(width, count: subviews.count)
        let height = zip(subviews, sizes).map { $0.sizeThatFits(ProposedViewSize(width: $1, height: nil)).height }.max() ?? 0
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        for (view, width) in zip(subviews, widths(bounds.width, count: subviews.count)) {
            view.place(at: CGPoint(x: x, y: bounds.minY), proposal: ProposedViewSize(width: width, height: bounds.height))
            x += width + spacing
        }
    }
}

/// Chips that wrap onto the next line.
struct WorkChipFlow: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, row: CGFloat = 0, widest: CGFloat = 0
        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if x > 0, x + size.width > width { x = 0; y += row + spacing; row = 0 }
            x += size.width + spacing
            row = max(row, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: proposal.width ?? widest, height: y + row)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, row: CGFloat = 0
        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if x > bounds.minX, x + size.width > bounds.maxX { x = bounds.minX; y += row + spacing; row = 0 }
            view.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            row = max(row, size.height)
        }
    }
}
