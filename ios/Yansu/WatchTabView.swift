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

    init(recordings: WatchRecordingsModel = .shared, model: WatchTabModel = .shared) {
        self.recordings = recordings
        self.model = model
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                nowCard
                if recordings.connection != .appMissing {
                    hoursCard
                    conversations
                    hoursLink
                    settings
                }
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.vertical, 12)
        }
        .background(YansuTheme.background)
        .navigationTitle(Text("Watch", comment: "Title of the Watch tab"))
        .refreshable {
            await WatchLink.shared.syncNow()
            await reload()
        }
        .task { await reload() }
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
                                   lastHeard: recordings.rows.first.map { $0.startedAt.addingTimeInterval($0.durationSeconds) })
        return VStack(alignment: .leading, spacing: 8) {
            Text(now.word)
                .font(.title3.weight(.semibold))
                .foregroundStyle(now.kind == .closedEyes ? YansuTheme.warning : YansuTheme.ink)
                .accessibilityIdentifier("watch.now.word")
            Text(now.line)
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("watch.now.line")
            if let waiting = WatchTabCopy.waitingLine(recordings.rows) {
                Button { showsWhere = true } label: {
                    HStack(spacing: 6) {
                        Text(waiting).font(.footnote.weight(.medium)).multilineTextAlignment(.leading)
                        Image(systemName: "chevron.right").font(.caption2.weight(.semibold))
                    }
                    .foregroundStyle(YansuTheme.warning)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(YansuTheme.warningWash, in: Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("watch.now.waiting")
            }
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
            } else if model.answer == nil {
                note(model.failed ? String(localized: "Couldn't reach your computer. Pull down to try again.")
                     : String(localized: "Reading from your computer"))
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

    private func note(_ text: String) -> some View {
        Text(text)
            .font(.subheadline).foregroundStyle(YansuTheme.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
    }

    // MARK: The hours

    /// A plain door to the list of hours (where each is, Sync now, play, delete). It stays until a
    /// conversation's own page can play and delete; design's Where sheet replaces it.
    private var hoursLink: some View {
        Button { showsWhere = true } label: {
            HStack(spacing: 8) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Hours and where they are").font(.body).foregroundStyle(YansuTheme.ink)
                    Text(WatchRecordingsCopy.summary(recordings.rows)).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                Spacer(minLength: 4)
                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
            }
            .padding(14)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("watch.hoursLink")
    }

    // MARK: Your watch

    @ViewBuilder private var settings: some View {
        if let notesOn = model.notesOn {
            VStack(alignment: .leading, spacing: 8) {
                Text("Your watch", comment: "Title of the settings group on the Watch tab")
                    .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
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
        }
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
