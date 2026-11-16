import SwiftUI

/// Where a brief's items sit in the brief in full (phone-brief v1.0.0 rule 5): under the paragraph the computer names
/// (`ref.paragraph`), else after the last paragraph. They are the same items as in Your work, by id.
enum BriefItemPlacement {
    static func sentenceID(brief: String, paragraph: Int) -> String { "brief:\(brief):p\(paragraph)" }

    /// The brief's own items, keyed by the paragraph they sit under. Sentences answered Not right are not items here.
    static func place(_ items: [WorkItem], brief: MobileBriefV1) -> [Int: [WorkItem]] {
        let paragraphs = brief.narrative.indices.filter {
            !($0 == brief.closingParagraphIndex) && !(brief.narrative[$0].text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
        guard let last = paragraphs.last ?? brief.narrative.indices.last else { return [:] }
        var placed: [Int: [WorkItem]] = [:]
        let own = items.filter { $0.briefID == brief.identifier && !isSentence($0.id, brief: brief.identifier) }
        for item in YourWork.ordered(own) {
            let at = item.ref?.paragraph.flatMap { paragraphs.contains($0) ? $0 : nil } ?? last
            placed[at, default: []].append(item)
        }
        return placed
    }

    static func isSentence(_ id: String, brief: String) -> Bool {
        let prefix = "brief:\(brief):p"
        guard id.hasPrefix(prefix) else { return false }
        return Int(id.dropFirst(prefix.count)) != nil
    }

    /// A sentence of the brief as an item, for Not right (B3: `brief:<briefId>:p<index>`, No with a claim's reason).
    static func sentence(brief: String, paragraph: Int, text: String) -> WorkItem {
        let title = text.count > 80 ? String(text.prefix(80)) + "…" : text
        return WorkItem(id: sentenceID(brief: brief, paragraph: paragraph), kind: "claim", state: "open", source: "brief",
                        ref: WorkRef(briefId: brief, paragraph: paragraph), first: [], noticed: text,
                        work: WorkBody(title: title), createdAt: RelayTime.format(Date()))
    }
}

/// A brief's item where the brief mentions it: where it came from, the title, and Yes, No, Not mine at 38 points.
/// An answered one is one line with a check, what happened and Undo.
struct BriefWorkItem: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let item: WorkItem
    let now: Date
    var answer: (WorkAction) -> Void
    var undo: () -> Void

    var body: some View {
        Group {
            if item.isOpen {
                VStack(alignment: .leading, spacing: 9) {
                    Text(verbatim: item.firstLine)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : 2)
                    Text(verbatim: item.work.title)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(YansuTheme.ink)
                        .workItemAccessibility(item, answer: answer, edit: nil)
                    if YourWork.asksUseful(item) {
                        WorkWeightedRow(weights: [1, 1], spacing: 8) {
                            Button { answer(.useful) } label: {
                                Text("work.useful").font(.body.weight(.semibold)).frame(maxWidth: .infinity, minHeight: 44)
                                    .foregroundStyle(YansuTheme.onSolid)
                                    .background(YansuTheme.solid, in: RoundedRectangle(cornerRadius: 12))
                            }
                            Button { answer(.notUseful) } label: {
                                Text("work.notUseful").font(.body).frame(maxWidth: .infinity, minHeight: 44)
                                    .foregroundStyle(YansuTheme.ink)
                                    .overlay { RoundedRectangle(cornerRadius: 12).stroke(YansuTheme.borderStrong) }
                            }
                        }
                        .buttonStyle(.plain)
                    } else {
                        WorkAnswerButtons(height: 44, answer: answer)
                    }
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
            } else {
                HStack(alignment: .center, spacing: 10) {
                    Image(systemName: "checkmark")
                        .font(.system(size: 10, weight: .bold))
                        .foregroundStyle(YansuTheme.success)
                        .frame(width: 20, height: 20)
                        .background(YansuTheme.successWash, in: Circle())
                    VStack(alignment: .leading, spacing: 2) {
                        Text(verbatim: item.work.title)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(YansuTheme.ink)
                        if let agent = item.agent, item.state == "with_agent" {
                            Text(String.localizedStringWithFormat(String(localized: "work.agent.since"), agent.name,
                                                                  agent.sinceDate.map { BriefClock.text($0) } ?? ""))
                                .font(.footnote).foregroundStyle(YansuTheme.secondary)
                        } else if let line = item.answer?.line {
                            Text(verbatim: line).font(.footnote).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    if let done = item.answer, YourWork.canUndo(done, now: now) || done.waits == true {
                        Button(String(localized: "Undo"), action: undo)
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.accent)
                            .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 14))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.item.\(item.id)")
    }
}
