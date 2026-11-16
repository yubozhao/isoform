import SwiftUI

// The one brief view (handoff 2026-10-28, section 3): the words, the controls and the sections of a brief, opened
// from Read all, the mini player, Chat, Past briefs and the Library. It reads the phone's library and never fetches.
struct BriefDetailView: View {
    let entry: HistoryEntry
    @ObservedObject var player: BriefAudioPlayer
    var readOnly = false
    var fallbackLead: String? = nil
    /// Open at the paragraph being spoken (from the mini player), rather than at the top.
    var landsOnSpoken = false
    /// A sheet shows its own Done button.
    var presentedAsSheet = false

    @EnvironmentObject private var store: BriefStore
    @EnvironmentObject private var environment: AppEnvironment
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var chat: ChatStore
    @EnvironmentObject private var router: NotificationRouter
    @EnvironmentObject private var onScreen: BriefOnScreen
    @EnvironmentObject private var work: WorkStore
    @Environment(\.dismiss) private var dismiss
    @Environment(\.tabChromeHeight) private var tabChromeHeight
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var briefID: String
    @State private var following = true
    @State private var tappedParagraph: Int?
    @State private var notice: String?
    @State private var memory: BriefPlaybackMemory?
    @State private var evidence: EvidenceRequest?
    @State private var askRequest: BriefAskRequest?
    @State private var answeredCards: Set<String> = []
    /// The brief from the library, read when it opens and when the library changes, not on every playback tick.
    @State private var brief: MobileBriefV1?
    @State private var loaded = false
    /// The sentence answered Not right, while its reasons show.
    @State private var notRight: WorkItem?

    init(entry: HistoryEntry, player: BriefAudioPlayer, readOnly: Bool = false, fallbackLead: String? = nil,
         landsOnSpoken: Bool = false, presentedAsSheet: Bool = false) {
        self.entry = entry
        self.player = player
        self.readOnly = readOnly
        self.fallbackLead = fallbackLead
        self.landsOnSpoken = landsOnSpoken
        self.presentedAsSheet = presentedAsSheet
        _briefID = State(initialValue: entry.id)
    }

    private var hubID: String? { pairing.paired?.hubID }
    private var canListen: Bool { pairing.paired?.scopes.contains("brief:read") == true && hubID != nil }
    private var canTalk: Bool {
        let scopes = pairing.paired?.scopes ?? []
        return scopes.contains("brief:read") && SettingsView.allowsChat(scopes)
    }

    /// The briefs with words, newest first: the order the swipes and arrows move through.
    private var order: [String] { BriefPages.order(store.cachedBriefs) }
    private var neighbours: (previous: String?, next: String?) { BriefPages.neighbours(of: briefID, in: order) }

    /// The written paragraph being spoken, when the player holds this brief and has started it.
    private func spoken(_ brief: MobileBriefV1, hubID: String) -> Int? {
        guard player.matches(brief: brief, hubID: hubID), player.started else { return nil }
        return player.spokenParagraph
    }

