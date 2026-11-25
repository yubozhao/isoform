import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { ContactsPermissionRow } from "./contacts-permission-row";

const mocks = vi.hoisted(() => ({
    status: vi.fn(),
    requestContacts: vi.fn(),
    openPermissionSettings: vi.fn(),
}));

vi.mock("@/lib/wails-shim-app", () => ({
    GetContactsAccessStatus: mocks.status,
    RequestContactsAccess: mocks.requestContacts,
    OpenPermissionSettings: mocks.openPermissionSettings,
}));
vi.mock("../../../../wailsjs/runtime/runtime", () => ({ EventsOn: () => () => {} }));

function status(permission: string, supported = true) {
    return { supported, platform: "darwin", permission, consentAccepted: false, consentAcceptedAt: "", importedCount: 0, lastImportAt: "", vcardSupported: true };
}

const TITLE = "Contacts · how Isoform knows who you know";
const WITHOUT = "Without Contacts, only conversations with people Isoform already knows by their number or email are read.";

// Messages' Contacts row (computer-conformance answers v1.5.0 S15).
describe("ContactsPermissionRow", () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        mocks.openPermissionSettings.mockResolvedValue(undefined);
        await i18n.changeLanguage("en");
    });

    it("shows its state and Open System Settings once macOS has been asked, with what Messages does without it", async () => {
        mocks.status.mockResolvedValue(status("granted"));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        expect(await screen.findByText(TITLE)).toBeInTheDocument();
        expect(screen.getByText("Allowed")).toBeInTheDocument();
        expect(screen.getByText(WITHOUT)).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Open System Settings" }));
        expect(mocks.openPermissionSettings).toHaveBeenCalledWith("contacts");
        expect(mocks.requestContacts).not.toHaveBeenCalled();
    });

    it("says Not allowed when Contacts was declined", async () => {
        mocks.status.mockResolvedValue(status("denied"));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        expect(await screen.findByText("Not allowed")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Open System Settings" })).toBeInTheDocument();
    });

    it("before macOS has asked, opens Who you know for consent, never the system prompt", async () => {
        mocks.status.mockResolvedValue(status("not_determined"));
        const onOpenPeople = vi.fn();
        render(<ContactsPermissionRow onOpenPeople={onOpenPeople} />);
        expect(await screen.findByText("Not asked yet")).toBeInTheDocument();
        fireEvent.click(screen.getByRole("button", { name: "Choose in Who you know" }));
        expect(onOpenPeople).toHaveBeenCalledTimes(1);
        expect(mocks.requestContacts).not.toHaveBeenCalled();
        expect(mocks.openPermissionSettings).not.toHaveBeenCalled();
    });

    it("says a failed read with Try again, never a state", async () => {
        mocks.status.mockRejectedValueOnce(new Error("no"));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        expect(await screen.findByText("Couldn't read whether Isoform can read your Contacts.")).toBeInTheDocument();
        expect(screen.queryByText(TITLE)).not.toBeInTheDocument();
        mocks.status.mockResolvedValue(status("granted"));
        fireEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await screen.findByText(TITLE)).toBeInTheDocument();
    });

    it("says a failed Open System Settings in place with Try again", async () => {
        mocks.status.mockResolvedValue(status("denied"));
        mocks.openPermissionSettings.mockRejectedValueOnce(new Error("no"));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        fireEvent.click(await screen.findByRole("button", { name: "Open System Settings" }));
        expect(await screen.findByText("Couldn't open System Settings.")).toBeInTheDocument();
    });

    it("shows nothing where the computer has no Contacts to give", async () => {
        mocks.status.mockResolvedValue(status("not_applicable", false));
        render(<ContactsPermissionRow onOpenPeople={vi.fn()} />);
        await waitFor(() => expect(mocks.status).toHaveBeenCalled());
        await act(async () => {
            await Promise.resolve();
        });
        expect(screen.queryByText(TITLE)).not.toBeInTheDocument();
    });
});
