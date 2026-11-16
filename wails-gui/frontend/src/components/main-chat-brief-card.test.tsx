import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import en from "@/i18n/locales/en/chat.json";
import { MainChatBriefCard } from "./main-chat-brief-card";

const getByID = vi.hoisted(() => vi.fn());
const handlers = vi.hoisted(() => ({} as Record<string, Array<() => void>>));
vi.mock("../../wailsjs/runtime/runtime", () => ({
    EventsOn: (name: string, callback: () => void) => { (handlers[name] ??= []).push(callback); return () => {}; },
}));
vi.mock("@/lib/wails-shim-app", () => ({
    GetTodayBriefByID: (...args: unknown[]) => getByID(...args),
    GetMobileBriefAudio: vi.fn(),
    GetMobileBriefHealth: vi.fn(),
}));

function view(brief: Record<string, unknown> | null, wordsWithheld = '') {
    return { brief, wordsWithheld, building: false, enabled: true, nextAt: "", nextSlot: "", empty: false, missing: [] };
}

const withWords = {
    briefId: "brief-1", slot: "evening", asOf: "2026-10-28T18:00:00Z", timezone: "UTC", narrativeStatus: "ok",
    narrative: [{ text: "Quiet day. Priya is waiting on your review." }],
    audio: { mime: "audio/wav", bytes: 10, durationSeconds: 90 },
};
// A brief with more than the card shows: Read it opens the second paragraph (design R40 v1.0.0).
const withMore = { ...withWords, narrative: [...withWords.narrative, { text: "Review the launch notes before you sign off." }] };

