import Foundation

// What the watch heard, as conversations (PROTOCOL.md 24.11), for the Watch tab
// (design's phone-watch v1.0.0). The phone shows these and does not keep them:
// they live in memory for as long as the app runs.

/// The answer of `GET /recordings/conversations`.
struct WatchConversationsAnswer: Decodable, Equatable, Sendable {
    struct Hour: Decodable, Equatable, Sendable {
        let start: String
        var recordedSeconds: Int = 0
        var talkSeconds: Int = 0
        var pausedSeconds: Int = 0
        var current: Bool? = nil

        enum CodingKeys: String, CodingKey { case start, recordedSeconds, talkSeconds, pausedSeconds, current }
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            start = try c.decode(String.self, forKey: .start)
            recordedSeconds = try c.decodeIfPresent(Int.self, forKey: .recordedSeconds) ?? 0
            talkSeconds = try c.decodeIfPresent(Int.self, forKey: .talkSeconds) ?? 0
            pausedSeconds = try c.decodeIfPresent(Int.self, forKey: .pausedSeconds) ?? 0
            current = try c.decodeIfPresent(Bool.self, forKey: .current)
        }
    }

    struct Span: Decodable, Equatable, Sendable {
        let startAt: String
        var endAt: String? = nil
    }

    var dayStart: String? = nil
    var hours: [Hour] = []
    var closed: [Span] = []
    var conversations: [WatchConversation] = []
    var truncated: Bool? = nil

    enum CodingKeys: String, CodingKey { case dayStart, hours, closed, conversations, truncated }
    // A list the computer leaves out reads as empty, so an older or shorter answer still shows.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        dayStart = try c.decodeIfPresent(String.self, forKey: .dayStart)
        hours = try c.decodeIfPresent([Hour].self, forKey: .hours) ?? []
        closed = try c.decodeIfPresent([Span].self, forKey: .closed) ?? []
        conversations = try c.decodeIfPresent([WatchConversation].self, forKey: .conversations) ?? []
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated)
    }
}

struct WatchConversation: Decodable, Equatable, Identifiable, Sendable {
    struct Followup: Decodable, Equatable, Sendable {
        let text: String
        var owner: String? = nil
        var person: String? = nil
        var due: String? = nil
    }

    /// Who a speaker might be, to be judged (PROTOCOL.md 24.13).
    struct SpeakerGuess: Decodable, Equatable, Sendable {
        let speaker: String
        let name: String
        var reason: String? = nil
        var eventStartAt: Int64? = nil
    }

    let id: String
    var name: String? = nil
    let startAt: String
    let endAt: String
    var talkSeconds: Int = 0
    var speakers: [String] = []
    var marks: [String] = []
    var recordingIds: [String] = []
    var summary: String? = nil
    var decisions: [String] = []
    var followups: [Followup] = []
    var place: String? = nil
    var heartRate: String? = nil
    /// The owner's own follow-ups as items of the one list of work.
    var itemIds: [String] = []
    /// The names the person gave its speakers, by label, and the guess still open.
    var speakerNames: [String: String] = [:]
    var speakerGuess: SpeakerGuess? = nil

