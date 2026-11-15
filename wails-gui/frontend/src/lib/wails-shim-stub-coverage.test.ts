import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

// The Quick Chat Spotlight bundles main-UI components through
// quick-chat-spotlight/frontend/src/lib/wails-app-stub.ts, which must export
// every binding the main frontend imports from wailsjs/go/main/App. A missing
// export breaks the spotlight build, and build-wails.sh only warns and ships
// no helper, so the gap is caught here instead.
describe("spotlight wails-app-stub covers the main shim", () => {
    it("exports every appBinding the shim declares", () => {
        const here = resolve(__dirname);
        const shim = readFileSync(resolve(here, "wails-shim-app.ts"), "utf8");
        const stub = readFileSync(
            resolve(here, "../../../quick-chat-spotlight/frontend/src/lib/wails-app-stub.ts"),
            "utf8",
        );
        const bindings = [...shim.matchAll(/^export const (\w+) = appBinding/gm)].map((m) => m[1]);
        const stubbed = new Set(
            [...stub.matchAll(/^export (?:const|async function|function) (\w+)/gm)].map((m) => m[1]),
        );
        const missing = bindings.filter((name) => !stubbed.has(name));
        expect(missing, "add `export const X = makeNoop(\"X\");` to wails-app-stub.ts").toEqual([]);
    });
});

// A binding whose Go method was deleted only fails when a page calls it, so a
// deletion that forgets the shim leaves dead names behind (the shadow-agent
// debug bindings outlived their methods this way).
describe("main shim bindings", () => {
    it("each names an App method in the Go code", () => {
        const here = resolve(__dirname);
        const shim = readFileSync(resolve(here, "wails-shim-app.ts"), "utf8");
        const goDir = resolve(here, "../../..");
        const methods = new Set<string>();
        for (const file of readdirSync(goDir)) {
            if (!file.endsWith(".go") || file.endsWith("_test.go")) continue;
            const src = readFileSync(resolve(goDir, file), "utf8");
            for (const m of src.matchAll(/^func \(\w+ \*App\) (\w+)\(/gm)) methods.add(m[1]);
        }
        // The name literal closes each appBinding<...>("Name") call.
        const bindings = [...shim.matchAll(/(?:>|appBinding)\(\s*["'](\w+)["']\s*,?\s*\)/g)].map((m) => m[1]);
        expect(bindings.length).toBeGreaterThan(100);
        const gone = bindings.filter((name) => !methods.has(name));
        expect(gone, "remove the shim binding (and its spotlight stub) or restore the Go method").toEqual([]);
    });
});
