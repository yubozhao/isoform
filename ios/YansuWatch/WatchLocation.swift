import CoreLocation
import Foundation

// Roughly where the wearer is while a part is recorded (PROTOCOL.md 24.4). Coarse on purpose: the
// watch asks for kilometer accuracy, rounds to two decimals before anything is stored or sent,
// and asks Apple Maps for the area's name with the rounded position only, once per area. Nothing is read unless the wearer
// turned this on, and nothing is read while not recording.
@MainActor
final class WatchLocationSampler: NSObject, CLLocationManagerDelegate {
    private let manager = CLLocationManager()
    private let geocoder = CLGeocoder()
    private var current: WatchRecordingContext.Location?
    private var generation = 0
    /// Between a part's begin and the recording's reset. A fix that arrives after listening
    /// ended is dropped, so Apple is asked only while the watch listens (v1.5.0 W9).
    private var listening = false
    private var authorization: CheckedContinuation<Bool, Never>?
    private var names = WatchPlaceNames.load()

    override init() {
        super.init()
        manager.delegate = self
        manager.desiredAccuracy = kCLLocationAccuracyKilometer
    }

    static func allowed(_ status: CLAuthorizationStatus) -> Bool {
        status == .authorizedWhenInUse || status == .authorizedAlways
    }

    var isAllowed: Bool { Self.allowed(manager.authorizationStatus) }

    /// Shows the system's location sheet the first time, and reports whether location may be read.
    func requestAccess() async -> Bool {
        switch manager.authorizationStatus {
        case .notDetermined:
            return await withCheckedContinuation { continuation in
                authorization = continuation
                manager.requestWhenInUseAuthorization()
            }
        default:
            return isAllowed
        }
    }

    /// A part began: ask once where the watch is. A recording keeps its last known place when a
    /// later part cannot get a new one (the screen is off).
    func begin() {
        guard isAllowed else {
            current = nil
            listening = false
            return
        }
        generation += 1
        listening = true
        manager.requestLocation()
    }

    /// The part ended: what is known about where it was recorded.
    func end() -> WatchRecordingContext.Location? { current }

    /// The recording ended: forget where it was.
    func reset() {
        generation += 1
        listening = false
        current = nil
        geocoder.cancelGeocode()
    }

    // MARK: CLLocationManagerDelegate

    nonisolated func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        let status = manager.authorizationStatus
        Task { @MainActor in
            guard status != .notDetermined, let waiting = self.authorization else { return }
            self.authorization = nil
            waiting.resume(returning: Self.allowed(status))
        }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
        guard let fix = locations.last else { return }
        let latitude = fix.coordinate.latitude, longitude = fix.coordinate.longitude, accuracy = fix.horizontalAccuracy
        Task { @MainActor in
            guard self.listening, let coarse = WatchRecordingContext.Location(latitude: latitude, longitude: longitude,
                                                              accuracyMeters: accuracy) else { return }
            let known = self.current
            // The same rounded position keeps the name it already has.
            if known?.latitude == coarse.latitude, known?.longitude == coarse.longitude { return }
            self.current = coarse
            self.name(coarse, generation: self.generation)
        }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {}

    /// Asks for the neighborhood or town of the rounded position. Only the coarse position is
    /// used for the lookup; without an answer the part carries no place.
    private func name(_ coarse: WatchRecordingContext.Location, generation: Int) {
        geocoder.cancelGeocode()
        // An area named before is not asked for again.
        if let known = names.name(latitude: coarse.latitude, longitude: coarse.longitude) {
            current = WatchRecordingContext.Location(latitude: coarse.latitude, longitude: coarse.longitude,
                                                     accuracyMeters: coarse.accuracyMeters, place: known)
            return
        }
        let rounded = CLLocation(latitude: coarse.latitude, longitude: coarse.longitude)
        geocoder.reverseGeocodeLocation(rounded) { [weak self] places, _ in
            let place = places?.first.flatMap { $0.subLocality ?? $0.locality ?? $0.administrativeArea }
            Task { @MainActor in
                guard let self, let place else { return }
                self.names.remember(place, latitude: coarse.latitude, longitude: coarse.longitude)
                self.names.save()
                guard self.generation == generation,
                      self.current?.latitude == coarse.latitude, self.current?.longitude == coarse.longitude else { return }
                self.current = WatchRecordingContext.Location(latitude: coarse.latitude, longitude: coarse.longitude,
                                                              accuracyMeters: coarse.accuracyMeters, place: place)
            }
        }
    }
}
