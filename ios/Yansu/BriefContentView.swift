import SwiftUI

// Renders the sections of one MobileBriefV1 under "In this brief" in the brief view. The spoken paragraphs,
// their chips and the player are drawn by BriefDetailView, which reads the same brief.
struct BriefContentView: View {
    let brief: MobileBriefV1
    var now: Date = Date()
    var readOnly = false
    var topics: TopicStore? = nil
    @EnvironmentObject private var actions: ActionStore
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var environment: AppEnvironment
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var correctionHolds = DecisionHoldCoordinator()
    @State private var evidence: EvidenceRequest?
    @State private var memoryCorrection: MemoryCorrection?
    @State private var correctionDraft = ""
    @State private var correctionFailed = false
    @State private var correctionNotice: String?

    private var canCorrectMemory: Bool {
        !readOnly && SettingsView.allowsMemoryCorrection(pairing.paired?.scopes ?? [])
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 24) {
            if let correctionNotice {
                NoticeBanner(message: correctionNotice, dismiss: { self.correctionNotice = nil })
            }
            if brief.isColdStart {
                EmptyStateCard(title: "Isoform is still learning from your work.",
                               detail: "This brief fills in as your computer observes more of your day.")
            }
            if brief.hasPurposeSections || (brief.limits?.excludedSensitiveItems ?? 0) > 0 {
                BriefPurposeSections(brief: brief, now: now) { request in
                    evidence = request
                }
            }
            itemSection("What Isoform moved", items: brief.sections.moved)
            itemSection("Worked on today", items: brief.sections.doing)
            itemSection("Watching", items: brief.sections.watching)
            itemSection("Meetings", items: brief.sections.meetings)
            itemSection("Who you know", items: brief.sections.people)
            if brief.showsOpenLoopsSection && !brief.remainingOpenLoops.isEmpty {
                openLoopsSection
            }
            itemSection("New learning", items: brief.sections.learned)
            itemSection("Scheduled work", items: brief.sections.schedules)
            itemSection("Deliverables", items: brief.sections.deliverables)
            // The Day shape section shows only when there is a shape to show (#649).
            if Self.hasDayShape(brief.dayShape) { dayShapeSection }
            conversationActions
        }
        .sheet(item: $evidence) { request in
            EvidenceSheet(request: request)
        }
        .onDisappear { Task { await correctionHolds.flushAll() } }
        .onChange(of: scenePhase) { _, phase in
            if DecisionHoldCoordinator.shouldFlush(when: phase) { Task { await correctionHolds.flushAll() } }
        }
    }

    /// The brief's clicks, the same on the Feed and here (backlog item 58).
    @ViewBuilder
    private var conversationActions: some View {
        if !readOnly {
            BriefCardsView(brief: brief, showsAttention: false, topics: topics)
        }
    }

    // MARK: Item sections

    /// A section as a plain list: for checking, not listening, so no card around it.
    /// A section with nothing in it is left out (P131): no heading and no "nothing here" line.
    @ViewBuilder
    private func itemSection(_ title: String, items: [BriefItem]) -> some View {
        if Self.showsSection(items) {
            itemSectionContent(title, items: items)
        }
    }

    static func showsSection(_ items: [BriefItem]) -> Bool { !items.isEmpty }

    private func itemSectionContent(_ title: String, items: [BriefItem]) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: title)
            VStack(alignment: .leading, spacing: 16) {
                let cards = readOnly ? [] : FeedAssembly.cards(in: brief)
                ForEach(Array(items.enumerated()), id: \.offset) { index, item in
                    if index > 0 {
                        Divider().overlay(YansuTheme.border)
                    }
                    itemRow(item, cards: cards)
                }
            }
        }
    }

    /// One item in its own row, with the actions its feed card offers in the same row (P152): Sources, Useful and
    /// Not for me. Open loops pass no cards, as the Feed never showed them.
    private func itemRow(_ item: BriefItem, cards: [FeedCardModel] = []) -> some View {
        let card = BriefItemActions.card(for: item, in: cards)
        return VStack(alignment: .leading, spacing: 6) {
            BriefItemTitle(item: item)
            if let detail = item.detail, !detail.isEmpty {
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack(spacing: 8) {
                let dueLabel = item.dueLabel(now: now, timezone: brief.timezone)
                let hasDeadline = item.evidenceUnavailable != true && item.dueDate != nil
                let status = hasDeadline && ["due_today", "slipping"].contains(item.status ?? "")
                    ? nil : item.status.map(Self.humanize)
                let meta = [status == dueLabel ? nil : status, dueLabel,
                            item.evidenceUnavailable == true || dueLabel != nil ? nil : item.atDate.map(Self.shortDateTime)]
                    .compactMap { $0 }
                    .joined(separator: ", ")
                if !meta.isEmpty {
                    Text(meta)
                        .font(.caption)
                        .foregroundStyle(YansuTheme.secondary)
                }
                Spacer(minLength: 0)
                let sources = brief.purposeSources(for: item).isEmpty ? (card?.sources ?? []) : brief.purposeSources(for: item)
                if !sources.isEmpty {
                    EvidenceChip(title: sourceCountText(sources.count)) {
                        evidence = EvidenceRequest(title: item.title ?? "Sources", sources: sources)
                    }
                }
            }
            // A remembered item's one action is Not quite, on the action row like the other items' capsules (P151).
            if item.evidenceUnavailable != true, canCorrectMemory, let memory = singleMemory(for: item) {
                HStack(spacing: 8) {
                    Button("Not quite") {
                        correctionDraft = ""
                        correctionFailed = false
                        memoryCorrection = MemoryCorrection(id: memory.id, label: memory.label)
                    }
                    .buttonStyle(YansuChipButtonStyle())
                    .accessibilityIdentifier("brief.notRight")
                }
            }
            if let card, !readOnly {
                BriefItemActions(card: card,
                                 canGiveFeedback: SettingsView.allowsFeedFeedback(pairing.paired?.scopes ?? []),
                                 feed: environment.feed, today: environment.today, briefStore: environment.brief)
            }
            if let target = memoryCorrection, singleMemory(for: item)?.id == target.id {
                InlineMemoryCorrection(anchor: "memory.correction.\(target.id)", initialReason: correctionDraft, initiallyFailed: correctionFailed,
                                       cancel: { memoryCorrection = nil }) { reason in
                    correctionHolds.begin(.init(id: target.id, label: String(localized: "Not quite"), detail: target.label)) {
                        let saved: Bool
                        if environment.today.hubUnreachable || environment.brief.hubOnline == false {
                            let version = environment.today.snapshot?.briefs.first { $0.id == brief.identifier }?.bodyVersion
                            saved = await actions.queueTodayCorrection(memoryID: target.id, reason: reason,
                                                                   briefID: brief.identifier, bodyVersion: version)
                        } else {
                            saved = await actions.rejectAttribution(memoryID: target.id, reason: reason)
                            correctionNotice = actions.notice
                        }
                        if !saved {
                            correctionDraft = reason
                            correctionFailed = true
                            memoryCorrection = target
                        }
                    }
                    return true
                }
            }
            if let memory = singleMemory(for: item), correctionHolds.holds.contains(where: { $0.id == memory.id }) {
                HStack {
                    Text("Not quite")
                    Button("Undo") { _ = correctionHolds.undo(memory.id) }
                        .accessibilityIdentifier("brief.correction.undo")
                }
                .font(.footnote).foregroundStyle(YansuTheme.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.item")
    }

    // MARK: Open loops grouped by kind

    private var openLoopsSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: "Priorities and open loops")
            if brief.remainingOpenLoops.isEmpty {
                YansuCard {
                    Text("No open loops right now.")
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            } else {
                ForEach(Array(Self.openLoopGroups(for: brief.remainingOpenLoops).enumerated()), id: \.offset) { _, group in
                    YansuCard {
                        VStack(alignment: .leading, spacing: 16) {
                            if let heading = group.heading {
                                Text(heading)
                                    .font(.subheadline.weight(.semibold))
                                    .foregroundStyle(YansuTheme.ink)
                            }
                            ForEach(Array(group.items.enumerated()), id: \.offset) { index, item in
                                if index > 0 {
                                    Divider().overlay(YansuTheme.border)
                                }
                                itemRow(item)
                            }
                        }
                    }
                }
            }
        }
    }

    static func openLoopGroups(for loops: [BriefItem]) -> [OpenLoopGroup] {
        var order: [String] = []
        var buckets: [String: [BriefItem]] = [:]
        for item in loops {
            let key = item.kind ?? ""
            if buckets[key] == nil {
                order.append(key)
                buckets[key] = []
            }
            buckets[key]?.append(item)
        }
        // A heading shows only when two or more kinds have a name. Otherwise the open loops are one list with no
        // heading; an item without a kind is never "Other" and never counted in parentheses.
        let named = order.filter { !$0.isEmpty }.count
        guard named >= 2 else {
            return [OpenLoopGroup(heading: nil, items: loops)]
        }
        return order.map { key in
            OpenLoopGroup(heading: key.isEmpty ? nil : BriefItem.kindLabel(key), items: buckets[key] ?? [])
        }
    }

    // MARK: Day shape

    /// Whether the brief has a day shape to show: active time or hours. An empty shape shows no section.
    static func hasDayShape(_ shape: BriefDayShape?) -> Bool {
        guard let shape else { return false }
        return (shape.activeMinutes ?? 0) > 0 || !shape.hours.isEmpty
    }

    private var dayShapeSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: "Day shape")
            YansuCard {
                if let shape = brief.dayShape, (shape.activeMinutes ?? 0) > 0 || !shape.hours.isEmpty {
                    VStack(alignment: .leading, spacing: 16) {
                        Text("Active for \(Self.duration(minutes: shape.activeMinutes ?? 0)).")
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(YansuTheme.ink)
                        if shape.firstActiveDate != nil || shape.lastActiveDate != nil || (shape.breakMinutes ?? 0) > 0 {
                            VStack(alignment: .leading, spacing: 4) {
                                if let first = shape.firstActiveDate {
                                    Text("Started at \(Self.dayTime(first, timezone: brief.timezone))")
                                }
                                if let last = shape.lastActiveDate {
                                    Text("Last active at \(Self.dayTime(last, timezone: brief.timezone))")
                                }
                                if let minutes = shape.breakMinutes, minutes > 0 {
                                    Text("Away for \(Self.duration(minutes: minutes))")
                                }
                            }
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                        }
                        DayShapeBars(hours: shape.hours)
                        if !shape.topApps.isEmpty {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("Most used apps")
                                    .font(.footnote.weight(.semibold))
                                    .foregroundStyle(YansuTheme.secondary)
                                ForEach(Array(shape.topApps.enumerated()), id: \.offset) { _, app in
                                    HStack {
                                        Text(app.name ?? "Unknown app")
                                            .font(.subheadline)
                                            .foregroundStyle(YansuTheme.ink)
                                        Spacer(minLength: 8)
                                        Text(Self.duration(minutes: app.activeMinutes ?? 0))
                                            .font(.subheadline)
                                            .foregroundStyle(YansuTheme.secondary)
                                    }
                                }
                            }
                        }
                    }
                } else {
                    Text("Not enough activity to show a day shape yet.")
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
    }

    // MARK: Text helpers

    /// The one memory this item rests on, or nil when it rests on none or on several (those need the Mac).
    private func singleMemory(for item: BriefItem) -> MemoryCorrection? {
        let memories = brief.sources(for: item.refs).filter { source in
            source.sourceKind == "memory" && !(source.sourceId ?? "").isEmpty
        }
        let ids = Set(memories.compactMap(\.sourceId))
        guard ids.count == 1, let memory = memories.first, let id = memory.sourceId else { return nil }
        return MemoryCorrection(id: id, label: memory.label ?? "")
    }

    private func sourceCountText(_ count: Int) -> String {
        BriefParagraphSources.countText(count)
    }

    static func humanize(_ value: String) -> String {
        let words = value.replacingOccurrences(of: "_", with: " ")
        return words.prefix(1).uppercased() + words.dropFirst()
    }

    static func shortDateTime(_ date: Date) -> String {
        BriefClock.stamp(date)
    }

    // Match the Mac-local hours in the day shape even when this iPhone is in another timezone.
    static func dayTime(_ date: Date, timezone: String?, locale: Locale = .current) -> String {
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.timeZone = timezone.flatMap(TimeZone.init(identifier:)) ?? .current
        formatter.timeStyle = .short
        return formatter.string(from: date)
    }

    static func duration(minutes: Int) -> String {
        let total = max(0, minutes)
        if total < 60 { return "\(total) min" }
        let hours = total / 60
        let rest = total % 60
        return rest == 0 ? "\(hours) h" : "\(hours) h \(rest) min"
    }
}