    enum CodingKeys: String, CodingKey {
        case id, name, startAt, endAt, talkSeconds, speakers, marks, recordingIds, summary, decisions, followups, place, heartRate
        case itemIds, speakerNames, speakerGuess
    }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name)
        startAt = try c.decode(String.self, forKey: .startAt)
        endAt = try c.decode(String.self, forKey: .endAt)
        talkSeconds = try c.decodeIfPresent(Int.self, forKey: .talkSeconds) ?? 0
        speakers = try c.decodeIfPresent([String].self, forKey: .speakers) ?? []
        marks = try c.decodeIfPresent([String].self, forKey: .marks) ?? []
        recordingIds = try c.decodeIfPresent([String].self, forKey: .recordingIds) ?? []
        summary = try c.decodeIfPresent(String.self, forKey: .summary)
        decisions = try c.decodeIfPresent([String].self, forKey: .decisions) ?? []
        followups = try c.decodeIfPresent([Followup].self, forKey: .followups) ?? []
        place = try c.decodeIfPresent(String.self, forKey: .place)
        heartRate = try c.decodeIfPresent(String.self, forKey: .heartRate)
        itemIds = try c.decodeIfPresent([String].self, forKey: .itemIds) ?? []
        speakerNames = try c.decodeIfPresent([String: String].self, forKey: .speakerNames) ?? [:]
        speakerGuess = try c.decodeIfPresent(SpeakerGuess.self, forKey: .speakerGuess)
    }

    var start: Date? { WatchConversationDates.parse(startAt) }
    var end: Date? { WatchConversationDates.parse(endAt) }
    var hasNote: Bool { !(summary ?? "").isEmpty || !decisions.isEmpty }
    /// What the wearer said they would do.
    var mine: [Followup] { followups.filter { $0.owner == "me" } }
    /// Speakers nobody has put a name to.
    var unknownSpeakers: [String] { speakers.filter { $0.hasPrefix("speaker_") && speakerNames[$0] == nil } }
}

/// The answer of `GET /recordings/conversation?id=`.
struct WatchConversationDetail: Decodable, Equatable, Sendable {
    struct Line: Decodable, Equatable, Sendable {
        let at: String
        var speaker: String? = nil
        let text: String
        var marked: Bool? = nil
    }

    let conversation: WatchConversation
    var lines: [Line] = []
    var truncated: Bool? = nil

    enum CodingKeys: String, CodingKey { case conversation, lines, truncated }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        conversation = try c.decode(WatchConversation.self, forKey: .conversation)
        lines = try c.decodeIfPresent([Line].self, forKey: .lines) ?? []
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated)
    }
}

enum WatchConversationDates {
    static func parse(_ value: String) -> Date? {
        let plain = ISO8601DateFormatter()
        if let date = plain.date(from: value) { return date }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: value)
    }
}

extension RecordingHubAPI {
    /// Nil when the computer does not serve conversations (an older build).
    func recordingConversations() async throws -> WatchConversationsAnswer? { nil }
    func recordingConversation(id: String) async throws -> WatchConversationDetail? { nil }
    func watchNotes() async throws -> Bool? { nil }
    func setWatchNotes(_ on: Bool) async throws -> Bool { throw HubRequestError.invalidIdentifier }
    func answerSpeaker(conversation: String, speaker: String, answer: String, name: String) async throws -> WatchConversation? { nil }
    func watchListening() async throws -> WatchListeningSetting? { nil }
    func turnWatchListeningOff() async throws -> WatchListeningSetting { throw HubRequestError.invalidIdentifier }
    func watchListeningStarted(at: Date) async throws -> WatchListeningSetting { throw HubRequestError.invalidIdentifier }
}

extension HubChannel {
    func recordingConversations() async throws -> WatchConversationsAnswer? {
        guard try await abilities().recordingConversations == true else { return nil }
        return try await perform(.recordingConversations).payload(WatchConversationsAnswer.self)
    }

    func recordingConversation(id: String) async throws -> WatchConversationDetail? {
        guard !id.isEmpty, id.count <= 120, try await abilities().recordingConversations == true else { return nil }
        return try await perform(.recordingConversation(id: id)).payload(WatchConversationDetail.self)
    }

    func watchNotes() async throws -> Bool? { try await status().watchNotes?.on }

    func watchListening() async throws -> WatchListeningSetting? { try await status().watchListening }

    func turnWatchListeningOff() async throws -> WatchListeningSetting {
        try await perform(.write(path: "/watch/listening", body: "{\"off\":true}")).payload(WatchListeningSetting.self)
    }

    /// The wearer started listening on the watch at `date`: the off ends then.
    func watchListeningStarted(at date: Date) async throws -> WatchListeningSetting {
        let body = try JSONEncoder().encode(WatchListeningStart(off: false, startedAt: ISO8601DateFormatter().string(from: date)))
        return try await perform(.write(path: "/watch/listening", body: String(decoding: body, as: UTF8.self)))
            .payload(WatchListeningSetting.self)
    }

