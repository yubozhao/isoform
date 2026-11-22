import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import appSource from "../App.tsx?raw";
import { useReadingWidth } from "./use-reading-width";

describe("Reading width and window chrome", () => {
    it("ignores a reading width saved before its switch went (answers v1.0.0 T2)", () => {
        localStorage.setItem("yansu.chat-wide-mode", "false");
        const { result } = renderHook(useReadingWidth);
        expect(result.current[0]).toBe(true);
        localStorage.removeItem("yansu.chat-wide-mode");
    });
    it("removes the update popup, its automatic hydration and top-right reading-width action", () => {
        expect(appSource).not.toContain('title="About & Updates"');
        expect(appSource).not.toContain("setUpdateDialogOpen");
        expect(appSource).not.toContain("updates.upToDate");
        expect(appSource).not.toContain("CheckForUpdates");
        expect(appSource).not.toContain("setChatWideMode");
        expect(appSource).toContain("const [chatWideMode] = useReadingWidth()");
        // The account controls now live exclusively in the sidebar (#183).
        expect(appSource).not.toContain("topBarUserPopoverOpen");
        expect(appSource).not.toContain("UserPopoverMenu");
        // One right side (K7): the project toolbar with Changes and Context is gone.
        expect(appSource).not.toContain('data-testid="project-session-toolbar"');
    });
});
