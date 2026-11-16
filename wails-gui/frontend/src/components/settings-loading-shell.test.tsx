import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import i18n from '@/i18n';
import enSettings from '@/i18n/locales/en/settings.json';
import { SettingsLoadingShell } from './settings-loading-shell';

// ST85: Settings opens drawn. The nav is the real component (labels, icons,
// section headings) and the default page's real header, with no gray bars.
describe('SettingsLoadingShell', () => {
    beforeEach(async () => {
        await i18n.changeLanguage('en');
    });

    it('draws the real nav and the default header at once, with no placeholder bars', () => {
        const { container } = render(<SettingsLoadingShell />);
        expect(screen.getByTestId('settings-loading-shell')).toBeInTheDocument();
        expect(container.querySelector('.settings-nav')).not.toBeNull();
        expect(container.querySelector('.settings-nav')!.textContent!.length).toBeGreaterThan(0);
        expect(screen.getByRole('heading', { name: enSettings.pages.activity.label })).toBeInTheDocument();
        expect(container.querySelectorAll('.bg-ink\\/\\[0\\.05\\], .bg-ink\\/\\[0\\.06\\]')).toHaveLength(0);
    });
});
