import SwiftUI
import UIKit

struct NotificationSettingsView: View {
    @ObservedObject var notifications: NotificationSettingsStore
    @EnvironmentObject private var app: AppEnvironment
    @EnvironmentObject private var push: PushRegistration
    @EnvironmentObject private var keepAwake: KeepAwakeStore
    @EnvironmentObject private var today: TodayStore
    @Environment(\.locale) private var locale
    @State private var deliveryMessage: String?
    @State private var testingDelivery = false
    @AppStorage("phone.tasks.iconBadge") private var iconBadge = false

    /// The page shows its choices only once the computer has answered; until then it shows one line and Try again (P132).
    /// Each kind's one line of why (phone-rings v1.0.0): the brief's times come from the computer's schedule.
    private func kindWhy(_ kind: String) -> String {
        switch kind {
        case "brief_ready":
            let times = briefTimes
            return times.isEmpty ? String(localized: "set.notifBriefSubNoTimes")
                : String.localizedStringWithFormat(String(localized: "set.notifBriefSub"), times)
        case "meeting_prep": return String(localized: "set.notifPrepSub")
        case "work_finished": return String(localized: "set.notifDoneSub")
        default: return ""
        }
    }

    private var briefTimes: String {
        guard let schedule = today.snapshot?.schedule else { return "" }
        return [schedule.morning, schedule.evening].compactMap { $0 }
            .compactMap { TodayBriefCard.clockDate($0, on: Date()) }
            .map { BriefClock.text($0, locale: locale) }
            .joined(separator: String(localized: "set.notifTimesJoin"))
    }

    static func showsChoices(_ state: NotificationSettingsLoadState) -> Bool { state == .loaded }

