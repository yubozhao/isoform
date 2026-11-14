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
        WatchRecorderModel.shared.stop()
        return .result()
    }
}

struct IsoformWatchShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(intent: StartRecordingIntent(),
                    phrases: ["Start recording in \(.applicationName)", "Record with \(.applicationName)"],
                    shortTitle: "Start recording", systemImageName: "record.circle")
        AppShortcut(intent: StopRecordingIntent(),
                    phrases: ["Stop recording in \(.applicationName)"],
                    shortTitle: "Stop recording", systemImageName: "stop.circle")
    }
}
