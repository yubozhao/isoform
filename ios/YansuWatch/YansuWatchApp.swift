import SwiftUI

@main
struct YansuWatchApp: App {
    @StateObject private var model = WatchRecorderModel.shared

    var body: some Scene {
        WindowGroup {
            WatchRootView()
                .environmentObject(model)
                .onOpenURL { url in
                    // The complication and the shortcut open isoformwatch://record.
                    if url.host == "record" { model.start() }
                }
        }
    }
}
