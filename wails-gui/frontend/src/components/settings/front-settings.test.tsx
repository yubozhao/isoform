import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/settings.json';
import { FrontSettings } from './front-settings';
import { LETS_ENCRYPT_AGREEMENT_URL } from '@/components/agents/connect-link';

const mocks = vi.hoisted(() => ({ status: vi.fn(), set: vi.fn(), open: vi.fn() }));
vi.mock('@/lib/wails-shim-app', () => ({ GetFrontStatus: mocks.status, SetFrontEnabled: mocks.set }));
vi.mock('../../../wailsjs/runtime/runtime', () => ({ BrowserOpenURL: mocks.open }));

beforeEach(async () => {
    vi.resetAllMocks();
    await i18n.changeLanguage('en');
});

describe('Agents reach this computer', () => {
    it('says turning it on accepts the Let\'s Encrypt agreement and links to it', async () => {
        mocks.status.mockResolvedValue({ enabled: false, running: false, hostname: '' });
        render(<FrontSettings />);
        expect(await screen.findByText(en.front.switch.agreement, { exact: false })).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: en.front.switch.agreementLink }));
        expect(mocks.open).toHaveBeenCalledWith(LETS_ENCRYPT_AGREEMENT_URL);
        expect(screen.queryByText(en.front.address.title)).toBeNull();
    });

    it('turns on only from the switch, then shows the address', async () => {
        mocks.status.mockResolvedValueOnce({ enabled: false, running: false, hostname: '' });
        mocks.status.mockResolvedValue({ enabled: true, running: true, hostname: 'abc.agents.isoform.ai' });
        mocks.set.mockResolvedValue(undefined);
        render(<FrontSettings />);
        const sw = await screen.findByRole('switch', { name: en.front.switch.title });
        await waitFor(() => expect(sw).not.toBeDisabled());
        expect(mocks.set).not.toHaveBeenCalled();
        fireEvent.click(sw);
        await waitFor(() => expect(mocks.set).toHaveBeenCalledWith(true));
        expect(await screen.findByText('abc.agents.isoform.ai')).toBeInTheDocument();
    });

    it('lists certificates this computer never asked for', async () => {
        mocks.status.mockResolvedValue({
            enabled: true, running: true, hostname: 'abc.agents.isoform.ai',
            unexpectedCertificates: [{ id: '2', issuer: 'Some CA', notBefore: '2026-11-14T00:00:00Z', foundAt: 1 }],
        });
        render(<FrontSettings />);
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent("A certificate you didn't request");
        expect(alert).toHaveTextContent('Issued by Some CA 2026-11-14');
    });

    it('puts the switch back and says so when the change fails', async () => {
        mocks.status.mockResolvedValue({ enabled: false, running: false, hostname: '' });
        mocks.set.mockRejectedValue(new Error('no ids'));
        render(<FrontSettings />);
        const sw = await screen.findByRole('switch', { name: en.front.switch.title });
        await waitFor(() => expect(sw).not.toBeDisabled());
        fireEvent.click(sw);
        expect(await screen.findByText(en.front.toggleFailed)).toBeInTheDocument();
        expect(sw).toHaveAttribute('aria-checked', 'false');
    });
});
