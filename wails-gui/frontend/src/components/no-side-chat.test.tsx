import { describe, expect, it } from "vitest";
import enApp from "@/i18n/locales/en/app.json";
import zhApp from "@/i18n/locales/zh-CN/app.json";
import enChat from "@/i18n/locales/en/chat.json";
import zhChat from "@/i18n/locales/zh-CN/chat.json";
import enToday from "@/i18n/locales/en/today.json";
import zhToday from "@/i18n/locales/zh-CN/today.json";
import appSource from "../App.tsx?raw";
import composerSource from "./composer.tsx?raw";
import sidebarSource from "./sidebar.tsx?raw";
import streamCardSource from "./stream-card.tsx?raw";
import streamBodySource from "./stream-body.tsx?raw";
import groupedSource from "./grouped-stream-list.tsx?raw";
import searchSource from "./search-dialog.tsx?raw";
import useProjectsSource from "../hooks/useProjects.ts?raw";
import shimSource from "../lib/wails-shim-app.ts?raw";

/** Side chats left the desktop (user, 2026-10-16): nothing opens one any
 *  more. Old side chats keep their data and still render; Search finds them. */
describe("no side chat entry point on the desktop", () => {
    it("has no opener, branch, bring-back or New side chat in the code", () => {
        for (const source of [appSource, composerSource, sidebarSource, streamCardSource, streamBodySource, groupedSource, searchSource, useProjectsSource]) {
            expect(source).not.toMatch(/handleNewSideChat|handleBranchToSideChat|onSideChatAboutThis|onBringBackToMainChat|onBranchToSideChat|onNewSideChat|BringBackToMainChat|branchToSideChat|newSideChat/);
        }
        expect(shimSource).not.toMatch(/BringBackToMainChat/);
        expect(composerSource).not.toMatch(/sideChat|DropdownMenu/);
        expect(streamCardSource).not.toMatch(/MessageBranchButton|data-branch-side-chat/);
        expect(sidebarSource).not.toMatch(/data-new-side-chat-row|sideChats/);
    });

    it("keeps only the strings that render stored side chats, in both locales", () => {
        for (const locale of [enApp, zhApp, enToday, zhToday]) {
            expect(JSON.stringify(locale)).not.toMatch(/side chat|sideChat|分支对话/i);
        }
        // Report lines and origin cards saved before the change still render.
        for (const locale of [enChat, zhChat]) {
            const text = JSON.stringify(locale);
            expect(text).not.toMatch(/branchToSideChat/);
            expect(locale.mainChat.report.side_chat.brought_back).toBeTruthy();
            expect(locale.mainChat.fallback.sideChat).toBeTruthy();
        }
        // Find offers Go to › Chat (the sidebar's name), never "New chat"
        // (RC 2 decision 5).
        expect(enApp.searchDialog).not.toHaveProperty("newChat");
        expect(JSON.stringify(enApp.searchDialog)).not.toMatch(/new chat/i);
        expect(enApp.searchDialog.groups.goTo).toBe("Go to");
        expect(enApp.sidebar.mainChat).toBe("Chat");
        expect(enApp.searchDialog.groups.chats).toBe("Chat");
        expect(zhApp.searchDialog.groups.chats).toBe("聊天");
    });

    it("labels every Today chat action Chat about this", () => {
        expect(enToday.actions.askAbout).toBe("Chat about this");
        expect(zhToday.actions.askAbout).toBe("聊聊这个");
    });
});
