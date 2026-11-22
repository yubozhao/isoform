import BackgroundTasks
import Foundation

/// A longer background run for watch recordings still on this phone.
///
/// A background refresh gives the app seconds. With a day of recordings waiting, iOS can also
/// be asked for a processing run: it comes when the phone is idle, often while it charges, and
/// lasts minutes. iOS chooses when; it is one more wake, not a promise. It is asked for only
/// while something waits (WatchLink, after each pass).
@MainActor
enum BackgroundRecordingSend {
    static let identifier = "com.isoform.companion.recordings.send"
    private static var registered = false

    static func register() {
        guard !registered else { return }
        registered = BGTaskScheduler.shared.register(forTaskWithIdentifier: identifier, using: nil) { task in
            let operation = Task { @MainActor in
                await WatchLink.shared.drainInBackground()
                task.setTaskCompleted(success: true)
            }
            task.expirationHandler = { operation.cancel() }
        }
    }

    /// Asks for a run when parts wait, and withdraws the ask when none do.
    static func schedule(waiting: Bool) {
        guard registered else { return }
        guard waiting else {
            BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: identifier)
            return
        }
        let request = BGProcessingTaskRequest(identifier: identifier)
        request.requiresNetworkConnectivity = true
        request.requiresExternalPower = false
        try? BGTaskScheduler.shared.submit(request)
    }
}
