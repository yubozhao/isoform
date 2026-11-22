import Foundation
import UIKit
import WatchConnectivity

// The phone's end of the watch link. It receives recording parts from the watch app, hands them
// to the forwarder, and tells the watch where each recording is. It asks for no permission and
// shows nothing; without a paired watch it does nothing.
final class WatchLink: NSObject, WCSessionDelegate, @unchecked Sendable {
    static let shared = WatchLink()

    private var inbox: WatchRecordingInbox?
    private var forwarder: WatchRecordingForwarder?
    private var observers: [NSObjectProtocol] = []
    /// The next pass the phone makes by itself, for parts that are still waiting.
    private var retryTask: Task<Void, Never>?

    /// Called once at launch with the pairing vault and the hub channel.
    func start(directory: URL, vault: PairingVault, hub: RecordingHubAPI) {
        guard forwarder == nil, WCSession.isSupported(), !WatchRecordingsModel.isSeeded else { return }
        let inbox = WatchRecordingInbox(directory: directory.appendingPathComponent("watch-recordings", isDirectory: true))
        // Parts go up as background uploads: the system finishes them while the app is closed.
        let uploads = RecordingBackgroundUploads.shared
        uploads.onEnded = { [weak self] in self?.drain() }
        // Why hours waited is said only when iOS says it (phone-watch v1.2.0 rule 1).
        uploads.onCancelReason = { reason in
            guard let cause = WatchWaitCause(rawValue: reason) else { return }
            Task { @MainActor in WatchRecordingsModel.shared.noteWaitCause(cause) }
        }
        // A part goes up in pieces, so a cut costs one piece and not the part: sent by the app
        // while it is open, handed to the system piece by piece while it is not.
        let pieces = RecordingPieceUploads(relay: RelayClient.shared, whole: uploads, background: uploads,
                                           stopWhole: { await uploads.cancel($0) })
        // A part takes the path that is up: the direct one first when the computer fetches from
        // it, the pinned relay otherwise.
        let forwarder = WatchRecordingForwarder(inbox: inbox, hub: hub, relay: pieces, session: { try vault.session() },
                                                otherSession: { vault.otherSession(than: $0) })
        self.inbox = inbox
        self.forwarder = forwarder
        Task { [weak self] in
            await forwarder.onStatesChanged { _, _ in self?.pushAll() }
            await forwarder.onActivityChanged { self?.refreshModel() }
            let recordings = await forwarder.recordings()
            let outcome = await forwarder.lastOutcome
            await WatchRecordingsModel.shared.show(recordings, outcome: outcome)
        }
        WCSession.default.delegate = self
        WCSession.default.activate()
        observers.append(NotificationCenter.default.addObserver(
            forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
                self?.readSession()
                self?.drain()
            })
    }

    /// Sends what is waiting; called when the app opens, a part arrives, or the computer answers.
    func drain() {
        Task { await drainNow() }
    }

    /// A pass asked for from here is running.
    @MainActor private var draining = false

    /// After a pass that ended waiting, no pass is started before this, however often one is
    /// asked for; the retry timer starts it then. A tap on Try again and a background run are
    /// not held back.
    @MainActor private var restUntil: Date?
    static let restAfterWaiting: TimeInterval = 10

