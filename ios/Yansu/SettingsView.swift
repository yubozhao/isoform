import LocalAuthentication
import SwiftUI
import UIKit

// Settings keeps pairing and notifications close at hand, with device details tucked away.
struct SettingsView: View {
    @EnvironmentObject private var app: AppEnvironment
    @EnvironmentObject private var pairing: PairingStore
    @EnvironmentObject private var brief: BriefStore
    @EnvironmentObject private var push: PushRegistration
    @EnvironmentObject private var notifications: NotificationSettingsStore
    @EnvironmentObject private var keepAwake: KeepAwakeStore
    @EnvironmentObject private var todayStore: TodayStore
    @Environment(\.locale) private var locale

    @State private var showPairing = false
    @State private var showDetails = false
    @State private var confirmUnpair = false
    @State private var confirmForgetPairing = false
    @State private var confirmDeleteCache = false
    @State private var showsSees = false
    @State private var unpairFailed = false
    @State private var removePairingFailed = false
    @State private var noticeDetailsMissing = false
    @ObservedObject private var watchRecordings = WatchRecordingsModel.shared
    @State private var unpairing = false
    @State private var faceIDFailure = false
    @State private var status: HubStatus?
    @AppStorage("faceIDToOpen") private var faceIDToOpen = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                settingsGroup("You", identifier: "settings.group.you") {
                    youSection
                    NavigationLink {
                        YourAgentView(store: app.yourAgent)
                    } label: {
                        YansuCard {
                            HStack(spacing: 8) {
                                Text("Agent of You").font(.body).foregroundStyle(YansuTheme.ink)
                                Text("Soon").font(.caption2.weight(.semibold)).foregroundStyle(YansuTheme.warning)
                                    .padding(.horizontal, 7).padding(.vertical, 3)
                                    .background(YansuTheme.warningWash, in: Capsule())
                                Spacer(minLength: 4)
                                Image(systemName: "chevron.right").font(.footnote.weight(.semibold))
                                    .foregroundStyle(YansuTheme.tertiary)
                            }
                        }
                    }.buttonStyle(.plain).accessibilityIdentifier("settings.yourAgent.open")
                }
                settingsGroup("Your computer", identifier: "settings.group.mac") { pairingSection }
                // Shown only once a recording has arrived from the watch.
                if !watchRecordings.recordings.isEmpty {
                    settingsGroup("Watch", identifier: "settings.group.watch") {
                        NavigationLink {
                            WatchRecordingsView(model: watchRecordings)
                        } label: {
                            YansuCard {
                                HStack(spacing: 8) {
                                    Text("Recordings").font(.body).foregroundStyle(YansuTheme.ink)
                                    Spacer(minLength: 4)
                                    Text("\(watchRecordings.recordings.count)").font(.body)
                                        .foregroundStyle(YansuTheme.secondary)
                                    Image(systemName: "chevron.right").font(.footnote.weight(.semibold))
                                        .foregroundStyle(YansuTheme.tertiary)
                                }
                            }
                        }.buttonStyle(.plain).accessibilityIdentifier("settings.watchRecordings.open")
                    }
                }
                if let notice = brief.notice {
                    NoticeBanner(message: notice, dismiss: brief.dismissNotice)
                        .accessibilityIdentifier("settings.briefNotice")
                }
                settingsGroup("Notifications", identifier: "settings.group.notifications") { notificationsControls }
                settingsGroup("Privacy", identifier: "settings.group.privacy") { privacySection }
            }
            .padding(.horizontal, YansuTheme.gutter)
            .padding(.top, 8)
            .padding(.bottom, 32)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        // P164: the last row rests above the bottom gesture zone; the scroll content keeps that margin at its end.
        .contentMargins(.bottom, GestureZone.bottomInset, for: .scrollContent)
        .yansuScreen()
        .hardTitleEdge()
        .navigationTitle(String(localized: "Settings"))
        .navigationBarTitleDisplayMode(.large)
        .onAppear {
            noticeDetailsMissing = push.noticeDetailsMissing()
            Task { await loadStatus() }
        }
        .onChange(of: faceIDToOpen) { _, _ in app.refreshWidget() }
        .sheet(isPresented: $showsSees) {
            IsoformSeesSheet(hub: app.seesHub)
        }
        .sheet(isPresented: $showPairing, onDismiss: { pairing.cancel() }) {
            PairingView()
        }
        .confirmationDialog("Clear this phone?", isPresented: $confirmUnpair, titleVisibility: .visible) {
            Button("Clear this phone", role: .destructive) {
                Task { await unpair() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Your computer stops sending briefs to this phone, and briefs stored on this phone are deleted.")
        }
        .confirmationDialog("Remove saved pairing?", isPresented: $confirmForgetPairing, titleVisibility: .visible) {
            Button("Remove saved pairing", role: .destructive) { removeSavedPairing() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This removes the saved pairing and cached briefs and chats from this phone. The old phone may still be listed in Isoform on your computer under Settings > Phone. Then choose Show pairing code on your computer to pair this phone again.")
        }
        .confirmationDialog("Remove saved briefs?", isPresented: $confirmDeleteCache, titleVisibility: .visible) {
            Button("Remove saved briefs", role: .destructive) {
                brief.deleteCachedBriefs()
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Briefs stay on your computer. This phone downloads them again when it is paired and online.")
        }
        .alert("Isoform could not reach your relay", isPresented: $unpairFailed) {
            Button("Remove from this phone", role: .destructive) {
                removeSavedPairing()
            }
            Button("Keep pairing", role: .cancel) {}
        } message: {
            Text("The pairing was not removed from the relay. You can remove it from this phone now, then remove this phone in the Isoform app on your computer.")
        }
        .alert("Saved pairing could not be removed", isPresented: $removePairingFailed) {
            Button("OK", role: .cancel) {}
        } message: {
            Text("Isoform could not remove the saved pairing from this phone. Unlock your phone and try again.")
        }
        .alert("Face ID could not be enabled", isPresented: $faceIDFailure) {
            Button("OK", role: .cancel) {}
        } message: {
            Text("Set a passcode on this iPhone, then try again.")
        }
    }

    // MARK: Pairing

    private var pairingSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            YansuCard {
                if let message = pairing.recoveryMessage {
                    VStack(alignment: .leading, spacing: 14) {
                        Text("Saved pairing unavailable")
                            .font(.headline)
                            .foregroundStyle(YansuTheme.ink)
                        Text(message)
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                        Button("Retry saved pairing") { app.retrySavedPairing() }
                            .buttonStyle(YansuSecondaryButtonStyle())
                            .accessibilityIdentifier("settings.retrySavedPairing")
                        Button(role: .destructive) {
                            confirmForgetPairing = true
                        } label: {
                            Text("Remove saved pairing")
                                .font(.body.weight(.semibold))
                                .foregroundStyle(YansuTheme.error)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .accessibilityIdentifier("settings.removeSavedPairing")
                    }
                } else if let paired = pairing.paired {
                    VStack(alignment: .leading, spacing: 16) {
                        Text("Paired with \(paired.hubName)")
                            .font(.body)
                            .foregroundStyle(YansuTheme.ink)
                        detailRow("Connection", connectionDescription)
                        if let value = keepAwake.on {
                            Toggle(isOn: Binding(get: { keepAwake.on ?? value }, set: { enabled in
                                Task { await keepAwake.set(enabled) }
                            })) { Text("Keep your computer awake while it's plugged in") }
                                .tint(YansuTheme.accent)
                                .accessibilityIdentifier("settings.keepAwake")
                            Text("So this phone can reach it while it's plugged in. The screen still turns off. A closed laptop still sleeps.")
                                .font(.footnote)
                                .foregroundStyle(YansuTheme.secondary)
                            if keepAwake.saveFailed {
                                Text(String(localized: "Could not save this change. Try again."))
                                    .font(.footnote)
                                    .foregroundStyle(YansuTheme.warning)
                                    .accessibilityIdentifier("settings.keepAwake.failure")
                            }
                            if keepAwake.pending || brief.hubOnline == false {
                                Text("Changes apply when your computer is back.")
                                    .font(.footnote)
                                    .foregroundStyle(YansuTheme.secondary)
                            }
                        }
                        if !Self.allowsNewerFeatures(paired.scopes) {
                            Text("Choose Pair again below to use Chat, corrections, one-time work and Today feedback.")
                                .font(.footnote)
                                .foregroundStyle(YansuTheme.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        if let message = pairing.previousDeviceRemovalMessage {
                            Text(message)
                                .font(.footnote)
                                .foregroundStyle(YansuTheme.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        Button("Pair another computer") {
                            pairing.prepareToPair()
                            showPairing = true
                        }
                        .buttonStyle(YansuSecondaryButtonStyle())
                        .disabled(unpairing)
                        .accessibilityIdentifier("settings.pairAgain")
                        Button {
                            confirmUnpair = true
                        } label: {
                            Text(Self.unpairTitle(unpairing: unpairing))
                                .font(.body.weight(.semibold))
                                .foregroundStyle(YansuTheme.error)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .disabled(unpairing)
                        .accessibilityIdentifier("settings.unpair")
                        DisclosureGroup("Details", isExpanded: $showDetails) {
                            VStack(alignment: .leading, spacing: 16) {
                                detailRow("Relay", Self.relayHost(paired.relayOrigin))
                                    .accessibilityIdentifier("settings.detail.relay")
                                detailRow("This phone", paired.deviceName)
                                    .accessibilityIdentifier("settings.detail.phone")
                                detailRow("Paired", paired.pairedAt.formatted(date: .abbreviated, time: .omitted))
                                    .accessibilityIdentifier("settings.detail.paired")
                                detailRow("Allowed on this phone", Self.permissionLabels(paired.scopes))
                                    .accessibilityIdentifier("settings.detail.permissions")
                                if let expires = paired.expiresAt {
                                    detailRow("Access ends", expires.formatted(date: .abbreviated, time: .omitted))
                                        .accessibilityIdentifier("settings.detail.expires")
                                }
                                if noticeDetailsMissing {
                                    Text("Alerts on this phone show a short line only. Pair this phone again in Settings to restore the details.")
                                        .font(.footnote)
                                        .foregroundStyle(YansuTheme.secondary)
                                        .fixedSize(horizontal: false, vertical: true)
                                }
                                Button("Remove saved briefs") {
                                    confirmDeleteCache = true
                                }
                                .buttonStyle(YansuSecondaryButtonStyle())
                                .accessibilityIdentifier("settings.deleteCachedBriefs")
                            }
                            .padding(.top, 12)
                        }
                        .font(.body)
                        .foregroundStyle(YansuTheme.ink)
                        .tint(YansuTheme.accent)
                    }
                } else {
                    VStack(alignment: .leading, spacing: 14) {
                        Text("This phone is not paired with a computer.")
                            .font(.headline)
                            .foregroundStyle(YansuTheme.ink)
                        Text("Pairing needs the Isoform app on your computer. Isoform shows a six-digit code on both devices. Compare them, then approve on the computer.")
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                        Button("Pair with your computer") {
                            showPairing = true
                        }
                        .buttonStyle(YansuPrimaryButtonStyle())
                        .accessibilityIdentifier("settings.pair")
                    }
                }
            }
        }
    }

    // MARK: Notifications

    private var notificationsSection: some View {
        NavigationLink {
            NotificationSettingsView(notifications: app.notifications)
        } label: {
            YansuCard {
                HStack(spacing: 12) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("notifications.title")
                            .font(.headline)
                            .foregroundStyle(YansuTheme.ink)
                        Text("notifications.settingsSummary")
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.secondary)
                    }
                    Spacer()
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(YansuTheme.secondary)
                }
            }
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("settings.notifications.open")
    }

    private var notificationsControls: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 14) {
                Toggle(isOn: Binding(get: { push.isEnabled }, set: { enabled in
                    Task { if enabled { await push.enable() } else { await push.disable() } }
                })) { Text("Phone alerts") }
                    .tint(YansuTheme.accent)
                    .accessibilityIdentifier("settings.notifications.toggle")
                if let status = push.statusMessage {
                    Text(status).font(.footnote).foregroundStyle(YansuTheme.secondary)
                }
                // Declined in iOS: the switch cannot turn alerts on again from here, so the button opens iOS Settings (P145).
                if push.state == .denied {
                    Button("Open Settings") {
                        if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                    }
                    .buttonStyle(YansuSecondaryButtonStyle())
                    .accessibilityIdentifier("settings.notifications.openSettings")
                }
                if notifications.hasPendingChanges {
                    Text("notifications.pending").font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("settings.notifications.pending")
                }
                if let failure = notifications.failure {
                    Text(failure).font(.footnote).foregroundStyle(YansuTheme.secondary)
                        .accessibilityIdentifier("settings.notifications.failure")
                }
                if let settings = notifications.settings, settings.available {
                    ForEach(settings.phoneKinds) { kind in
                        Toggle(isOn: Binding(get: { kind.effectivelyEnabled }, set: { enabled in
                            Task { await notifications.setEnabled(kind.kind, enabled) }
                        })) { Text(kind.phoneTitle) }
                            .tint(YansuTheme.accent)
                            .accessibilityIdentifier("settings.notification.\(kind.kind)")
                    }
                }
                NavigationLink {
                    NotificationQuietHoursDestination(notifications: app.notifications)
                } label: {
                    HStack {
                        Text("Quiet hours")
                            .foregroundStyle(YansuTheme.ink)
                        Spacer()
                        Text(quietHoursSummary)
                            .font(.subheadline)
                            .foregroundStyle(YansuTheme.secondary)
                        Image(systemName: "chevron.right")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(YansuTheme.secondary)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("settings.notifications.open")
                NavigationLink {
                    NotificationSettingsView(notifications: app.notifications)
                } label: {
                    Text("notifications.title")
                }
                .accessibilityIdentifier("settings.notifications.preferences")
                if !keepAwake.instantPush {
                    Text("Your phone checks your computer every so often, so a notice can come a little late. While your computer is asleep, most notices wait until it wakes.")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("settings.notifications.lateFooter")
                }
            }
        }
    }

    private var quietHoursSummary: String {
        guard let settings = notifications.settings, settings.available else {
            return String(localized: "Set on your computer")
        }
        return NotificationSettings.quietHoursRange(start: settings.quietStart, end: settings.quietEnd, locale: locale)
            ?? String(localized: "Set on your computer")
    }

    private var youSection: some View {
        YansuCard {
            HStack(spacing: 14) {
                if let initial = ownerName?.first {
                    Text(String(initial).uppercased())
                        .font(.headline.weight(.semibold))
                        .foregroundStyle(YansuTheme.accent)
                        .frame(width: 44, height: 44)
                        .background(YansuTheme.accent.opacity(0.12), in: Circle())
                } else {
                    Image(systemName: "person.crop.circle")
                        .font(.largeTitle)
                        .foregroundStyle(YansuTheme.secondary)
                        .frame(width: 44, height: 44)
                }
                VStack(alignment: .leading, spacing: 4) {
                    Text(ownerName ?? String(localized: "You"))
                        .font(.headline)
                        .foregroundStyle(YansuTheme.ink)
                    Text(String(localized: "This iPhone"))
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                    Text(UIDevice.current.name)
                        .font(.subheadline)
                        .foregroundStyle(YansuTheme.ink)
                }
                Spacer(minLength: 0)
            }
        }
        .accessibilityIdentifier("settings.you")
    }

    private var privacySection: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 16) {
                if pairing.isPaired {
                    // The same page as the mark in Chat (Phone Threads §5): one page, three doors.
                    Button { showsSees = true } label: {
                        HStack {
                            Text(String(localized: "What Isoform sees"))
                                .font(.body.weight(.semibold))
                                .foregroundStyle(YansuTheme.ink)
                            Spacer()
                            Image(systemName: "chevron.right")
                                .font(.footnote.weight(.semibold))
                                .foregroundStyle(YansuTheme.tertiary)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("settings.whatIsoformSees")
                }
                Toggle(isOn: Binding(get: { faceIDToOpen }, set: { enabled in
                    if enabled { Task { await enableFaceID() } } else { faceIDToOpen = false }
                })) { Text("Face ID to open") }
                    .tint(YansuTheme.accent)
                    .accessibilityIdentifier("settings.faceID")
                VStack(alignment: .leading, spacing: 5) {
                    Text("Keep on this phone")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(YansuTheme.ink)
                    Text("Briefs, chats, tasks and ideas you have opened are saved on this iPhone.")
                        .font(.footnote)
                        .foregroundStyle(YansuTheme.secondary)
                }
                Link("Privacy Policy", destination: URL(string: "https://www.isoform.ai/privacy")!)
                    .accessibilityIdentifier("settings.privacyPolicy")
                Button("Remove saved briefs") { confirmDeleteCache = true }
                    .buttonStyle(YansuSecondaryButtonStyle())
                    .accessibilityIdentifier("settings.deleteCachedBriefs")
                Button(role: .destructive) {
                    if pairing.isPaired { confirmUnpair = true } else { confirmForgetPairing = true }
                } label: {
                    Text("Clear this phone")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(YansuTheme.error)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityIdentifier("settings.clearThisPhone")
            }
        }
        .accessibilityIdentifier("settings.privacy")
    }

    private func settingsGroup<Content: View>(_ title: String, identifier: String,
                                               @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            SectionLabel(title: NSLocalizedString(title, comment: ""))
            content()
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(identifier)
    }

    private var ownerName: String? {
        guard let name = pairing.paired?.hubName, let suffix = name.range(of: "'s") else { return nil }
        let owner = String(name[..<suffix.lowerBound]).trimmingCharacters(in: .whitespaces)
        return owner.isEmpty ? nil : owner
    }

    private var connectionDescription: String {
        PairingLineCopy.connection(remote: pairing.isRemote, online: brief.hubOnline,
                                   lastHeard: todayStore.snapshot?.generatedDate)
    }

    private func loadStatus() async {
        guard pairing.isPaired, pairing.paired?.scopes.contains("brief:read") == true else { return }
        status = try? await app.hub.status()
        if let status { keepAwake.accept(status) }
        await keepAwake.refresh()
    }

    private func enableFaceID() async {
        do {
            let context = LAContext()
            guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: nil),
                  try await context.evaluatePolicy(.deviceOwnerAuthentication,
                                                   localizedReason: String(localized: "Enable app lock")) else {
                faceIDFailure = true
                return
            }
            // Remove the previous public widget snapshot before enabling app lock.
            guard app.widget.clear() else { faceIDFailure = true; return }
            faceIDToOpen = true
        } catch {
            faceIDFailure = true
        }
    }

    // MARK: About

    private var aboutSection: some View {
        Text("Version \(Self.appVersion). Briefs are end to end encrypted.")
            .font(.footnote)
            .foregroundStyle(YansuTheme.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("settings.about")
    }

    // MARK: Helpers

    private func detailRow(_ title: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.footnote)
                .foregroundStyle(YansuTheme.tertiary)
            Text(value)
                .font(.body)
                .foregroundStyle(YansuTheme.ink)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .accessibilityElement(children: .combine)
    }

    private func unpair() async {
        unpairing = true
        defer { unpairing = false }
        do {
            try await app.unpair()
        } catch {
            unpairFailed = true
        }
    }

    private func removeSavedPairing() {
        do {
            try app.forgetPairingLocally()
        } catch {
            removePairingFailed = true
        }
    }

    /// The wait is named like "Turning on notifications..." and "Checking with your computer...": three dots.
    static func unpairTitle(unpairing: Bool) -> String {
        unpairing ? "Unpairing..." : "Unpair this phone"
    }

    /// Product names for the scopes this pairing grants, in the order the Mac lists them. `desk:read` and `decide`
    /// are retired: an older pairing may still hold them, and they are not listed.
    static func permissionLabels(_ scopes: [String]) -> String {
        let known: [(scope: String, label: String)] = [
            ("brief:read", "Briefs"),
            ("brief:refresh", "Refresh"),
            ("presence:read", "Presence"),
            ("push", "Notifications"),
            ("chat:read", "Chat"),
            ("chat:write", "Chat replies"),
            ("memory:correct", "Corrections"),
            ("handoff:create", "One-time work"),
            ("brief:feedback", "Today feedback"),
            ("offers:read", "Ideas"),
            ("offers:decide", "Answers to ideas"),
            ("goals:read", "Goals"),
            ("goals:write", "Goal changes"),
            ("library:read", "Library"),
        ]
        let labels = known.filter { scopes.contains($0.scope) }.map(\.label)
        return labels.isEmpty ? "None" : labels.joined(separator: ", ")
    }

    /// Chat needs to read and to send messages and answers.
    static func allowsChat(_ scopes: [String]) -> Bool {
        scopes.contains("chat:read") && scopes.contains("chat:write")
    }

    /// "Not right" on a memory the brief used.
    static func allowsMemoryCorrection(_ scopes: [String]) -> Bool {
        scopes.contains("memory:correct")
    }

    /// "New one-time work" in the Chat tab.
    static func allowsOneTimeWork(_ scopes: [String]) -> Bool {
        scopes.contains("handoff:create")
    }

    /// Like and Not right on Feed cards.
    static func allowsFeedFeedback(_ scopes: [String]) -> Bool {
        scopes.contains("brief:feedback")
    }

    /// The scopes release 4 declares at one re-pair (PROTOCOL.md section 20.1). Their routes arrive step by step.
    static let releaseFourScopes = ["brief:feedback", "offers:read", "offers:decide", "goals:read", "goals:write",
                                    "library:read"]

    /// Every scope Chat, corrections, one-time work and release 4 need. A pairing that lacks one of them shows the
    /// re-pair note.
    static func allowsNewerFeatures(_ scopes: [String]) -> Bool {
        allowsChat(scopes) && allowsMemoryCorrection(scopes) && allowsOneTimeWork(scopes) &&
            releaseFourScopes.allSatisfy(scopes.contains)
    }

    static func relayHost(_ origin: String) -> String {
        URL(string: origin)?.host ?? origin
    }

    static var appVersion: String {
        let info = Bundle.main.infoDictionary
        let short = info?["CFBundleShortVersionString"] as? String ?? "0"
        let build = info?["CFBundleVersion"] as? String ?? "0"
        return "\(short) (\(build))"
    }
}

// Keep the native switch still until registration confirms the change. A native interactive
// switch animates optimistically before an asynchronous binding can report a refusal.
struct ConfirmedNotificationToggleStyle: ToggleStyle {
    func makeBody(configuration: Configuration) -> some View {
        Button {
            configuration.isOn.toggle()
        } label: {
            HStack {
                configuration.label
                Spacer()
                Toggle(isOn: .constant(configuration.isOn)) { EmptyView() }
                    .labelsHidden()
                    .toggleStyle(.switch)
                    .allowsHitTesting(false)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityRepresentation {
            Toggle(isOn: configuration.$isOn) { configuration.label }
                .toggleStyle(.switch)
        }
    }
}
