/**
 * Whether chat shows each tool's raw input and result. Its Settings switch
 * went (computer-conformance-answers v1.0.0 T2, with the steps expander in
 * computer-chat v1.2.0), so it is always the default, off, and a choice saved
 * before ("yansu.show-tool-steps") is ignored: a value nobody can see or
 * change must not steer the app.
 */
export function useShowToolSteps(): readonly [boolean] {
    return [false] as const;
}
