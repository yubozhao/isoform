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
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 12) {
                pageButton(neighbours.previous, systemImage: "chevron.left",
                           label: String(localized: "Earlier brief"), identifier: "brief.previous")
                Spacer(minLength: 0)
                NavigationLink {
                    PastBriefsView()
                } label: {
                    Text("Past briefs")
                        .font(.footnote.weight(.medium))
                        .foregroundStyle(YansuTheme.accent)
                        .frame(minHeight: 44)
                }
                .accessibilityIdentifier("brief.pastBriefs")
                Spacer(minLength: 0)
                pageButton(neighbours.next, systemImage: "chevron.right",
                           label: String(localized: "Later brief"), identifier: "brief.next")
            }
            Text(brief.titleText)
                .font(.title2.weight(.semibold))
                .foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("brief.title")
            Text(verbatim: Self.dateText(brief))
                .font(.subheadline)
                .foregroundStyle(YansuTheme.secondary)
            if store.hubOnline == false, let asOf = brief.asOfDate {
                Text(String(format: String(localized: "as of %@"), BriefView.timeText(asOf)))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .accessibilityIdentifier("brief.asOf")
            }
            if let late = brief.lateLineText {
                Text(late)
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("brief.late")
            }
        }
    }

    private static func dateText(_ brief: MobileBriefV1) -> String {
        BriefView.dayTitle(brief)
    }

    @ViewBuilder
    private func pageButton(_ target: String?, systemImage: String, label: String, identifier: String) -> some View {
        Button {
            guard let target else { return }
            briefID = target
        } label: {
            Image(systemName: systemImage)
                .font(.body.weight(.semibold))
                .foregroundStyle(target == nil ? YansuTheme.faint : YansuTheme.accent)
                .frame(width: 44, height: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(target == nil)
        .accessibilityLabel(label)
        .accessibilityIdentifier(identifier)
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
        return VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 14) {
                Button {
                    Task { await player.toggle(brief: brief, hubID: hubID) }
                } label: {
                    Group {
                        if loading {
                            ProgressView().controlSize(.small)
                        } else {
                            Image(systemName: playing ? "pause.fill" : "play.fill")
                                .contentTransition(.symbolEffect(.replace))
                        }
                    }
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundStyle(YansuTheme.accent)
                    .frame(width: 52, height: 52)
                    .background(YansuTheme.accentWash, in: Circle())
                }
                .buttonStyle(.plain)
                .disabled(!startable || loading)
                .opacity(startable ? 1 : 0.4)
                .accessibilityLabel(playing ? String(localized: "Pause your brief") : String(localized: "Hear your brief"))
                .accessibilityIdentifier("brief.play")
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: "\(BriefPlaybackTime.text(elapsed)) / \(BriefPlaybackTime.text(length))")
                        .font(.subheadline.monospacedDigit())
                        .foregroundStyle(YansuTheme.ink)
                        .accessibilityIdentifier("brief.time")
                    if played {
                        Text("Played")
                            .font(.footnote.weight(.medium))
                            .foregroundStyle(YansuTheme.secondary)
                            .accessibilityIdentifier("brief.played")
                    } else if !startable {
                        Text("Plays when your computer is back")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityIdentifier("brief.unavailable")
                    }
                }
                Spacer(minLength: 0)
                Menu {
                    ForEach(BriefPlaybackSpeed.allCases, id: \.self) { value in
                        Button(value.label) { player.setSpeed(value) }
                    }
                } label: {
                    Text(verbatim: player.speed.label)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(YansuTheme.accent)
                        .frame(minWidth: 44, minHeight: 44)
                }
                .accessibilityLabel(String(localized: "Playback speed"))
                .accessibilityValue(player.speed.label)
                .accessibilityIdentifier("brief.speed")
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
            } else if length > 0 {
                Slider(value: Binding(get: { elapsed }, set: { if selected { player.seek(to: $0) } }), in: 0...length)
                    .tint(YansuTheme.accent)
                    .disabled(!selected)
                    .accessibilityLabel(String(localized: "Brief playback position"))
                    .accessibilityValue("\(BriefPlaybackTime.text(elapsed)) / \(BriefPlaybackTime.text(length))")
                    .accessibilityIdentifier("brief.progress")
            }
            HStack {
                skipButton(seconds: -15, systemImage: "gobackward.15", label: String(localized: "Back 15 seconds"),
                           identifier: "brief.back15", enabled: selected)
                Spacer(minLength: 0)
                skipButton(seconds: 15, systemImage: "goforward.15", label: String(localized: "Ahead 15 seconds"),
                           identifier: "brief.ahead15", enabled: selected)
            }
        }
    }

    /// Why the audio cannot play right now, when the player has said so.
    private static func problem(_ state: BriefAudioPlayer.State) -> String? {
        switch state {
        case .unavailable(let reason): return reason.errorDescription
        case .failed(let message): return message
        default: return nil
        }
    }

    private func skipButton(seconds: TimeInterval, systemImage: String, label: String, identifier: String,
                            enabled: Bool) -> some View {
        Button { player.skip(by: seconds) } label: {
            Image(systemName: systemImage)
                .font(.title2)
                .foregroundStyle(YansuTheme.ink)
                .frame(width: 52, height: 44)
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
        VStack(alignment: .leading, spacing: 22) {
            ForEach(Array(brief.narrative.enumerated()), id: \.offset) { index, paragraph in
                paragraphView(brief, index: index, text: paragraph.text ?? "", spokenNow: spokenNow, hubID: hubID)
                    .id("brief.paragraph.\(index)")
            }
        }
    }

    @ViewBuilder
    private func paragraphView(_ brief: MobileBriefV1, index: Int, text: String, spokenNow: Int?, hubID: String?) -> some View {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            let isSpoken = spokenNow == index
            let muted = spokenNow != nil && !isSpoken
            VStack(alignment: .leading, spacing: 10) {
                Button {
                    if canTalk, brief.chatReference != nil, index != brief.closingParagraphIndex {
                        askAbout(brief, index: index)
                    } else {
                        tappedParagraph = tappedParagraph == index ? nil : index
                    }
                } label: {
                    Text(verbatim: trimmed)
                        .font(.body)
                        .lineSpacing(4)
                        .foregroundStyle(muted ? YansuTheme.secondary : YansuTheme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(Rectangle())
                        .animation(reduceMotion ? nil : .easeInOut(duration: 0.25), value: muted)
                }
                .buttonStyle(.plain)
                .accessibilityHint(String(localized: "Shows what you can do with this paragraph"))
                .accessibilityAddTraits(isSpoken ? .isSelected : [])
                .accessibilityIdentifier("brief.paragraph.\(index)")
                BriefParagraphSources(brief: brief, refs: brief.narrative[index].refs) { evidence = $0 }
                if tappedParagraph == index {
                    paragraphActions(brief, index: index, hubID: hubID)
                }
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

    private func paragraphActions(_ brief: MobileBriefV1, index: Int, hubID: String?) -> some View {
        HStack(spacing: 10) {
            if canListen, let hubID, brief.audio != nil {
                Button(String(localized: "Play from here")) {
                    Task { await player.play(brief: brief, hubID: hubID, fromParagraph: index) }
                }
                .buttonStyle(YansuPillButtonStyle(prominent: true))
                .disabled(!player.canStart(brief: brief, hubID: hubID, computerOnline: store.hubOnline))
                .accessibilityIdentifier("brief.playFromHere.\(index)")
            }
            if canTalk, brief.chatReference != nil {
                Button(String(localized: "Ask about this")) { askAbout(brief, index: index) }
                    .buttonStyle(YansuPillButtonStyle(prominent: false))
                    .accessibilityIdentifier("brief.askAbout.\(index)")
            }
        }
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
        var answered = Set<String>()
        for card in brief.sendCards {
            reference.cardId = card.id
            reference.cardMessage = card.message
            if await chat.savedBriefAsk(reference: reference) != nil { answered.insert(card.id) }
        }
        answeredCards = answered
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
