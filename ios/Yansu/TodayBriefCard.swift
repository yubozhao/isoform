import SwiftUI

// The Today card (handoff 2026-10-28, sections 2 and 4). What it shows is decided by `TodayBriefCard`, which is a set
// of pure functions of what the phone holds, so every state of the design's table is tested without a view. The view
// lays out the decided lines and calls the player; it reads the brief from the phone's library and never fetches it.
enum TodayBriefCard {
    /// The most chips the card shows (design section 2).
    static let chipLimit = 3

    /// The opening paragraph: the first paragraph that says something, other than the closing question. A greeting on
    /// its own is passed over for the paragraph after it, so the card never shows a bare greeting. Only a brief whose
    /// words are all greetings, or a brief with a single closing question, falls back to what it has.
    static func opening(_ brief: MobileBriefV1) -> (index: Int, text: String)? {
        let paragraphs: [(index: Int, text: String)] = brief.narrative.enumerated().compactMap { index, paragraph in
            guard let text = paragraph.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else { return nil }
            return (index, text)
        }
        let spoken = paragraphs.filter { $0.index != brief.closingParagraphIndex }
        let candidates = spoken.isEmpty ? paragraphs : spoken
        return candidates.first { !isGreeting($0.text) } ?? candidates.first
    }

    /// The paragraphs whose chips the card shows: the first `chipLimit` paragraphs, in order, that have a chip. The
    /// closing question is not one of them; its answers are the brief's own.
    static func chipParagraphs(_ brief: MobileBriefV1) -> [Int] {
        brief.narrative.indices
            .filter { $0 != brief.closingParagraphIndex && brief.paragraphChip(forParagraph: $0) != nil }
            .prefix(chipLimit)
            .map { $0 }
    }

    private static let greetings: Set<String> = [
        "good morning", "good afternoon", "good evening", "good night", "hello", "hi",
        "早上好", "上午好", "中午好", "下午好", "晚上好", "你好",
    ]

    /// A paragraph that is only a greeting: "Good morning.", "早上好。".
    static func isGreeting(_ text: String) -> Bool {
        let word = text.trimmingCharacters(in: .whitespacesAndNewlines)
            .trimmingCharacters(in: CharacterSet(charactersIn: ".!,。！，"))
            .lowercased()
        return greetings.contains(word)
    }

    /// Play is shown only for a brief with words (design section 2) once its audio can play: it is on the phone, the
    /// player already holds this brief (playing or paused, so the card is the player and the mini player steps aside,
    /// section 4), or the brief has audio on the computer while the computer can be reached (the tap fetches it,
    /// section 6). A brief whose package has no audio yet shows no Play: text first, audio right after.
    static func showsPlay(hasWords: Bool, hasAudio: Bool, holdsBrief: Bool, audioOnComputer: Bool,
                          computerOnline: Bool?) -> Bool {
        hasWords && (hasAudio || holdsBrief || audioOnComputer && computerOnline != false)
    }

    /// The wordless rows the card shows once, before any brief has arrived after pairing (design section 2).
    static func showsPlaceholder(hasBrief: Bool, placeholderShown: Bool) -> Bool {
        !hasBrief && !placeholderShown
    }

    /// "Recording on its way" in the player's place: the brief has words, Play is not shown, and the computer says its
    /// recording is still being made (audio status `downloading`, PROTOCOL.md section 8). The computer sends no time,
    /// so the line names none. A brief with no audio status, such as the template sentence, shows nothing there (P138).
    static func showsRecordingLine(hasWords: Bool, playShown: Bool, audio: BriefAudio?) -> Bool {
        hasWords && !playShown && audio?.isRecording == true
    }

