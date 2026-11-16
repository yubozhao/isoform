import SwiftUI

struct WatchRootView: View {
    @EnvironmentObject private var model: WatchRecorderModel
    @AppStorage("watch.consentSeen") private var consentSeen = false
    @Environment(\.scenePhase) private var scenePhase

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
                    WatchContextSection()
                }
                .padding(.horizontal, 4)
            }
            .background(WatchTheme.ground)
            .navigationTitle(Text("Isoform", comment: "Product name; do not translate"))
        }
        .sheet(isPresented: Binding(get: { !consentSeen }, set: { if !$0 { consentSeen = true } })) {
            ConsentSheet { consentSeen = true }
        }
        // Recording is the standing state: it begins when the app comes to the front, and after
        // the first-run sheet, unless the wearer stopped it.
        .onChange(of: scenePhase, initial: true) {
            if scenePhase == .active { model.cameToFront(consentSeen: consentSeen) }
        }
        .onChange(of: consentSeen) {
            if scenePhase == .active { model.cameToFront(consentSeen: consentSeen) }
        }
    }

    @ViewBuilder private var statusLine: some View {
        switch model.phase {
        case .idle:
            Text("Not recording. Tap to start.",
                 comment: "Hint under the record button on the watch when it is stopped; recording starts by itself otherwise")
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
                .multilineTextAlignment(.center)
        case .recording where model.interrupted:
            // Say so plainly: nothing is recorded while a call or Siri has the microphone.
            Text("Waiting for the microphone", comment: "Shown on the watch while a call or Siri holds the microphone")
                .font(.footnote.weight(.medium))
                .foregroundStyle(WatchTheme.text)
                .multilineTextAlignment(.center)
            Text("Recording comes back by itself. Hold to stop.",
                 comment: "Hint on the watch while a call or Siri holds the microphone")
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
                .multilineTextAlignment(.center)
        case .recording:
            // No running clock: recordings are kept by the hour, so there is no length to watch.
            Text("Recording", comment: "Shown on the watch while it records; there is no timer")
                .font(.title3.weight(.medium))
                .foregroundStyle(WatchTheme.text)
            Text("Kept by the hour. Hold to stop.",
                 comment: "Hint under the stop button on the watch while it records: each clock hour is its own recording")
                .font(.footnote)
                .foregroundStyle(WatchTheme.secondary)
                .multilineTextAlignment(.center)
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
    /// How far the hold to stop has come, 0 to 1; drawn as the ring round the control.
    @State private var holdProgress: Double = 0

    var body: some View {
        ZStack {
            Circle()
                .fill(model.isRecording ? WatchTheme.recording.opacity(0.18) : WatchTheme.surface)
            if model.isRecording {
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .fill(WatchTheme.recording)
                    .frame(width: 30, height: 30)
                Circle()
                    .trim(from: 0, to: holdProgress)
                    .stroke(WatchTheme.recording, style: StrokeStyle(lineWidth: 4, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                    .padding(2)
            } else {
                Circle()
                    .fill(WatchTheme.recording)
                    .frame(width: 44, height: 44)
            }
        }
        .frame(width: 92, height: 92)
        .contentShape(Circle())
        // A tap starts and never stops; stopping is a hold, so a wrist bump cannot end a recording.
        .onTapGesture { perform(.tap) }
        .onLongPressGesture(minimumDuration: WatchRecordControl.holdSeconds, maximumDistance: 40) {
            perform(.hold(seconds: WatchRecordControl.holdSeconds))
        } onPressingChanged: { pressing in
            let filling = pressing && model.isRecording
            withAnimation(filling ? .linear(duration: WatchRecordControl.holdSeconds) : .easeOut(duration: 0.15)) {
                holdProgress = filling ? WatchRecordControl.progress(heldSeconds: WatchRecordControl.holdSeconds) : 0
            }
        }
        .onChange(of: model.isRecording) { holdProgress = 0 }
        .accessibilityElement()
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel(model.isRecording
            ? Text("Stop recording", comment: "Accessibility label of the stop button on the watch")
            : Text("Start recording", comment: "Accessibility label of the record button on the watch"))
        .accessibilityAction { perform(.accessibilityActivate) }
        .padding(.top, 2)
    }

    private func perform(_ touch: WatchRecordControl.Touch) {
        switch WatchRecordControl.action(for: touch, isRecording: model.isRecording) {
        case .none: break
        case .start: model.start()
        case .stop: model.stopByWearer() // plays the stop haptic; stays off until started again
        }
    }
}

private struct RecordingRow: View {
    let entry: WatchRecordingEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                // The hour it belongs to ("Sat 10 AM"), then how much of it was recorded.
                Text(entry.startedAt, format: .dateTime.weekday(.abbreviated).hour())
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(WatchTheme.text)
                Spacer()
                Text("\(WatchHourClock.minutes(entry.durationSeconds)) min",
                     comment: "Length of one hour's recording on the watch, in whole minutes")
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(WatchTheme.secondary)
            }
            // Once the computer has named what it heard, the names stand in for the state.
            Text(entry.names.flatMap { $0.isEmpty ? nil : $0.joined(separator: ", ") } ?? WatchRecordingCopy.state(entry.display))
                .font(.caption2)
                .foregroundStyle(WatchTheme.tertiary)
        }
        .padding(.vertical, 7)
        .padding(.horizontal, 9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(WatchTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .accessibilityElement(children: .combine)
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
