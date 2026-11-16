import AVFoundation
import SwiftUI

// The phone's Watch page: where every watch recording is on its way to the computer, a way to
// push the sync along, and each recording's audio, words and delete. It is a plain page on
// purpose (watch handoff v1.0.0, section 7); the designed page replaces it.

/// Whether this phone has a watch with Isoform on it.
enum WatchConnection: Equatable, Sendable {
    case unknown
    case noWatch
    case appMissing
    case ready(reachable: Bool)

    /// A watch is paired, with or without the app: the Watch page is worth showing.
    var hasWatch: Bool {
        switch self {
        case .appMissing, .ready: return true
        case .unknown, .noWatch: return false
        }
    }
}

/// Where one recording is, as the phone's list says it.
enum WatchRecordingStage: Equatable, Sendable {
    case recording
    case onWatch
    case onPhone
    case sending
    case onComputer
    case transcribed
    case failedToSend
    case failedOnComputer

    /// The computer does not have all of it yet.
    var waiting: Bool {
        switch self {
        case .recording, .onComputer, .transcribed, .failedOnComputer: return false
        case .onWatch, .onPhone, .sending, .failedToSend: return true
        }
    }
}

/// One row of the Watch page.
struct WatchRecordingRow: Equatable, Identifiable, Sendable {
    let id: String
    let startedAt: Date
    let durationSeconds: Double
    let stage: WatchRecordingStage
    /// Parts whose audio is still on the watch, and on this phone.
    var partsOnWatch: Int = 0
    var partsOnPhone: Int = 0
    var bytesOnPhone: Int = 0
    /// What the computer named the conversations heard in this hour ("Pricing with Sam").
    var names: [String] = []

    /// The line under the hour: the names of what it heard once the computer has them, else nil
    /// and the row says where the recording is.
    var namesLine: String? { names.isEmpty ? nil : names.joined(separator: ", ") }

    /// Joins what the phone holds with what the watch says it still holds, newest first.
    static func rows(phone: [PhoneRecording], watch: [WatchRecordingSummary]) -> [WatchRecordingRow] {
        let onWatch = Dictionary(watch.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        var rows = phone.map { recording -> WatchRecordingRow in
            let stage: WatchRecordingStage
            switch recording.state {
            case .onPhone: stage = recording.sending ? .sending : .onPhone
            case .onComputer: stage = recording.sending ? .sending : .onComputer
            case .transcribed: stage = .transcribed
            case .failed: stage = recording.partsOnPhone > 0 ? .failedToSend : .failedOnComputer
            case .deleted: stage = .onComputer
            }
            let held = onWatch[recording.id]
            return WatchRecordingRow(id: recording.id, startedAt: recording.startedAt,
                                     durationSeconds: max(recording.durationSeconds, held?.durationSeconds ?? 0),
                                     stage: stage, partsOnWatch: held?.unsentParts ?? 0,
                                     partsOnPhone: recording.partsOnPhone, bytesOnPhone: recording.bytesOnPhone,
                                     names: recording.names)
        }
        let known = Set(phone.map(\.id))
        rows += watch.filter { !known.contains($0.id) }.map {
            WatchRecordingRow(id: $0.id, startedAt: $0.startedAt, durationSeconds: $0.durationSeconds,
                              stage: $0.finished ? .onWatch : .recording, partsOnWatch: $0.unsentParts)
        }
        return rows.sorted { $0.startedAt > $1.startedAt }
    }
}

/// One day of the watch's hours on the phone's list. The watch keeps recordings by the clock hour
/// (WatchHourClock), so the list is a day and then its hours, not a run of long recordings.
struct WatchRecordingDay: Equatable, Identifiable, Sendable {
    /// The start of the day.
    let day: Date
    /// Newest hour first.
    let rows: [WatchRecordingRow]
    var id: Date { day }

