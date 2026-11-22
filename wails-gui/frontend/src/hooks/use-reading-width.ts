/**
 * The chat's reading width. Its Settings switch went (computer-conformance-
 * answers v1.0.0 T2: the window's widths decide it), so it is always the
 * default, full width, and a choice saved before ("yansu.chat-wide-mode") is
 * ignored: a value nobody can see or change must not steer the app.
 */
export function useReadingWidth(): readonly [boolean] {
    return [true] as const;
}