describe("MainChatBriefCard", () => {
    beforeEach(() => {
        for (const key of Object.keys(handlers)) delete handlers[key];
        getByID.mockReset();
        i18n.changeLanguage("en");
    });

    it('updates a Friday due line on Monday and does not play stale audio', async () => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date('2026-11-02T12:00:00-08:00'));
        try {
            getByID.mockResolvedValue(view({
                ...withWords, briefId: '2026-10-30-evening', asOf: '2026-10-30T18:00:00-07:00', timezone: 'America/Los_Angeles',
                sections: { commitments: [{ refs: ['due'], dueAt: '2026-10-30T23:59:00-07:00' }] },
                narrative: [{ text: 'The commitment is due today.', refs: ['due'] }],
            }));
            render(<MainChatBriefCard briefId="2026-10-30-evening" onOpenBrief={vi.fn()} />);
            expect(await screen.findByText('The commitment was due Friday.')).toBeInTheDocument();
            expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
            expect(screen.getByRole('button', { name: en.mainChat.card.button.readIt })).toBeInTheDocument();
        } finally {
            vi.useRealTimers();
        }
    });

    it.each([
        ["en", "Evening brief · 5:24 PM"],
        ["zh-CN", "晚间简报 · 17:24"],
    ])("names the brief from its displayed time in %s", async (language, title) => {
        await i18n.changeLanguage(language);
        getByID.mockReturnValue(new Promise(() => {}));
        render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'morning', asOf: '2026-11-03T17:24:00Z', timezone: 'UTC' }} />);
        expect(screen.getByRole("heading", { name: title })).toBeInTheDocument();
    });

    it("shows a title while the brief loads", async () => {
        let finish!: (result: ReturnType<typeof view>) => void;
        getByID.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
        render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'evening', asOf: '2026-10-28T18:00:00Z', timezone: 'UTC' }} />);
        expect(screen.getByTestId("main-chat-brief-card")).toHaveAttribute("aria-busy", "true");
        expect(screen.getByRole("heading", { name: "Evening brief · 6:00 PM" })).toBeInTheDocument();
        await waitFor(() => expect(getByID).toHaveBeenCalled());
        finish(view(withWords));
        expect(await screen.findByRole("heading", { name: "Evening brief · 6:00 PM" })).toBeInTheDocument();
    });
    it("shows the title and opening sentence, and Read it opens the brief in the card", async () => {
        getByID.mockResolvedValue(view(withMore));
        const onReadAll = vi.fn();
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={onReadAll} meta={() => <span>From brief</span>} />);
        expect(await screen.findByRole("heading", { name: "Evening brief · 6:00 PM" })).toBeInTheDocument();
        expect(screen.getByText("Quiet day. Priya is waiting on your review.")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: en.mainChat.card.button.hearIt })).toHaveClass('bg-solid-btn-bg');
        expect(screen.getByRole("button", { name: en.mainChat.card.button.readIt })).toHaveClass('bg-transparent');
        expect(screen.getByText("From brief").compareDocumentPosition(screen.getByRole("button", { name: en.mainChat.card.button.hearIt })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        fireEvent.click(screen.getByRole("button", { name: en.mainChat.card.button.readIt }));
        expect(onReadAll).not.toHaveBeenCalled();
        expect(screen.queryByRole("button", { name: en.mainChat.card.button.readIt })).toBeNull();
    });

    it("a quiet brief with nothing beyond the card shows no Read it (design R40 v1.0.0)", async () => {
        getByID.mockResolvedValue(view({ ...withWords, audio: undefined, narrative: [{ text: "A quiet evening. Nothing needs you." }] }));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        expect(await screen.findByText("A quiet evening. Nothing needs you.")).toBeInTheDocument();
        expect(screen.queryByRole("button", { name: en.mainChat.card.button.readIt })).toBeNull();
    });

    it("hides Play when the brief has no recording", async () => {
        getByID.mockResolvedValue(view({ ...withMore, audio: undefined }));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        await screen.findByRole("heading", { name: "Evening brief · 6:00 PM" });
        expect(screen.queryByRole("button", { name: en.mainChat.card.button.hearIt })).toBeNull();
        expect(screen.getByRole("button", { name: en.mainChat.card.button.readIt })).toHaveClass('bg-solid-btn-bg');
    });

    it('does not carry a previous brief or its expanded state to another id', async () => {
        getByID.mockResolvedValueOnce(view(withMore)).mockReturnValueOnce(new Promise(() => {}));
        const component = render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Read it' }));
        component.rerender(<MainChatBriefCard briefId="brief-2" display={{ slot: 'morning', asOf: '2026-10-29T09:23:00Z', timezone: 'UTC' }} onOpenBrief={vi.fn()} />);
        expect(screen.queryByText('Quiet day. Priya is waiting on your review.')).toBeNull();
        expect(screen.getByRole('heading', { name: 'Morning brief · 9:23 AM' })).toBeInTheDocument();
    });

    it("never plays a recording without its words", async () => {
        getByID.mockResolvedValue(view({ ...withWords, narrative: [] }));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        await screen.findByRole("heading", { name: "Evening brief · 6:00 PM" });
        expect(screen.queryByRole("button", { name: en.mainChat.card.button.hearIt })).toBeNull();
    });

    it("never shows another brief or a provisional headline when the id does not match; it fills in on the store's event (R40)", async () => {
        getByID.mockResolvedValueOnce(view({ ...withWords, briefId: "brief-9" }));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        await waitFor(() => expect(getByID).toHaveBeenCalledWith("brief-1"));
        expect(screen.queryByText(withWords.narrative[0].text)).toBeNull();
        expect(await screen.findByRole("alert")).toHaveTextContent(en.mainChat.card.wordsUnreadable);
        getByID.mockResolvedValue(view(withWords));
        await act(async () => { (handlers['brief:stored'] ?? []).forEach((callback) => callback()); });
        expect(await screen.findByRole("heading", { name: "Evening brief · 6:00 PM" })).toBeInTheDocument();
    });
    it.each([
        ['en', 'Evening brief'],
        ['zh-CN', '晚间简报'],
    ])('keeps the title and source after a failed read, says it could not be read with Try again, in %s (design v1.0.1)', async (language, title) => {
        await i18n.changeLanguage(language);
        getByID.mockRejectedValue(new Error('unavailable'));
        render(<MainChatBriefCard briefId="2026-10-30-evening" onOpenBrief={vi.fn()} meta={() => <span>source</span>} />);
        expect(screen.getByRole('heading', { name: title })).toBeInTheDocument();
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent(i18n.t('chat:mainChat.card.wordsUnreadable'));
        expect(within(alert).getByRole('button', { name: i18n.t('chat:mainChat.view.retry') })).toBeInTheDocument();
        expect(screen.getByRole('heading', { name: title })).toBeInTheDocument();
        expect(screen.getByText('source')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: i18n.t('chat:mainChat.card.button.readIt') })).toBeNull();
    });
    it("Read it and Hear it appear only with the words; a held card has neither, and no reserved space (design, PR #646)", async () => {
        getByID.mockResolvedValueOnce(view({ ...withWords, narrative: [] }, 'store_unavailable'));
        getByID.mockResolvedValueOnce(view(withMore));
        const { container } = render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'evening', asOf: '2026-10-28T18:00:00Z', timezone: 'UTC' }} onOpenBrief={vi.fn()} meta={() => <span>From brief</span>} />);
        expect(await screen.findByRole('heading', { name: 'Evening brief · 6:00 PM' })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
        expect(container.querySelector('[data-brief-body]')).toBeNull();
        expect(screen.getByText('From brief').compareDocumentPosition(screen.getByRole('heading')) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy();
        await act(async () => { (handlers['app:ready'] ?? []).forEach((callback) => callback()); });
        expect(await screen.findByRole('button', { name: en.mainChat.card.button.readIt })).toBeInTheDocument();
        expect(screen.getByText('Quiet day. Priya is waiting on your review.')).toBeInTheDocument();
    });

    it("a failed card puts its line under the title, before the source line, with no Read it (design, PR #646)", async () => {
        getByID.mockResolvedValue(view({ ...withWords, narrative: [] }, 'closed_eyes'));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} meta={() => <span>From brief</span>} />);
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent(en.mainChat.card.wordsUnreadable);
        expect(alert.compareDocumentPosition(screen.getByText('From brief')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
    });
    it.each(['en', 'zh-CN'])('a failed read shows the line and fills in on the store\'s event, in %s (design v1.0.1)', async (language) => {
        await i18n.changeLanguage(language);
        getByID.mockRejectedValueOnce(new Error('unavailable')).mockResolvedValue(view(withWords));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        expect(await screen.findByRole('alert')).toHaveClass('text-copper');
        await act(async () => { (handlers['app:ready'] ?? []).forEach((callback) => callback()); });
        expect(await screen.findByText(withWords.narrative[0].text)).toBeInTheDocument();
        expect(screen.queryByRole('alert')).toBeNull();
    });
    it("opens the week's brief on Today from Read it, since its sections are not in the card", async () => {
        getByID.mockResolvedValue(view({ ...withMore, yourWeek: { start: '2026-10-26', end: '2026-11-01', lead: 'Your week' } } as never));
        const onOpenBrief = vi.fn();
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={onOpenBrief} />);
        await screen.findByText("Quiet day. Priya is waiting on your review.");
        fireEvent.click(screen.getByRole("button", { name: en.mainChat.card.button.readIt }));
        expect(onOpenBrief).toHaveBeenCalledTimes(1);
    });

    it('a failed read says Couldn\'t read this brief. with Try again, keeps the source line, and Try again fills it in (design v1.0.1)', async () => {
        getByID.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(view(withWords));
        render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'evening', asOf: '2026-10-30T18:00:00Z', timezone: 'UTC' }} onOpenBrief={vi.fn()} meta={() => <span>From your brief</span>} />);
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent(en.mainChat.card.wordsUnreadable);
        expect(alert).toHaveTextContent(en.mainChat.view.retry);
        expect(screen.getByText('From your brief')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
        fireEvent.click(within(alert).getByRole('button', { name: en.mainChat.view.retry }));
        expect(await screen.findByText('Quiet day. Priya is waiting on your review.')).toBeInTheDocument();
        expect(screen.queryByRole('alert')).toBeNull();
        expect(getByID).toHaveBeenCalledTimes(2);
    });
    it("a held card shows its title and time with no body and no error, and fills in when the store opens (R40)", async () => {
        getByID.mockResolvedValueOnce(view({ ...withWords, narrative: [] }, 'store_unavailable'));
        getByID.mockResolvedValueOnce(view(withMore));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        expect(await screen.findByRole("heading", { name: "Evening brief · 6:00 PM" })).toBeInTheDocument();
        expect(screen.queryByRole('alert')).toBeNull();
        expect(screen.queryByText('Quiet day. Priya is waiting on your review.')).toBeNull();
        await act(async () => { (handlers['app:ready'] ?? []).forEach((callback) => callback()); });
        expect(await screen.findByText('Quiet day. Priya is waiting on your review.')).toBeInTheDocument();
        expect(getByID).toHaveBeenCalledTimes(2);
    });

    it("says Couldn't read this brief. only once the store is open and the words still can't be read (R40)", async () => {
        getByID.mockResolvedValue(view({ ...withWords, narrative: [] }, 'closed_eyes'));
        render(<MainChatBriefCard briefId="brief-1" onOpenBrief={vi.fn()} />);
        const alert = await screen.findByRole("alert");
        expect(alert).toHaveTextContent(en.mainChat.card.wordsUnreadable);
        expect(alert).toHaveClass('text-copper');
        expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
    });

    it("never waits on a timer: a slow read that finishes later fills the card in", async () => {
        let finish!: (result: ReturnType<typeof view>) => void;
        getByID.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
        render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'evening', asOf: '2026-10-28T18:00:00Z', timezone: 'UTC' }} onOpenBrief={vi.fn()} />);
        await waitFor(() => expect(getByID).toHaveBeenCalled());
        expect(screen.queryByRole('alert')).toBeNull();
        await act(async () => finish(view(withWords)));
        expect(screen.getByText('Quiet day. Priya is waiting on your review.')).toBeInTheDocument();
    });


    it('a loaded brief with no words and no withheld reason shows its title and time only, with no line, Try again, Read it or Hear it (design R74)', async () => {
        getByID.mockResolvedValue(view({ ...withWords, narrative: [], audio: undefined }));
        render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'evening', asOf: '2026-10-28T18:00:00Z', timezone: 'UTC' }} onOpenBrief={vi.fn()} meta={() => <span>From brief</span>} />);
        expect(await screen.findByRole('heading', { name: 'Evening brief · 6:00 PM' })).toBeInTheDocument();
        await waitFor(() => expect(getByID).toHaveBeenCalled());
        await act(async () => {});
        expect(screen.queryByRole('alert')).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.view.retry })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.readIt })).toBeNull();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
        expect(screen.getByText('From brief')).toBeInTheDocument();
    });


    it('a loaded brief with no words but items shows Read it and no Hear it, so its panel is not empty (design R74)', async () => {
        getByID.mockResolvedValue(view({ ...withWords, narrative: [], audio: undefined, sections: { moved: [{ id: 'm1', title: 'Moved item' }] } }));
        const open = vi.fn();
        render(<MainChatBriefCard briefId="brief-1" display={{ slot: 'evening', asOf: '2026-10-28T18:00:00Z', timezone: 'UTC' }} onOpenBrief={open} />);
        expect(await screen.findByRole('button', { name: en.mainChat.card.button.readIt })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: en.mainChat.card.button.hearIt })).toBeNull();
        expect(screen.queryByRole('alert')).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: en.mainChat.card.button.readIt }));
        expect(open).toHaveBeenCalledTimes(1);
    });

});