    /// Rows grouped by the day they began on, newest day first.
    static func days(_ rows: [WatchRecordingRow], calendar: Calendar = .current) -> [WatchRecordingDay] {
        Dictionary(grouping: rows) { calendar.startOfDay(for: $0.startedAt) }
            .map { WatchRecordingDay(day: $0.key, rows: $0.value.sorted { $0.startedAt > $1.startedAt }) }
            .sorted { $0.day > $1.day }
    }
}

@MainActor
final class WatchRecordingsModel: ObservableObject {
    static let shared = WatchRecordingsModel()

    @Published private(set) var recordings: [PhoneRecording] = []
    @Published private(set) var outcome: WatchRecordingForwarder.Outcome = .drained
    @Published private(set) var onWatch: [WatchRecordingSummary] = []
    @Published var connection: WatchConnection = .unknown
    /// What the watch last said it is doing; nil until it has said.
    @Published var watchStatus: WatchLiveStatus?
    @Published private(set) var lastChecked: Date?
    @Published var syncing = false
    /// Deleted here a moment ago: hidden even if the watch still lists it.
    private var forgotten: Set<String> = []

    /// Sample rows for screenshots and UI tests (YANSU_UITEST_WATCH_RECORDINGS=1 beside a seeded
    /// settings state). Nothing is read from a watch or sent anywhere in that mode.
    nonisolated static var isSeeded: Bool {
        let environment = ProcessInfo.processInfo.environment
        return environment["YANSU_UITEST_SETTINGS_STATE"] != nil && environment["YANSU_UITEST_WATCH_RECORDINGS"] == "1"
    }

    init() {
        guard Self.isSeeded else { return }
        let now = Date()
        func id(_ n: Int) -> String { String(format: "00000000-0000-4000-8000-%012d", n) }
        connection = .ready(reachable: true)
        lastChecked = now
        outcome = .waiting
        onWatch = [WatchRecordingSummary(id: id(1), startedAt: now.addingTimeInterval(-600), durationSeconds: 540,
                                         unsentParts: 1, finished: true)]
        recordings = [
            PhoneRecording(id: id(2), startedAt: now.addingTimeInterval(-3 * 3600), durationSeconds: 3725, state: .onPhone,
                           partsOnPhone: 5, bytesOnPhone: 14_600_000),
            PhoneRecording(id: id(3), startedAt: now.addingTimeInterval(-6 * 3600), durationSeconds: 1260, state: .onComputer),
            PhoneRecording(id: id(4), startedAt: now.addingTimeInterval(-26 * 3600), durationSeconds: 2840, state: .transcribed),
        ]
    }

    nonisolated static let seededTranscript = RecordingTranscript(id: "", lines: [
        .init(at: "", speaker: "you", text: "Let's move the launch review to Thursday."),
        .init(at: "", speaker: "speaker_1", text: "Thursday works. I'll send the notes tonight."),
        .init(at: "", speaker: "you", text: "Good. I'll tell Priya."),
    ])

    var rows: [WatchRecordingRow] {
        WatchRecordingRow.rows(phone: recordings, watch: onWatch.filter { !forgotten.contains($0.id) })
    }

    /// The Watch page has something to show.
    var worthShowing: Bool { connection.hasWatch || !recordings.isEmpty || !onWatch.isEmpty }

    /// The "Your watch" card on Today shows whenever Isoform is on the watch, also before any
    /// recording has arrived, so the watch's page is one tap from the main screen.
    var showsTodayCard: Bool {
        if case .ready = connection { return true }
        return !recordings.isEmpty || !onWatch.isEmpty
    }

    func show(_ recordings: [PhoneRecording], outcome: WatchRecordingForwarder.Outcome?) {
        self.recordings = recordings
        if let outcome { self.outcome = outcome }
    }

    func showOnWatch(_ summaries: [WatchRecordingSummary]) { onWatch = summaries }
    func checked(at date: Date) { lastChecked = date }
    func forget(_ id: String) { forgotten.insert(id) }

