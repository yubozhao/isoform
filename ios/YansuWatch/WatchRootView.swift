import SwiftUI

struct WatchRootView: View {
    @EnvironmentObject private var model: WatchRecorderModel
    @AppStorage("watch.consentSeen") private var consentSeen = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 12) {
                    RecordButton()
                    statusLine
                    if !model.entries.isEmpty {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Recent", comment: "Heading over the list of recent recordings on the watch")
                                .font(.footnote.weight(.semibold))
                                .foregroundStyle(WatchTheme.tertiary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            ForEach(model.entries.prefix(12)) { entry in
                                RecordingRow(entry: entry)
                            }
                        }
                        .padding(.top, 4)
                    }
                }
                .padding(.horizontal, 4)
            }
            .background(WatchTheme.ground)
            .navigationTitle(Text("Isoform", comment: "Product name; do not translate"))
        }
        .sheet(isPresented: Binding(get: { !consentSeen }, set: { if !$0 { consentSeen = true } })) {
            ConsentSheet { consentSeen = true }
        }
    }

    @ViewBuilder private var statusLine: some View {
        switch model.phase {
        case .idle:
            Text("Tap to record. It keeps going with the screen off.",
                 comment: "Hint under the record button on the watch")
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
                .multilineTextAlignment(.center)
        case .recording(let since):
            Text(timerInterval: since...Date.distantFuture, countsDown: false)
                .font(.title3.monospacedDigit())
                .foregroundStyle(WatchTheme.text)
                .accessibilityLabel(Text("Recording time", comment: "Accessibility label for the elapsed recording time"))
        case .denied:
            Text("Allow the microphone for Isoform in Settings on this watch.",
                 comment: "Shown on the watch when microphone permission is off")
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
                .multilineTextAlignment(.center)
        case .failed:
            Text("Recording could not start. Try again.",
                 comment: "Shown on the watch when a recording failed to start")
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
                .multilineTextAlignment(.center)
        }
    }
}

private struct RecordButton: View {
    @EnvironmentObject private var model: WatchRecorderModel

    var body: some View {
        Button {
            model.toggle()
        } label: {
            ZStack {
                Circle()
                    .fill(model.isRecording ? WatchTheme.recording.opacity(0.18) : WatchTheme.surface)
                if model.isRecording {
                    RoundedRectangle(cornerRadius: 8, style: .continuous)
                        .fill(WatchTheme.recording)
                        .frame(width: 30, height: 30)
                } else {
                    Circle()
                        .fill(WatchTheme.recording)
                        .frame(width: 44, height: 44)
                }
            }
            .frame(width: 92, height: 92)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(model.isRecording
            ? Text("Stop recording", comment: "Accessibility label of the stop button on the watch")
            : Text("Start recording", comment: "Accessibility label of the record button on the watch"))
        .padding(.top, 2)
    }
}

private struct RecordingRow: View {
    let entry: WatchRecordingEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(entry.startedAt, format: .dateTime.weekday(.abbreviated).hour().minute())
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(WatchTheme.text)
                Spacer()
                Text(Self.length(entry.durationSeconds))
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(WatchTheme.secondary)
            }
            Text(WatchRecordingCopy.state(entry.display))
                .font(.caption2)
                .foregroundStyle(WatchTheme.tertiary)
        }
        .padding(.vertical, 7)
        .padding(.horizontal, 9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(WatchTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .accessibilityElement(children: .combine)
    }

    static func length(_ seconds: Double) -> String {
        let total = Int(seconds.rounded())
        return total >= 3600
            ? String(format: "%d:%02d:%02d", total / 3600, (total % 3600) / 60, total % 60)
            : String(format: "%d:%02d", total / 60, total % 60)
    }
}

private struct ConsentSheet: View {
    let done: () -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                Text("Recording on your wrist", comment: "Title of the first-run sheet on the watch")
                    .font(.headline)
                Text("Recordings go to your phone, then to your computer, sealed on the way. Your computer turns them into text. Nothing is sent anywhere else.",
                     comment: "First-run sheet on the watch: where recordings go")
                    .font(.footnote)
                    .foregroundStyle(WatchTheme.secondary)
                Text("Tell the people you record. Some places require their agreement.",
                     comment: "First-run sheet on the watch: consent reminder")
                    .font(.footnote)
                    .foregroundStyle(WatchTheme.secondary)
                Button(action: done) {
                    Text("Continue", comment: "Button that closes the first-run sheet on the watch")
                }
                .padding(.top, 4)
            }
        }
    }
}
