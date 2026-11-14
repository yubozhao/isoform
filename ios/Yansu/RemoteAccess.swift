import Combine
import Foundation
import Network
import UIKit

enum RemoteReachability: Equatable {
    case unknown
    case reachable
    case unreachable
}

final class RemoteAccess: ObservableObject, @unchecked Sendable {
    static let shared = RemoteAccess(tunnel: IrohTunnel(), observesAppLifecycle: true)

    private struct OriginAttempt {
        let id: UUID
        let ticket: String
        let generation: UInt64
        let task: Task<UInt16, Error>
    }

    @Published private(set) var reachability: RemoteReachability = .unknown

    private let lock = NSLock()
    private let tunnel: RemoteTunnel
    private let dialTimeout: Duration
    private let observesAppLifecycle: Bool
    private let beforeListenerReady: (@Sendable (String) async -> Void)?
    private var ticketProvider: (@Sendable () -> String?)?
    private var currentTicket: String?
    private var currentPort: UInt16?
    private var lastPortTicket: String?
    private var lastPort: UInt16?
    private var generation: UInt64 = 0
    private var originRequestGeneration: UInt64 = 0
    private var latestRequestedTicket: String?
    private var originAttempt: OriginAttempt?
    private var forwarder: IrohForwarder?
    private var observers: [NSObjectProtocol] = []
    private var pathMonitor: NWPathMonitor?
    private var hasSeenPath = false

    init(tunnel: RemoteTunnel, dialTimeout: Duration = .seconds(10), observesAppLifecycle: Bool = false,
         beforeListenerReady: (@Sendable (String) async -> Void)? = nil) {
        self.tunnel = tunnel
        self.dialTimeout = dialTimeout
        self.observesAppLifecycle = observesAppLifecycle
        self.beforeListenerReady = beforeListenerReady
        if observesAppLifecycle {
            observeApplicationLifecycle()
        }
    }

    func configure(vault: PairingVault) {
        withLock {
            ticketProvider = { vault.forwarderTicket() }
        }
        startPathMonitor()
        startFromVault()
    }

    func localOrigin(ticket: String) -> URL? {
        let port = withLock {
            currentTicket == ticket ? currentPort : nil
        }
        guard let port else { return nil }
        return URL(string: "http://127.0.0.1:\(port)")
    }

    func origin(ticket: String) async -> URL? {
        let selection = withLock { () -> (URL?, OriginAttempt?) in
            originRequestGeneration &+= 1
            latestRequestedTicket = ticket
            return originLocked(ticket: ticket, requestGeneration: nil)
        }
        return await resolveOrigin(ticket: ticket, selection: selection)
    }

    private func origin(ticket: String, requestGeneration: UInt64) async -> URL? {
        let selection = withLock {
            originLocked(ticket: ticket, requestGeneration: requestGeneration)
        }
        return await resolveOrigin(ticket: ticket, selection: selection)
    }

    private func originLocked(ticket: String, requestGeneration: UInt64?) -> (URL?, OriginAttempt?) {
        if let requestGeneration,
           requestGeneration != originRequestGeneration,
           latestRequestedTicket != ticket {
            return (nil, nil)
        }
        if currentTicket == ticket, let currentPort {
            return (URL(string: "http://127.0.0.1:\(currentPort)"), nil)
        }
        if currentTicket != ticket {
            currentTicket = ticket
            currentPort = nil
            generation &+= 1
            originAttempt = nil
        }
        let selectedForwarder: IrohForwarder
        if let forwarder = self.forwarder {
            selectedForwarder = forwarder
        } else {
            selectedForwarder = IrohForwarder(
                tunnel: tunnel, dialTimeout: dialTimeout, beforeListenerReady: beforeListenerReady
            ) { [weak self] reachable in
                self?.publishReachability(reachable)
            }
            self.forwarder = selectedForwarder
        }
        if let originAttempt, originAttempt.ticket == ticket,
           originAttempt.generation == generation {
            return (nil, originAttempt)
        }
        let preferredPort = lastPortTicket == ticket ? lastPort : nil
        let attempt = OriginAttempt(
            id: UUID(), ticket: ticket, generation: generation,
            task: Task { try await selectedForwarder.start(ticket: ticket, preferredPort: preferredPort) }
        )
        originAttempt = attempt
        return (nil, attempt)
    }

