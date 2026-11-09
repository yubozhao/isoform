import SwiftUI

struct YourWeekCardView: View {
    let brief: MobileBriefV1
    let week: MobileBriefWeek
    let topics: TopicStore?
    let canTalk: Bool
    let onPut: (MobileBriefCard) -> Void
    let onPrepareInTopic: ((String, String) -> Bool)?
    let onFindTime: () -> Void
    let onAskTopic: (String, String) -> Void
    /// Inside the brief's own card (P139): no surface of its own, and the brief's Play or recording line sits under the title.
    var embedded = false
    var belowTitle: AnyView? = nil
    /// "Make my brief now" in the same ··· menu, so a week brief keeps the action the brief card had.
    var makeNow: (() -> Void)? = nil
    var canMakeNow = false

    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var actions: ActionStore
    @EnvironmentObject private var notifications: NotificationSettingsStore
    @EnvironmentObject private var environment: AppEnvironment
    @Environment(\.locale) private var locale
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var correctionHolds = DecisionHoldCoordinator()
    @State private var turnedOff = false
    @State private var correctionTarget: WeekCorrectionTarget?
    @State private var correctionDraft = ""
    @State private var correctionFailed = false
    @State private var notice: String?

    private var scopes: [String] { pairing.paired?.scopes ?? [] }
    private var canMute: Bool { scopes.contains("push") }
    private var canCorrectMemory: Bool { SettingsView.allowsMemoryCorrection(scopes) }
    private var askTarget: MobileBriefWeekAskTarget? {
        guard canTalk else { return nil }
        guard let target = brief.yourWeekAskTarget else { return nil }
        return target
    }

    var body: some View {
        // The week keeps coming when its notification is off; only the notification stops (design v1.0.18). The card stays,
        // and its ··· menu offers "Turn on Your week notifications" (P139, P160).
        Group {
            do {
                VStack(alignment: .leading, spacing: 20) {
                    title
                    if let belowTitle { belowTitle }
                    if let lead = week.lead {
                        Text(verbatim: lead)
                            .font(.body.weight(.medium))
                            .lineSpacing(4)
                            .foregroundStyle(YansuTheme.ink)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityIdentifier("brief.yourWeek.lead")
                    }
                    timeSection
                    doneSection
                    openSection
                    learnedSection
                    askSection
                    if let notice {
                        NoticeBanner(message: notice, dismiss: { self.notice = nil })
                            .accessibilityIdentifier("brief.yourWeek.notice")
                    }
                }
                .modifier(WeekSurface(framed: !embedded))
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("brief.yourWeek")
            }
        }
        .onDisappear { Task { await correctionHolds.flushAll() } }
        .onChange(of: scenePhase) { _, phase in
            if DecisionHoldCoordinator.shouldFlush(when: phase) { Task { await correctionHolds.flushAll() } }
        }
    }

