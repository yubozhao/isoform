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
            // Ranked as phone-settings v1.0.0: what everything depends on first, then trust, then the Yes path.
            VStack(alignment: .leading, spacing: 24) {
                settingsGroup("set.computer", identifier: "settings.group.mac") { pairingSection }
                if pairing.isPaired, status?.observingPauseControl == true {
                    ClosedEyesRow(status: $status, hub: app.hub)
                }
                if let notice = brief.notice {
                    NoticeBanner(message: notice, dismiss: brief.dismissNotice)
                        .accessibilityIdentifier("settings.briefNotice")
                }
                VStack(spacing: 0) {
                    if pairing.isPaired {
                        Button { showsSees = true } label: {
                            settingsRow(String(localized: "set.data"), sub: String(localized: "set.dataSub"))
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("settings.yourData.open")
                        rowDivider
                        NavigationLink {
                            YourAgentsView(hub: agentsHub)
                        } label: {
                            settingsRow(String(localized: "set.agent"), sub: String(localized: "set.agentSub"))
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("settings.yourAgents.open")
                    }
                    // Shown only when the paired computer takes a voice sample from the phone.
                    if status?.ownerVoice == true, Self.allowsOwnerVoice(pairing.paired?.scopes ?? []) {
                        rowDivider
                        NavigationLink {
                            OwnerVoiceView(hub: app.hub, session: { [vault = app.vault] in try vault.session() })
                        } label: {
                            settingsRow(String(localized: "set.voice"), sub: nil)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("settings.ownerVoice.open")
                    }
                    // Apple Watch opens the Watch tab (phone-settings v1.0.0 rule 7). Shown only while a watch is
                    // paired; with none, nothing about a watch shows.
                    if watchRecordings.connection.hasWatch {
                        rowDivider
                        Button {
                            app.router.selectedTab = .watch
                            app.router.sheet = nil
                        } label: {
                            settingsRow(String(localized: "set.watch"), sub: String(localized: "Opens the Watch tab"))
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("settings.watchRecordings.open")
                    } else if watchRecordings.worthShowing {
                        // No watch is paired, but recordings are still held (made on this phone, or from a watch
                        // since unpaired): their list stays reachable here, as it was.
                        rowDivider
                        NavigationLink {
                            WatchRecordingsView(model: watchRecordings)
                        } label: {
                            settingsRow(String(localized: "Recordings"), sub: WatchRecordingsCopy.summary(watchRecordings.rows))
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("settings.recordings.open")
                    }
                    rowDivider
                    NavigationLink {
                        NotificationSettingsView(notifications: app.notifications)
                    } label: {
                        settingsRow(String(localized: "set.notif"), sub: String(localized: "set.notifSub"))
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("settings.notifications.open")
                }
                .padding(.horizontal, 16)
                .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: YansuTheme.cornerRadius))
                settingsGroup("Isoform", identifier: "settings.group.isoform") {
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
                                    .foregroundStyle(YansuTheme.secondary)
                            }
                        }
                    }.buttonStyle(.plain).accessibilityIdentifier("settings.yourAgent.open")
                }
                settingsGroup("set.phone", identifier: "settings.group.privacy") { privacySection }
                aboutSection
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
                        VStack(alignment: .leading, spacing: 3) {
                            Text(String.localizedStringWithFormat(String(localized: "set.paired"), paired.hubName))
                                .font(.body)
                                .foregroundStyle(YansuTheme.ink)
                            Text(verbatim: computerState)
                                .font(.footnote)
                                .foregroundStyle(YansuTheme.secondary)
                                .accessibilityIdentifier("settings.computerState")
                        }
                        if let value = keepAwake.on {
                            Toggle(isOn: Binding(get: { keepAwake.on ?? value }, set: { enabled in
                                Task { await keepAwake.set(enabled) }
                            })) {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text("set.awake")
                                    Text("set.awakeSub").font(.footnote).foregroundStyle(YansuTheme.secondary)
                                }
                            }
                                .tint(YansuTheme.accent)
                                .accessibilityIdentifier("settings.keepAwake")
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
                        DisclosureGroup("Details", isExpanded: $showDetails) {
                            VStack(alignment: .leading, spacing: 16) {
                                Button("Pair another computer") {
                                    pairing.prepareToPair()
                                    showPairing = true
                                }
                                .buttonStyle(YansuSecondaryButtonStyle())
                                .disabled(unpairing)
                                .accessibilityIdentifier("settings.pairAgain")
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

    /// This phone (phone-settings rule 10): Face ID, Clear this phone, and Unpair this phone in red, last.
    private var privacySection: some View {
        YansuCard {
            VStack(alignment: .leading, spacing: 16) {
                Toggle(isOn: Binding(get: { faceIDToOpen }, set: { enabled in
                    if enabled { Task { await enableFaceID() } } else { faceIDToOpen = false }
                })) { Text("Face ID to open") }
                    .tint(YansuTheme.accent)
                    .accessibilityIdentifier("settings.faceID")
                Button { confirmDeleteCache = true } label: {
                    VStack(alignment: .leading, spacing: 3) {
                        Text("set.clear").font(.body).foregroundStyle(YansuTheme.ink)
                        Text("set.clearSub").font(.footnote).foregroundStyle(YansuTheme.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("settings.clearThisPhone")
                Link("Privacy Policy", destination: URL(string: "https://www.isoform.ai/privacy")!)
                    .accessibilityIdentifier("settings.privacyPolicy")
                if pairing.isPaired || pairing.recoveryMessage != nil {
                    Button(role: .destructive) {
                        if pairing.isPaired { confirmUnpair = true } else { confirmForgetPairing = true }
                    } label: {
                        Text(Self.unpairTitle(unpairing: unpairing))
                            .font(.body.weight(.semibold))
                            .foregroundStyle(YansuTheme.error)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .disabled(unpairing)
                    .accessibilityIdentifier("settings.unpair")
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("settings.privacy")
    }

    /// One row of the middle list: a title, a line under it, and a chevron.
    private func settingsRow(_ title: String, sub: String?) -> some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: title).font(.body).foregroundStyle(YansuTheme.ink)
                if let sub, !sub.isEmpty {
                    Text(verbatim: sub).font(.footnote).foregroundStyle(YansuTheme.secondary).lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(YansuTheme.secondary)
        }
        .padding(.vertical, 12)
        .contentShape(Rectangle())
    }

    private var rowDivider: some View { Divider().overlay(YansuTheme.borderSubtle) }

    private var agentsHub: AgentsHubAPI {
        #if DEBUG && targetEnvironment(simulator)
        if WorkUITestSeed.isOn { return AgentsUITestHub() }
        #endif
        return app.hub
    }

    /// "Online · heard from it just now", "Asleep since 4:12 PM · answers wait on this phone", "Can't reach it ·
    /// last heard 2:41 PM" (phone-settings rule 2).
    private var computerState: String {
        let time = { (date: Date) in BriefClock.text(date, locale: locale) }
        if brief.hubOnline != false && !todayStore.hubUnreachable {
            return String(localized: "set.online")
        }
        let heard = todayStore.lastConnected ?? todayStore.snapshot?.generatedDate
        if todayStore.connectionReason != nil, let heard {
            return String.localizedStringWithFormat(String(localized: "set.asleep"), time(heard))
        }
        if let heard { return String.localizedStringWithFormat(String(localized: "set.cantReach"), time(heard)) }
        return connectionDescription
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

    /// Adding a voice sample from the phone (PROTOCOL.md 25).
    static func allowsOwnerVoice(_ scopes: [String]) -> Bool {
        scopes.contains("memory:correct") && scopes.contains("chat:write")
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