    /// "Your evening brief is being made · ready around 6:03 PM", only while the computer says a brief is being made
    /// (phone-today rule 7). Nil for any other state.
    static func makingLine(_ request: TodayBriefRequest?, calendar: Calendar = .current, time: (Date) -> String) -> String? {
        guard let request, ["queued", "making"].contains(request.state), let ready = request.readyDate else { return nil }
        let kind = calendar.component(.hour, from: ready) < 15 ? String(localized: "today.brief.kindMorning")
            : String(localized: "today.brief.kindEvening")
        return String.localizedStringWithFormat(String(localized: "today.brief.making"), kind, time(ready))
    }

    /// The same line from the computer's own status (B1): any build in progress, not only one the phone asked for.
    /// The time shows only while `readyAt` is ahead.
    static func makingLine(_ making: MobileBriefStatus.Making?, now: Date, time: (Date) -> String) -> String? {
        guard let making else { return nil }
        let kind = making.kind == "morning" ? String(localized: "today.brief.kindMorning")
            : making.kind == "evening" ? String(localized: "today.brief.kindEvening") : String(localized: "today.brief.kindNew")
        if let ready = making.readyAt.flatMap(RelayTime.parse), ready > now {
            return String.localizedStringWithFormat(String(localized: "today.brief.making"), kind, time(ready))
        }
        return String.localizedStringWithFormat(String(localized: "today.brief.makingNoTime"), kind)
    }

    /// The brief's first sentences, at most `limit`, from its words other than the closing question. Greetings alone
    /// are passed over, as on the old card's opening paragraph.
    static func firstSentences(_ brief: MobileBriefV1, limit: Int = 3, locale: Locale = .current) -> String? {
        let paragraphs = brief.narrative.enumerated().compactMap { index, paragraph -> String? in
            guard index != brief.closingParagraphIndex,
                  let text = paragraph.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty,
                  !isGreeting(text) else { return nil }
            return text
        }
        let joined = paragraphs.joined(separator: " ")
        guard !joined.isEmpty else { return opening(brief)?.text }
        var sentences: [String] = []
        joined.enumerateSubstrings(in: joined.startIndex..., options: [.bySentences, .localized]) { sentence, _, _, stop in
            if let sentence = sentence?.trimmingCharacters(in: .whitespaces), !sentence.isEmpty { sentences.append(sentence) }
            if sentences.count >= limit { stop = true }
        }
        return sentences.isEmpty ? joined : sentences.joined(separator: " ")
    }

    /// The scheduled brief that is due while the Mac is asleep and that the current brief does not cover: its time, or
    /// nil. A slot counts from its time today or yesterday, and the current brief covers it when it was made at or after
    /// the slot (within the late tolerance).
    static func dueWhileAsleep(_ schedule: MobileBriefSchedule, current: MobileBriefV1, asleep: Bool, now: Date,
                               calendar: Calendar = .current) -> Date? {
        guard asleep else { return nil }
        return uncoveredSlot(schedule, current: current, now: now, calendar: calendar)
    }

    /// The scheduled brief that is due and not here because this phone has not reached the computer since its slot
    /// (phone-today rule 7; computer-conformance-answers v1.4.0): its time, or nil. It is a different state from the
    /// computer asleep, where the phone reached the relay and the computer did not answer; that line wins.
    static func dueWhileOutOfTouch(_ schedule: MobileBriefSchedule, current: MobileBriefV1, asleep: Bool,
                                   lastConnected: Date?, now: Date, calendar: Calendar = .current) -> Date? {
        guard !asleep, let due = uncoveredSlot(schedule, current: current, now: now, calendar: calendar) else { return nil }
        if let lastConnected, lastConnected >= due { return nil }
        return due
    }

    /// "Your 7:00 AM brief comes when this phone reaches your computer." Never "missed", never "late".
    static func outOfTouchLine(_ due: Date, time: (Date) -> String) -> String {
        String.localizedStringWithFormat(String(localized: "today.phoneBrief"), time(due))
    }