    func sync() async {
        guard !syncing else { return }
        syncing = true
        await WatchLink.shared.syncNow()
        syncing = false
    }
}

enum WatchRecordingsCopy {
    static func state(_ state: WatchRecordingRemoteState) -> String {
        switch state {
        case .onPhone: return String(localized: "On this phone", comment: "Watch recording state: waiting on the phone")
        case .onComputer: return String(localized: "On your computer", comment: "Watch recording state: the computer has it")
        case .transcribed: return String(localized: "In Isoform", comment: "Watch recording state: transcribed and part of memory")
        case .failed: return String(localized: "Could not be read", comment: "Watch recording state: the computer could not transcribe it")
        case .deleted: return String(localized: "Deleted", comment: "Watch recording state: deleted everywhere")
        }
    }

    static func stage(_ stage: WatchRecordingStage) -> String {
        switch stage {
        case .recording: return String(localized: "Recording now", comment: "Watch recording stage: the watch is still recording it")
        case .onWatch: return String(localized: "On your watch", comment: "Watch recording stage: the phone does not have it yet")
        case .onPhone: return String(localized: "On this phone", comment: "Watch recording state: waiting on the phone")
        case .sending: return String(localized: "Sending to your computer", comment: "Watch recording stage: a part is being sent now")
        case .onComputer: return String(localized: "On your computer", comment: "Watch recording state: the computer has it")
        case .transcribed: return String(localized: "Transcribed", comment: "Watch recording stage: transcribed on the computer")
        case .failedToSend: return String(localized: "Couldn't send", comment: "Watch recording stage: the computer refused it")
        case .failedOnComputer: return String(localized: "Your computer couldn't read it", comment: "Watch recording stage: transcription failed on the computer")
        }
    }

    /// What the stage means for the person, one line under the state in the detail.
    static func stageDetail(_ stage: WatchRecordingStage) -> String {
        switch stage {
        case .recording: return String(localized: "It comes to this phone in parts while it records.", comment: "Detail of a watch recording still being made")
        case .onWatch: return String(localized: "It comes to this phone when your watch is near.", comment: "Detail of a recording still on the watch")
        case .onPhone, .sending: return String(localized: "It goes to your computer when this phone can reach it.", comment: "Detail of a recording waiting on the phone")
        case .onComputer: return String(localized: "Your computer is transcribing it.", comment: "Detail of a recording on the computer")
        case .transcribed: return String(localized: "On your computer and in your brief.", comment: "Detail of a transcribed recording")
        case .failedToSend: return String(localized: "The audio is still on this phone. Try again.", comment: "Detail of a recording the computer refused")
        case .failedOnComputer: return String(localized: "Your computer tries again the next time Isoform starts.", comment: "Detail of a recording the computer could not transcribe")
        }
    }

    /// One line above the list when recordings are waiting, saying why.
    static func waiting(_ outcome: WatchRecordingForwarder.Outcome, anyOnPhone: Bool) -> String? {
        guard anyOnPhone else { return nil }
        switch outcome {
        case .drained: return nil
        case .waiting:
            return String(localized: "Recordings go to your computer when it is awake and observing.",
                          comment: "Shown above the watch recordings list while some wait on the phone")
        case .unsupported:
            return String(localized: "Update Isoform on your computer to receive recordings.",
                          comment: "Shown above the watch recordings list when the computer is too old")
        }
    }

    /// The line at the top that sums up where everything is.
    static func summary(_ rows: [WatchRecordingRow]) -> String {
        let onWatch = rows.filter { $0.stage == .onWatch || $0.stage == .recording }.count
        let onPhone = rows.filter { $0.stage == .onPhone || $0.stage == .sending || $0.stage == .failedToSend }.count
        var parts: [String] = []
        if onWatch > 0 {
            parts.append(String(localized: "\(onWatch) on your watch", comment: "Count of watch recordings still on the watch"))
        }
        if onPhone > 0 {
            parts.append(String(localized: "\(onPhone) on this phone", comment: "Count of watch recordings waiting on the phone"))
        }
        if parts.isEmpty {
            return rows.isEmpty
                ? String(localized: "Nothing recorded yet.", comment: "Watch page summary with no recordings")
                : String(localized: "Everything is on your computer.", comment: "Watch page summary when nothing waits")
        }
        return parts.joined(separator: " · ")
    }

