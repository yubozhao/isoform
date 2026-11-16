import SwiftUI

/// Your work on Today, under the brief (phone-today v1.0.0 rules 8 to 16): the open item, Up next, With your agent,
/// Done today. It loads and fails on its own; nothing here moves the brief above it.
struct YourWorkSection<Legacy: View>: View {
    @ObservedObject var work: WorkStore
    let now: Date
    /// Called after the first answer, so the brief folds to two lines.
    var onAnswered: () -> Void = {}
    /// What a computer without Your work (B2) still shows, in the same place: its Needs you and Done rows.
    @ViewBuilder var legacy: () -> Legacy

    @State private var openIndex = 0
    @State private var declined: WorkItem?
    @State private var editing: WorkItem?
    @State private var why: WorkItem?
    @State private var answerHaptics = 0
    /// Yes on offered work: the hand-off shows exactly what goes (phone-agent v1.0.0 rule 3).
    @State private var handingOff: WorkItem?
    @EnvironmentObject private var environment: AppEnvironment
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if work.supported == true {
            served
        } else {
            legacy()
        }
    }

    private var served: some View {
        let groups = work.groups(now: now)
        let index = min(openIndex, max(0, groups.open.count - 1))
        return VStack(alignment: .leading, spacing: 12) {
            YourWorkHeader(count: groups.toAnswer)
            if let declined {
                WorkReasonPicker(item: declined, pick: { reason in
                    Task { await work.addReason(reason, to: declined) }
                    self.declined = nil
                }, undo: {
                    Task { await work.undo(declined) }
                    self.declined = nil
                }, skip: { self.declined = nil })
                .transition(.opacity)
            }
            if groups.open.indices.contains(index) {
                let item = groups.open[index]
                WorkItemCard(item: item, position: (index, groups.open.count),
                             answer: { action in answer(item, action) },
                             edit: { editing = item },
                             showWhy: { why = item })
                    .id(item.id)
                    .transition(reduceMotion ? .opacity : .asymmetric(insertion: .move(edge: .trailing).combined(with: .opacity),
                                                                       removal: .opacity))
                    .gesture(DragGesture(minimumDistance: 24).onEnded { value in
                        // Swiping moves between items without answering.
                        guard abs(value.translation.width) > abs(value.translation.height) else { return }
                        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) {
                            if value.translation.width < 0 { openIndex = min(index + 1, groups.open.count - 1) }
                            else { openIndex = max(index - 1, 0) }
                        }
                    })
            } else if declined == nil {
                VStack(alignment: .leading, spacing: 6) {
                    Text("work.empty").font(.body.weight(.semibold)).foregroundStyle(YansuTheme.ink)
                    Text("work.emptyNext").font(.subheadline).foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("work.empty")
            }
            let upNext = groups.open.enumerated().filter { $0.offset != index }.prefix(YourWork.upNextLimit)
            if !upNext.isEmpty {
                YourWorkGroup(title: String(localized: "work.upNext"), identifier: "work.upNext") {
                    ForEach(Array(upNext), id: \.element.id) { offset, item in
                        Button {
                            withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) { openIndex = offset }
                        } label: { WorkUpNextRow(item: item) }
                            .buttonStyle(.plain)
                            .accessibilityIdentifier("work.upNext.\(item.id)")
                        if item.id != upNext.last?.element.id { Divider().overlay(YansuTheme.borderSubtle) }
                    }
                }
            }
            if !groups.withAgent.isEmpty {
                YourWorkGroup(title: String(localized: "work.withAgent"), identifier: "work.withAgent") {
                    ForEach(groups.withAgent) { item in
                        WorkAgentRow(item: item)
                        if item.id != groups.withAgent.last?.id { Divider().overlay(YansuTheme.borderSubtle) }
                    }
                }
            }
            if !groups.doneToday.isEmpty {
                YourWorkGroup(title: String(localized: "work.doneToday"), identifier: "work.doneToday") {
                    ForEach(groups.doneToday) { item in
                        WorkDoneLine(item: item, now: now, send: item.answer?.notSent == true ? { handingOff = item } : nil) {
                            Task { await work.undo(item) }
                        }
                        if item.id != groups.doneToday.last?.id { Divider().overlay(YansuTheme.borderSubtle) }
                    }
                }
            }
            if work.failed && work.page == nil {
                Text("work.failed").font(.footnote).foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("work.failed")
            }
        }
        .animation(reduceMotion ? .easeOut(duration: 0.18) : .easeOut(duration: 0.25), value: groups.open.map(\.id))
        // A light tap confirms an answer; it never stands in for words (pass-access rule 10).
        .sensoryFeedback(.impact(weight: .light), trigger: answerHaptics)
        .sheet(item: $handingOff) { item in
            HandOffSheet(source: HandOffSource.work(item),
                         hub: agentsHub, alreadyHandedOver: !work.sendsSeparately,
                         sendThroughWork: work.sendsSeparately ? { agent in
                             try await work.sendHandOff(item, agentID: agent.id, removed: [])
                         } : nil)
                .presentationDetents([.large])
        }
        .sheet(item: $editing) { item in
            WorkEditSheet(item: item) { text in
                Task { await work.answer(item, action: .edit, before: item.work.text, after: text) }
                onAnswered()
            }
        }
        .alert(Text("work.menu.why"), isPresented: Binding(get: { why != nil }, set: { if !$0 { why = nil } })) {
            Button(String(localized: "Done")) { why = nil }
        } message: {
            Text(verbatim: why?.why?.map(\.label).joined(separator: "\n") ?? why?.firstLine ?? "")
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("today.yourWork")
    }

    private var agentsHub: AgentsHubAPI {
        #if DEBUG && targetEnvironment(simulator)
        if WorkUITestSeed.isOn { return AgentsUITestHub() }
        #endif
        return environment.hub
    }

    private func answer(_ item: WorkItem, _ action: WorkAction) {
        answerHaptics += 1
        onAnswered()
        // Yes on offered work: recorded on the first tap, sent from the hand-off when the computer takes that (26.1).
        let recordOnly = action == .yes && item.kind == "offered" && work.sendsSeparately
        Task { await work.answer(item, action: action, send: recordOnly ? false : nil) }
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) {
            declined = action == .no ? item : nil
        }
        if action == .yes, item.kind == "offered" { handingOff = item }
    }
}

/// "Your work" and "{n} to answer", or "all answered".
struct YourWorkHeader: View {
    let count: Int

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text("work.title").font(.title3.weight(.bold)).foregroundStyle(YansuTheme.ink)
                .accessibilityAddTraits(.isHeader)
            Group {
                if count == 0 { Text("work.allAnswered") }
                else { Text(String.localizedStringWithFormat(String(localized: "work.count"), count)) }
            }
            .font(.footnote)
            .foregroundStyle(YansuTheme.secondary)
            .accessibilityIdentifier("work.count")
        }
        .padding(.top, 6)
        .padding(.horizontal, 4)
    }
}

/// A titled group of one-line rows on a white card.
struct YourWorkGroup<Content: View>: View {
    let title: String
    let identifier: String
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            SectionLabel(title: title)
                .padding(.horizontal, 4)
            VStack(spacing: 0) { content() }
                .padding(.horizontal, 14)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 16))
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(identifier)
    }
}
