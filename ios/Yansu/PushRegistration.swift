import Foundation
import UIKit
import UserNotifications

// Push is opt-in from Settings. Permission is requested only after the user turns the switch on.
// The APNs token exists only in this process: it is asked for again on each launch while push is on
// and is never written to the Keychain, the vault or the cache. The relay learns the token through
// PUT /v1/push/token; the alert carries no content (PROTOCOL.md section 9).
@MainActor
final class PushRegistration: ObservableObject {
    enum State: Equatable {
        case off
        case registering
        case on
        case denied
        case unavailable
    }

    // Persist only known failure codes, never an error description, token or relay response.
    enum FailureReason: String, CaseIterable {
        case permissionDenied, permissionRequired, registration, relayUnavailable, relayUnreachable, tokenRejected, deviceLimit

        func message(locale: Locale = .current) -> String {
            let chinese = locale.language.languageCode?.identifier == "zh"
            switch self {
            case .permissionDenied:
                return chinese ? "Isoform 的通知已在 iOS 设置中关闭。" : "Notifications for Isoform are off in iOS Settings."
            case .permissionRequired:
                return chinese ? "请在这部手机上打开通知开关，以允许 Isoform 发送通知。" : "Turn on Notifications on this phone to allow alerts from Isoform."
            case .registration:
                return chinese ? "这部手机无法注册推送通知。测试通知仍可使用。" : "This phone couldn't register for push notifications. The test notification still works."
            case .relayUnavailable:
                return chinese ? "此中继暂不支持通知。通知开启期间，Isoform 会重试。" : "Notifications are not available on this relay yet. Isoform will retry while alerts are on."
            case .relayUnreachable:
                return chinese ? "连接到中继后，Isoform 会为这部手机注册通知。" : "Isoform will register this phone for notifications when it can reach the relay."
            case .tokenRejected:
                return chinese ? "中继未接受这部手机的通知令牌。请关闭通知后重新开启。" : "The relay did not accept this phone's notification token. Turn notifications off and on again."
            case .deviceLimit:
                return chinese ? "这台电脑已为 10 部手机开启通知。请在另一部手机上关闭通知，然后重试。" : "This computer already has notifications on for 10 phones. Turn notifications off on another phone, then try again."
            }
        }

        var isPermissionFailure: Bool { self == .permissionDenied || self == .permissionRequired }
    }

    static let optInKey = "yansu.push.optIn"
    static let failureReasonKey = "yansu.push.failureReason"
    // Set once the explanation has been answered, so the question is asked at most once.
    static let permissionAskedKey = "yansu.push.permissionAsked"
    static let pendingRemovalKey = "yansu.push.pendingRemovalPairing"
    static let revocationGrace: TimeInterval = 60

    @Published private(set) var state: State
    @Published private(set) var failureReason: FailureReason? {
        didSet {
            if let failureReason {
                defaults.set(failureReason.rawValue, forKey: Self.failureReasonKey)
            } else {
                defaults.removeObject(forKey: Self.failureReasonKey)
            }
        }
    }
    var failure: String? { failureReason?.message() }
    // Drives the explanation sheet that comes before the system prompt.
    @Published var showsPermissionExplanation = false
    @Published private(set) var removalPending = false
    private(set) var token: String?
    // Lets pairing cleanup resume after a deferred token registration succeeds, or push is turned off.
    var onRegistrationReady: (() async -> Void)?

    private let vault: PairingVault
    private let relay: RelayClient
    private let defaults: UserDefaults
    private let center: UNUserNotificationCenter
    private let retrySleep: @Sendable (UInt64) async throws -> Void
    private let revocationSleep: @Sendable (UInt64) async throws -> Void
    private let now: () -> Date
    private let authorize: @MainActor () async -> Bool
    private let authorizationStatus: @MainActor () async -> UNAuthorizationStatus
    private let registerRemoteNotifications: @MainActor () -> Void
    private let unregisterRemoteNotifications: @MainActor () -> Void
    private var tokenUpload: Task<Bool, Never>?
    private var retryTask: Task<Void, Never>?
    private var revocationTask: Task<Void, Never>?
    private var revocationGeneration = UUID()
    private var disableTask: Task<Void, Never>?
    private var toggleIntent = UUID()
    private var uploadGeneration = UUID()
    private var registeredDeviceID: String?
    private var registeredToken: String?
    private var tokenCredentials: [RelayCredentials] = []
    private var disabling = false

