import BackgroundTasks
import Foundation
import UserNotifications

/// iOS chooses when to run these requests; none of them promises a wake at the requested time.
@MainActor
enum BackgroundTodayRefresh {
    private static let periodic = "com.isoform.companion.today.refresh"
    private static let morning = "com.isoform.companion.today.morning"
    private static let evening = "com.isoform.companion.today.evening"
    private static var registered = false

    static func register() {
        guard !registered else { return }
        var allRegistered = true
        for identifier in [periodic, morning, evening] {
            let accepted = BGTaskScheduler.shared.register(forTaskWithIdentifier: identifier, using: nil) { task in
                guard let task = task as? BGAppRefreshTask else { task.setTaskCompleted(success: false); return }
                let operation = Task { @MainActor in
                    let app = AppEnvironment.shared
                    // The brief and Today beside the watch drain, never in front of it: recordings come first, and
                    // a wake is often cut off. The task's result is the refresh's; a drain cut off is normal.
                    async let refreshed = app.backgroundBriefRefresh()
                    // Watch recordings still on this phone go with every wake, tapped or not.
                    async let drained: Void = WatchLink.shared.drainInBackground()
                    let result = await refreshed
                    await drained
                    schedule(app.keepAwake.briefSchedule, paired: app.pairing.isPaired)
                    task.setTaskCompleted(success: result)
                }
                task.expirationHandler = { operation.cancel() }
            }
            allRegistered = allRegistered && accepted
        }
        registered = allRegistered
    }

    static func schedule(_ slots: [ScheduledPhoneBrief], paired: Bool, now: Date = Date()) {
        guard registered else { return }
        let scheduler = BGTaskScheduler.shared
        if !paired {
            for identifier in [periodic, morning, evening] { scheduler.cancel(taskRequestWithIdentifier: identifier) }
            return
        }
        let request = BGAppRefreshTaskRequest(identifier: periodic)
        request.earliestBeginDate = now.addingTimeInterval(15 * 60)
        try? scheduler.submit(request)
        for (slot, identifier) in [("morning", morning), ("evening", evening)] {
            scheduler.cancel(taskRequestWithIdentifier: identifier)
            guard let date = slots.filter({ $0.slot == slot }).compactMap(\.date).filter({ $0 > now }).min() else { continue }
            let request = BGAppRefreshTaskRequest(identifier: identifier)
            request.earliestBeginDate = max(now, date.addingTimeInterval(-5 * 60))
            try? scheduler.submit(request)
        }
    }
}
