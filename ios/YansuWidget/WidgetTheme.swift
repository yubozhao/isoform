import SwiftUI
import UIKit

/// The Isoform colours the widget draws with: Paper in light mode, One Dark in dark mode. The widget cannot import the
/// app target, so these values mirror YansuTheme in ios/Yansu/Theme.swift. Keep the two in step.
enum WidgetTheme {
    static let background = dynamic(light: 0xf6f6f9, dark: 0x121217) // color-background
    static let surface = dynamic(light: 0xffffff, dark: 0x1a1a21) // color-panel
    static let border = dynamic(light: 0xe2e3ec, dark: 0x2a2b35) // color-border
    static let ink = dynamic(light: 0x16161d, dark: 0xececf2) // color-ink
    static let secondary = dynamic(light: 0x5d5f6e, dark: 0xa6a8b8) // color-muted
    static let tertiary = dynamic(light: 0x66687c, dark: 0x868899) // color-subtle
    static let accent = dynamic(light: 0x4f5ed8, dark: 0x7a8aff) // color-accent
    static let warning = dynamic(light: 0x9b4f1d, dark: 0xe8a066) // color-warning

    private static func dynamic(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: UIColor { traits in
            let hex = traits.userInterfaceStyle == .dark ? dark : light
            return UIColor(red: Double((hex >> 16) & 0xff) / 255, green: Double((hex >> 8) & 0xff) / 255,
                           blue: Double(hex & 0xff) / 255, alpha: 1)
        })
    }
}

/// Each widget region identifies its destination in the app.
enum WidgetLink {
    static let brief = URL(string: "isoform://brief")!
    static let settings = URL(string: "isoform://settings")!
    static let day = URL(string: "isoform://today?section=day")!
    static func brief(id: String?) -> URL {
        guard let id else { return brief }
        var parts = URLComponents()
        parts.scheme = "isoform"
        parts.host = "brief"
        parts.queryItems = [URLQueryItem(name: "id", value: id)]
        return parts.url ?? brief
    }
    static func item(kind: String?, id: String) -> URL? {
        guard kind == "task" || kind == "chat" else { return nil }
        var parts = URLComponents()
        parts.scheme = "isoform"
        parts.host = kind == "task" ? "tasks" : "chat"
        parts.queryItems = [URLQueryItem(name: "id", value: id)]
        return parts.url
    }
}