    private func resolveOrigin(ticket: String, selection: (URL?, OriginAttempt?)) async -> URL? {
        let (existingOrigin, attempt) = selection
        if let existingOrigin { return existingOrigin }
        guard let attempt else { return nil }
        do {
            let port = try await attempt.task.value
            return withLock {
                if originAttempt?.id == attempt.id { originAttempt = nil }
                guard generation == attempt.generation, currentTicket == ticket else { return nil }
                currentPort = port
                lastPortTicket = ticket
                lastPort = port
                return URL(string: "http://127.0.0.1:\(port)")
            }
        } catch {
            let isCurrent = withLock { () -> Bool in
                if originAttempt?.id == attempt.id { originAttempt = nil }
                return generation == attempt.generation && currentTicket == ticket
            }
            if isCurrent { publishReachability(false) }
            return nil
        }
    }

    func activate(ticket: String?) {
        guard let ticket else {
            deactivate()
            return
        }
        startPathMonitor()
        let requestGeneration = withLock { () -> UInt64 in
            originRequestGeneration &+= 1
            latestRequestedTicket = ticket
            return originRequestGeneration
        }
        Task { _ = await origin(ticket: ticket, requestGeneration: requestGeneration) }
    }

    func deactivate() {
        let activeForwarder = withLock { () -> IrohForwarder? in
            generation &+= 1
            originRequestGeneration &+= 1
            latestRequestedTicket = nil
            currentTicket = nil
            currentPort = nil
            lastPortTicket = nil
            lastPort = nil
            originAttempt = nil
            return forwarder
        }
        publishUnknown()
        Task { await activeForwarder?.stop() }
    }

    private func observeApplicationLifecycle() {
        let center = NotificationCenter.default
        let active = center.addObserver(forName: UIApplication.didBecomeActiveNotification,
                                        object: nil, queue: .main) { [weak self] _ in
            self?.startFromVault()
        }
        let background = center.addObserver(forName: UIApplication.didEnterBackgroundNotification,
                                            object: nil, queue: .main) { [weak self] _ in
            self?.pauseForBackground()
        }
        observers = [active, background]
    }

    private func startFromVault() {
        let ticket = withLock { ticketProvider?() }
        activate(ticket: ticket)
    }

    private func pauseForBackground() {
        let activeForwarder = withLock { () -> IrohForwarder? in
            generation &+= 1
            originRequestGeneration &+= 1
            latestRequestedTicket = nil
            currentPort = nil
            originAttempt = nil
            return forwarder
        }
        publishUnknown()
        Task { await activeForwarder?.stop() }
    }

    private func startPathMonitor() {
        let monitor = withLock { () -> NWPathMonitor? in
            guard pathMonitor == nil else { return nil }
            let monitor = NWPathMonitor()
            pathMonitor = monitor
            return monitor
        }
        guard let monitor else { return }
        monitor.pathUpdateHandler = { [weak self] _ in
            guard let self else { return }
            let shouldDrop = self.withLock { () -> Bool in
                if !self.hasSeenPath {
                    self.hasSeenPath = true
                    return false
                }
                return true
            }
            guard shouldDrop else { return }
            let activeForwarder = self.withLock { self.forwarder }
            Task { await activeForwarder?.dropConnection() }
        }
        monitor.start(queue: DispatchQueue(label: "com.isoform.remote-path-monitor"))
    }

    private func publishReachability(_ reachable: Bool) {
        DispatchQueue.main.async {
            self.reachability = reachable ? .reachable : .unreachable
        }
    }

    private func publishUnknown() {
        DispatchQueue.main.async {
            self.reachability = .unknown
        }
    }

    private func withLock<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}
