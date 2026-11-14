import SwiftUI

// The watch's colors, taken from docs/design-system/tokens.json (dark surface values): a warm
// near-black ground, warm text grays, and the recording red used only while recording.
enum WatchTheme {
    static let ground = Color(red: 0.082, green: 0.075, blue: 0.067)
    static let surface = Color(red: 0.137, green: 0.125, blue: 0.114)
    static let text = Color(red: 0.957, green: 0.945, blue: 0.929)
    static let secondary = Color(red: 0.710, green: 0.682, blue: 0.651)
    static let tertiary = Color(red: 0.522, green: 0.494, blue: 0.467)
    static let recording = Color(red: 0.851, green: 0.325, blue: 0.290)
}