    func answerSpeaker(conversation: String, speaker: String, answer: String, name: String) async throws -> WatchConversation? {
        guard try await abilities().recordingSpeakers == true else { return nil }
        let body = try JSONEncoder().encode(["conversationId": conversation, "speaker": speaker, "answer": answer, "name": name])
        return try await perform(.write(path: "/recordings/conversation/speaker", body: String(decoding: body, as: UTF8.self)))
            .payload(WatchConversation.self)
    }

    func setWatchNotes(_ on: Bool) async throws -> Bool {
        try await perform(.write(path: "/settings/watch-notes", body: "{\"on\":\(on)}")).payload(WatchNotesSetting.self).on
    }

    func watchContextChoices() async throws -> WatchContextSettingsAnswer? {
        let status = try await status()
        guard let heartRate = status.watchHeartRate, let place = status.watchPlace else { return nil }
        return WatchContextSettingsAnswer(heartRate: heartRate, place: place)
    }

    func setWatchHeartRate(_ on: Bool) async throws -> Bool {
        try await perform(.write(path: "/settings/watch-heart-rate", body: "{\"on\":\(on)}")).payload(WatchNotesSetting.self).on
    }

    func setWatchPlace(_ on: Bool) async throws -> Bool {
        try await perform(.write(path: "/settings/watch-place", body: "{\"on\":\(on)}")).payload(WatchNotesSetting.self).on
    }

    func reportWatchPairing(_ paired: Bool) async throws -> Bool {
        guard try await status().watchPairing == true else { return false }
        _ = try await perform(.write(path: "/watch/pairing", body: "{\"paired\":\(paired)}"))
        return true
    }
}

private struct WatchListeningStart: Encodable {
    let off: Bool
    let startedAt: String
}

/// One day of earlier conversations, folded.
struct WatchConversationDay: Equatable, Identifiable, Sendable {
    let day: Date
    let conversations: [WatchConversation]
    var id: Date { day }
    var talkSeconds: Int { conversations.reduce(0) { $0 + $1.talkSeconds } }
}

/// What the Now card says.
struct WatchNow: Equatable, Sendable {
    enum Kind: Equatable, Sendable { case listening, closedEyes, offToday, notListening, notNear, setUp, unknown }
    let kind: Kind
    let word: String
    let line: String
}

enum WatchTabCopy {
    /// The watch's state in one word and one line. `pauseUntil` is when a pause ends by itself.
    /// `since` is when the watch came into its state, as this phone saw it.
    static func now(connection: WatchConnection, status: WatchLiveStatus?, pauseUntil: Date?, lastHeard: Date?,
                    since: Date? = nil, now: Date = Date(), calendar: Calendar = .current,
                    locale: Locale = .current) -> WatchNow {
        // "9:40 AM", "7:15 PM yesterday", or the day and the time.
        let when = since.map { sinceText($0, now: now, calendar: calendar, locale: locale) }
        switch connection {
        case .appMissing:
            return WatchNow(kind: .setUp, word: String(localized: "Put Isoform on your watch"),
                            line: String(localized: "Your Apple Watch is paired with this phone. Isoform isn't on it yet."))
        case .appAway:
            // Only what is known: it was there, and it does not answer now. What was recorded
            // stays where it is, and the rest of the page stays as it was.
            let heard = lastHeard.map { String(localized: "Last heard \(clock($0, locale: locale)).") + " " } ?? ""
            return WatchNow(kind: .unknown, word: String(localized: "Your watch isn't answering"),
                            line: heard + String(localized: "After an update Isoform comes back on your watch by itself. If it doesn't, install it from the Watch app on this phone."))
        case .ready(reachable: false) where status == .recording || status == nil:
            let heard = lastHeard.map { String(localized: "Last heard \(clock($0, locale: locale)).") + " " } ?? ""
            return WatchNow(kind: .notNear, word: String(localized: "Not near this phone"),
                            line: heard + String(localized: "It keeps listening and sends when it's near."))
        default:
            break
        }
        switch status {
        case .recording:
            return WatchNow(kind: .listening, word: String(localized: "Listening"), line: String(localized: "On your wrist"))
        case .waitingForMicrophone:
            let lead = when.map { String(localized: "A call has the microphone, since \($0).") } ?? String(localized: "A call has the microphone.")
            return WatchNow(kind: .notListening, word: String(localized: "Not listening"),
                            line: lead + " " + String(localized: "It listens again by itself after."))
        case .pausedOnComputer, .pausedOnWatch:
            let word = pauseUntil.map { String(localized: "Closed Eyes until \(clock($0, locale: locale))") } ?? String(localized: "Closed Eyes")
            var line = String(localized: "Nothing is heard or kept, on every device.")
            if status == .pausedOnComputer { line += " " + String(localized: "Closed Eyes on your computer") + "." }
            return WatchNow(kind: .closedEyes, word: word, line: line)
        case .stoppedToday:
            // The watch's own hold (watch-off-phone v1.0.0 rule 4); it says when.
            let line = when.map { String(localized: "You held the mark to stop, at \($0). It listens again tomorrow.") }
                ?? String(localized: "You held the mark to stop. It listens again tomorrow.")
            return WatchNow(kind: .offToday, word: String(localized: "Off for today"), line: line)
        case .microphoneOff:
            return WatchNow(kind: .notListening, word: String(localized: "Not listening"),
                            line: String(localized: "Allow the microphone on your watch"))
        case .notRecording:
            // Why it stopped is not known to the phone yet: it says since when (rule 8).
            let lead = when.map { String(localized: "Since \($0).") + " " } ?? ""
            return WatchNow(kind: .notListening, word: String(localized: "Not listening"),
                            line: lead + String(localized: "Tap the mark on your watch to listen."))
        case nil:
            return WatchNow(kind: .unknown, word: String(localized: "Your watch"),
                            line: String(localized: "Open Isoform on your watch to start"))
        }
    }