    static func connection(_ connection: WatchConnection) -> String? {
        switch connection {
        case .unknown: return nil
        case .noWatch: return String(localized: "No watch is paired with this phone.", comment: "Watch page: no watch")
        case .appMissing:
            return String(localized: "Isoform isn't on your watch yet. Open the Watch app on this phone and install Isoform under Available Apps.",
                          comment: "Watch page: the watch app is not installed")
        case .ready(let reachable):
            return reachable
                ? String(localized: "Your watch is connected.", comment: "Watch page: the watch is reachable now")
                : String(localized: "Your watch sends recordings when it is near this phone.", comment: "Watch page: the watch is not reachable now")
        }
    }

    /// The Today line, only while something has not reached the computer.
    static func todayLine(_ rows: [WatchRecordingRow]) -> String? {
        let failed = rows.filter { $0.stage == .failedToSend }.count
        if failed > 0 {
            return String(localized: "\(failed) recordings from your watch couldn't be sent.",
                          comment: "Today line when watch recordings failed to send")
        }
        let waiting = rows.filter(\.stage.waiting).count
        guard waiting > 0 else { return nil }
        return String(localized: "\(waiting) recordings from your watch are waiting for your computer.",
                      comment: "Today line when watch recordings have not reached the computer")
    }

    static func speaker(_ raw: String?) -> String? {
        guard let raw, !raw.isEmpty else { return nil }
        if raw == "you" { return String(localized: "You", comment: "Speaker label for the owner in a transcript") }
        let number = raw.hasPrefix("speaker_") ? String(raw.dropFirst("speaker_".count)) : raw
        return String(localized: "Speaker \(number)", comment: "Speaker label for another voice in a transcript")
    }

    /// What the watch is doing right now, for the card on Today.
    static func status(_ status: WatchLiveStatus?) -> String {
        switch status {
        case .recording?: return String(localized: "Recording", comment: "Watch status on the phone: the watch is recording now")
        case .waitingForMicrophone?:
            return String(localized: "Waiting for the microphone", comment: "Watch status on the phone: a call or Siri has the watch's microphone")
        case .pausedOnComputer?:
            return String(localized: "Paused on your computer", comment: "Watch status on the phone: observing is paused on the computer, so the watch does not record")
        case .pausedOnWatch?: return String(localized: "Paused", comment: "Watch status on the phone: the wearer paused Isoform from the watch")
        case .stoppedToday?: return String(localized: "Stopped for today", comment: "Watch status on the phone: the wearer stopped recording; it starts again tomorrow")
        case .microphoneOff?:
            return String(localized: "Allow the microphone on your watch", comment: "Watch status on the phone: the microphone is off for Isoform on the watch")
        case .notRecording?: return String(localized: "Not recording", comment: "Watch status on the phone: the watch is not recording")
        case nil:
            return String(localized: "Open Isoform on your watch to start", comment: "Watch status on the phone before the watch has said what it is doing")
        }
    }

    /// The newest hour the watch kept and where it is: "10 AM · 48 min · Transcribed".
    static func lastHour(_ rows: [WatchRecordingRow]) -> String {
        guard let row = rows.first else {
            return String(localized: "Nothing recorded yet.", comment: "Watch page summary with no recordings")
        }
        let hour = row.startedAt.formatted(.dateTime.hour())
        return "\(hour) · \(minutes(row.durationSeconds)) · \(row.namesLine ?? stage(row.stage))"
    }

    /// "Today", "Yesterday", or the date, over a day's hours.
    static func dayTitle(_ day: Date, now: Date = Date(), calendar: Calendar = .current) -> String {
        if calendar.isDate(day, inSameDayAs: now) {
            return String(localized: "Today", comment: "Section title over today's hours from the watch")
        }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(day, inSameDayAs: yesterday) {
            return String(localized: "Yesterday", comment: "Section title over yesterday's hours from the watch")
        }
        var style = Date.FormatStyle.dateTime.weekday(.wide).month().day()
        style.calendar = calendar
        style.timeZone = calendar.timeZone
        return day.formatted(style)
    }

