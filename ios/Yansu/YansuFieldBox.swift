import SwiftUI

extension View {
    /// The Chat composer's field box: the surface fill with a hairline border. Every text field that sits on a card
    /// uses it, so a field reads the same in light and dark mode. Use it with `.textFieldStyle(.plain)`.
    func yansuFieldBox(cornerRadius: CGFloat = 20, horizontal: CGFloat = 14, vertical: CGFloat = 11) -> some View {
        self
            .padding(.horizontal, horizontal)
            .padding(.vertical, vertical)
            .background(YansuTheme.surface, in: RoundedRectangle(cornerRadius: cornerRadius, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
                    .strokeBorder(YansuTheme.border, lineWidth: 1)
            }
    }
}
