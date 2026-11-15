import Foundation

// Area names the watch already looked up, by rounded position, so a place the wearer records in
// again is not asked for again (the name comes from Apple Maps). Small on purpose: the newest
// few places only, kept on the watch and nowhere else.
struct WatchPlaceNames: Codable, Equatable, Sendable {
    static let kept = 30
    static let defaultsKey = "watch.context.placeNames"

    private struct Entry: Codable, Equatable {
        let cell: String
        let name: String
    }

    /// Oldest first.
    private var entries: [Entry] = []

    init() {}

    /// The rounded position as a key. Two decimals, as WatchRecordingContext.Location rounds.
    static func cell(latitude: Double, longitude: Double) -> String {
        String(format: "%.2f,%.2f", latitude, longitude)
    }

    func name(latitude: Double, longitude: Double) -> String? {
        let cell = Self.cell(latitude: latitude, longitude: longitude)
        return entries.last { $0.cell == cell }?.name
    }

    mutating func remember(_ name: String, latitude: Double, longitude: Double) {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        let cell = Self.cell(latitude: latitude, longitude: longitude)
        entries.removeAll { $0.cell == cell }
        entries.append(Entry(cell: cell, name: String(trimmed.prefix(WatchRecordingContext.Location.maxPlaceLength))))
        if entries.count > Self.kept { entries.removeFirst(entries.count - Self.kept) }
    }

    var count: Int { entries.count }

    static func load(from defaults: UserDefaults = .standard) -> WatchPlaceNames {
        guard let data = defaults.data(forKey: defaultsKey),
              let stored = try? JSONDecoder().decode(WatchPlaceNames.self, from: data) else { return WatchPlaceNames() }
        return stored
    }

    func save(to defaults: UserDefaults = .standard) {
        if let data = try? JSONEncoder().encode(self) { defaults.set(data, forKey: Self.defaultsKey) }
    }

    /// Turning location off forgets the names too.
    static func forget(in defaults: UserDefaults = .standard) {
        defaults.removeObject(forKey: defaultsKey)
    }
}