    /// How much of an hour was recorded, in whole minutes: "48 min". Never a running clock.
    static func minutes(_ seconds: Double) -> String {
        let minutes = WatchHourClock.minutes(seconds)
        return String(localized: "\(minutes) min", comment: "How much of an hour the watch recorded, in whole minutes")
    }

    static func size(_ bytes: Int) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(bytes), countStyle: .file)
    }
}

struct WatchRecordingsView: View {
    @ObservedObject var model: WatchRecordingsModel

    var body: some View {
        let rows = model.rows
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                syncCard(rows)
                if rows.isEmpty {
                    Text("Recordings from your watch show here.",
                         comment: "Empty state of the watch recordings list on the phone")
                        .font(.body)
                        .foregroundStyle(YansuTheme.secondary)
                } else {
                    // A day, then its hours: the watch keeps recordings by the clock hour.
                    ForEach(WatchRecordingDay.days(rows)) { day in
                        VStack(alignment: .leading, spacing: 10) {
                            SectionLabel(title: WatchRecordingsCopy.dayTitle(day.day))
                            YansuCard {
                                VStack(alignment: .leading, spacing: 14) {
                                    ForEach(day.rows) { row in
                                        NavigationLink {
                                            WatchRecordingDetailView(model: model, id: row.id)
                                        } label: {
                                            rowLabel(row)
                                        }
                                        .buttonStyle(.plain)
                                        .accessibilityIdentifier("watchRecordings.row")
                                    }
                                }
                            }
                        }
                    }
                }
                howItTravels
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.top, 8)
            .padding(.bottom, 32)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .yansuScreen()
        .navigationTitle(String(localized: "Watch", comment: "Title of the phone's page for the watch and its recordings"))
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.sync() }
        .onAppear { WatchLink.shared.drain() }
    }

    private func syncCard(_ rows: [WatchRecordingRow]) -> some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 10) {
                Text(WatchRecordingsCopy.summary(rows))
                    .font(.body.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                    .accessibilityIdentifier("watchRecordings.summary")
                if let line = WatchRecordingsCopy.connection(model.connection) {
                    Text(line).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                if let line = WatchRecordingsCopy.waiting(model.outcome, anyOnPhone: rows.contains { $0.stage == .onPhone }) {
                    Text(line)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("watchRecordings.waiting")
                }
                HStack {
                    Button {
                        Task { await model.sync() }
                    } label: {
                        Text(model.syncing
                             ? String(localized: "Syncing…", comment: "The Sync now button while a sync runs")
                             : String(localized: "Sync now", comment: "Sends waiting watch recordings on now"))
                            .font(.body.weight(.semibold))
                            .foregroundStyle(model.syncing ? YansuTheme.tertiary : YansuTheme.accent)
                    }
                    .buttonStyle(.plain)
                    .disabled(model.syncing)
                    .accessibilityIdentifier("watchRecordings.sync")
                    Spacer()
                    if let checked = model.lastChecked {
                        Text("Checked \(checked, format: .dateTime.hour().minute())",
                             comment: "When the phone last tried to send watch recordings")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.tertiary)
                    }
                }
            }
        }
    }

    private func rowLabel(_ row: WatchRecordingRow) -> some View {
        HStack(alignment: .center, spacing: 8) {
            VStack(alignment: .leading, spacing: 3) {
                HStack {
                    // The hour it belongs to ("10 AM"); the day is the section's title.
                    Text(row.startedAt, format: .dateTime.hour())
                        .font(.body)
                        .foregroundStyle(YansuTheme.ink)
                    Spacer()
                    Text(WatchRecordingsCopy.minutes(row.durationSeconds))
                        .font(.footnote.monospacedDigit())
                        .foregroundStyle(YansuTheme.secondary)
                }
                // Once the computer has named what it heard, the names stand in for the state.
                Text(row.namesLine ?? WatchRecordingsCopy.stage(row.stage))
                    .font(.footnote)
                    .foregroundStyle(row.stage == .failedToSend ? YansuTheme.ink : YansuTheme.tertiary)
                    .lineLimit(2)
            }
            Image(systemName: "chevron.right").font(.footnote.weight(.semibold))
                .foregroundStyle(YansuTheme.tertiary)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    private var howItTravels: some View {
        VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: String(localized: "Where a recording goes", comment: "Section title explaining the path of a watch recording"))
            YansuCard {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Your watch keeps a recording until this phone has it.",
                         comment: "Step 1 of where a watch recording goes")
                    Text("This phone keeps it until your computer has it.",
                         comment: "Step 2 of where a watch recording goes")
                    Text("Your computer turns it into words and keeps the audio for 14 days.",
                         comment: "Step 3 of where a watch recording goes")
                }
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
            }
        }
    }
}

