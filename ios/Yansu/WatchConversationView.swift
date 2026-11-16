import SwiftUI

// A conversation's page (design's phone-watch v1.0.0, artboard 6): its note, what is the wearer's
// to do, who spoke, and the words with the marked lines. The words are read from the computer
// each time and are not kept on the phone.
//
// Not here yet: playing from a mark (the audio is on the computer), the follow-ups as items that
// can be answered (the one list of work), and deleting one conversation everywhere.

struct WatchConversationView: View {
    @State private var conversation: WatchConversation
    @State private var lines: [WatchConversationDetail.Line] = []
    @State private var truncated = false
    @State private var state: LoadState = .loading
    @State private var naming: String?
    @State private var typedName = ""
    @State private var answering = false

    enum LoadState { case loading, loaded, failed }

    init(conversation: WatchConversation) {
        _conversation = State(initialValue: conversation)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(WatchConversationCopy.subtitle(conversation))
                    .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("conversation.subtitle")
                if let guess = conversation.speakerGuess { guessCard(guess) }
                note
                toAnswer
                heard
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.vertical, 12)
        }
        .background(YansuTheme.background)
        .navigationTitle(WatchTabCopy.title(conversation))
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .alert(String(localized: "Who is this?"), isPresented: Binding(get: { naming != nil }, set: { if !$0 { naming = nil } })) {
            TextField(String(localized: "Name"), text: $typedName)
            Button(String(localized: "Save")) {
                if let speaker = naming { Task { await answer(speaker, "edit", typedName) } }
            }
            Button(String(localized: "Cancel"), role: .cancel) {}
        } message: {
            Text("The name is kept on your computer with this conversation.")
        }
    }

    private func load() async {
        do {
            if let detail = try await WatchLink.shared.conversation(conversation.id) {
                conversation = detail.conversation
                lines = detail.lines
                truncated = detail.truncated == true
            }
            state = .loaded
        } catch {
            state = .failed
        }
    }

    private func answer(_ speaker: String, _ answer: String, _ name: String = "") async {
        guard !answering else { return }
        answering = true
        defer { answering = false }
        if let updated = try? await WatchLink.shared.answerSpeaker(conversation: conversation.id, speaker: speaker, answer: answer, name: name) {
            conversation = updated
        }
    }

    private func card<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 10, content: content)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
    }

    private func label(_ text: String) -> some View {
        Text(text).font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
    }

    // MARK: Who is Speaker N?

    private func guessCard(_ guess: WatchConversation.SpeakerGuess) -> some View {
        card {
            Text(WatchConversationCopy.guess(guess))
                .font(.body).foregroundStyle(YansuTheme.ink)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 8) {
                Button(String(localized: "Yes")) { Task { await answer(guess.speaker, "yes") } }
                    .buttonStyle(.borderedProminent).tint(YansuTheme.accent)
                Button(String(localized: "No")) { Task { await answer(guess.speaker, "no") } }
                    .buttonStyle(.bordered)
                Button(String(localized: "Edit")) { typedName = ""; naming = guess.speaker }
                    .buttonStyle(.bordered)
            }
            .disabled(answering)
        }
        .accessibilityIdentifier("conversation.guess")
    }

    // MARK: Note

    @ViewBuilder private var note: some View {
        VStack(alignment: .leading, spacing: 8) {
            label(String(localized: "Note"))
            card {
                if conversation.hasNote {
                    if let summary = conversation.summary, !summary.isEmpty {
                        Text(summary).font(.body).foregroundStyle(YansuTheme.ink).fixedSize(horizontal: false, vertical: true)
                    }
                    ForEach(conversation.decisions.prefix(4), id: \.self) { line in
                        Text(line).font(.body).foregroundStyle(YansuTheme.ink).fixedSize(horizontal: false, vertical: true)
                    }
                } else {
                    Text(WatchConversationCopy.noNote(conversation))
                        .font(.subheadline).foregroundStyle(YansuTheme.secondary)
                }
            }
        }
    }

    // MARK: To answer

    @ViewBuilder private var toAnswer: some View {
        if !conversation.mine.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                label(String(localized: "What you said you'd do"))
                card {
                    ForEach(Array(conversation.mine.enumerated()), id: \.offset) { _, item in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(item.text).font(.body).foregroundStyle(YansuTheme.ink).fixedSize(horizontal: false, vertical: true)
                            if let due = item.due, !due.isEmpty {
                                Text(due).font(.footnote).foregroundStyle(YansuTheme.secondary)
                            }
                        }
                    }
                }
            }
            .accessibilityIdentifier("conversation.toAnswer")
        }
    }

    // MARK: What it heard

    private var heard: some View {
        VStack(alignment: .leading, spacing: 8) {
            label(String(localized: "What it heard", comment: "Section title of a recording's transcript"))
            card {
                switch state {
                case .loading:
                    ProgressView()
                case .failed:
                    Text("The words could not be read from your computer just now.",
                         comment: "A recording's transcript could not be loaded")
                        .font(.footnote).foregroundStyle(YansuTheme.secondary)
                case .loaded:
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(spacing: 6) {
                                if let raw = line.speaker, !raw.isEmpty {
                                    let name = WatchConversationCopy.speaker(raw, in: conversation)
                                    if raw.hasPrefix("speaker_") {
                                        Button(name) { typedName = conversation.speakerNames[raw] ?? ""; naming = raw }
                                            .font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.secondary).buttonStyle(.plain)
                                    } else {
                                        Text(name).font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.accent)
                                    }
                                }
                                if line.marked == true {
                                    Text("marked", comment: "Beside a transcript line the wearer marked")
                                        .font(.caption.weight(.medium)).foregroundStyle(YansuTheme.accent)
                                }
                            }
                            Text(line.text).font(.body).foregroundStyle(YansuTheme.ink).fixedSize(horizontal: false, vertical: true)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    if lines.isEmpty {
                        Text("No words to show.").font(.footnote).foregroundStyle(YansuTheme.secondary)
                    }
                    if truncated {
                        Text("This is the beginning. The rest is on your computer.")
                            .font(.footnote).foregroundStyle(YansuTheme.secondary)
                    }
                }
            }
        }
    }
}