    var body: some View {
        Group {
            if let brief {
                content(brief)
            } else if loaded {
                missing
            } else {
                Color.clear
            }
        }
        .yansuScreen()
        .sheet(item: $evidence) { request in
            EvidenceSheet(request: request)
        }
        .sheet(item: $askRequest, onDismiss: { Task { await refreshAnsweredCards() } }) { BriefAskSheet(request: $0) }
        .task(id: brief?.conversationKey) { await refreshAnsweredCards() }
        .onChange(of: chat.recentTurnEvents) { _, events in
            if events.last?.state == "done" { Task { await refreshAnsweredCards() } }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.view")
        .navigationTitle(String(localized: "Brief"))
        .navigationBarTitleDisplayMode(.inline)
        // The page's own title is the brief's kind and time; the bar stays quiet (board artboard 3).
        .toolbar { ToolbarItem(placement: .principal) { Text(verbatim: "") } }
        .hardTitleEdge()
        .toolbar(.hidden, for: .tabBar)
        .toolbar {
            if presentedAsSheet {
                ToolbarItem(placement: .confirmationAction) {
                    Button(String(localized: "Done")) { dismiss() }
                        .accessibilityIdentifier("brief.done")
                }
            }
        }
        .onAppear {
            onScreen.show(briefID, reading: true)
            loadBrief()
        }
        .task(id: briefID) {
            if brief == nil { _ = await store.fetchOpenedBrief(briefID) }
        }
        .onChange(of: store.hubOnline) { _, online in
            if online == true, brief == nil { Task { _ = await store.fetchOpenedBrief(briefID) } }
        }
        .onDisappear { onScreen.hide(briefID, reading: true) }
        .onChange(of: briefID) { old, new in
            onScreen.hide(old, reading: true)
            onScreen.show(new, reading: true)
            tappedParagraph = nil
            following = true
            loadBrief()
        }
        .onChange(of: store.cachedBriefs) { _, _ in loadBrief() }
        .onChange(of: player.state) { _, _ in loadMemory() }
    }

    private func content(_ brief: MobileBriefV1) -> some View {
        let hub = hubID
        let spokenNow = hub.flatMap { spoken(brief, hubID: $0) }
        return ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    header(brief)
                    playWaiting
                    if let notice {
                        NoticeBanner(message: notice, dismiss: { self.notice = nil })
                            .accessibilityIdentifier("brief.notice")
                    }
                    if canListen, let hub, brief.audio != nil {
                        controls(brief, hubID: hub)
                    }
                    if brief.narrative.isEmpty, brief.narrativeStatus == "unavailable" {
                        NoticeBanner(message: String(localized: "Isoform could not write a summary for this brief. The sections below are complete."))
                    }
                    paragraphs(brief, spokenNow: spokenNow, hubID: hub)
                    SectionLabel(title: String(localized: "In this brief"))
                    BriefContentView(brief: brief, now: Date(), readOnly: readOnly, topics: environment.topics)
                }
                .padding(.horizontal, YansuTheme.gutter)
                .padding(.top, 8)
                .padding(.bottom, 32 + tabChromeHeight)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .environment(\.correctionReveal) { id in proxy.scrollTo(id, anchor: InlineMemoryCorrection.revealAnchor) }
            .simultaneousGesture(swipes)
            .overlay(alignment: .bottom) {
                if !following, spokenNow != nil {
                    backToNow(proxy: proxy, spokenNow: spokenNow)
                }
            }
            .onChange(of: spokenNow) { _, new in
                guard following, let new else { return }
                scroll(proxy, to: new, animated: true)
            }
            .onAppear {
                guard landsOnSpoken, let spokenNow else { return }
                DispatchQueue.main.async { scroll(proxy, to: spokenNow, animated: false) }
            }
        }
    }

    // MARK: Header

    private func header(_ brief: MobileBriefV1) -> some View {
        // The kind and the time it was made, then the day (phone-brief rule 1). Swiping moves between briefs; the
        // arrows and the Past briefs link went (rule 14). Never "late".
        VStack(alignment: .leading, spacing: 4) {
            Text(brief.titleText)
                .font(.system(size: 28, weight: .bold))
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("brief.title")
            Text(String.localizedStringWithFormat(String(localized: "brief.made"), Self.dateText(brief)))
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
                .accessibilityIdentifier("brief.made")
        }
    }

    private static func dateText(_ brief: MobileBriefV1) -> String {
        BriefView.dayTitle(brief)
    }

    // MARK: Controls

    private func controls(_ brief: MobileBriefV1, hubID: String) -> some View {
        let selected = player.matches(brief: brief, hubID: hubID)
        let elapsed = selected ? player.position : (memory?.seconds ?? 0)
        let length = selected ? player.duration : (memory?.duration ?? brief.audio?.durationSeconds ?? 0)
        let playing = selected && player.state == .playing
        let loading = selected && player.state == .loading
        let online = store.hubOnline
        let startable = player.canStart(brief: brief, hubID: hubID, computerOnline: online)
        let played = selected ? player.finished : (memory?.played == true)
        let problem = selected ? Self.problem(player.state) : nil
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                Button {
                    Task { await player.toggle(brief: brief, hubID: hubID) }
                } label: {
                    Group {
                        if loading {
                            ProgressView().controlSize(.small).tint(YansuTheme.onSolid)
                        } else {
                            Image(systemName: playing ? "pause.fill" : "play.fill")
                                .contentTransition(.symbolEffect(.replace))
                        }
                    }
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(YansuTheme.onSolid)
                    .frame(width: 44, height: 44)
                    .background(YansuTheme.solid, in: Circle())
                }
                .buttonStyle(.plain)
                .disabled(!startable || loading)
                .opacity(startable ? 1 : 0.4)
                .accessibilityLabel(playing ? String(localized: "Pause your brief") : String(localized: "Hear your brief"))
                .accessibilityIdentifier("brief.play")
                VStack(alignment: .leading, spacing: 6) {
                    if length > 0, problem == nil {
                        Slider(value: Binding(get: { elapsed }, set: { if selected { player.seek(to: $0) } }), in: 0...length)
                            .tint(YansuTheme.accent)
                            .disabled(!selected)
                            .accessibilityLabel(String(localized: "Brief playback position"))
                            .accessibilityValue("\(BriefPlaybackTime.text(elapsed)) / \(BriefPlaybackTime.text(length))")
                            .accessibilityIdentifier("brief.progress")
                    }
                    HStack {
                        Text(verbatim: BriefPlaybackTime.text(elapsed))
                        Spacer()
                        Text(verbatim: BriefPlaybackTime.text(length))
                    }
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("brief.time")
                }
            }
            if played {
                Text("Played")
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("brief.played")
            } else if !startable {
                // The text shows at once; the audio waits for the computer (phone-brief rule 4).
                Text("brief.audioLater")
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("brief.unavailable")
            }
            if let problem {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Not available yet").font(.subheadline)
                    Text(problem).font(.footnote)
                }
                .foregroundStyle(YansuTheme.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("brief.audioStatus")
            }
            HStack {
                skipButton(seconds: -15, title: String(localized: "brief.back15"), label: String(localized: "Back 15 seconds"),
                           identifier: "brief.back15", enabled: selected)
                Spacer(minLength: 0)
                Menu {
                    ForEach(BriefPlaybackSpeed.allCases, id: \.self) { value in
                        Button(value.label) { player.setSpeed(value) }
                    }
                } label: {
                    Text(verbatim: player.speed.label)
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.accent)
                        .frame(minWidth: 44, minHeight: 36)
                }
                .accessibilityLabel(String(localized: "Playback speed"))
                .accessibilityValue(player.speed.label)
                .accessibilityIdentifier("brief.speed")
                Spacer(minLength: 0)
                skipButton(seconds: 15, title: String(localized: "brief.on15"), label: String(localized: "Ahead 15 seconds"),
                           identifier: "brief.ahead15", enabled: selected)
            }
        }
        .padding(16)
        .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: 18))
    }

    /// Why the audio cannot play right now, when the player has said so.
    private static func problem(_ state: BriefAudioPlayer.State) -> String? {
        switch state {
        case .unavailable(let reason): return reason.errorDescription
        case .failed(let message): return message
        default: return nil
        }
    }

    private func skipButton(seconds: TimeInterval, title: String, label: String, identifier: String,
                            enabled: Bool) -> some View {
        Button { player.skip(by: seconds) } label: {
            Text(verbatim: title)
                .font(.subheadline)
                .foregroundStyle(YansuTheme.accent)
                .frame(minHeight: 36)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.4)
        .accessibilityLabel(label)
        .accessibilityIdentifier(identifier)
    }

    // MARK: Paragraphs

    private func paragraphs(_ brief: MobileBriefV1, spokenNow: Int?, hubID: String?) -> some View {
        let placed = BriefItemPlacement.place(work.items, brief: brief)
        return VStack(alignment: .leading, spacing: 18) {
            ForEach(Array(brief.narrative.enumerated()), id: \.offset) { index, paragraph in
                paragraphView(brief, index: index, text: paragraph.text ?? "", spokenNow: spokenNow, hubID: hubID)
                    .id("brief.paragraph.\(index)")
                // The brief's items sit where it mentions them, the same items as in Your work (phone-brief rule 5).
                ForEach(placed[index] ?? []) { item in
                    BriefWorkItem(item: item, now: Date(),
                                  answer: { action in Task { await work.answer(item, action: action) } },
                                  undo: { Task { await work.undo(item) } })
                }
            }
        }
    }

    @ViewBuilder
    private func paragraphView(_ brief: MobileBriefV1, index: Int, text: String, spokenNow: Int?, hubID: String?) -> some View {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            let isSpoken = spokenNow == index
            VStack(alignment: .leading, spacing: 10) {
                Button {
                    tappedParagraph = tappedParagraph == index ? nil : index
                } label: {
                    Text(verbatim: trimmed)
                        .font(.system(size: 17))
                        .lineSpacing(5)
                        .foregroundStyle(YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        // Read-along: the part being read is highlighted, and the page keeps it in view (rule 3).
                        .padding(isSpoken ? 6 : 0)
                        .background(isSpoken ? YansuTheme.accentWash : .clear, in: RoundedRectangle(cornerRadius: 8))
                        .padding(isSpoken ? -6 : 0)
                        .contentShape(Rectangle())
                        .animation(reduceMotion ? nil : .easeInOut(duration: 0.25), value: isSpoken)
                }
                .buttonStyle(.plain)
                .accessibilityHint(String(localized: "Shows what you can do with this paragraph"))
                .accessibilityAddTraits(isSpoken ? .isSelected : [])
                .accessibilityIdentifier("brief.paragraph.\(index)")
                BriefParagraphSources(brief: brief, refs: brief.narrative[index].refs) { evidence = $0 }
                if tappedParagraph == index || (tappedParagraph == nil && isSpoken) {
                    paragraphActions(brief, index: index, hubID: hubID)
                }
                if notRight?.id == BriefItemPlacement.sentenceID(brief: brief.identifier, paragraph: index), let item = notRight {
                    WorkReasonPicker(item: item, pick: { reason in
                        Task { await sendNotRight(item, reason: reason) }
                        notRight = nil
                    }, undo: { notRight = nil }, skip: {
                        Task { await sendNotRight(item, reason: nil) }
                        notRight = nil
                    })
                }
                // The chips stay until every computer lists the brief's items and sentences (review 2026-11-15).
                if canTalk, let card = brief.paragraphChip(forParagraph: index) {
                    Button { put(card, brief: brief) } label: {
                        HStack(spacing: 5) {
                            Text(verbatim: card.label ?? "")
                            if answeredCards.contains(card.id) { Image(systemName: "checkmark").accessibilityHidden(true) }
                        }
                    }
                    .buttonStyle(YansuChipButtonStyle())
                    .accessibilityValue(answeredCards.contains(card.id) ? String(localized: "Answered") : "")
                    .accessibilityHint(String(localized: "Asks this in a sheet"))
                    .accessibilityIdentifier("brief.chip.\(index)")
                }
            }
        }
    }

    /// Reply about this (Chat, with the sentence attached) and Not right (a No on that sentence, with a claim's
    /// reasons); Play from here stays for a brief with audio (phone-brief rule 6).
    private func paragraphActions(_ brief: MobileBriefV1, index: Int, hubID: String?) -> some View {
        HStack(spacing: 16) {
            if canTalk, brief.chatReference != nil, index != brief.closingParagraphIndex {
                Button(String(localized: "brief.reply")) { askAbout(brief, index: index) }
                    .foregroundStyle(YansuTheme.accent)
                    .accessibilityIdentifier("brief.askAbout.\(index)")
            }
            if work.supported == true, let text = brief.narrative[index].text {
                Button(String(localized: "brief.notRight")) {
                    notRight = BriefItemPlacement.sentence(brief: brief.identifier, paragraph: index, text: text)
                }
                .foregroundStyle(YansuTheme.secondary)
                .accessibilityIdentifier("brief.notRight.\(index)")
            }
            if canListen, let hubID, brief.audio != nil {
                Button(String(localized: "Play from here")) {
                    Task { await player.play(brief: brief, hubID: hubID, fromParagraph: index) }
                }
                .foregroundStyle(YansuTheme.secondary)
                .disabled(!player.canStart(brief: brief, hubID: hubID, computerOnline: store.hubOnline))
                .accessibilityIdentifier("brief.playFromHere.\(index)")
            }
        }
        .font(.subheadline)
        .buttonStyle(.plain)
        .fixedSize(horizontal: false, vertical: true)
    }

    private func backToNow(proxy: ScrollViewProxy, spokenNow: Int?) -> some View {
        Button {
            following = true
            if let spokenNow { scroll(proxy, to: spokenNow, animated: true) }
        } label: {
            Text("Back to now")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
                .padding(.horizontal, 18)
                .padding(.vertical, 10)
                .background(YansuTheme.surface, in: Capsule())
                .overlay(Capsule().stroke(YansuTheme.border, lineWidth: 1))
                .shadow(color: YansuTheme.ink.opacity(0.1), radius: 10, y: 4)
        }
        .buttonStyle(.plain)
        .padding(.bottom, 16 + tabChromeHeight)
        .accessibilityIdentifier("brief.backToNow")
    }

    // MARK: Following and paging

    /// A vertical drag stops following the spoken paragraph. A clear sideways swipe moves to the next or previous brief.
    private var swipes: some Gesture {
        DragGesture(minimumDistance: 10)
            .onChanged { value in
                if abs(value.translation.height) > abs(value.translation.width) { following = false }
            }
            .onEnded { value in
                let dx = value.translation.width
                let dy = value.translation.height
                guard abs(dx) > 60, abs(dx) > abs(dy) * 2 else { return }
                if dx < 0 {
                    if let next = neighbours.next { briefID = next }
                } else if let previous = neighbours.previous {
                    briefID = previous
                }
            }
    }

    private func scroll(_ proxy: ScrollViewProxy, to index: Int, animated: Bool) {
        let target = "brief.paragraph.\(index)"
        if animated && !reduceMotion {
            withAnimation(.easeInOut(duration: 0.35)) { proxy.scrollTo(target, anchor: .center) }
        } else {
            proxy.scrollTo(target, anchor: .center)
        }
    }

    // MARK: Actions

    /// Not right goes at once, not through the queue: a computer that doesn't list the sentence answers 404, and the
    /// phone says so instead of dropping it silently (review 2026-11-15).
    private func sendNotRight(_ item: WorkItem, reason: WorkReason?) async {
        do {
            try await work.answerNow(item, action: .no, reason: reason)
            notice = nil
        } catch {
            notice = String(localized: "brief.notRight.failed")
        }
    }

    private func put(_ card: MobileBriefCard, brief: MobileBriefV1) {
        guard var reference = brief.chatReference, brief.sendCards.contains(card) else { return }
        reference.cardId = card.id
        reference.cardMessage = card.message
        reference.ask = true
        let quote = card.paragraph.flatMap { brief.narrative.indices.contains($0) ? brief.narrative[$0].text : nil }
        askRequest = BriefAskRequest(reference: reference, quote: quote, initialQuestion: card.message)
    }

    private func refreshAnsweredCards() async {
        guard let brief, var reference = brief.chatReference else { return }
        reference.ask = true
        answeredCards = await chat.answeredBriefCards(reference: reference, cards: brief.sendCards.map { ($0.id, $0.message) })
    }

    private func askAbout(_ brief: MobileBriefV1, index: Int) {
        guard var reference = brief.chatReference, brief.narrative.indices.contains(index),
              let quote = brief.narrative[index].text else { return }
        reference.ask = true
        reference.paragraph = index
        reference.paragraphText = quote
        askRequest = BriefAskRequest(reference: reference, quote: quote)
    }

    private func loadBrief() {
        brief = store.cachedBrief(withID: briefID)
        loaded = true
        loadMemory()
    }

    private func loadMemory() {
        guard let brief, let hubID else {
            memory = nil
            return
        }
        memory = player.playbackMemory(brief: brief, hubID: hubID)
    }

    private var missing: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                playWaiting
                if let fallbackLead, !fallbackLead.isEmpty {
                    Text(verbatim: fallbackLead).font(.body).foregroundStyle(YansuTheme.ink)
                }
                Text("The rest arrives when your computer is back")
                    .font(.subheadline).foregroundStyle(YansuTheme.secondary)
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.top, 8)
            .padding(.bottom, 32 + tabChromeHeight)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    @ViewBuilder private var playWaiting: some View {
        if environment.pendingBriefPlay == briefID {
            Text(String(localized: "connection.playWhenBack"))
                .font(.footnote).foregroundStyle(YansuTheme.secondary)
                .accessibilityIdentifier("brief.playWaiting")
        }
        if let message = environment.briefPlayNotice {
            NoticeBanner(message: message, dismiss: { environment.briefPlayNotice = nil })
        }
    }
}