    init(vault: PairingVault, relay: RelayClient = .shared, defaults: UserDefaults = .standard,
         center: UNUserNotificationCenter = .current(),
         retrySleep: @escaping @Sendable (UInt64) async throws -> Void = { try await Task.sleep(nanoseconds: $0) },
         revocationSleep: @escaping @Sendable (UInt64) async throws -> Void = { try await Task.sleep(nanoseconds: $0) },
         now: @escaping () -> Date = { Date() },
         authorize: (@MainActor () async -> Bool)? = nil,
         authorizationStatus: (@MainActor () async -> UNAuthorizationStatus)? = nil,
         registerRemoteNotifications: @escaping @MainActor () -> Void = { UIApplication.shared.registerForRemoteNotifications() },
         unregisterRemoteNotifications: @escaping @MainActor () -> Void = { UIApplication.shared.unregisterForRemoteNotifications() }) {
        self.vault = vault
        self.relay = relay
        self.defaults = defaults
        self.center = center
        self.retrySleep = retrySleep
        self.revocationSleep = revocationSleep
        self.now = now
        self.authorize = authorize ?? {
            let settings = await center.notificationSettings()
            switch settings.authorizationStatus {
            case .denied: return false
            case .notDetermined:
                return (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
            default: return true
            }
        }
        self.authorizationStatus = authorizationStatus ?? { await center.notificationSettings().authorizationStatus }
        self.registerRemoteNotifications = registerRemoteNotifications
        self.unregisterRemoteNotifications = unregisterRemoteNotifications
        let savedFailure = defaults.string(forKey: Self.failureReasonKey).flatMap(FailureReason.init(rawValue:))
        self.failureReason = savedFailure
        self.state = savedFailure.map { $0.isPermissionFailure ? .denied : .unavailable }
            ?? (defaults.bool(forKey: Self.optInKey) ? .registering : .off)
        self.removalPending = defaults.string(forKey: Self.pendingRemovalKey) == vault.current().map({ "\($0.hubID)/\($0.deviceID)" })
            && defaults.string(forKey: Self.pendingRemovalKey) != nil
    }

    var isOptedIn: Bool {
        defaults.bool(forKey: Self.optInKey)
    }

    private var localDelivery = false

    func setLocalDelivery(_ local: Bool) {
        guard localDelivery != local else { return }
        localDelivery = local
        uploadGeneration = UUID()
        retryTask?.cancel()
        retryTask = nil
        Task { await resumeIfOptedIn() }
    }

    // An opt-in is only an intent until APNs accepts this build. Relay retries remain cancellable.
    var isEnabled: Bool {
        isOptedIn && state != .denied && (localDelivery ? state == .on : token != nil)
    }

    var statusMessage: String? {
        if localDelivery && state == .on { return nil }
        // Keep the last failure visible during a launch retry until registration succeeds.
        if let failure { return failure }
        if removalPending { return String(localized: "notifications.alertsOffPending") }
        switch state {
        case .registering:
            return Locale.current.language.languageCode?.identifier == "zh" ? "正在开启通知..." : "Turning on notifications..."
        case .denied: return FailureReason.permissionDenied.message()
        case .unavailable: return failure
        case .off, .on: return nil
        }
    }

    var isReadyForRevocation: Bool {
        guard !disabling else { return false }
        let paired = vault.current()
        guard vault.loadError == nil else { return false }
        // An unpaired install has no replacement token to preserve, even if its opt-in survived.
        guard let paired else { return true }
        guard isOptedIn else { return true }
        if localDelivery { return true }
        if now() >= paired.pairedAt.addingTimeInterval(Self.revocationGrace) { return true }
        return token != nil && registeredToken == token && registeredDeviceID == paired.deviceID
    }

    // Settings switch on. Denied permission leaves the opt-in off and says why.
    func enable() async {
        guard !isEnabled else { return }
        let intent = UUID()
        toggleIntent = intent
        await disableTask?.value
        guard toggleIntent == intent else { return }
        // A token delivered while opted out must not light the switch before this attempt succeeds.
        token = nil
        failureReason = nil
        state = .registering
        let granted = await authorize()
        guard toggleIntent == intent else { return }
        guard granted else {
            let drain = stopRegistration()
            failureReason = .permissionDenied
            state = .denied
            await drain.value
            if toggleIntent == intent, isReadyForRevocation { await onRegistrationReady?() }
            return
        }
        defaults.set(true, forKey: Self.optInKey)
        defaults.removeObject(forKey: Self.pendingRemovalKey)
        removalPending = false
        if localDelivery {
            state = .on
            return
        }
        scheduleRevocationGrace()
        registerRemoteNotifications()
    }

    // Settings switch off. Stop asking APNs, drain pending uploads, then remove their relay tokens.
    func disable() async {
        let intent = UUID()
        toggleIntent = intent
        let drain = stopRegistration()
        failureReason = nil
        state = .off
        await drain.value
        // A later enable may already be asking for permission; only the newest off request publishes cleanup.
        if toggleIntent == intent { await onRegistrationReady?() }
    }

    private func stopRegistration() -> Task<Void, Never> {
        disabling = true
        uploadGeneration = UUID()
        retryTask?.cancel()
        retryTask = nil
        cancelRevocationGrace()
        if isOptedIn || token != nil || removalPending, let paired = vault.current() {
            defaults.set("\(paired.hubID)/\(paired.deviceID)", forKey: Self.pendingRemovalKey)
            removalPending = true
            if let credentials = try? vault.session().credentials { rememberTokenCredentials(credentials) }
        }
        defaults.set(false, forKey: Self.optInKey)
        unregisterRemoteNotifications()
        if let disableTask { return disableTask }
        let drain = Task { await drainTokenUploads() }
        disableTask = drain
        return drain
    }

    private func drainTokenUploads() async {
        // A PUT already sent may still finish at the relay. Let it settle before deleting its token.
        _ = await tokenUpload?.value
        if token != nil, let credentials = try? vault.session().credentials {
            rememberTokenCredentials(credentials)
        }
        let pendingPairing = defaults.string(forKey: Self.pendingRemovalKey)
        let currentPairing = vault.current().map { "\($0.hubID)/\($0.deviceID)" }
        let pendingCredentials = pendingPairing == currentPairing ? (try? vault.session().credentials) : nil
        var pendingRemovalAcknowledged = pendingPairing == nil
        var failed = false
        for credentials in tokenCredentials {
            do {
                try await relay.deletePushToken(credentials)
                if credentials == pendingCredentials { pendingRemovalAcknowledged = true }
            } catch { failed = true }
        }
        if !failed, pendingRemovalAcknowledged, defaults.string(forKey: Self.pendingRemovalKey) == pendingPairing {
            defaults.removeObject(forKey: Self.pendingRemovalKey)
            removalPending = false
        }
        tokenCredentials.removeAll()
        token = nil
        registeredDeviceID = nil
        registeredToken = nil
        disabling = false
        disableTask = nil
    }

    // Called when the first brief of a paired phone is stored. Shows the explanation before the system prompt,
    // and only while iOS has not decided yet and the question has not been answered before.
    func offerAfterFirstBrief() async {
        guard vault.current() != nil, !isOptedIn, !defaults.bool(forKey: Self.permissionAskedKey) else { return }
        let settings = await center.notificationSettings()
        guard settings.authorizationStatus == .notDetermined else { return }
        showsPermissionExplanation = true
    }

    // The sheet's primary button. Answering counts as asked, then the normal enable path runs the system prompt.
    func acceptPermissionExplanation() async {
        markPermissionAsked()
        await enable()
    }

    // The sheet's secondary button, or dismissing the sheet. The system prompt is not shown.
    func declinePermissionExplanation() {
        markPermissionAsked()
    }

    private func markPermissionAsked() {
        defaults.set(true, forKey: Self.permissionAskedKey)
        showsPermissionExplanation = false
    }

    // Settings' test button. Asks iOS for permission when it has not decided yet, then schedules a moment notice.
    func sendTestNotification() async -> String {
        var settings = await center.notificationSettings()
        if settings.authorizationStatus == .notDetermined {
            _ = try? await center.requestAuthorization(options: [.alert, .sound, .badge])
            settings = await center.notificationSettings()
        }
        guard [.authorized, .provisional, .ephemeral].contains(settings.authorizationStatus) else {
            return "Turn on notifications for Isoform in the iOS Settings app first."
        }
        do {
            try await LocalNotices.scheduleTest(center: center)
            return String(localized: "notifications.localTestScheduled")
        } catch {
            return "Isoform could not schedule the test notification."
        }
    }

    // True when this paired phone cannot open sealed notices, so alerts show the fixed line only.
    func noticeDetailsMissing() -> Bool {
        !vault.hasNoticeSecrets()
    }

    // Launch checks existing permission without prompting. A stale read cannot undo a newer toggle or pairing.
    func resumeIfOptedIn() async {
        scheduleRevocationGrace()
        if !isOptedIn, let pendingPairing = defaults.string(forKey: Self.pendingRemovalKey) {
            // Keep the marker while the pairing is unreadable. It may become
            // available after first unlock without recreating this object.
            guard vault.loadError == nil, let paired = vault.current() else { return }
            guard pendingPairing == "\(paired.hubID)/\(paired.deviceID)" else {
                defaults.removeObject(forKey: Self.pendingRemovalKey)
                removalPending = false
                return
            }
            removalPending = true
            await stopRegistration().value
            return
        }
        if isOptedIn {
            // A newer opt-in supersedes a previous pending opt-out, including a
            // process exit between saving the opt-in and removing its marker.
            defaults.removeObject(forKey: Self.pendingRemovalKey)
            removalPending = false
        }
        guard isOptedIn, !disabling, let paired = vault.current() else { return }
        let intent = UUID()
        toggleIntent = intent
        let status = await authorizationStatus()
        guard toggleIntent == intent, isOptedIn, !disabling,
              vault.current()?.deviceID == paired.deviceID, vault.current()?.hubID == paired.hubID else { return }
        guard [.authorized, .provisional, .ephemeral].contains(status) else {
            // A cold launch has no APNs token, but the relay may still hold the previous process's
            // registration. Deleting it requires only the current pairing credentials.
            if let credentials = try? vault.session().credentials { rememberTokenCredentials(credentials) }
            let drain = stopRegistration()
            failureReason = status == .denied ? .permissionDenied : .permissionRequired
            state = .denied
            await drain.value
            if toggleIntent == intent, isReadyForRevocation { await onRegistrationReady?() }
            return
        }
        if localDelivery {
            failureReason = nil
            state = .on
            return
        }
        state = .registering
        registerRemoteNotifications()
    }

    func receiveToken(_ data: Data) async {
        guard !localDelivery, !data.isEmpty else { return }
        let hex = data.map { String(format: "%02x", $0) }.joined()
        token = hex
        _ = await restartTokenUpload()
    }

    // The relay binds a token to a device, so a replacement pairing must register it again.
    @discardableResult
    func pairingDidChange() async -> Bool {
        registeredDeviceID = nil
        registeredToken = nil
        scheduleRevocationGrace()
        guard isOptedIn else { return isReadyForRevocation }
        if token != nil {
            return await restartTokenUpload()
        } else {
            await resumeIfOptedIn()
            return isReadyForRevocation
        }
    }

    private func restartTokenUpload() async -> Bool {
        guard isOptedIn, !disabling else { return isReadyForRevocation }
        retryTask?.cancel()
        retryTask = nil
        uploadGeneration = UUID()
        return await uploadTokenIfOptedIn(generation: uploadGeneration, attempt: 0)
    }

    private func uploadTokenIfOptedIn(generation: UUID, attempt: Int) async -> Bool {
        // The relay moves a token to the last device that registers it. Drain any old upload
        // before registering a replacement, so a late response cannot move it back.
        let previous = tokenUpload
        let upload = Task {
            _ = await previous?.value
            return await uploadCurrentToken(generation: generation, attempt: attempt)
        }
        tokenUpload = upload
        let ready = await upload.value
        // This callback runs after the upload task ends, so cleanup can safely inspect registration state.
        if ready, isReadyForRevocation { await onRegistrationReady?() }
        return ready && isReadyForRevocation
    }

    private func uploadCurrentToken(generation: UUID, attempt: Int) async -> Bool {
        guard !disabling, isOptedIn, generation == uploadGeneration, let token else { return false }
        let session: SessionContext
        do {
            session = try vault.session()
        } catch RelayClientError.offline {
            // A pairing over the computer's direct path has no session until its tunnel opens, and at launch the
            // APNs token usually arrives first. Try again with the usual backoff instead of leaving notifications
            // "Turning on" until the next time the app opens.
            scheduleRetry(generation: generation, attempt: attempt)
            return false
        } catch {
            return false
        }
        rememberTokenCredentials(session.credentials)
        do {
            _ = try await relay.registerPushToken(session.credentials, deviceToken: token, environment: PushEnvironment.current)
            guard isCurrentUpload(session, token: token, generation: generation) else { return false }
            registeredDeviceID = session.deviceID
            registeredToken = token
            cancelRevocationGrace()
            tokenCredentials = [session.credentials]
            failureReason = nil
            state = .on
        } catch RelayClientError.unavailable {
            guard isCurrentUpload(session, token: token, generation: generation) else { return false }
            // Relay availability and APNs configuration can recover without another pairing.
            failureReason = .relayUnavailable
            state = .unavailable
            scheduleRetry(generation: generation, attempt: attempt)
            return false
        } catch RelayClientError.conflict {
            guard isCurrentUpload(session, token: token, generation: generation) else { return false }
            // Terminal errors keep the opt-in and defer old-device cleanup until registration,
            // explicit opt-out, or the grace deadline. 409 means the Mac has its cap of 10 tokens.
            reportUnavailable(reason: .deviceLimit)
        } catch RelayClientError.badRequest {
            guard isCurrentUpload(session, token: token, generation: generation) else { return false }
            reportUnavailable(reason: .tokenRejected)
        } catch {
            guard isCurrentUpload(session, token: token, generation: generation) else { return false }
            // Keep the opt-in and retry without requiring the user to relaunch the app.
            failureReason = .relayUnreachable
            state = .unavailable
            scheduleRetry(generation: generation, attempt: attempt)
            return false
        }
        return isReadyForRevocation
    }

    private func isCurrentUpload(_ session: SessionContext, token: String, generation: UUID) -> Bool {
        !disabling && isOptedIn && generation == uploadGeneration && self.token == token
            && vault.current()?.deviceID == session.deviceID
    }

    private func rememberTokenCredentials(_ credentials: RelayCredentials) {
        if !tokenCredentials.contains(credentials) { tokenCredentials.append(credentials) }
    }

    private func scheduleRetry(generation: UUID, attempt: Int) {
        // One second, then double up to one minute. A new token, pairing, or opt-out invalidates this loop.
        let delay = min(UInt64(60), UInt64(1) << min(attempt, 6)) * 1_000_000_000
        retryTask = Task { [weak self, retrySleep] in
            do { try await retrySleep(delay) } catch { return }
            guard let self, !Task.isCancelled, self.isOptedIn, !self.disabling,
                  generation == self.uploadGeneration else { return }
            _ = await self.uploadTokenIfOptedIn(generation: generation, attempt: attempt + 1)
        }
    }

    private func cancelRevocationGrace() {
        revocationGeneration = UUID()
        revocationTask?.cancel()
        revocationTask = nil
    }

    private func scheduleRevocationGrace() {
        cancelRevocationGrace()
        guard !disabling, isOptedIn, let paired = vault.current(), vault.loadError == nil else { return }
        guard registeredDeviceID != paired.deviceID || registeredToken != token || token == nil else { return }
        let generation = revocationGeneration
        let deadline = paired.pairedAt.addingTimeInterval(Self.revocationGrace)
        let delay = min(Self.revocationGrace, max(0, deadline.timeIntervalSince(now())))
        revocationTask = Task { [weak self, revocationSleep] in
            guard !Task.isCancelled else { return }
            if delay > 0 {
                do { try await revocationSleep(UInt64((delay * 1_000_000_000).rounded(.up))) } catch { return }
            }
            guard let self, !Task.isCancelled, generation == self.revocationGeneration,
                  self.vault.current()?.deviceID == paired.deviceID, self.isOptedIn, !self.disabling else { return }
            if self.now() < deadline {
                self.scheduleRevocationGrace()
                return
            }
            guard self.isReadyForRevocation else { return }
            await self.onRegistrationReady?()
            // Removing the old device may free the relay's token cap. Try once after this cleanup;
            // a terminal failure still needs user action, while transient failures retain their backoff.
            guard !Task.isCancelled, generation == self.revocationGeneration, !self.disabling,
                  self.isOptedIn, self.vault.current()?.deviceID == paired.deviceID, self.token != nil,
                  self.registeredDeviceID != paired.deviceID || self.registeredToken != self.token else { return }
            _ = await self.restartTokenUpload()
        }
    }

    /// APNs could not register this launch: no connection yet, or a passing service error. The person's switch
    /// stays on and the next launch or return to the app asks APNs again (`resumeIfOptedIn`); only the switch or
    /// permission denied in iOS Settings turns notifications off. It used to clear the opt-in, so one failed launch
    /// turned notifications off for good. An upload still in flight is no longer trusted to turn the state on.
    func receiveRegistrationFailure() {
        guard isOptedIn, !disabling else { return }
        uploadGeneration = UUID()
        reportUnavailable(reason: .registration)
    }

    private func reportUnavailable(reason: FailureReason) {
        retryTask?.cancel()
        retryTask = nil
        failureReason = reason
        state = .unavailable
    }
}
