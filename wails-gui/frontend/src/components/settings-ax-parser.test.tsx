import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { SettingsPage } from './settings-page';

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name) => name === 'then' ? undefined : vi.fn().mockResolvedValue([]),
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn(() => () => {}) }));

beforeEach(async () => { localStorage.clear(); await i18n.changeLanguage('en'); });

describe.each(['paper', 'onedark'])('AX parser selection in %s', (theme) => {
    it('switches the parser mode and disables Probe while it runs', async () => {
        document.documentElement.dataset.theme = theme;
        render(<SettingsPage onClose={vi.fn()} onReplayOnboarding={vi.fn()} onOpenPeople={vi.fn()} initialTopic="ax-parser" devToolsUnlocked />);
        const group = await screen.findByRole('radiogroup', { name: i18n.t('settings:axParser.modeLabel') });
        const choices = within(group).getAllByRole('radio');
        expect(choices[0]).toBeChecked();
        fireEvent.click(choices[1]);
        expect(choices[1]).toBeChecked();
        expect(choices[0]).not.toBeChecked();
        await waitFor(() => expect(screen.getByRole('button', { name: i18n.t('settings:axParser.probeButton') })).toBeDisabled());
    });
});