    private var title: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if let range = dateRange {
                Text(format("Your week · %@", range))
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("brief.yourWeek.title")
            }
            Spacer(minLength: 4)
            if canMute || makeNow != nil {
                Menu {
                    if let makeNow {
                        Button("Make my brief now") { makeNow() }
                            .disabled(!canMakeNow)
                    }
                    if canMute {
                        // The same item reads "Turn on" once the week's notifications are off (P139, design v1.0.17).
                        Button {
                            Task { weekNotificationsOn ? await turnOff() : await turnOn() }
                        } label: {
                            Label(weekNotificationsOn ? "Turn off Your week notifications" : "Turn on Your week notifications",
                                  systemImage: weekNotificationsOn ? "bell.slash" : "bell")
                        }
                    }
                } label: {
                    Image(systemName: "ellipsis")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(YansuTheme.secondary)
                        .frame(width: 36, height: 36)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("More options for Your week")
                .accessibilityIdentifier("brief.yourWeek.menu")
            }
        }
    }

    @ViewBuilder
    private var timeSection: some View {
        if !week.time.isEmpty {
            VStack(alignment: .leading, spacing: 9) {
                sectionHeading("Where your time went")
                GeometryReader { geometry in
                    let spacing: CGFloat = 3
                    let available = max(0, geometry.size.width - spacing * CGFloat(max(0, week.time.count - 1)))
                    HStack(spacing: spacing) {
                        ForEach(Array(week.time.enumerated()), id: \.element.id) { index, row in
                            Capsule()
                                .fill(timeColor(row, index: index))
                                .frame(width: available * CGFloat(row.hours) / CGFloat(week.time.reduce(0) { $0 + $1.hours }))
                        }
                    }
                }
                .frame(height: 10)
                .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(week.time.enumerated()), id: \.element.id) { index, row in
                        HStack(spacing: 5) {
                            Circle()
                                .fill(timeColor(row, index: index))
                                .frame(width: 7, height: 7)
                            Text(format("%@ %lld h", row.other ? String(localized: "Other") : (row.name ?? ""), Int64(row.hours)))
                                .font(.footnote)
                                .foregroundStyle(YansuTheme.ink)
                        }
                    }
                }
                Text("Time on this computer, by topic.")
                    .font(.caption)
                    .foregroundStyle(YansuTheme.secondary)
            }
            .accessibilityIdentifier("brief.yourWeek.time")
        }
    }

    @ViewBuilder
    private var doneSection: some View {
        if !week.done.isEmpty || week.doneMore != nil {
            VStack(alignment: .leading, spacing: 7) {
                sectionHeading("Done for you")
                ForEach(week.done) { row in
                    Text(verbatim: doneLabel(row))
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("brief.yourWeek.done.\(row.id)")
                }
                if let count = week.doneMore {
                    Text(count == 1
                         ? String(localized: "1 more in Activity")
                         : format("%lld more in Activity", Int64(count)))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.accent)
                        .accessibilityIdentifier("brief.yourWeek.doneMore")
                }
            }
            .accessibilityIdentifier("brief.yourWeek.done")
        }
    }

    @ViewBuilder
    private var openSection: some View {
        if !week.open.isEmpty {
            VStack(alignment: .leading, spacing: 12) {
                sectionHeading("Still open")
                ForEach(week.open) { row in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(alignment: .firstTextBaseline, spacing: 4) {
                            Text(verbatim: row.title)
                                .font(.subheadline)
                                .foregroundStyle(YansuTheme.ink)
                                .fixedSize(horizontal: false, vertical: true)
                            if let since = weekday(row.since, timeZone: brief.timezone) {
                                Text(format("since %@", since))
                                    .font(.footnote)
                                    .foregroundStyle(YansuTheme.secondary)
                            }
                        }
                        if canTalk, !isCommitmentBriefItemKey(row.itemKey), let card = brief.yourWeekOpenCard(for: row) {
                            Button {
                                onPut(card)
                            } label: {
                                Text(verbatim: row.label ?? "")
                            }
                            .buttonStyle(YansuChipButtonStyle())
                            .accessibilityHint("Asks this in a sheet")
                            .accessibilityIdentifier("brief.yourWeek.chip.\(row.itemKey)")
                        }
                    }
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("brief.yourWeek.open")
        }
    }

    @ViewBuilder
    private var learnedSection: some View {
        if !week.learned.isEmpty {
            VStack(alignment: .leading, spacing: 10) {
                sectionHeading("What I learned")
                ForEach(week.learned) { row in
                    VStack(alignment: .leading, spacing: 8) {
                      HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(verbatim: row.text)
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.ink)
                            .fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 0)
                        if row.kind == "mute", canMute,
                           let kind = row.engagementKind, Identifiers.isSafePathID(kind) {
                            Button("Undo") {
                                Task { await undoMute(kind) }
                            }
                            .font(.footnote.weight(.medium))
                            .foregroundStyle(YansuTheme.accent)
                            .accessibilityIdentifier("brief.yourWeek.undo.\(row.id)")
                        } else if canCorrectMemory, let memoryID = row.memoryId,
                                  Identifiers.isSafePathID(memoryID) {
                            Button("Not quite") {
                                correctionDraft = ""
                                correctionFailed = false
                                correctionTarget = WeekCorrectionTarget(id: memoryID)
                            }
                                .font(.footnote.weight(.medium))
                                .foregroundStyle(YansuTheme.accent)
                                .accessibilityIdentifier("brief.yourWeek.notRight.\(row.id)")
                        }
                      }
                      if let target = correctionTarget, target.id == row.memoryId {
                        InlineMemoryCorrection(anchor: "memory.correction.\(target.id)", initialReason: correctionDraft, initiallyFailed: correctionFailed,
                                               cancel: { correctionTarget = nil }) { reason in
                            correctionHolds.begin(.init(id: target.id, label: String(localized: "Not quite"), detail: row.text)) {
                                let saved: Bool
                                if environment.today.hubUnreachable || environment.brief.hubOnline == false {
                                    let version = environment.today.snapshot?.briefs.first { $0.id == brief.identifier }?.bodyVersion
                                    saved = await actions.queueTodayCorrection(memoryID: target.id, reason: reason,
                                                                           briefID: brief.identifier, bodyVersion: version)
                                } else {
                                    saved = await actions.rejectAttribution(memoryID: target.id, reason: reason)
                                    notice = actions.notice
                                }
                                if !saved {
                                    correctionDraft = reason
                                    correctionFailed = true
                                    correctionTarget = target
                                }
                            }
                            return true
                        }
                      }
                      if let id = row.memoryId, correctionHolds.holds.contains(where: { $0.id == id }) {
                        HStack {
                            Text("Not quite")
                            Button("Undo") { _ = correctionHolds.undo(id) }
                                .accessibilityIdentifier("brief.correction.undo")
                        }.font(.footnote).foregroundStyle(YansuTheme.secondary)
                      }
                    }
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("brief.yourWeek.learned")
        }
    }

    @ViewBuilder
    private var askSection: some View {
        if let ask = week.ask {
            WeekAskSection(store: environment.weekDeclines, brief: brief, week: week, ask: ask,
                           label: askButtonLabel(ask), target: askTarget, onPut: onPut, onAskTopic: onAskTopic)
        }
    }

    private var dateRange: String? {
        guard let start = date(week.start), let end = date(week.end) else { return nil }
        let calendar = Calendar(identifier: .gregorian)
        let monthDay = DateFormatter()
        monthDay.locale = locale
        monthDay.calendar = calendar
        monthDay.timeZone = TimeZone(secondsFromGMT: 0)
        monthDay.dateFormat = "MMM d"
        let startText = monthDay.string(from: start)
        let endText: String
        if calendar.component(.month, from: start) == calendar.component(.month, from: end) {
            let day = DateFormatter()
            day.locale = locale
            day.calendar = calendar
            day.timeZone = TimeZone(secondsFromGMT: 0)
            day.dateFormat = "d"
            endText = day.string(from: end)
        } else {
            endText = monthDay.string(from: end)
        }
        return format("%@ to %@", startText, endText)
    }

    private func date(_ raw: String?) -> Date? {
        guard let raw, raw.count >= 10 else { return nil }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter.date(from: String(raw.prefix(10)))
    }

    private func weekday(_ raw: String?, timeZone: String?, full: Bool = false) -> String? {
        guard let raw, let date = RelayTime.parse(raw) ?? date(raw) else { return nil }
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.dateFormat = full ? "EEEE" : "EEE"
        if let timeZone, let zone = TimeZone(identifier: timeZone) {
            formatter.timeZone = zone
        } else {
            formatter.timeZone = TimeZone.current
        }
        return formatter.string(from: date)
    }

    private func doneLabel(_ row: MobileBriefWeekDone) -> String {
        let title: String
        if row.kind == "meetings", let count = row.count {
            title = count == 1
                ? String(localized: "Notes from a call")
                : format("Notes from %lld calls", Int64(count))
        } else {
            title = row.title ?? ""
        }
        guard let day = weekday(row.at, timeZone: brief.timezone) else { return title }
        return format("%@ · %@", title, day)
    }

    private func askButtonLabel(_ ask: MobileBriefWeekAsk) -> String {
        if let hold = ask.hold {
            return format("Hold %@ morning", weekday(hold.start, timeZone: brief.timezone, full: true) ?? "")
        }
        return format("Plan %@", weekday(ask.day, timeZone: "UTC") ?? "")
    }

    private func timeColor(_ row: MobileBriefWeekTime, index: Int) -> Color {
        if row.other { return YansuTheme.border }
        switch index {
        case 0: return YansuTheme.ink
        case 1: return YansuTheme.ink.opacity(0.55)
        default: return YansuTheme.ink.opacity(0.3)
        }
    }

    private func sectionHeading(_ value: LocalizedStringKey) -> some View {
        Text(value)
            .font(.footnote.weight(.semibold))
            .foregroundStyle(YansuTheme.secondary)
            .accessibilityAddTraits(.isHeader)
    }

    private func format(_ key: String, _ arguments: CVarArg...) -> String {
        String(format: NSLocalizedString(key, comment: ""), locale: locale, arguments: arguments)
    }

    private var weekNotificationsOn: Bool {
        !turnedOff && notifications.settings?.kinds.first(where: { $0.kind == "weekly_recap" })?.enabled != false &&
            !notifications.pendingToday.contains(where: { $0.kind == "weekly_recap" && $0.enabled == false })
    }

    private func turnOn() async {
        let offline = environment.today.hubUnreachable || environment.brief.hubOnline == false
        await environment.notifications.setEnabled("weekly_recap", true, deferForToday: offline)
        if let failure = environment.notifications.failure {
            notice = failure
        } else {
            turnedOff = false
        }
    }

    private func turnOff() async {
        let offline = environment.today.hubUnreachable || environment.brief.hubOnline == false
        await environment.notifications.setEnabled("weekly_recap", false, deferForToday: offline)
        if let failure = environment.notifications.failure {
            notice = failure
        } else {
            if !offline { turnedOff = true }
        }
    }

    private func undoMute(_ kind: String) async {
        await environment.notifications.setEnabled(kind, true,
            deferForToday: environment.today.hubUnreachable || environment.brief.hubOnline == false)
        if let failure = environment.notifications.failure { notice = failure }
    }
}

