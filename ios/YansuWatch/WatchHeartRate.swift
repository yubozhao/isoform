import Foundation
import HealthKit

// The wearer's heart rate while a part is recorded (PROTOCOL.md 24.4). Read only: Isoform never
// writes to Health and starts no workout, so the readings are the ones the watch takes anyway.
// Nothing is read unless the wearer turned this on, and nothing is read while not recording.
@MainActor
final class WatchHeartRateSampler {
    private let store = HKHealthStore()
    private var query: HKAnchoredObjectQuery?
    private var readings: [Double] = []
    private var generation = 0

    static var supported: Bool { HKHealthStore.isHealthDataAvailable() }

    /// Shows the system's Health sheet the first time. Health never says whether reading was
    /// allowed; a wearer who declined simply has no readings, and recording goes on without them.
    func requestAccess() async -> Bool {
        guard Self.supported else { return false }
        do {
            try await store.requestAuthorization(toShare: [], read: [HKQuantityType(.heartRate)])
            return true
        } catch {
            return false
        }
    }

    /// Starts collecting the readings taken from `date` on.
    func begin(at date: Date) {
        cancel()
        guard Self.supported else { return }
        generation += 1
        let current = generation
        let unit = HKUnit.count().unitDivided(by: .minute())
        let take: @Sendable ([HKSample]?) -> Void = { [weak self] samples in
            let values = (samples as? [HKQuantitySample])?.map { $0.quantity.doubleValue(for: unit) } ?? []
            guard !values.isEmpty else { return }
            Task { @MainActor in
                guard let self, self.generation == current else { return }
                self.readings.append(contentsOf: values)
            }
        }
        let query = HKAnchoredObjectQuery(
            type: HKQuantityType(.heartRate),
            predicate: HKQuery.predicateForSamples(withStart: date, end: nil, options: .strictStartDate),
            anchor: nil, limit: HKObjectQueryNoLimit) { _, samples, _, _, _ in take(samples) }
        query.updateHandler = { _, samples, _, _, _ in take(samples) }
        store.execute(query)
        self.query = query
    }

    /// Stops and hands over the summary of what was read. Nil without a reading.
    func end() -> WatchRecordingContext.HeartRate? {
        let summary = WatchRecordingContext.HeartRate(readings: readings)
        cancel()
        return summary
    }

    func cancel() {
        if let query { store.stop(query) }
        query = nil
        readings = []
        generation += 1
    }
}

/// What the wearer chose to send with recordings. Everything is off until they turn it on.
enum WatchContextSettings {
    static let heartRateKey = "watch.context.heartRate"

    static var heartRate: Bool {
        get { UserDefaults.standard.bool(forKey: heartRateKey) }
        set { UserDefaults.standard.set(newValue, forKey: heartRateKey) }
    }
}