    /// "9:40 AM" today, "7:15 PM yesterday", else the day and the time.
    static func sinceText(_ date: Date, now: Date = Date(), calendar: Calendar = .current, locale: Locale = .current) -> String {
        let time = clock(date, locale: locale, timeZone: calendar.timeZone)
        if calendar.isDate(date, inSameDayAs: now) { return time }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(date, inSameDayAs: yesterday) {
            return String(localized: "\(time) yesterday")
        }
        var style = Date.FormatStyle(date: .abbreviated, time: .omitted)
        style.locale = locale
        style.timeZone = calendar.timeZone
        return date.formatted(style) + ", " + time
    }

    /// "49 min of talk · waiting for your computer": an hour the computer has not named yet.
    static func waitingRow(_ row: WatchRecordingRow) -> String {
        String(localized: "\(duration(Int(row.durationSeconds))) of talk · waiting for your computer")
    }

    static func clock(_ date: Date, locale: Locale = .current, timeZone: TimeZone = .current) -> String {
        var style = Date.FormatStyle(date: .omitted, time: .shortened)
        style.locale = locale
        style.timeZone = timeZone
        return date.formatted(style)
    }

    /// "2 h 17 min" or "36 min"; under a minute reads as one.
    static func duration(_ seconds: Int) -> String {
        let minutes = max(1, Int((Double(seconds) / 60).rounded()))
        if minutes < 60 { return String(localized: "\(minutes) min") }
        let hours = minutes / 60, rest = minutes % 60
        return rest == 0 ? String(localized: "\(hours) h") : String(localized: "\(hours) h \(rest) min")
    }

    /// "You", a name, or "Speaker 2".
    static func speaker(_ raw: String) -> String {
        if raw == "you" { return String(localized: "You") }
        if raw.hasPrefix("speaker_"), let number = Int(raw.dropFirst("speaker_".count)) { return String(localized: "Speaker \(number)") }
        return raw
    }

    static func marks(_ count: Int) -> String? {
        switch count {
        case 0: return nil
        case 1: return String(localized: "1 mark")
        default: return String(localized: "\(count) marks")
        }
    }

