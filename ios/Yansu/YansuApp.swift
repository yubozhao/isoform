import SwiftUI

@main
struct YansuApp: App {
    @UIApplicationDelegateAdaptor(AgentPushAppDelegate.self) private var appDelegate
    @StateObject private var environment: AppEnvironment

    init() {
        FirstLoadTrace.record("app.launch")
        #if DEBUG && targetEnvironment(simulator)
        if let environment = AppEnvironment.settingsUITestEnvironment() {
            _environment = StateObject(wrappedValue: environment)
            return
        }
        #endif
        let environment = AppEnvironment.shared
        _environment = StateObject(wrappedValue: environment)
        environment.launch()
        // Recordings from the watch travel through this phone to the computer.
        if let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first {
            WatchLink.shared.start(directory: support, vault: environment.vault, hub: environment.hub)
        }
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                #if DEBUG && targetEnvironment(simulator)
                .preferredColorScheme(uiTestColorScheme)
                #endif
                .environmentObject(environment)
                .environmentObject(environment.router)
                .environmentObject(environment.firstRun)
                .environmentObject(environment.brief)
                .environmentObject(environment.briefOnScreen)
                .environmentObject(environment.today)
                .environmentObject(environment.tasks)
                .environmentObject(environment.actions)
                .environmentObject(environment.chat)
                .environmentObject(environment.pairing)
                .environmentObject(environment.push)
                .environmentObject(environment.notifications)
                .environmentObject(environment.keepAwake)
                .onAppear { appDelegate.environment = environment }
        }
    }

    #if DEBUG && targetEnvironment(simulator)
    private var uiTestColorScheme: ColorScheme? {
        guard ProcessInfo.processInfo.environment["YANSU_UITEST_SETTINGS_STATE"] != nil else { return nil }
        switch ProcessInfo.processInfo.environment["YANSU_UITEST_APPEARANCE"] {
        case "light": return .light
        case "dark": return .dark
        default: return nil
        }
    }
    #endif
}