/// Plays the parts of a recording that are still on the phone, one after the other.
@MainActor
final class WatchRecordingPlayer: NSObject, ObservableObject, AVAudioPlayerDelegate {
    @Published private(set) var playing = false
    private var queue: [URL] = []
    private var player: AVAudioPlayer?

    func toggle(_ urls: [URL]) {
        if playing {
            stop()
            return
        }
        queue = urls
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
        try? AVAudioSession.sharedInstance().setActive(true)
        next()
    }

    func stop() {
        player?.stop()
        player = nil
        queue = []
        if playing {
            playing = false
            try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        }
    }

    private func next() {
        while !queue.isEmpty {
            let url = queue.removeFirst()
            if let made = try? AVAudioPlayer(contentsOf: url) {
                made.delegate = self
                player = made
                playing = made.play()
                if playing { return }
            }
        }
        stop()
    }

    nonisolated func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        Task { @MainActor in self.next() }
    }
}

struct WatchRecordingDetailView: View {
    @ObservedObject var model: WatchRecordingsModel
    let id: String
    @Environment(\.dismiss) private var dismiss
    @StateObject private var player = WatchRecordingPlayer()
    @State private var transcript: RecordingTranscript?
    @State private var transcriptState = TranscriptState.idle
    @State private var confirmDelete = false
    @State private var working = false

    private enum TranscriptState { case idle, loading, loaded, unavailable, failed }

    var body: some View {
        ScrollView {
            if let row = model.rows.first(where: { $0.id == id }) {
                VStack(alignment: .leading, spacing: 16) {
                    header(row)
                    whereItIs(row)
                    if row.stage == .transcribed { heard }
                    actions(row)
                }
                .padding(.horizontal, YansuTheme.gutter)
                .padding(.top, 8)
                .padding(.bottom, 32)
                .frame(maxWidth: .infinity, alignment: .leading)
                .task(id: row.stage) { await loadTranscript(row) }
            }
        }
        .yansuScreen()
        .navigationTitle(String(localized: "Recording", comment: "Title of one watch recording's page on the phone"))
        .navigationBarTitleDisplayMode(.inline)
        .onDisappear { player.stop() }
        .confirmationDialog(String(localized: "Delete everywhere?", comment: "Confirms deleting a watch recording on every device"),
                            isPresented: $confirmDelete, titleVisibility: .visible) {
            Button(String(localized: "Delete", comment: "Confirms deleting a watch recording"), role: .destructive) {
                working = true
                player.stop()
                Task {
                    await WatchLink.shared.delete(id)
                    working = false
                    dismiss()
                }
            }
        } message: {
            Text("The recording and what Isoform learned only from it go from your watch, your phone and your computer.",
                 comment: "Explains what deleting a watch recording does")
        }
    }