    /// The newest slot at or before now (today or yesterday) that the current brief does not cover.
    private static func uncoveredSlot(_ schedule: MobileBriefSchedule, current: MobileBriefV1, now: Date,
                                      calendar: Calendar) -> Date? {
        let times = [schedule.morning, schedule.evening].compactMap { $0 }
        let days = [0, -1].compactMap { calendar.date(byAdding: .day, value: $0, to: now) }
        let due = days.flatMap { day in times.compactMap { clockDate($0, on: day, calendar: calendar) } }
            .filter { $0 <= now }
            .max()
        guard let due else { return nil }
        let made = current.asOfDate ?? .distantPast
        return made >= due.addingTimeInterval(-BriefTitle.lateTolerance) ? nil : due
    }

    /// "Your 6:00 PM brief comes when your computer wakes. It's asleep." The last brief stays under its own time.
    static func waitingLine(_ due: Date, time: (Date) -> String) -> String {
        String.localizedStringWithFormat(String(localized: "today.brief.asleep"), time(due))
    }

    /// The local time of an "HH:mm" slot on a day, or nil for any other text.
    static func clockDate(_ value: String, on day: Date, calendar: Calendar = .current) -> Date? {
        let parts = value.split(separator: ":").compactMap { Int($0) }
        guard parts.count == 2, (0..<24).contains(parts[0]), (0..<60).contains(parts[1]) else { return nil }
        return calendar.date(bySettingHour: parts[0], minute: parts[1], second: 0, of: day)
    }
}

/// Whether the brief card is folded to its title and two lines (phone-today rule 5): after the first answer in Your
/// work, or once the brief has been heard or read in full. Kept per brief on this phone; it never leaves the screen.
enum TodayBriefFold {
    private static let key = "today.briefFolded"

    static func isFolded(_ briefID: String, defaults: UserDefaults = .standard) -> Bool {
        (defaults.stringArray(forKey: key) ?? []).contains(briefID)
    }

    static func fold(_ briefID: String, defaults: UserDefaults = .standard) {
        var folded = defaults.stringArray(forKey: key) ?? []
        guard !folded.contains(briefID) else { return }
        folded.append(briefID)
        defaults.set(Array(folded.suffix(30)), forKey: key)
    }
}

/// Whether the wordless placeholder rows have been shown for this pairing. They show once: the first time the card has no
/// brief to show after pairing (design section 2).
enum TodayBriefPlaceholder {
    static func key(hubID: String) -> String { "today.briefPlaceholderShown." + hubID }

    static func wasShown(hubID: String, defaults: UserDefaults = .standard) -> Bool {
        defaults.bool(forKey: key(hubID: hubID))
    }

    static func markShown(hubID: String, defaults: UserDefaults = .standard) {
        defaults.set(true, forKey: key(hubID: hubID))
    }

    static func reset(hubID: String, defaults: UserDefaults = .standard) {
        defaults.removeObject(forKey: key(hubID: hubID))
    }
}

