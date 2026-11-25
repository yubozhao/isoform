// @vitest-environment node
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

// The captured-counts card went (computer-conformance answers v1.5.0 T16):
// its numbers are on Your data's source rows. Its anchor and Today's
// onOpenCaptureCounts link went with it, and nothing may bring them back.
const src = resolve(__dirname, "..");

function sources(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
        const path = resolve(dir, entry.name);
        if (entry.isDirectory()) return entry.name === "node_modules" ? [] : sources(path);
        return /\.(ts|tsx)$/.test(entry.name) && path !== __filename ? [path] : [];
    });
}

describe("captured-counts card is gone", () => {
    it("has no card file, no import of it and no link to it", () => {
        expect(existsSync(resolve(src, "components/settings/pages/captured-counts-card.tsx"))).toBe(false);
        const readers = sources(src).filter((path) =>
            /captured-counts-card|CAPTURED_COUNTS_ANCHOR|onOpenCaptureCounts/.test(readFileSync(path, "utf8")),
        );
        expect(readers).toEqual([]);
    });
});