    private func header(_ row: WatchRecordingRow) -> some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 6) {
                Text(row.startedAt, format: .dateTime.weekday(.wide).month().day().hour())
                    .font(.body.weight(.semibold))
                    .foregroundStyle(YansuTheme.ink)
                Text(WatchRecordingsCopy.minutes(row.durationSeconds))
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(YansuTheme.secondary)
                if let names = row.namesLine {
                    Text(names)
                        .font(.body)
                        .foregroundStyle(YansuTheme.ink)
                        .accessibilityIdentifier("watchRecording.names")
                }
                Text(WatchRecordingsCopy.stage(row.stage))
                    .font(.body)
                    .foregroundStyle(YansuTheme.ink)
                    .accessibilityIdentifier("watchRecording.stage")
                Text(WatchRecordingsCopy.stageDetail(row.stage))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.secondary)
            }
        }
    }

    /// Where the audio is right now, device by device.
    private func whereItIs(_ row: WatchRecordingRow) -> some View {
        let here = String(localized: "Audio is here", comment: "A device holds a recording's audio")
        let notYet = String(localized: "Not here yet", comment: "A device does not have a recording yet")
        let watch = row.partsOnWatch > 0 || row.stage == .onWatch || row.stage == .recording
            ? here : String(localized: "Removed after this phone got it", comment: "The watch no longer holds the audio")
        let phone: String
        if row.partsOnPhone > 0 {
            phone = here + " · " + WatchRecordingsCopy.size(row.bytesOnPhone)
        } else if row.stage == .onWatch || row.stage == .recording {
            phone = notYet
        } else {
            phone = String(localized: "Removed after your computer got it", comment: "The phone no longer holds the audio")
        }
        let computer: String
        switch row.stage {
        case .onComputer, .transcribed, .failedOnComputer: computer = here
        case .onPhone, .onWatch, .recording, .failedToSend, .sending: computer = notYet
        }
        return VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: String(localized: "Where the audio is", comment: "Section title: which device holds a recording's audio"))
            YansuCard {
                VStack(alignment: .leading, spacing: 10) {
                    place(String(localized: "Your watch", comment: "Device name in the list of where audio is"), watch)
                    place(String(localized: "This phone", comment: "Device name in the list of where audio is"), phone)
                    place(String(localized: "Your computer", comment: "Device name in the list of where audio is"), computer)
                }
            }
        }
    }

    private func place(_ name: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(name).font(.body).foregroundStyle(YansuTheme.ink)
            Spacer(minLength: 8)
            Text(value).font(.footnote).foregroundStyle(YansuTheme.secondary).multilineTextAlignment(.trailing)
        }
        .accessibilityElement(children: .combine)
    }

    private var heard: some View {
        VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: String(localized: "What it heard", comment: "Section title of a recording's transcript"))
            YansuCard {
                VStack(alignment: .leading, spacing: 10) {
                    switch transcriptState {
                    case .idle, .loading:
                        ProgressView()
                    case .unavailable:
                        Text("Update Isoform on your computer to read the words here. They are in your brief and chat.",
                             comment: "The computer is too old to send a recording's transcript")
                            .font(.footnote).foregroundStyle(YansuTheme.secondary)
                    case .failed:
                        Text("The words could not be read from your computer just now.",
                             comment: "A recording's transcript could not be loaded")
                            .font(.footnote).foregroundStyle(YansuTheme.secondary)
                    case .loaded:
                        if let lines = transcript?.lines, !lines.isEmpty {
                            ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                                VStack(alignment: .leading, spacing: 2) {
                                    if let speaker = WatchRecordingsCopy.speaker(line.speaker) {
                                        Text(speaker).font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.secondary)
                                    }
                                    Text(line.text).font(.body).foregroundStyle(YansuTheme.ink)
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            if transcript?.truncated == true {
                                Text("The rest is on your computer.", comment: "A long transcript was cut on the phone")
                                    .font(.footnote).foregroundStyle(YansuTheme.tertiary)
                            }
                        } else {
                            Text("Nothing to show. Nothing was said, the words are private, or observing is paused.",
                                 comment: "A recording's transcript is empty")
                                .font(.footnote).foregroundStyle(YansuTheme.secondary)
                        }
                    }
                }
            }
            .accessibilityIdentifier("watchRecording.transcript")
        }
    }

    private func actions(_ row: WatchRecordingRow) -> some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 14) {
                if row.partsOnPhone > 0 {
                    Button {
                        Task { player.toggle(await WatchLink.shared.audioOnPhone(id)) }
                    } label: {
                        Text(player.playing
                             ? String(localized: "Stop", comment: "Stops playing a watch recording")
                             : String(localized: "Play", comment: "Plays a watch recording that is on the phone"))
                            .font(.body.weight(.semibold)).foregroundStyle(YansuTheme.accent)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("watchRecording.play")
                } else if row.stage != .onWatch && row.stage != .recording {
                    Text("To listen, open Activity on your computer and choose From your watch.",
                         comment: "Where to play a recording whose audio left the phone")
                        .font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                if row.stage == .failedToSend {
                    Button {
                        working = true
                        Task {
                            await WatchLink.shared.retry(id)
                            working = false
                        }
                    } label: {
                        Text("Try again", comment: "Sends a refused watch recording again")
                            .font(.body.weight(.semibold)).foregroundStyle(working ? YansuTheme.tertiary : YansuTheme.accent)
                    }
                    .buttonStyle(.plain)
                    .disabled(working)
                    .accessibilityIdentifier("watchRecording.retry")
                }
                if row.stage != .recording {
                    Button(role: .destructive) {
                        confirmDelete = true
                    } label: {
                        Text("Delete everywhere", comment: "Deletes a watch recording on every device")
                            .font(.body.weight(.semibold))
                            .foregroundStyle(YansuTheme.error)
                    }
                    .buttonStyle(.plain)
                    .disabled(working)
                    .accessibilityIdentifier("watchRecording.delete")
                }
            }
        }
    }

    private func loadTranscript(_ row: WatchRecordingRow) async {
        guard row.stage == .transcribed, transcriptState == .idle || transcriptState == .failed else { return }
        transcriptState = .loading
        do {
            if let got = try await WatchLink.shared.transcript(id) {
                transcript = got
                transcriptState = .loaded
            } else {
                transcriptState = .unavailable
            }
        } catch {
            transcriptState = .failed
        }
    }
}

