import AppIntents

// "Start recording" as a shortcut. It can be put on the Action button (Settings > Action Button >
// Shortcut), asked of Siri, or run from the Shortcuts app; it opens Isoform already recording.
struct StartRecordingIntent: AppIntent {
    static let title: LocalizedStringResource = "Start recording"
    static let description = IntentDescription("Starts a recording in Isoform on this watch.")
    static let openAppWhenRun = true

    @MainActor
    func perform() async throws -> some IntentResult {
        WatchRecorderModel.shared.start()
        return .result()
    }
}

struct StopRecordingIntent: AppIntent {
    static let title: LocalizedStringResource = "Stop recording"
    static let description = IntentDescription("Stops the recording in Isoform on this watch.")
    static let openAppWhenRun = false

    @MainActor
    func perform() async throws -> some IntentResult {
        WatchRecorderModel.shared.stopByWearer()
        return .result()
    }
}

// "Mark a moment" as a shortcut, meant for the Action button (Settings > Action Button >
// Shortcut). While the watch records it marks the moment; otherwise it starts recording. It
// never stops one. It opens the app so that starting can open the microphone, which watchOS does
// not allow from the background.
struct MarkMomentIntent: AppIntent {
    static let title: LocalizedStringResource = "Mark a moment"
    static let description = IntentDescription("Marks this moment in the recording in Isoform on this watch, or starts recording.")
    static let openAppWhenRun = true

    @MainActor
    func perform() async throws -> some IntentResult {
        let model = WatchRecorderModel.shared
        switch WatchRecordControl.action(for: .actionButton, isRecording: model.isRecording) {
        case .mark: model.mark()
        case .start: model.start()
        case .stop, .none: break
        }
        return .result()
    }
}

struct IsoformWatchShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(intent: MarkMomentIntent(),
                    phrases: ["Mark a moment in \(.applicationName)", "Mark this in \(.applicationName)"],
                    shortTitle: "Mark a moment", systemImageName: "bookmark")
        AppShortcut(intent: StartRecordingIntent(),
                    phrases: ["Start recording in \(.applicationName)", "Record with \(.applicationName)"],
                    shortTitle: "Start recording", systemImageName: "record.circle")
        AppShortcut(intent: StopRecordingIntent(),
                    phrases: ["Stop recording in \(.applicationName)"],
                    shortTitle: "Stop recording", systemImageName: "stop.circle")
    }
}
