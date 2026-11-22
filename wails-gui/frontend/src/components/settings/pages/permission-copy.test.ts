import { describe, expect, it } from "vitest";
import en from "@/i18n/locales/en/settings.json";
import zh from "@/i18n/locales/zh-CN/settings.json";
import observationSource from "./observation-page.tsx?raw";
import sharedSource from "./capture-shared.tsx?raw";

// Kept from Privacy's tests when its page went (settings batch D2): each
// source on What Isoform sees labels its permission from these keys, and
// usePermissions names them in its request failures.
function leafKeys(value: unknown, prefix = ""): string[] {
    if (value === null || typeof value !== "object") return [prefix];
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
        leafKeys(child, prefix ? `${prefix}.${key}` : key),
    );
}

describe("Observation, privacy and permission copy", () => {
    it("has every key in both languages, with label and purpose for each permission Go reports", () => {
        for (const namespace of ["observation", "privacy", "permissions"] as const) {
            expect(leafKeys(zh[namespace]).sort()).toEqual(leafKeys(en[namespace]).sort());
        }
        const goPermissionKeys = [
            "screen_recording", "accessibility", "microphone", "system_audio", "notifications",
            "automation_messages", "automation_mail", "messages_history", "contacts", "calendars",
            "reminders", "clipboard", "files", "screen_capture", "input_automation", "pipewire",
            "portal", "uinput",
        ];
        for (const locale of [en, zh]) {
            const items = locale.permissions.items as Record<string, { label?: string; usedFor?: string }>;
            for (const key of goPermissionKeys) {
                expect(items[key]?.label, key).toBeTruthy();
                expect(items[key]?.usedFor, key).toBeTruthy();
            }
        }
    });

    it("never falls back to inline English", () => {
        for (const source of [observationSource, sharedSource]) {
            expect(source).not.toContain("defaultValue");
            expect(source).not.toMatch(/\bt\(\s*["'`][^"'`]+["'`]\s*,\s*["'`]/);
        }
    });
});
