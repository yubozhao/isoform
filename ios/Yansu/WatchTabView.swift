import SwiftUI

// The Watch tab (design's phone-watch v1.0.0): whether the watch listens, today's hours, what it
// heard as conversations, earlier days, and the watch's settings. Plain on purpose: the designed
// card, the mark's poses and the Where sheet replace parts of this.
//
// What to do is always on the watch: nothing here starts or stops listening.

struct WatchTabView: View {
    @ObservedObject var recordings: WatchRecordingsModel
    @ObservedObject var model: WatchTabModel
    @State private var pauseUntil: Date?
    @State private var showsWhere = false
    @State private var showsStartOnWatch = false
    @State private var offFailed = false
    @EnvironmentObject private var today: TodayStore
    @Environment(\.scenePhase) private var scenePhase
    /// How long a failed load waits before it is tried again while the page is open.
    static let retrySeconds: UInt64 = 15

    /// Which parts of the page this view holds. One column on a narrow phone; on the open wide
    /// phone the left pane holds `side` and the right pane `main` (phone-watch v1.1.0 rules 5 to 7).
    enum Sections: Equatable {
        case all
        /// Now, where the hours are, the watch's settings.
        case side
        /// Today's hours, conversations, earlier days.
        case main
    }
    private let sections: Sections
    /// On the open wide phone: 24 pt inside the pane, and for the left pane the tab's one title
    /// at its top, scrolling with it (phone-duo v1.0.0 rules 2, 3 and 6).
    private let wide: Bool
    private let wideTitle: String?