/// The Today card for the phone's newest brief (phone-today v1.0.0 section 1). It is always on the first screen, drawn
/// from what this phone saved, and a newer brief replaces it in place: never a banner asking to be tapped.
struct TodayBriefCardView: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let current: MobileBriefV1
    let hubID: String
    @ObservedObject var player: BriefAudioPlayer
    var topics: TopicStore? = nil
    var onPrepareInTopic: ((String, String) -> Bool)? = nil
    let canMakeBrief: Bool
    let now: Date
    var onPastBriefs: () -> Void
    /// Folded to its title and two lines once the person moved on (rule 5). Tapping unfolds it.
    var folded = false

    @EnvironmentObject private var brief: BriefStore
    @EnvironmentObject private var todayStore: TodayStore
    @Environment(\.locale) private var locale
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// Whether the shown brief's audio is on the phone. It is read when the brief or the audio changes, not on every
    /// playback tick, which re-renders this card.
    @State private var hasAudio = false
    @State private var unfolded = false
    @State private var readingAll = false

    var body: some View {
        let shown = current
        let holdsBrief = player.started && player.hubID == hubID && player.brief?.identifier == shown.identifier
        let asleep = todayStore.hubUnreachable || brief.hubOnline == false
        let schedule = todayStore.snapshot?.schedule ?? MobileBriefSchedule(morning: nil, evening: nil)
        let due = TodayBriefCard.dueWhileAsleep(schedule, current: shown, asleep: asleep, now: now)
        let outOfTouch = TodayBriefCard.dueWhileOutOfTouch(schedule, current: shown, asleep: asleep,
                                                           lastConnected: todayStore.lastConnected, now: now)
        let weekShown = shown.yourWeek != nil
        let entry = HistoryEntry(id: shown.identifier, slot: shown.slot, localDate: shown.localDate,
                                 asOf: shown.asOfDate, narrativeStatus: shown.narrativeStatus, isCached: true,
                                 displaySlot: shown.displaySlot, timezone: shown.timezone,
                                 kind: shown.kind, dueAt: shown.dueAt)
        // At accessibility sizes the card folds to its title so Your work starts on the first screen (pass-access 7).
        let isFolded = (folded || dynamicTypeSize.isAccessibilitySize) && !unfolded
        VStack(alignment: .leading, spacing: 10) {
            if weekShown {
                YansuCard {
                    // One card for a week brief (P139): Your week's title and ··· menu, its Play or recording line,
                    // its words and sections, then Read all at the bottom, as the ordinary card has it.
                    VStack(alignment: .leading, spacing: 14) {
                        BriefCardsView(brief: shown, showsAttention: false, showsParagraphs: true, topics: topics,
                                       onPrepareInTopic: onPrepareInTopic, preview: true, weekEmbedded: true,
                                       weekBelowTitle: weekPlayLine(shown, holdsBrief: holdsBrief, asleep: asleep),
                                       weekMakeNow: { Task { await todayStore.requestFresh() } }, weekCanMakeNow: canMakeBrief)
                        Button { readingAll = true } label: {
                            Text("today.brief.readAll")
                                .font(.subheadline)
                                .foregroundStyle(YansuTheme.accent)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("feed.readAll")
                    }
                }
            } else {
                card(shown, entry: entry, holdsBrief: holdsBrief, asleep: asleep, isFolded: isFolded, due: due,
                     outOfTouch: outOfTouch)
            }
        }
        .id(shown.identifier)
        .transition(.opacity)
        .animation(reduceMotion ? nil : .easeInOut(duration: 0.3), value: shown.identifier)
        .animation(reduceMotion ? nil : .easeInOut(duration: 0.25), value: isFolded)
        .briefOnScreen(shown.identifier)
        .navigationDestination(isPresented: $readingAll) {
            BriefDetailView(entry: entry, player: player,
                            fallbackLead: todayStore.snapshot?.briefs.first { $0.id == shown.identifier }?.lead)
                .onAppear { TodayBriefFold.fold(shown.identifier) }
        }
        .onChange(of: AudioCheck(briefID: shown.identifier, revision: player.audioRevision), initial: true) { _, _ in
            hasAudio = player.hasAudioOnPhone(brief: shown, hubID: hubID)
        }
        .onChange(of: holdsBrief && player.finished) { _, heard in
            if heard { TodayBriefFold.fold(shown.identifier) }
        }
        .onChange(of: folded) { _, value in if value { unfolded = false } }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("feed.brief")
    }

    private func card(_ shown: MobileBriefV1, entry: HistoryEntry, holdsBrief: Bool, asleep: Bool, isFolded: Bool,
                      due: Date?, outOfTouch: Date?) -> some View {
        let playShown = TodayBriefCard.showsPlay(hasWords: shown.hasWords, hasAudio: hasAudio, holdsBrief: holdsBrief,
                                                 audioOnComputer: shown.audio?.isFinished == true && !shown.audioIsWithdrawn,
                                                 computerOnline: asleep ? false : brief.hubOnline)
        return VStack(alignment: .leading, spacing: 10) {
            if let due {
                // A newer brief is made but not here, the computer asleep: one amber line above the title (rule 7).
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Image(systemName: "clock").font(.caption).foregroundStyle(YansuTheme.warning)
                    Text(TodayBriefCard.waitingLine(due, time: time))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.warning)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("feed.brief.waiting")
            } else if let outOfTouch {
                // A newer brief is due but this phone hasn't reached the computer since its slot: the last brief stays
                // under its own time, with one amber line above its title (rule 7).
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Image(systemName: "clock").font(.caption).foregroundStyle(YansuTheme.warning)
                    Text(TodayBriefCard.outOfTouchLine(outOfTouch, time: time))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.warning)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("feed.brief.outOfTouch")
            }
            if let making = TodayBriefCard.makingLine(todayStore.snapshot?.briefStatus?.making, now: now, time: time)
                ?? TodayBriefCard.makingLine(todayStore.snapshot?.briefRequest, time: time) {
                Text(making)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("feed.brief.making")
            }
            HStack(alignment: .center, spacing: 8) {
                Text(verbatim: TodayAwaitingBrief.cardTitle(shown, now: now, locale: locale))
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .accessibilityIdentifier("feed.brief.title")
                Spacer(minLength: 0)
                Menu {
                    Button(String(localized: "today.brief.menuPast"), action: onPastBriefs)
                        .accessibilityIdentifier("feed.pastBriefs")
                    Button(String(localized: "today.brief.menuMake")) { Task { await todayStore.requestFresh() } }
                        .disabled(!canMakeBrief)
                } label: {
                    // P134: the 44 pt tap target stays, but its height no longer adds a band under the title row.
                    Image(systemName: "ellipsis").foregroundStyle(YansuTheme.secondary)
                        .frame(width: 44, height: 44)
                        .padding(.vertical, -12)
                }
                .accessibilityLabel("More options")
                .accessibilityIdentifier("feed.brief.menu")
            }
            if !isFolded {
                if playShown {
                    HStack(spacing: 10) {
                        TodayBriefPlayButton(brief: shown, hubID: hubID, player: player, hasAudio: hasAudio)
                        Text("today.brief.hear")
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(YansuTheme.ink)
                        if let length = shown.audio?.minutesLabel {
                            Text(verbatim: length)
                                .font(.subheadline)
                                .foregroundStyle(YansuTheme.secondary)
                                .accessibilityIdentifier("feed.brief.length")
                        }
                    }
                } else if TodayBriefCard.showsRecordingLine(hasWords: shown.hasWords, playShown: playShown, audio: shown.audio) {
                    Text("Recording on its way")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("feed.brief.recording")
                }
            }
            if let words = TodayBriefCard.firstSentences(shown, locale: locale),
               !(isFolded && dynamicTypeSize.isAccessibilitySize) {
                Text(verbatim: words)
                    .font(.system(size: 16.5))
                    .lineSpacing(3)
                    .foregroundStyle(YansuTheme.ink)
                    .lineLimit(isFolded ? 2 : nil)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("feed.brief.words")
            }
            if !isFolded {
                if todayStore.freshFailed {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text("Couldn't make the new brief.")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                        Spacer(minLength: 0)
                        Button("Make it now") { Task { await todayStore.requestFresh() } }
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(YansuTheme.accent)
                            .disabled(!canMakeBrief)
                            .accessibilityIdentifier("feed.brief.makeNow")
                    }
                    .accessibilityIdentifier("feed.brief.failed")
                }
                if let notice = brief.notice {
                    NoticeBanner(message: notice, dismiss: brief.dismissNotice)
                }
                Button { readingAll = true } label: {
                    Text("today.brief.readAll")
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.accent)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("feed.readAll")
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .contentShape(RoundedRectangle(cornerRadius: 18))
        .onTapGesture { if isFolded { unfolded = true } }
        .accessibilityAction(named: Text("today.brief.unfold")) { unfolded = true }
    }

    /// A week brief's making line and its Play or "Recording on its way" line, which sit under the week's title (P139).
    /// Nil when there is neither, so the card adds no gap.
    private func weekPlayLine(_ shown: MobileBriefV1, holdsBrief: Bool, asleep: Bool) -> AnyView? {
        let making = TodayBriefCard.makingLine(todayStore.snapshot?.briefRequest, time: time)
        let playShown = TodayBriefCard.showsPlay(hasWords: shown.hasWords, hasAudio: hasAudio, holdsBrief: holdsBrief,
                                                 audioOnComputer: shown.audio?.isFinished == true && !shown.audioIsWithdrawn,
                                                 computerOnline: asleep ? false : brief.hubOnline)
        let recording = !playShown && TodayBriefCard.showsRecordingLine(hasWords: shown.hasWords, playShown: playShown, audio: shown.audio)
        guard making != nil || playShown || recording else { return nil }
        return AnyView(VStack(alignment: .leading, spacing: 10) {
            if let making {
                Text(making)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("feed.brief.making")
            }
            if playShown {
                HStack(spacing: 10) {
                    TodayBriefPlayButton(brief: shown, hubID: hubID, player: player, hasAudio: hasAudio)
                    if let length = shown.audio?.durationLabel {
                        Text(verbatim: length)
                            .font(.footnote.weight(.medium))
                            .foregroundStyle(YansuTheme.secondary)
                            .accessibilityIdentifier("feed.brief.length")
                    }
                }
            } else if recording {
                Text("Recording on its way")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("feed.brief.recording")
            }
        })
    }

    private func time(_ date: Date) -> String {
        BriefClock.text(date, locale: locale)
    }

    /// What the card's audio check depends on: the brief shown, and the player's revision when audio is stored or dropped.
    private struct AudioCheck: Equatable {
        let briefID: String
        let revision: Int
    }
}