/// The door to the Watch page on Today: one line, only while a recording has not reached the computer.
/// "Your watch" on Today: what the watch is doing now and the last hour it kept. Tapping it opens
/// the watch's page. It shows whenever Isoform is on the watch. A plain card on purpose; design's
/// "Your watch today" (hours as columns) replaces it.
struct WatchRecordingsTodayLine: View {
    @ObservedObject var model: WatchRecordingsModel
    @State private var open = false

    var body: some View {
        if model.showsTodayCard {
            let rows = model.rows
            Button {
                open = true
            } label: {
                HStack(spacing: 8) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Your watch", comment: "Title of the card on Today that opens the watch's page")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(YansuTheme.tertiary)
                        Text(WatchRecordingsCopy.status(model.watchStatus))
                            .font(.body)
                            .foregroundStyle(YansuTheme.ink)
                            .accessibilityIdentifier("today.watch.status")
                        Text(WatchRecordingsCopy.todayLine(rows) ?? WatchRecordingsCopy.lastHour(rows))
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .multilineTextAlignment(.leading)
                    }
                    Spacer(minLength: 4)
                    Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.tertiary)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius, style: .continuous))
                .padding(.horizontal, YansuTheme.gutter)
                .padding(.bottom, 6)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("today.watchRecordings")
            .sheet(isPresented: $open) {
                NavigationStack {
                    WatchTabView(recordings: model)
                        .navigationBarTitleDisplayMode(.inline)
                        .toolbar {
                            ToolbarItem(placement: .confirmationAction) {
                                Button(String(localized: "Done")) { open = false }
                            }
                        }
                }
            }
        }
    }
}
