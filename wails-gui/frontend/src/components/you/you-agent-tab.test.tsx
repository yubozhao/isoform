import { act, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import enMemory from '@/i18n/locales/en/memory.json';
import { resetAoyChromeForTests } from '@/components/settings/pages/agent-of-you/aoy-shared';
import { ownerStatus } from '@/components/settings/pages/agent-of-you/aoy-fixtures';
import { YouPage, type YouPageTab } from './you-page';

const mocks = vi.hoisted(() => ({ snapshots: {} as Record<string, unknown>, emit: vi.fn() }));

vi.mock('@/lib/wails-shim-app', () => ({
    GetAgentOwnerSnapshot: (kind: string) => {
        const data = mocks.snapshots[kind];
        return Promise.resolve({ available: data !== undefined, data: data ?? null });
    },
}));

function Harness() {
    const [tab, setTab] = useState<YouPageTab>('about');
    return <YouPage tab={tab} onTabChange={setTab}><div data-testid="tab-body">{tab}</div></YouPage>;
}

async function openAgentTab() {
    render(<Harness />);
    fireEvent.click(screen.getByRole('tab', { name: /Agent of You/ }));
    await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
    });
}

beforeEach(async () => {
    vi.clearAllMocks();
    (window as unknown as { runtime: unknown }).runtime = { EventsOn: () => () => undefined, EventsEmit: mocks.emit };
    resetAoyChromeForTests();
    await i18n.changeLanguage('en');
    mocks.snapshots = {};
});

describe('You › Agent of You (computer-settings v1.1.0 rules 4 and 5)', () => {
    it('says Soon while Agent of You is not open to this person', async () => {
        mocks.snapshots = { status: ownerStatus('none') };
        await openAgentTab();
        expect(screen.getByTestId('you-agent-soon')).toHaveTextContent(enMemory.you.agentSoon);
        expect(screen.getByTestId('you-agent-tag')).toHaveTextContent(enMemory.you.soon);
        expect(screen.queryByTestId('you-agent-settings-link')).toBeNull();
    });

    it('says Soon when the owner status cannot be read', async () => {
        await openAgentTab();
        expect(screen.getByTestId('you-agent-soon')).toBeInTheDocument();
    });

    it('points to Settings › Agent of You once the owner is approved, without the Soon mark', async () => {
        mocks.snapshots = { status: ownerStatus('on') };
        await openAgentTab();
        const link = screen.getByTestId('you-agent-settings-link');
        expect(link).toHaveTextContent(enMemory.you.aoySetupLink);
        expect(screen.queryByTestId('you-agent-soon')).toBeNull();
        expect(screen.queryByTestId('you-agent-tag')).toBeNull();
        fireEvent.click(link);
        expect(mocks.emit).toHaveBeenCalledWith('settings:open', 'your-agent');
    });
});