private struct WeekAskSection: View {
    @ObservedObject var store: YourWeekDeclineStore
    let brief: MobileBriefV1
    let week: MobileBriefWeek
    let ask: MobileBriefWeekAsk
    let label: String
    let target: MobileBriefWeekAskTarget?
    let onPut: (MobileBriefCard) -> Void
    let onAskTopic: (String, String) -> Void

    var body: some View {
        Group {
            if !store.isDeclined(week) {
                VStack(alignment: .leading, spacing: 10) {
                    Rectangle().fill(YansuTheme.border).frame(height: 1).accessibilityHidden(true)
                    Text(verbatim: ask.text).font(.body.weight(.medium)).foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                    if let hold = ask.hold, let start = RelayTime.parse(hold.start), let end = RelayTime.parse(hold.end) {
                        // The dates follow the brief rule (P136): no year for this year, the year for an earlier one.
                        (Text("Proposed time") + Text(verbatim: " · \(BriefClock.stamp(start)) – \(BriefClock.text(end))"))
                            .font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .accessibilityIdentifier("brief.yourWeek.holdReadOnly")
                    }
                    if store.isPending(week) {
                        Text("Waits for your computer").font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .accessibilityIdentifier("brief.yourWeek.pending")
                    } else {
                        HStack(spacing: 8) {
                            if ask.hold == nil, let target {
                                Button(label) {
                                    switch target {
                                    case .card(let card): onPut(card)
                                    case .topic(let id, let message): onAskTopic(id, message)
                                    }
                                }.buttonStyle(YansuPrimaryButtonStyle()).accessibilityIdentifier("brief.yourWeek.ask")
                            }
                            Button("Not now") { Task { await store.decline(brief: brief, week: week) } }
                                .buttonStyle(YansuSecondaryButtonStyle())
                                .accessibilityIdentifier("brief.yourWeek.notNow")
                        }
                        if store.hasFailed(week) {
                            Text("Didn't save. Try again.").font(.footnote).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("brief.yourWeek.askSection")
            }
        }
        .task(id: brief.conversationKey) { await store.load(brief: brief, week: week) }
    }
}

private struct WeekCorrectionTarget: Identifiable {
    let id: String
}

/// The week's own card surface (P139). Inside the brief's card it has none, because that card is the surface.
private struct WeekSurface: ViewModifier {
    let framed: Bool

    func body(content: Content) -> some View {
        if framed {
            content
                .padding(16)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous)
                        .strokeBorder(YansuTheme.border, lineWidth: 1)
                }
        } else {
            content
        }
    }
}
