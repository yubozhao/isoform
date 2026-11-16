import type { Page } from "@playwright/test";
import { TODAY_LAYOUT_WIDTHS } from "./today-layout-checks";

/** The window widths every page is checked at, the same as Today's. */
export const PAGE_LAYOUT_WIDTHS = TODAY_LAYOUT_WIDTHS;

/** The layout rules today-layout-checks.ts holds for Today, for any page:
 * no sideways scroll, nothing past the page's content area, its sections
 * never on top of each other, and its rows (rowSelector) never on top of
 * each other. A real day may have no rows yet, so the built app passes
 * requireRows false. Returns one line per broken rule; an empty list passes. Used by
 * page-layout.spec.ts (fixture data) and daily-real-app.spec.ts (the built
 * app). */
export async function pageLayoutFailures(page: Page, areaSelector: string, rowSelector: string, requireRows = true): Promise<string[]> {
    return page.evaluate(([areaSel, rowSel, needRows]) => {
        const failures: string[] = [];
        const root = document.documentElement;
        if (root.scrollWidth > root.clientWidth + 1) failures.push(`the page scrolls sideways (${root.scrollWidth} px in a ${root.clientWidth} px window)`);
        const shown = (element: Element) => {
            const rect = element.getBoundingClientRect();
            const style = getComputedStyle(element);
            return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
        };
        // Retained pages stay mounted while hidden: check the one on screen.
        const column = Array.from(document.querySelectorAll<HTMLElement>(areaSel)).find(shown);
        if (!column) return [...failures, `no ${areaSel}`];
        const name = (element: Element) => (element as HTMLElement).dataset.testid ?? element.tagName.toLowerCase();
        const area = column.getBoundingClientRect();
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
                failures.push(`${name(element)} runs past ${areaSel} (${Math.round(rect.left)}-${Math.round(rect.right)} px, area ${Math.round(area.left)}-${Math.round(area.right)} px)`);
                break;
            }
        }
        const stacked = (elements: Element[], what: string) => {
            for (let i = 1; i < elements.length; i++) {
                const above = elements[i - 1].getBoundingClientRect(), below = elements[i].getBoundingClientRect();
                if (below.top < above.bottom - 1) failures.push(`${what} ${name(elements[i])} overlaps ${name(elements[i - 1])} by ${Math.round(above.bottom - below.top)} px`);
            }
        };
        stacked(Array.from(column.children).filter((element) => shown(element) && ["static", "relative"].includes(getComputedStyle(element).position)), "section");
        const rows = Array.from(column.querySelectorAll(rowSel)).filter(shown);
        if (needRows && rows.length === 0) failures.push(`no ${rowSel} rows`);
        // Rows may sit side by side in a grid; two rows fail only when their
        // boxes cover each other.
        const boxes = rows.map((row) => row.getBoundingClientRect());
        for (let i = 0; i < boxes.length; i++) {
            for (let j = i + 1; j < boxes.length; j++) {
                const a = boxes[i], b = boxes[j];
                const x = Math.min(a.right, b.right) - Math.max(a.left, b.left);
                const y = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
                if (x > 1 && y > 1) failures.push(`rows ${i + 1} and ${j + 1} overlap by ${Math.round(y)} px`);
            }
        }
        return failures;
    }, [areaSelector, rowSelector, requireRows] as const);
}
