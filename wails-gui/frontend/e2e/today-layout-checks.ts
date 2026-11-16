import type { Page } from "@playwright/test";

/** The window widths Today is checked at: a narrow window and a wide one. */
export const TODAY_LAYOUT_WIDTHS = [920, 1440] as const;

/** Group H of docs/today-failure-history.md, measured in the page: the
 * layout rules that broke again and again. Returns one line per broken rule;
 * an empty list passes. Used by today-layout.spec.ts (fixture data with a
 * full day strip) and daily-real-app.spec.ts (the built app). */
export async function todayLayoutFailures(page: Page): Promise<string[]> {
    return page.evaluate(() => {
        const failures: string[] = [];
        const root = document.documentElement;
        if (root.scrollWidth > root.clientWidth + 1) failures.push(`the page scrolls sideways (${root.scrollWidth} px in a ${root.clientWidth} px window)`);
        const column = document.querySelector<HTMLElement>('[data-testid="today-column"]');
        if (!column) return [...failures, "no Today column"];
        const shown = (element: Element) => {
            const rect = element.getBoundingClientRect();
            const style = getComputedStyle(element);
            return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
        };
        const name = (element: Element) => (element as HTMLElement).dataset.testid ?? element.tagName.toLowerCase();
        const area = column.getBoundingClientRect();
        // Clipping or scrolling ancestors inside the column own their overflow.
        const clipped = (element: Element) => {
            for (let node = element.parentElement; node && node !== column; node = node.parentElement) {
                if (getComputedStyle(node).overflowX !== "visible") return true;
            }
            return false;
        };
        for (const element of Array.from(column.querySelectorAll("*"))) {
            if (!shown(element) || clipped(element)) continue;
            const rect = element.getBoundingClientRect();
            if (rect.right > area.right + 1 || rect.left < area.left - 1) {
                failures.push(`${name(element)} runs past the Today column (${Math.round(rect.left)}-${Math.round(rect.right)} px, column ${Math.round(area.left)}-${Math.round(area.right)} px)`);
                break;
            }
        }
        // Sections of the column never overlap each other.
        const sections = Array.from(column.children).filter((element) => shown(element) && ["static", "relative"].includes(getComputedStyle(element).position));
        for (let i = 1; i < sections.length; i++) {
            const above = sections[i - 1].getBoundingClientRect(), below = sections[i].getBoundingClientRect();
            if (below.top < above.bottom - 1) failures.push(`${name(sections[i])} overlaps ${name(sections[i - 1])} by ${Math.round(above.bottom - below.top)} px`);
        }
        const track = column.querySelector('[data-testid="today-day-track"]');
        if (track) {
            const strip = track.getBoundingClientRect();
            const hours = Array.from(column.querySelectorAll(".today-day-hour")).filter(shown).map((element) => element.getBoundingClientRect()).sort((a, b) => a.left - b.left);
            hours.forEach((rect, index) => {
                if (rect.left < strip.left - 1 || rect.right > strip.right + 1) failures.push(`hour label ${index + 1} sits outside the strip`);
                if (index > 0 && rect.left < hours[index - 1].right + 5) failures.push(`hour labels ${index} and ${index + 1} touch`);
            });
            for (const label of Array.from(column.querySelectorAll<HTMLElement>(".today-day-block-label"))) {
                if (label.textContent && label.scrollWidth > label.clientWidth + 1) failures.push(`the strip cuts the name "${label.textContent}"`);
            }
            for (const free of Array.from(column.querySelectorAll<HTMLElement>('[data-testid="today-day-free"] > span'))) {
                if (free.getBoundingClientRect().width > free.parentElement!.getBoundingClientRect().width + 1) failures.push(`"${free.textContent}" is wider than its free time`);
            }
            const under = Array.from(column.querySelectorAll('[data-testid="today-day-brief-label"]')).map((element) => element.getBoundingClientRect()).sort((a, b) => a.left - b.left);
            under.forEach((rect, index) => {
                if (index > 0 && rect.left < under[index - 1].right) failures.push(`labels under the strip ${index} and ${index + 1} overlap`);
            });
        }
        return failures;
    });
}