    /// "2:05 PM–2:41 PM · 36 min · You, Speaker 1 · 1 mark"
    static func meta(_ conversation: WatchConversation, locale: Locale = .current, timeZone: TimeZone = .current) -> String {
        var parts: [String] = []
        if let start = conversation.start, let end = conversation.end {
            parts.append(clock(start, locale: locale, timeZone: timeZone) + "–" + clock(end, locale: locale, timeZone: timeZone))
            parts.append(duration(Int(end.timeIntervalSince(start))))
        }
        if !conversation.speakers.isEmpty {
            parts.append(conversation.speakers.map { conversation.speakerNames[$0] ?? speaker($0) }.joined(separator: ", "))
        }
        if let marks = marks(conversation.marks.count) { parts.append(marks) }
        return parts.joined(separator: " · ")
    }

    /// The row's first line: its name, or "Talk · 1 min" while it has none.
    static func title(_ conversation: WatchConversation) -> String {
        if let name = conversation.name, !name.isEmpty { return name }
        let seconds = conversation.start.flatMap { start in conversation.end.map { Int($0.timeIntervalSince(start)) } } ?? conversation.talkSeconds
        return String(localized: "Talk · \(duration(seconds))")
    }

    /// "2 h 17 min of talk kept · 2 marks"
    static func todaySummary(_ answer: WatchConversationsAnswer, today: [WatchConversation]) -> String {
        let talk = answer.hours.reduce(0) { $0 + $1.talkSeconds }
        var line = talk < 60 ? String(localized: "No talk kept yet") : String(localized: "\(duration(talk)) of talk kept")
        if let marks = marks(today.reduce(0) { $0 + $1.marks.count }) { line += " · " + marks }
        return line
    }

    static func earlierRow(_ day: WatchConversationDay) -> String {
        let count = day.conversations.count
        let lead = count == 1 ? String(localized: "1 conversation") : String(localized: "\(count) conversations")
        return lead + " · " + duration(day.talkSeconds)
    }

    static func toAnswer(_ count: Int) -> String { String(localized: "\(count) to answer") }

    static func whoIs(_ rawSpeaker: String) -> String { String(localized: "Who is \(speaker(rawSpeaker))?") }

    /// The amber line: only when hours have waited for the computer half an hour, or one could not be sent.
    static func waitingLine(_ rows: [WatchRecordingRow], now: Date = Date()) -> String? {
        let failed = rows.filter { $0.stage == .failedToSend || $0.stage == .failedOnComputer }.count
        if failed == 1 { return String(localized: "1 hour couldn't be sent.") }
        if failed > 1 { return String(localized: "\(failed) hours couldn't be sent.") }
        let waiting = rows.filter { $0.stage.waiting && now.timeIntervalSince($0.startedAt.addingTimeInterval($0.durationSeconds)) >= 30 * 60 }.count
        if waiting == 1 { return String(localized: "1 hour is waiting for your computer.") }
        if waiting > 1 { return String(localized: "\(waiting) hours are waiting for your computer.") }
        return nil
    }
}

/// How tall an hour's column is and how it is drawn.
struct WatchHourColumn: Equatable, Identifiable, Sendable {
    enum Kind: Equatable, Sendable { case talk, quiet, closed, current }
    let start: Date
    /// 0 to 1: the talk kept out of the whole hour.
    let height: Double
    let kind: Kind
    var id: Date { start }

    static func columns(_ hours: [WatchConversationsAnswer.Hour]) -> [WatchHourColumn] {
        hours.compactMap { hour in
            guard let start = WatchConversationDates.parse(hour.start) else { return nil }
            let height = min(1, max(0, Double(hour.talkSeconds) / 3600))
            let kind: Kind = hour.current == true ? .current
                : hour.talkSeconds >= 60 ? .talk
                : hour.pausedSeconds >= 60 ? .closed : .quiet
            return WatchHourColumn(start: start, height: height, kind: kind)
        }
    }
}

@MainActor
final class WatchTabModel: ObservableObject {
    static let shared = WatchTabModel()