    @MainActor
    private func drainNow(now: Bool = false) async {
        guard let forwarder else { return }
        // Every answer from the computer asks for a pass (AppEnvironment, "heard from the
        // computer"), so an open app asks most of a time a second. While a pass runs the
        // forwarder only notes that another one is wanted; the rest of this (background time, the
        // list, the retry timer) is done once, by the pass that runs.
        if draining {
            _ = await forwarder.drain()
            return
        }
        if !now, let restUntil, Date() < restUntil {
            // The retry set after the last pass stands; asking again must not push it out.
            if retryTask == nil { scheduleRetry(at: restUntil) }
            return
        }
        draining = true
        defer { draining = false }
        // Ask for time to finish when the app is in the background (a part just arrived).
        let application = UIApplication.shared
        let task = application.beginBackgroundTask(withName: "watch-recordings")
        let outcome = await forwarder.drain()
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: outcome)
        WatchRecordingsModel.shared.checked(at: Date())
        restUntil = outcome == .drained || outcome == .sending ? nil : Date().addingTimeInterval(Self.restAfterWaiting)
        let next = await forwarder.nextRetry()
        scheduleRetry(at: next)
        // While parts wait, iOS is also asked for a longer run when the phone is idle.
        BackgroundRecordingSend.schedule(waiting: next != nil)
        if task != .invalid { application.endBackgroundTask(task) }
    }

    /// Sends what is waiting, from a background refresh: waiting parts never depend on a tap.
    @MainActor
    func drainInBackground() async { await drainNow(now: true) }

    /// While the app runs, what is still waiting is sent again when its turn comes.
    @MainActor
    private func scheduleRetry(at date: Date?) {
        retryTask?.cancel()
        retryTask = nil
        guard let date else { return }
        let wait = max(5, date.timeIntervalSinceNow)
        retryTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000))
            guard !Task.isCancelled else { return }
            await self?.drainNow()
        }
    }

    /// The person tapped Sync now: ask the watch to send what it holds, send what is here, and
    /// read the computer's states again.
    @MainActor
    func syncNow() async {
        guard let forwarder else { return }
        let session = WCSession.default
        if session.activationState == .activated, session.isReachable {
            session.sendMessage([WatchRecordingWire.syncMessageKey: true], replyHandler: nil, errorHandler: nil)
        }
        // Uploads iOS has held for a while are started afresh: asked to send now, it sends now.
        await RecordingBackgroundUploads.shared.stopHeld(olderThan: 30)
        // Parts the computer refused are tried again now, not when their turn comes.
        await forwarder.retryRefused()
        await drainNow(now: true)
        await forwarder.refreshStates()
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
    }

    /// Deletes a recording from this phone, the watch and the computer.
    @MainActor
    func delete(_ id: String) async {
        guard let forwarder else { return }
        await forwarder.delete(id)
        WatchRecordingsModel.shared.forget(id)
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
    }

    /// Forget everything: the phone's recordings, and the watch's through a transfer that arrives even when the
    /// watch app isn't running.
    @MainActor
    func forgetAll() async {
        if let forwarder {
            await forwarder.forgetAll()
            WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
        }
        let session = WCSession.default
        guard WCSession.isSupported(), session.activationState == .activated, session.isPaired, session.isWatchAppInstalled else { return }
        session.transferUserInfo([WatchRecordingWire.forgetAllKey: true])
    }

    /// Tries again to send a recording the computer refused.
    @MainActor
    func retry(_ id: String) async {
        guard let forwarder else { return }
        let outcome = await forwarder.retry(id)
        WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: outcome)
    }

    /// What the watch heard as conversations; nil when the computer does not serve them.
    func conversations() async throws -> WatchConversationsAnswer? {
        guard let forwarder else { return nil }
        return try await forwarder.conversations()
    }

    func conversation(_ id: String) async throws -> WatchConversationDetail? {
        guard let forwarder else { return nil }
        return try await forwarder.conversation(id)
    }

    func notesSetting() async throws -> Bool? {
        guard let forwarder else { return nil }
        return try await forwarder.notesSetting()
    }

    func setNotes(_ on: Bool) async throws -> Bool {
        guard let forwarder else { throw HubRequestError.invalidIdentifier }
        return try await forwarder.setNotes(on)
    }

    func pauseUntil() async -> Date? { await forwarder?.pauseUntil() }

    /// Heart rate and Place as the computer has them; nil from a computer with no such settings.
    /// Read after `listeningAllowed`, which asks the computer.
    func contextSettings() async -> WatchContextSettingsAnswer? { await forwarder?.contextSettings }

    func setContext(heartRate: Bool? = nil, place: Bool? = nil) async -> WatchContextSettingsAnswer? {
        guard let forwarder, let settings = await forwarder.setContext(heartRate: heartRate, place: place) else { return nil }
        pushAll()
        return settings
    }

    /// Whether listening on the watch is allowed; nil when the computer has no such setting.
    func listeningAllowed() async -> Bool? {
        guard let forwarder else { return nil }
        await forwarder.refreshListening()
        guard await forwarder.listeningKnown else { return nil }
        return await forwarder.listenOffSince == nil
    }

    /// Turns listening off: the computer first (it drops what comes after), then the watch.
    func turnListeningOff() async -> Bool {
        guard let forwarder, await forwarder.turnListeningOff() else { return false }
        pushAll()
        return true
    }

    /// The watch says it is recording while the phone's off holds: the wearer started it there,
    /// so the off ends at the time that recording began.
    private func endOffIfWearerStarted(status: WatchLiveStatus?, onWatch: [WatchRecordingSummary]) {
        guard status == .recording, let forwarder, let started = onWatch.map(\.startedAt).max() else { return }
        Task {
            if await forwarder.wearerStartedListening(at: started) {
                await MainActor.run { WatchTabModel.shared.listening = true }
                pushAll()
            }
        }
    }

    func answerSpeaker(conversation: String, speaker: String, answer: String, name: String) async throws -> WatchConversation? {
        guard let forwarder else { return nil }
        return try await forwarder.answerSpeaker(conversation: conversation, speaker: speaker, answer: answer, name: name)
    }

    func transcript(_ id: String) async throws -> RecordingTranscript? {
        guard let forwarder else { return WatchRecordingsModel.isSeeded ? WatchRecordingsModel.seededTranscript : nil }
        return try await forwarder.transcript(id)
    }

    func audioOnPhone(_ id: String) async -> [URL] {
        await forwarder?.audioOnPhone(id) ?? []
    }

    private func refreshModel() {
        guard let forwarder else { return }
        Task { await WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil) }
    }

    /// On every return to the app: a session that never activated is activated again, and an
    /// active one is read again, so a missed report never leaves the phone without its watch.
    private func readSession() {
        let session = WCSession.default
        if session.activationState == .activated {
            publishConnection(session)
        } else {
            session.activate()
        }
    }

    private func publishConnection(_ session: WCSession) {
        let defaults = UserDefaults.standard
        if session.isPaired, session.isWatchAppInstalled { defaults.set(true, forKey: WatchConnection.hadAppKey) }
        let connection = WatchConnection.from(paired: session.isPaired, installed: session.isWatchAppInstalled,
                                              reachable: session.isReachable,
                                              hadApp: defaults.bool(forKey: WatchConnection.hadAppKey))
        let onWatch = WatchRecordingWire.summaries(fromContext: session.receivedApplicationContext)
        let status = WatchRecordingWire.status(fromContext: session.receivedApplicationContext)
        endOffIfWearerStarted(status: status, onWatch: onWatch)
        Task { @MainActor in
            WatchRecordingsModel.shared.connection = connection
            WatchRecordingsModel.shared.showOnWatch(onWatch)
            WatchRecordingsModel.shared.watchStatus = status
        }
    }

    /// Sends the watch everything the phone knows for it: where each recording is, what the
    /// computer named them, and whether observing is paused there.
    private func pushAll() {
        guard let forwarder else { return }
        Task {
            let states = await forwarder.currentStates()
            let names = await forwarder.currentNames()
            let pause = await forwarder.currentPause()
            let serial = await forwarder.pauseSerial
            let listenOff = await forwarder.listenOffSince
            let choices = await forwarder.contextChoices
            let session = WCSession.default
            // The computer hears whether a watch with Isoform is paired, once it is known.
            if session.activationState == .activated {
                await forwarder.reportPairing(session.isPaired && session.isWatchAppInstalled)
            }
            guard session.activationState == .activated, session.isPaired, session.isWatchAppInstalled else { return }
            try? session.updateApplicationContext(WatchRecordingWire.context(states, names: names, pause: pause, pauseSerial: serial,
                                                                             listenOff: listenOff, choices: choices))
        }
    }

    // MARK: WCSessionDelegate

    func session(_ session: WCSession, activationDidCompleteWith activationState: WCSessionActivationState, error: Error?) {
        guard activationState == .activated, forwarder != nil else { return }
        publishConnection(session)
        pushAll()
        drain()
    }

    func sessionReachabilityDidChange(_ session: WCSession) { publishConnection(session) }

    func sessionWatchStateDidChange(_ session: WCSession) { publishConnection(session) }

    /// The watch says what it still holds.
    func session(_ session: WCSession, didReceiveApplicationContext applicationContext: [String: Any]) {
        publishConnection(session)
    }

    func sessionDidBecomeInactive(_ session: WCSession) {}

    func sessionDidDeactivate(_ session: WCSession) {
        // A newly paired watch: activate again so its transfers arrive.
        session.activate()
    }

    /// The watch asked to pause Isoform or to end its pause. It arrives as user info (also when
    /// this app was not running) and, when the phone was reachable, as a message too; asking the
    /// computer twice is harmless.
    func session(_ session: WCSession, didReceiveUserInfo userInfo: [String: Any] = [:]) {
        carry(userInfo)
    }

    func session(_ session: WCSession, didReceiveMessage message: [String: Any]) {
        carry(message)
    }

    private func carry(_ payload: [String: Any]) {
        // The wearer changed Heart rate or Place on the watch: the computer keeps the one choice.
        if let forwarder, let choice = WatchContextChoices(userInfo: payload) {
            Task {
                let application = await UIApplication.shared
                let task = await application.beginBackgroundTask(withName: "watch-context-choice")
                if await forwarder.setContext(heartRate: choice.heartRate, place: choice.place) != nil { self.pushAll() }
                if task != .invalid { await application.endBackgroundTask(task) }
            }
            return
        }
        guard let forwarder, let request = WatchPauseRequest(userInfo: payload) else { return }
        Task {
            let application = await UIApplication.shared
            let task = await application.beginBackgroundTask(withName: "watch-pause")
            await forwarder.carry(request)
            if task != .invalid { await application.endBackgroundTask(task) }
        }
    }

    func session(_ session: WCSession, didReceive file: WCSessionFile) {
        // The file is removed when this method returns, so it is copied before anything else.
        // A file from the watch: Isoform is on it, whatever the system said a moment ago.
        UserDefaults.standard.set(true, forKey: WatchConnection.hadAppKey)
        guard let inbox, let forwarder, let info = WatchRecordingPartInfo(metadata: file.metadata) else { return }
        do {
            try inbox.accept(file: file.fileURL, info: info)
        } catch {
            return
        }
        Task {
            await forwarder.received(info)
            await WatchRecordingsModel.shared.show(await forwarder.recordings(), outcome: nil)
            self.drain()
        }
    }
}
