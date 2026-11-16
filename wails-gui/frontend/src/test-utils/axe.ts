import axe from "axe-core";

// Pass-access v1.0.0: every page's tests can ask axe for the WCAG A and AA
// problems it can see in jsdom. Colour contrast needs real layout and fonts,
// so it is checked in the browser walks, not here.
const DISABLED_IN_JSDOM = ["color-contrast", "region"];

/** Runs axe on `node` and returns one readable line per violation. */
export async function axeViolations(node: Element = document.body): Promise<string[]> {
    const results = await axe.run(node, {
        runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] },
        rules: Object.fromEntries(DISABLED_IN_JSDOM.map((id) => [id, { enabled: false }])),
        resultTypes: ["violations"],
    });
    return results.violations.flatMap((v) =>
        v.nodes.map((n) => `${v.id}: ${v.help} at ${n.target.join(" ")}`),
    );
}