extension HistoryEntry {
    var slotLabel: String {
        briefSlotLabel(slot: displaySlot ?? slot, asOf: asOf, timezone: timezone)
    }

    /// The brief's title with its time, the same as the Today card's (see `BriefTitle`).
    var titleText: String {
        BriefTitle.title(BriefTitleInput(entry: self))
    }

    /// The brief and the time it was written, and what keeps it from opening right now.
    var detailLine: String {
        detailLine(namingTheBrief: true)
    }

    func detailLine(namingTheBrief: Bool) -> String {
        var parts = namingTheBrief ? [titleText] : []
        if namingTheBrief == false, let asOf {
            parts.append(BriefView.timeText(asOf))
        }
        if narrativeStatus == "unavailable" {
            parts.append(String(localized: "Summary unavailable"))
        }
        if !isCached {
            parts.append(String(localized: "Opens when your computer is online"))
        }
        return parts.joined(separator: ", ")
    }

    /// "Wednesday, October 15" for the brief's own calendar day.
    var dayTitle: String {
        guard let localDate, let day = BriefDates.day.date(from: localDate) else { return String(localized: "Earlier") }
        let formatter = DateFormatter()
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.locale = .current
        formatter.setLocalizedDateFormatFromTemplate("EEEEMMMMd")
        return formatter.string(from: day)
    }
}
