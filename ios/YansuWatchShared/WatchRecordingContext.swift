import Foundation

// What the watch knew while a part was recorded (PROTOCOL.md 24.4): the wearer's heart rate and
// roughly where they were. Each needs its own permission on the watch and either may be absent.
// The same shape crosses the watch link (flattened into the transfer's metadata) and is sent to
// the computer inside the sealed POST /recordings request. Values that make no sense are
// dropped here, so a bad reading never travels and never holds back the audio.
struct WatchRecordingContext: Codable, Equatable, Sendable {
    struct HeartRate: Codable, Equatable, Sendable {
        /// Beats per minute.
        let avg: Double
        let min: Double
        let max: Double
        let samples: Int

        static let range = 20.0...260.0

        /// Summarizes the readings taken during one part. Nil without a usable reading.
        init?(readings: [Double]) {
            let usable = readings.filter { Self.range.contains($0) }
            guard let low = usable.min(), let high = usable.max() else { return nil }
            avg = (usable.reduce(0, +) / Double(usable.count)).rounded()
            min = low.rounded()
            max = high.rounded()
            samples = usable.count
        }

        init?(avg: Double, min: Double, max: Double, samples: Int) {
            guard samples > 0, samples <= 100_000, Self.range.contains(avg), Self.range.contains(min),
                  Self.range.contains(max), min <= avg, avg <= max else { return nil }
            self.avg = avg
            self.min = min
            self.max = max
            self.samples = samples
        }
    }

    struct Location: Codable, Equatable, Sendable {
        let latitude: Double
        let longitude: Double
        var accuracyMeters: Double?
        /// A neighborhood or town, when the device could name one. Never coordinates.
        var place: String?

        static let maxPlaceLength = 80

        /// Rounds to two decimals (about 1 km), so a precise position never leaves the watch.
        init?(latitude: Double, longitude: Double, accuracyMeters: Double? = nil, place: String? = nil) {
            guard latitude.isFinite, longitude.isFinite, (-90.0...90.0).contains(latitude),
                  (-180.0...180.0).contains(longitude), !(latitude == 0 && longitude == 0) else { return nil }
            self.latitude = (latitude * 100).rounded() / 100
            self.longitude = (longitude * 100).rounded() / 100
            if let accuracyMeters, accuracyMeters.isFinite, accuracyMeters > 0, accuracyMeters <= 100_000 {
                // The rounding itself hides about a kilometer.
                self.accuracyMeters = Swift.max(accuracyMeters.rounded(), 1_000)
            }
            let name = place?.split(whereSeparator: \.isNewline).joined(separator: " ")
                .trimmingCharacters(in: .whitespacesAndNewlines)
            if let name, !name.isEmpty { self.place = String(name.prefix(Self.maxPlaceLength)) }
        }
    }

    var heartRate: HeartRate?
    var location: Location?

    /// Nil when there is nothing to carry.
    init?(heartRate: HeartRate?, location: Location?) {
        guard heartRate != nil || location != nil else { return nil }
        self.heartRate = heartRate
        self.location = location
    }
}

extension WatchRecordingContext {
    /// Property-list values for a WCSession transfer's metadata.
    func write(into metadata: inout [String: Any]) {
        if let heartRate {
            metadata["hrAvg"] = heartRate.avg
            metadata["hrMin"] = heartRate.min
            metadata["hrMax"] = heartRate.max
            metadata["hrSamples"] = heartRate.samples
        }
        if let location {
            metadata["lat"] = location.latitude
            metadata["lon"] = location.longitude
            if let accuracy = location.accuracyMeters { metadata["acc"] = accuracy }
            if let place = location.place { metadata["place"] = place }
        }
    }

    /// Reads the context out of a transfer's metadata. Anything unusable is left out; the part
    /// itself is still accepted.
    init?(metadata: [String: Any]) {
        var heartRate: HeartRate?
        if let avg = metadata["hrAvg"] as? Double, let low = metadata["hrMin"] as? Double,
           let high = metadata["hrMax"] as? Double, let samples = metadata["hrSamples"] as? Int {
            heartRate = HeartRate(avg: avg, min: low, max: high, samples: samples)
        }
        var location: Location?
        if let latitude = metadata["lat"] as? Double, let longitude = metadata["lon"] as? Double {
            location = Location(latitude: latitude, longitude: longitude, accuracyMeters: metadata["acc"] as? Double,
                                place: metadata["place"] as? String)
        }
        self.init(heartRate: heartRate, location: location)
    }
}
