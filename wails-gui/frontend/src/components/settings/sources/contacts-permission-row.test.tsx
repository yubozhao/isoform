import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ContactsPermissionRow } from "./contacts-permission-row";

const mocks = vi.hoisted(() => ({
    getPermissions: vi.fn(),
    requestPermission: vi.fn(),
    openPermissionSettings: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetPlatformPermissions: mocks.getPermissions,
    RequestPermission: mocks.requestPermission,
    OpenPermissionSettings: mocks.openPermissionSettings,
}));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({ EventsOn: () => () => {} }));

function permissions(contacts: { granted: boolean; actionable: boolean } | null, platform = "darwin") {
    const items = [{ key: "microphone", label: "Microphone", granted: true, actionable: true }];
    if (contacts) items.push({ key: "contacts", label: "Contacts", ...contacts });
    return { platform, supported: true, items };
}

// Kept from Learning and memory's tests when the row moved to What Isoform
// sees › Messages' details (settings batch D3).
describe("ContactsPermissionRow", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        await i18n.changeLanguage("en");
    });

    it("opens Who you know for Contacts consent without a generic permission request", async () => {
        mocks.getPermissions.mockResolvedValue(permissions({ granted: false, actionable: true }));
        const onOpenPeople = vi.fn();
        render(<ContactsPermissionRow onOpenPeople={onOpenPeople} />);
        fireEvent.click(await screen.findByRole("button", { name: "Choose in Who you know" }));
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
        expect(mocks.requestPermission).not.toHaveBeenCalled();
        expect(mocks.openPermissionSettings).not.toHaveBeenCalled();
    });

    it("shows nothing when Contacts is granted or not offered", async () => {
        mocks.getPermissions.mockResolvedValue(permissions(null));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        await waitFor(() => expect(mocks.getPermissions).toHaveBeenCalled());
        expect(screen.queryByText("Recognize people from your Contacts")).not.toBeInTheDocument();
    });

    it("does not ask for Contacts where the computer cannot grant it", async () => {
        mocks.getPermissions.mockResolvedValue(permissions({ granted: false, actionable: false }, "windows"));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        await waitFor(() => expect(mocks.getPermissions).toHaveBeenCalled());
        await act(async () => {
            await Promise.resolve();
        });
        expect(screen.queryByText("Recognize people from your Contacts")).not.toBeInTheDocument();
    });
});