enum WatchConversationCopy {
    /// "Today 2:05 PM–2:41 PM · 36 min · You, Sam · 1 mark", then the place and heart rate when shared.
    static func subtitle(_ conversation: WatchConversation, now: Date = Date(), calendar: Calendar = .current,
                         locale: Locale = .current, timeZone: TimeZone = .current) -> String {
        var first = WatchTabCopy.meta(named(conversation), locale: locale, timeZone: timeZone)
        if let start = conversation.start {
            first = WatchRecordingsCopy.dayTitle(start, now: now, calendar: calendar) + " " + first
        }
        var parts = [first]
        var context: [String] = []
        if let place = conversation.place, !place.isEmpty { context.append(String(localized: "at \(place)")) }
        if let word = heartRate(conversation.heartRate) { context.append(word) }
        if !context.isEmpty { parts.append(context.joined(separator: " · ")) }
        return parts.joined(separator: "\n")
    }

    /// The conversation with its speakers shown by the names the person gave.
    private static func named(_ conversation: WatchConversation) -> WatchConversation {
        var copy = conversation
        copy.speakers = conversation.speakers.map { conversation.speakerNames[$0] ?? $0 }
        return copy
    }

    static func heartRate(_ raw: String?) -> String? {
        switch raw {
        case "calm": return String(localized: "heart rate calm")
        case "raised": return String(localized: "heart rate raised")
        case "high": return String(localized: "heart rate high")
        default: return nil
        }
    }

    /// A speaker as the page names them: the name the person gave, else "You" or "Speaker 2".
    static func speaker(_ raw: String, in conversation: WatchConversation) -> String {
        conversation.speakerNames[raw] ?? WatchTabCopy.speaker(raw)
    }

    /// "Speaker 2 might be Priya Shah: your 2:00 PM meeting was with them."
    static func guess(_ guess: WatchConversation.SpeakerGuess, locale: Locale = .current, timeZone: TimeZone = .current) -> String {
        let who = WatchTabCopy.speaker(guess.speaker)
        guard guess.reason == "calendar", let at = guess.eventStartAt else {
            return String(localized: "\(who) might be \(guess.name).")
        }
        let time = WatchTabCopy.clock(Date(timeIntervalSince1970: Double(at) / 1000), locale: locale, timeZone: timeZone)
        return String(localized: "\(who) might be \(guess.name): your \(time) meeting was with them.")
    }

    static func noNote(_ conversation: WatchConversation) -> String {
        let seconds = conversation.start.flatMap { start in conversation.end.map { $0.timeIntervalSince(start) } } ?? 0
        return seconds < 5 * 60 ? String(localized: "Notes come for conversations over 5 minutes.")
            : String(localized: "No note for this conversation yet.")
    }
}