    /// The last answer this run of the app read; nil until the first one.
    @Published private(set) var answer: WatchConversationsAnswer?
    /// False when the computer is an older build that does not serve conversations.
    @Published private(set) var served = true
    @Published private(set) var failed = false
    @Published private(set) var loading = false
    /// "Notes for long talks" on the computer; nil when it does not say.
    @Published var notesOn: Bool?
    /// "Listen on your watch": false while the phone's off holds; nil when the computer has no such setting.
    @Published var listening: Bool?
    /// Heart rate and Place as the computer has them; nil when it has no such settings (an
    /// older computer: the watch's own switches decide and the phone shows none).
    @Published var heartRateOn: Bool?
    @Published var placeOn: Bool?

    /// Shows what the computer answered. No answer (a computer that does not serve conversations)
    /// drops what was shown before: it may have come from another computer.
    func show(_ answer: WatchConversationsAnswer?) {
        served = answer != nil
        self.answer = answer
        failed = false
    }

    /// Forgets everything read from the computer: the phone was unpaired, or paired again.
    func clear() {
        answer = nil
        served = true
        failed = false
        notesOn = nil
        listening = nil
        heartRateOn = nil
        placeOn = nil
    }

    func split(now: Date = Date(), calendar: Calendar = .current) -> (today: [WatchConversation], earlier: [WatchConversationDay]) {
        Self.split(answer?.conversations ?? [], now: now, calendar: calendar)
    }

    nonisolated static func split(_ conversations: [WatchConversation], now: Date, calendar: Calendar) -> (today: [WatchConversation], earlier: [WatchConversationDay]) {
        var today: [WatchConversation] = []
        var byDay: [Date: [WatchConversation]] = [:]
        for conversation in conversations {
            guard let start = conversation.start else { continue }
            if calendar.isDate(start, inSameDayAs: now) {
                today.append(conversation)
            } else {
                byDay[calendar.startOfDay(for: start), default: []].append(conversation)
            }
        }
        let earlier = byDay.keys.sorted(by: >).prefix(7).map { WatchConversationDay(day: $0, conversations: byDay[$0] ?? []) }
        return (today, Array(earlier))
    }

    func load() async {
        guard !loading else { return }
        loading = true
        defer { loading = false }
        do {
            show(try await WatchLink.shared.conversations())
        } catch {
            failed = true
        }
        if let setting = try? await WatchLink.shared.notesSetting() { notesOn = setting }
        listening = await WatchLink.shared.listeningAllowed()
        show(await WatchLink.shared.contextSettings())
    }

    private func show(_ settings: WatchContextSettingsAnswer?) {
        heartRateOn = settings?.heartRate.on
        placeOn = settings?.place.on
        if placeOn == true { UserDefaults.standard.set(Date(), forKey: Self.placeLastOnKey) }
    }

    /// When Place was last seen on, kept across launches.
    static let placeLastOnKey = "watch.place.lastOn"

    /// Whether Your data names Apple under "Left your devices": while Place is on, and for a
    /// week after it was last on. With Place on, the watch asks Apple to name the area of a
    /// position rounded to about a kilometer (design's data.appleArea).
    var namesAppleForTheArea: Bool { Self.namesApple(placeOn: placeOn, lastOn: UserDefaults.standard.object(forKey: Self.placeLastOnKey) as? Date) }

    nonisolated static func namesApple(placeOn: Bool?, lastOn: Date?, now: Date = Date()) -> Bool {
        if placeOn == true { return true }
        guard let lastOn else { return false }
        return now.timeIntervalSince(lastOn) < 7 * 24 * 3600
    }

    func setHeartRate(_ on: Bool) async {
        let before = heartRateOn
        heartRateOn = on
        if let settings = await WatchLink.shared.setContext(heartRate: on) { show(settings) } else { heartRateOn = before }
    }

    func setPlace(_ on: Bool) async {
        let before = placeOn
        placeOn = on
        if let settings = await WatchLink.shared.setContext(place: on) { show(settings) } else { placeOn = before }
    }

    /// The phone can only turn listening off. It returns false when the computer could not be told.
    func turnListeningOff() async -> Bool {
        guard await WatchLink.shared.turnListeningOff() else { return false }
        listening = false
        return true
    }

    func setNotes(_ on: Bool) async {
        let before = notesOn
        notesOn = on
        do { notesOn = try await WatchLink.shared.setNotes(on) } catch { notesOn = before }
    }
}