    var body: some View {
        ScrollView {
            if Self.showsChoices(notifications.loadState) {
            VStack(alignment: .leading, spacing: 20) {
                YansuCard {
                    VStack(alignment: .leading, spacing: 12) {
                        Toggle(isOn: Binding(get: { push.isEnabled }, set: { enabled in
                            Task {
                                if enabled { await push.enable() } else { await push.disable() }
                            }
                        })) {
                            Text("notifications.alerts")
                        }
                        .toggleStyle(ConfirmedNotificationToggleStyle())
                        .tint(YansuTheme.accent)
                        .accessibilityIdentifier("settings.notifications.toggle")
                        Text("notifications.phoneAlertsDetail")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                        if let status = push.statusMessage {
                            Text(status)
                                .font(.footnote)
                                .foregroundStyle(YansuTheme.secondary)
                                .accessibilityIdentifier("settings.notifications.status")
                        }
                        // One test: it goes through the computer and says where it stopped if it fails (P132).
                        Button("notifications.deliveryTest") {
                            Task { await testDelivery() }
                        }
                        .buttonStyle(YansuSecondaryButtonStyle())
                        .disabled(testingDelivery)
                        .accessibilityIdentifier("notifications.deliveryTest")
                        if let deliveryMessage {
                            Text(deliveryMessage).font(.footnote).foregroundStyle(YansuTheme.secondary)
                                .accessibilityIdentifier("notifications.deliveryResult")
                        }
                    }
                }
                VStack(alignment: .leading, spacing: 10) {
                    YansuCard {
                        Toggle(String(localized: "Count on the app icon"), isOn: $iconBadge)
                            .tint(YansuTheme.accent)
                            .accessibilityIdentifier("notifications.iconBadge")
                    }
                }
                if notifications.hasPendingChanges {
                    Text("notifications.pending")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("notifications.pending")
                }
                if let failure = notifications.failure {
                    Text(failure).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                if let settings = notifications.settings, settings.available {
                    // Where it rings, the three kinds with their why, then quiet hours (phone-rings v1.0.0, 17c).
                    Text("set.notifWhere")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("notifications.where")
                    VStack(alignment: .leading, spacing: 10) {
                        SectionLabel(title: String(localized: "set.notifKinds"))
                        YansuCard {
                            VStack(alignment: .leading, spacing: 14) {
                                ForEach(settings.phoneKinds) { kind in
                                    Toggle(isOn: Binding(get: { kind.effectivelyEnabled }, set: { enabled in
                                        Task { await notifications.setEnabled(kind.kind, enabled) }
                                    })) {
                                        VStack(alignment: .leading, spacing: 2) {
                                            Text(kind.phoneTitle)
                                            Text(verbatim: kindWhy(kind.kind)).font(.footnote).foregroundStyle(YansuTheme.secondary)
                                                .fixedSize(horizontal: false, vertical: true)
                                        }
                                    }
                                    .tint(YansuTheme.accent)
                                    .accessibilityIdentifier("notifications.kind.\(kind.kind)")
                                }
                            }
                        }
                        Text("set.prepFocus")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                        YansuCard {
                            NavigationLink {
                                NotificationQuietHoursView(notifications: notifications, settings: settings)
                            } label: {
                                HStack {
                                    Text("notifications.quietHours")
                                    Spacer()
                                    Text(NotificationSettings.quietHoursRange(start: settings.quietStart,
                                         end: settings.quietEnd, locale: locale) ?? String(localized: "notifications.off"))
                                        .foregroundStyle(YansuTheme.secondary)
                                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(YansuTheme.secondary)
                                }
                            }
                            .accessibilityIdentifier("notifications.quietHours")
                        }
                        Text("set.quietFoot")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                } else if notifications.loadState == .loading {
                    ProgressView(String(localized: "notifications.loading"))
                } else {
                    Text("notifications.couldNotReach")
                        .font(.body)
                        .foregroundStyle(YansuTheme.secondary)
                }
                if case .failed(let message) = notifications.loadState {
                    Text(message).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                Button("notifications.checkAgain") { Task { await notifications.refresh() } }
                    .buttonStyle(YansuSecondaryButtonStyle())
                    .accessibilityIdentifier("notifications.refresh")
            }
            .padding(YansuTheme.gutter)
            .fixedSize(horizontal: false, vertical: true)
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    if notifications.loadState == .loading {
                        ProgressView(String(localized: "notifications.loading"))
                    } else {
                        Text("notifications.couldNotReach")
                            .font(.body)
                            .foregroundStyle(YansuTheme.secondary)
                        Button(String(localized: "Try again")) { Task { await notifications.refresh() } }
                            .buttonStyle(.plain)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(YansuTheme.accent)
                            .accessibilityIdentifier("notifications.refresh")
                    }
                }
                .padding(YansuTheme.gutter)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        // Moved here from the Settings sheet with its Notifications row (phone-settings rule 8): a declined setting
        // needs a way to iOS Settings, and the line on late notices stays.
        .safeAreaInset(edge: .top, spacing: 0) {
            if push.state == .denied || !keepAwake.instantPush {
                VStack(alignment: .leading, spacing: 10) {
                    if push.state == .denied {
                        Button("Open Settings") {
                            if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                        }
                        .buttonStyle(YansuSecondaryButtonStyle())
                        .accessibilityIdentifier("settings.notifications.openSettings")
                    }
                    if !keepAwake.instantPush {
                        Text("Your phone checks your computer every so often, so a notice can come a little late. While your computer is asleep, most notices wait until it wakes.")
                            .font(.footnote)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityIdentifier("settings.notifications.lateFooter")
                    }
                }
                .padding(.horizontal, YansuTheme.gutter)
                .padding(.vertical, 10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(YansuTheme.background)
            }
        }
        .yansuScreen()
        .navigationTitle(String(localized: "notifications.title"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await notifications.refresh() }
        .refreshable { await notifications.refresh() }
        .onChange(of: iconBadge) { _, enabled in
            app.tasks.updateIconBadge(enabled: enabled)
        }
    }

    @MainActor private func testDelivery() async {
        guard let paired = app.vault.current() else {
            deliveryMessage = String(localized: "notifications.deliveryUnavailable")
            return
        }
        guard push.isOptedIn else {
            deliveryMessage = String(localized: "notifications.alertsOff")
            return
        }
        guard !testingDelivery else { return }
        testingDelivery = true
        defer { testingDelivery = false }
        do {
            guard try await app.hub.abilities().notificationTest == true else {
                deliveryMessage = String(localized: "notifications.deliveryUnsupported")
                return
            }
            struct Request: Encodable { let idempotencyKey: String; let language: String }
            struct Result: Decodable { let noticeId: String }
            let language = locale.language.languageCode?.identifier == "zh" ? "zh-Hans" : "en"
            let result = try await app.hub.post("/notifications/test", Request(idempotencyKey: UUID().uuidString,
                                                                               language: language), as: Result.self)
            guard NoticeIdentifier.isSafe(result.noticeId) else { throw HubRequestError.malformedResponse }
            deliveryMessage = String(localized: "notifications.deliveryWaiting")
            for _ in 0..<30 {
                guard app.vault.current()?.hubID == paired.hubID, app.vault.current()?.deviceID == paired.deviceID else { return }
                if SharedNoticeDedup.containsVerifiedTest(hub: paired.hubID, device: paired.deviceID, id: result.noticeId) {
                    deliveryMessage = String(localized: "notifications.deliveryArrived")
                    return
                }
                try await Task.sleep(for: .seconds(1))
            }
            deliveryMessage = String(localized: "notifications.deliveryUnconfirmed")
        } catch {
            deliveryMessage = String(localized: "notifications.deliveryFailed")
        }
    }
}

struct NotificationQuietHoursDestination: View {
    @ObservedObject var notifications: NotificationSettingsStore

    var body: some View {
        Group {
            if let settings = notifications.settings, settings.available {
                NotificationQuietHoursView(notifications: notifications, settings: settings)
            } else if case .failed(let message) = notifications.loadState {
                VStack(spacing: 12) {
                    Text(message).foregroundStyle(YansuTheme.secondary).multilineTextAlignment(.center)
                    Button(String(localized: "Try again")) { Task { await notifications.refresh() } }
                }
                .frame(maxWidth: .infinity)
            } else if notifications.loadState == .loaded {
                VStack(spacing: 12) {
                    Text("notifications.couldNotReach").foregroundStyle(YansuTheme.secondary).multilineTextAlignment(.center)
                    Button(String(localized: "Try again")) { Task { await notifications.refresh() } }
                }
                .frame(maxWidth: .infinity)
            } else {
                ProgressView()
            }
        }
        .navigationTitle(String(localized: "notifications.quietHours"))
        .task { if notifications.settings == nil { await notifications.refresh() } }
    }
}

struct NotificationQuietHoursView: View {
    @ObservedObject var notifications: NotificationSettingsStore
    @Environment(\.dismiss) private var dismiss
    @State private var enabled: Bool
    @State private var start: Date
    @State private var end: Date
    @State private var saving = false
    @State private var saveFailed = false

    init(notifications: NotificationSettingsStore, settings: NotificationSettings) {
        self.notifications = notifications
        let hasQuietHours = settings.quietStart != settings.quietEnd
        _enabled = State(initialValue: hasQuietHours)
        _start = State(initialValue: Self.date(hasQuietHours ? settings.quietStart : "22:00"))
        _end = State(initialValue: Self.date(hasQuietHours ? settings.quietEnd : "08:00"))
    }

    var body: some View {
        Form {
            Toggle("notifications.quietHours", isOn: $enabled)
                .tint(YansuTheme.accent)
            if enabled {
                DatePicker("notifications.quietStart", selection: $start, displayedComponents: .hourAndMinute)
                DatePicker("notifications.quietEnd", selection: $end, displayedComponents: .hourAndMinute)
            }
            Text("notifications.quietDetail")
                .font(.footnote)
                .foregroundStyle(YansuTheme.secondary)
            if saveFailed {
                Text(notifications.failure ?? String(localized: "notifications.error.save"))
                    .font(.footnote)
                    .foregroundStyle(YansuTheme.warning)
                    .accessibilityIdentifier("notifications.quietHours.failure")
            }
            Button(saveFailed ? String(localized: "Try again") : String(localized: "notifications.save")) {
                let quietStart = enabled ? Self.clock(start) : "00:00"
                let quietEnd = enabled ? Self.clock(end) : "00:00"
                saving = true
                saveFailed = false
                Task {
                    let saved = await notifications.setQuietHours(start: quietStart, end: quietEnd)
                    saving = false
                    if saved { dismiss() } else { saveFailed = true }
                }
            }
            .disabled(saving)
            .accessibilityIdentifier("notifications.quietHours.save")
            if saving { ProgressView(String(localized: "Saving…")) }
        }
        .navigationTitle(String(localized: "notifications.quietHours"))
    }

    private static func date(_ clock: String) -> Date {
        let parts = clock.split(separator: ":")
        return Calendar.current.date(from: DateComponents(hour: Int(parts.first ?? "") ?? 22,
                                                           minute: Int(parts.last ?? "") ?? 0)) ?? Date()
    }

    private static func clock(_ date: Date) -> String {
        let parts = Calendar.current.dateComponents([.hour, .minute], from: date)
        return String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
    }
}