    init(recordings: WatchRecordingsModel = .shared, model: WatchTabModel = .shared, sections: Sections = .all,
         wide: Bool = false, wideTitle: String? = nil) {
        self.recordings = recordings
        self.model = model
        self.sections = sections
        self.wide = wide || wideTitle != nil
        self.wideTitle = wideTitle
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let wideTitle { DuoPaneTitle(wideTitle) }
                if sections != .main { nowCard }
                if recordings.connection != .appMissing {
                    // In rank order (rule 7): Now, where the hours are, today's hours,
                    // conversations and earlier days, the watch's settings.
                    if sections != .main { whereCard }
                    if sections != .side {
                        hoursCard
                        conversations
                    }
                    if sections != .main { settings }
                }
            }
            .padding(.horizontal, wide ? DuoLayout.paneInset : YansuTheme.gutter)
            .padding(.top, wide ? DuoLayout.paneTop : 12)
            .padding(.bottom, 12)
        }
        .background(YansuTheme.background)
        .navigationTitle(Text("Watch", comment: "Title of the Watch tab"))
        .refreshable {
            await WatchLink.shared.syncNow()
            await reload()
        }
        .task {
            await reload()
            // A load that failed is tried again by itself while the page is open. On 2026-11-18 one
            // lost answer left "Couldn't reach your computer" on the page for as long as it stayed open.
            while !Task.isCancelled, model.failed {
                try? await Task.sleep(nanoseconds: Self.retrySeconds * 1_000_000_000)
                if Task.isCancelled { break }
                await reload()
            }
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await reload() } }
        }
        .sheet(isPresented: $showsWhere) {
            NavigationStack {
                WatchRecordingsView(model: recordings)
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button(String(localized: "Done")) { showsWhere = false }
                        }
                    }
            }
        }
    }

    private func reload() async {
        pauseUntil = await WatchLink.shared.pauseUntil()
        await model.load()
    }

    // MARK: Now

    private var nowCard: some View {
        let now = WatchTabCopy.now(connection: recordings.connection, status: recordings.watchStatus, pauseUntil: pauseUntil,
                                   lastHeard: recordings.rows.first.map { $0.startedAt.addingTimeInterval($0.durationSeconds) },
                                   since: recordings.statusSince)
        // Amber is for a stop: Not listening, and Closed Eyes because it is on (phone-watch v1.1.0 rule 10).
        let amber = now.kind == .closedEyes || now.kind == .notListening
        return VStack(alignment: .leading, spacing: 8) {
            Text(now.word)
                .font(.title3.weight(.semibold))
                .foregroundStyle(amber ? YansuTheme.warning : YansuTheme.ink)
                .accessibilityIdentifier("watch.now.word")
            Text(now.line)
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("watch.now.line")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
    }

    // MARK: Today's hours

    @ViewBuilder private var hoursCard: some View {
        if let answer = model.answer, !answer.hours.isEmpty {
            let columns = WatchHourColumn.columns(answer.hours)
            VStack(alignment: .leading, spacing: 8) {
                HStack(alignment: .firstTextBaseline) {
                    Text("Today", comment: "Title of the card of today's hours on the Watch tab")
                        .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
                    Spacer(minLength: 8)
                    Text(WatchTabCopy.todaySummary(answer, today: model.split().today))
                        .font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                HStack(alignment: .bottom, spacing: 4) {
                    ForEach(columns) { column in
                        WatchHourBar(column: column)
                    }
                }
                .frame(height: 48)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(WatchTabCopy.todaySummary(answer, today: model.split().today))
            }
            .padding(14)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
            .accessibilityIdentifier("watch.hours")
        }
    }

    // MARK: Conversations

    @ViewBuilder private var conversations: some View {
        let split = model.split()
        VStack(alignment: .leading, spacing: 8) {
            Text("Conversations", comment: "Section title on the Watch tab")
                .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
            if !model.served {
                note(String(localized: "Update Isoform on your computer to see what your watch heard as conversations."))
            } else if model.answer == nil, model.failed {
                // Never an error alone (phone-watch v1.1.0 rule 16): what this phone holds, in grey;
                // Where your hours are already says why, once.
                heldHours
            } else if model.answer == nil {
                note(String(localized: "Reading from your computer"))
            } else if split.today.isEmpty {
                note(String(localized: "Nothing heard today yet."))
            } else {
                ForEach(split.today) { conversation in
                    NavigationLink { WatchConversationView(conversation: conversation) } label: {
                        WatchConversationRow(conversation: conversation)
                    }
                    .buttonStyle(.plain)
                }
            }
            // Earlier days only beside a list this computer just served.
            if model.served, model.answer != nil, !split.earlier.isEmpty {
                Text("Earlier days", comment: "Section title on the Watch tab")
                    .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary).padding(.top, 8)
                ForEach(split.earlier) { day in
                    DisclosureGroup {
                        VStack(spacing: 8) {
                            ForEach(day.conversations) { conversation in
                                NavigationLink { WatchConversationView(conversation: conversation) } label: {
                                    WatchConversationRow(conversation: conversation)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                        .padding(.top, 8)
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(WatchRecordingsCopy.dayTitle(day.day)).font(.body).foregroundStyle(YansuTheme.ink)
                            Text(WatchTabCopy.earlierRow(day)).font(.footnote).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                    .tint(YansuTheme.tertiary)
                    .padding(14)
                    .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                }
            }
        }
    }

    /// The hours this phone and the watch still hold, while the computer cannot name them.
    @ViewBuilder private var heldHours: some View {
        let held = recordings.rows.filter { $0.stage.waiting }
        Text("Names and notes come from your computer.", comment: "Over the unnamed hours on the Watch tab while the computer cannot be reached")
            .font(.footnote).foregroundStyle(YansuTheme.secondary)
            .accessibilityIdentifier("watch.conv.fromComputer")
        if held.isEmpty {
            note(String(localized: "Nothing heard today yet."))
        } else {
            ForEach(held.prefix(24)) { row in
                VStack(alignment: .leading, spacing: 2) {
                    Text(row.startedAt, format: .dateTime.hour())
                        .font(.body).foregroundStyle(YansuTheme.secondary)
                    Text(WatchTabCopy.waitingRow(row))
                        .font(.footnote).foregroundStyle(YansuTheme.tertiary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(14)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                .accessibilityIdentifier("watch.conv.waiting")
            }
        }
    }

    private func note(_ text: String) -> some View {
        Text(text)
            .font(.subheadline).foregroundStyle(YansuTheme.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
    }

    // MARK: The hours

    /// Where your hours are (phone-watch v1.1.0 rules 11 to 14, v1.2.0): always shown under Now.
    /// Three places with their counts, one sentence, Try again only when something is stuck,
    /// and the door to the hours by place.
    private var whereCard: some View {
        let here = WatchWhere.of(rows: recordings.rows, outcome: recordings.outcome, computer: computerState,
                                 cause: recordings.waitCause, sendingSince: recordings.sendingSince,
                                 watchFound: !recordings.connection.isoformNotFound)
        return VStack(alignment: .leading, spacing: 12) {
            Text("Where your hours are", comment: "Title of the card on the Watch tab that says where the watch's hours are")
                .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
            HStack(alignment: .top, spacing: 8) {
                place(String(localized: "On your watch"), here.onWatch, ringed: here.ringed == .watch, id: "watch")
                place(String(localized: "On this phone"), here.onPhone, ringed: here.ringed == .phone, id: "phone")
                place(String(localized: "On your computer"), here.onComputer, ringed: here.ringed == .computer, id: "computer")
            }
            if !here.sentence.isEmpty {
                Text(here.sentence)
                    .font(.subheadline).foregroundStyle(YansuTheme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("watch.where.sentence")
            }
            if let fact = here.fact {
                Text(fact)
                    .font(.footnote).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("watch.where.fact")
            }
            if here.showsTryAgain {
                Button {
                    Task { await recordings.sync() }
                } label: {
                    Text(recordings.syncing ? String(localized: "Trying…", comment: "Try again while it runs")
                         : String(localized: "Try again", comment: "Sends a refused watch recording again"))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(recordings.syncing ? YansuTheme.tertiary : YansuTheme.accent)
                }
                .buttonStyle(.plain)
                .disabled(recordings.syncing)
                .accessibilityIdentifier("watch.where.tryAgain")
            }
            Button { showsWhere = true } label: {
                HStack(spacing: 4) {
                    Text("Hours by place", comment: "Opens the list of the watch's hours grouped by where they are")
                        .font(.subheadline.weight(.medium))
                    Image(systemName: "chevron.right").font(.caption2.weight(.semibold))
                }
                .foregroundStyle(YansuTheme.accent)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("watch.where.byPlace")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .accessibilityIdentifier("watch.where")
    }

    private func place(_ title: String, _ count: Int, ringed: Bool, id: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.caption).foregroundStyle(YansuTheme.secondary).lineLimit(2).fixedSize(horizontal: false, vertical: true)
            Text(WatchWhere.hours(count)).font(.subheadline.weight(.semibold)).foregroundStyle(YansuTheme.ink)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(YansuTheme.surfaceWarm, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay {
            if ringed {
                RoundedRectangle(cornerRadius: 10, style: .continuous).stroke(YansuTheme.warning, lineWidth: 1.5)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("watch.where.\(id)")
    }

    /// What this phone knows of the computer: asleep, out of reach, or with Closed Eyes on.
    private var computerState: WatchComputerState {
        if recordings.watchStatus == .pausedOnComputer { return .closedEyes }
        switch today.connectionCause {
        case .sleeping: return .asleep
        case .some: return .unreachable
        case nil: return .reachable
        }
    }

    // MARK: Your watch

    /// "Listen on your watch": the phone can turn it off, never on. Off holds until the wearer
    /// taps the mark on the watch.
    private func listenToggle(_ listening: Bool) -> some View {
        Toggle(isOn: Binding(get: { listening }, set: { on in
            if on {
                showsStartOnWatch = true
            } else {
                Task { offFailed = !(await model.turnListeningOff()) }
            }
        })) {
            VStack(alignment: .leading, spacing: 2) {
                Text("Listen on your watch").font(.body).foregroundStyle(YansuTheme.ink)
                Text(listening ? String(localized: "The only off. Closed Eyes is for a while.")
                     : String(localized: "Off. Tap the mark on your watch to start listening."))
                    .font(.footnote).foregroundStyle(YansuTheme.secondary)
            }
        }
        .tint(YansuTheme.accent)
        .padding(14)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .accessibilityIdentifier("watch.settings.listen")
        .alert(String(localized: "Tap the mark on your watch to start listening."), isPresented: $showsStartOnWatch) {
            Button(String(localized: "OK"), role: .cancel) {}
        }
        .alert(String(localized: "Couldn't reach your computer, so listening is still on."), isPresented: $offFailed) {
            Button(String(localized: "OK"), role: .cancel) {}
        }
    }

    @ViewBuilder private var settings: some View {
        if model.notesOn != nil || model.listening != nil || model.heartRateOn != nil {
            VStack(alignment: .leading, spacing: 8) {
                Text("Your watch", comment: "Title of the settings group on the Watch tab")
                    .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
                if let listening = model.listening { listenToggle(listening) }
                if let notesOn = model.notesOn {
                Toggle(isOn: Binding(get: { notesOn }, set: { on in Task { await model.setNotes(on) } })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Notes for long talks").font(.body).foregroundStyle(YansuTheme.ink)
                        Text("Over 5 minutes: a note, and what you said you'd do").font(.footnote).foregroundStyle(YansuTheme.secondary)
                    }
                }
                .tint(YansuTheme.accent)
                .padding(14)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                .accessibilityIdentifier("watch.settings.notes")
                }
                // Heart rate and Place: design's words (computer-conformance-answers v1.3.0, W1),
                // the same as on the computer. Shown when the computer has the shared settings.
                if let heartRateOn = model.heartRateOn {
                    contextToggle(String(localized: "watch.set.heart"), String(localized: "watch.set.heartSub"),
                                  on: heartRateOn, id: "watch.settings.heartRate") { on in await model.setHeartRate(on) }
                }
                if let placeOn = model.placeOn {
                    contextToggle(String(localized: "watch.set.place"), String(localized: "watch.set.placeSub"),
                                  on: placeOn, id: "watch.settings.place") { on in await model.setPlace(on) }
                }
            }
        }
    }
}

extension WatchTabView {
    func contextToggle(_ title: String, _ line: String, on: Bool, id: String, set: @escaping (Bool) async -> Void) -> some View {
        Toggle(isOn: Binding(get: { on }, set: { value in Task { await set(value) } })) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.body).foregroundStyle(YansuTheme.ink)
                Text(line).font(.footnote).foregroundStyle(YansuTheme.secondary)
            }
        }
        .tint(YansuTheme.accent)
        .padding(14)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .accessibilityIdentifier(id)
    }
}

/// One hour of today's strip: as tall as the talk kept in it.
private struct WatchHourBar: View {
    let column: WatchHourColumn

    var body: some View {
        GeometryReader { geometry in
            ZStack(alignment: .bottom) {
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(column.kind == .closed ? YansuTheme.warningWash : YansuTheme.surfaceWarm)
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(YansuTheme.accent.opacity(column.kind == .current ? 0.5 : 1))
                    .frame(height: max(column.height > 0 ? 2 : 0, geometry.size.height * column.height))
            }
        }
        .frame(maxWidth: 24)
    }
}

/// One conversation: its name, when and who, and what is in it.
struct WatchConversationRow: View {
    let conversation: WatchConversation

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Circle()
                .fill(conversation.marks.isEmpty ? Color.clear : YansuTheme.accent)
                .frame(width: 6, height: 6)
                .padding(.top, 7)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                Text(WatchTabCopy.title(conversation))
                    .font(.body.weight(.semibold))
                    .foregroundStyle((conversation.name ?? "").isEmpty ? YansuTheme.secondary : YansuTheme.ink)
                Text(WatchTabCopy.meta(conversation))
                    .font(.footnote).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                let tags = self.tags
                if !tags.isEmpty {
                    HStack(spacing: 6) {
                        ForEach(tags, id: \.text) { tag in
                            Text(tag.text)
                                .font(.caption.weight(.medium))
                                .foregroundStyle(tag.filled ? Color.white : YansuTheme.secondary)
                                .padding(.horizontal, 8).padding(.vertical, 3)
                                .background(tag.filled ? YansuTheme.accent : YansuTheme.surfaceWarm, in: Capsule())
                        }
                    }
                }
            }
            Spacer(minLength: 0)
        }
        .padding(14)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("watch.conversation")
    }

    private var tags: [(text: String, filled: Bool)] {
        var out: [(String, Bool)] = []
        if conversation.hasNote { out.append((String(localized: "Note"), false)) }
        if !conversation.mine.isEmpty { out.append((WatchTabCopy.toAnswer(conversation.mine.count), true)) }
        if let unknown = conversation.unknownSpeakers.first { out.append((WatchTabCopy.whoIs(unknown), false)) }
        return out.map { (text: $0.0, filled: $0.1) }
    }
}