struct OpenLoopGroup {
    /// Shown only when the open loops have two or more named kinds; nil for an unnamed item or a single list.
    let heading: String?
    let items: [BriefItem]
}

// Hour bars for the day shape. Hours without activity show as hairlines so the shape stays honest.
struct DayShapeBars: View {
    let hours: [BriefHour]

    var body: some View {
        let minutesByHour = hourMap
        if let first = minutesByHour.keys.min(), let last = minutesByHour.keys.max() {
            let peak = max(minutesByHour.values.max() ?? 1, 1)
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .bottom, spacing: 3) {
                    ForEach(Array(first...last), id: \.self) { hour in
                        let minutes = minutesByHour[hour] ?? 0
                        RoundedRectangle(cornerRadius: 2, style: .continuous)
                            .fill(minutes > 0 ? YansuTheme.tertiary : YansuTheme.border)
                            .frame(maxWidth: .infinity)
                            .frame(height: max(3, 56 * CGFloat(minutes) / CGFloat(peak)))
                            .accessibilityLabel("\(Self.hourLabel(hour)), \(minutes) minutes")
                    }
                }
                .frame(height: 56, alignment: .bottom)
                HStack {
                    Text(Self.hourLabel(first))
                    Spacer(minLength: 0)
                    Text(Self.hourLabel(last))
                }
                .font(.caption2)
                .foregroundStyle(YansuTheme.secondary)
            }
            .accessibilityElement(children: .contain)
        }
    }

    private var hourMap: [Int: Int] {
        var map: [Int: Int] = [:]
        for entry in hours {
            guard let hour = entry.hour, (0...23).contains(hour) else { continue }
            map[hour] = entry.activeMinutes ?? 0
        }
        return map
    }

    static func hourLabel(_ hour: Int) -> String {
        let date = Calendar.current.date(bySettingHour: hour, minute: 0, second: 0, of: Date()) ?? Date()
        return date.formatted(.dateTime.hour().minute())
    }
}

