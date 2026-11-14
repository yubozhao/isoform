import SwiftUI

// The phone's list of recordings that came from the watch: when, how long, and where each one
// is on its way to the computer. It is a plain list on purpose; the designed page replaces it.
// Settings shows the door to it only once a recording has arrived, so a phone without a watch
// looks as it did.
@MainActor
final class WatchRecordingsModel: ObservableObject {
    static let shared = WatchRecordingsModel()

    @Published private(set) var recordings: [PhoneRecording] = []
    @Published private(set) var outcome: WatchRecordingForwarder.Outcome = .drained

    func show(_ recordings: [PhoneRecording], outcome: WatchRecordingForwarder.Outcome?) {
        self.recordings = recordings
        if let outcome { self.outcome = outcome }
    }
}

enum WatchRecordingsCopy {
    static func state(_ state: WatchRecordingRemoteState) -> String {
        switch state {
        case .onPhone: return String(localized: "On this phone", comment: "Watch recording state: waiting on the phone")
        case .onComputer: return String(localized: "On your computer", comment: "Watch recording state: the computer has it")
        case .transcribed: return String(localized: "In Isoform", comment: "Watch recording state: transcribed and part of memory")
        case .failed: return String(localized: "Could not be read", comment: "Watch recording state: the computer could not transcribe it")
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

    static func length(_ seconds: Double) -> String {
        let total = Int(seconds.rounded())
        return total >= 3600
            ? String(format: "%d:%02d:%02d", total / 3600, (total % 3600) / 60, total % 60)
            : String(format: "%d:%02d", total / 60, total % 60)
    }
}

struct WatchRecordingsView: View {
    @ObservedObject var model: WatchRecordingsModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if let line = WatchRecordingsCopy.waiting(model.outcome,
                                                          anyOnPhone: model.recordings.contains { $0.state == .onPhone }) {
                    Text(line)
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("watchRecordings.waiting")
                }
                if model.recordings.isEmpty {
                    Text("Recordings from your watch show here.",
                         comment: "Empty state of the watch recordings list on the phone")
                        .font(.body)
                        .foregroundStyle(YansuTheme.secondary)
                } else {
                    YansuCard {
                        VStack(alignment: .leading, spacing: 14) {
                            ForEach(model.recordings) { recording in
                                VStack(alignment: .leading, spacing: 3) {
                                    HStack {
                                        Text(recording.startedAt, format: .dateTime.weekday(.abbreviated).month().day().hour().minute())
                                            .font(.body)
                                            .foregroundStyle(YansuTheme.ink)
                                        Spacer()
                                        Text(WatchRecordingsCopy.length(recording.durationSeconds))
                                            .font(.footnote.monospacedDigit())
                                            .foregroundStyle(YansuTheme.secondary)
                                    }
                                    Text(WatchRecordingsCopy.state(recording.state))
                                        .font(.footnote)
                                        .foregroundStyle(YansuTheme.tertiary)
                                }
                                .accessibilityElement(children: .combine)
                            }
                        }
                    }
                }
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.top, 8)
            .padding(.bottom, 32)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .yansuScreen()
        .navigationTitle(String(localized: "Watch recordings", comment: "Title of the phone's list of recordings from the watch"))
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { WatchLink.shared.drain() }
    }
}