/// The Play control on the Today card: a play or pause symbol with a ring for the progress of the brief it plays. It shows
/// no spinner; a tap while the audio is still being prepared simply waits.
private struct TodayBriefPlayButton: View {
    let brief: MobileBriefV1
    let hubID: String
    @ObservedObject var player: BriefAudioPlayer
    /// Whether the audio is on the phone. When it is not, the tap fetches it from the computer.
    let hasAudio: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let selected = player.matches(brief: brief, hubID: hubID)
        let playing = selected && player.state == .playing
        let progress = selected && player.duration > 0 ? min(1, max(0, player.position / player.duration)) : 0
        Button {
            let tappedAt = ProcessInfo.processInfo.systemUptime
            Task {
                await Task.yield()
                await player.toggle(brief: brief, hubID: hubID, tappedAt: tappedAt, localFirst: hasAudio)
            }
        } label: {
            Image(systemName: playing ? "pause.fill" : "play.fill")
                .contentTransition(.symbolEffect(.replace))
                .animation(reduceMotion ? nil : .easeInOut(duration: 0.25), value: playing)
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(YansuTheme.onSolid)
                .frame(width: 38, height: 38)
                .background(YansuTheme.solid, in: Circle())
                .overlay {
                    if progress > 0 {
                        Circle()
                            .trim(from: 0, to: progress)
                            .stroke(YansuTheme.accent, style: StrokeStyle(lineWidth: 2, lineCap: .round))
                            .rotationEffect(.degrees(-90))
                            .padding(-3)
                    }
                }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(playing ? String(localized: "Pause your brief")
                            : brief.audio?.minutesLabel.map { String(localized: "today.brief.hear") + ", " + $0 }
                                ?? String(localized: "Hear your brief"))
        .accessibilityValue(selected ? "\(BriefPlaybackTime.text(player.position)) / \(BriefPlaybackTime.text(player.duration))" : "")
        .accessibilityIdentifier("brief.hearBrief")
    }
}