// The chip that opens an evidence sheet. It is a plain button with a generous hit area.
/// The sources chip under a written paragraph. It counts only observed sources: purpose identities are not activity.
struct BriefParagraphSources: View {
    let brief: MobileBriefV1
    let refs: [String]
    let show: (EvidenceRequest) -> Void

    var body: some View {
        let sources = brief.evidenceSources(for: refs)
        if !sources.isEmpty {
            EvidenceChip(title: Self.countText(sources.count)) {
                show(EvidenceRequest(title: "Sources for this summary", sources: sources))
            }
        }
    }

    static func countText(_ count: Int) -> String {
        count == 1 ? "1 source" : "\(count) sources"
    }
}

struct EvidenceChip: View {
    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.caption.weight(.medium))
                .foregroundStyle(YansuTheme.secondary)
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(YansuTheme.background, in: Capsule())
                .overlay {
                    Capsule().strokeBorder(YansuTheme.border, lineWidth: 1)
                }
                .contentShape(Rectangle())
                .frame(minHeight: 32)
        }
        .buttonStyle(.plain)
        .accessibilityHint("Shows where this came from")
    }
}

struct EvidenceRequest: Identifiable {
    let id = UUID()
    let title: String
    let sources: [BriefEvidence]
}

// Lists the sources behind an item. Isoform records what it observed, not why the work happened.
struct EvidenceSheet: View {
    let request: EvidenceRequest
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Text("Isoform observed these items. It does not know why you did them unless you told it.")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                Section {
                    if request.sources.isEmpty {
                        Text("Isoform cannot find the sources for this item.")
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.secondary)
                    } else {
                        ForEach(Array(request.sources.enumerated()), id: \.offset) { _, source in
                            VStack(alignment: .leading, spacing: 4) {
                                Text(source.sourceLabel)
                                    .font(.footnote.weight(.semibold))
                                    .foregroundStyle(YansuTheme.secondary)
                                Text(source.label ?? "Untitled source")
                                    .font(.body)
                                    .foregroundStyle(YansuTheme.ink)
                                if let observed = source.observedDate {
                                    Text("Observed \(BriefContentView.shortDateTime(observed))")
                                        .font(.caption)
                                        .foregroundStyle(YansuTheme.secondary)
                                }
                            }
                            .padding(.vertical, 2)
                        }
                    }
                }
            }
            .navigationTitle(request.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// A memory an item rests on, named for the "Not right" sheet.
struct MemoryCorrection: Identifiable {
    let id: String
    let label: String
}
