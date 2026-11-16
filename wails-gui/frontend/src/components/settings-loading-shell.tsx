import { useTranslation } from 'react-i18next';
import { SettingsNav } from '@/components/settings/settings-nav';
import { DEFAULT_SETTINGS_PAGE, getSettingsPage } from '@/components/settings/settings-registry';

const noop = () => {};

// Drawn while the Settings page chunk loads (ST85). The nav is the real
// component, so its labels, icons and section headings draw at once; the
// header is the default page's real title. Only the page area waits.
export function SettingsLoadingShell({ showDevTools = false }: { showDevTools?: boolean }) {
    const { t } = useTranslation('settings');
    const entry = getSettingsPage(DEFAULT_SETTINGS_PAGE);
    const title = t((entry.titleKey ?? entry.labelKey) as never) as string;
    return (
        <div className="settings-surface h-full flex flex-col" data-testid="settings-loading-shell">
            <div className="flex-1 min-h-0 flex flex-row">
                <SettingsNav activePage={DEFAULT_SETTINGS_PAGE} onSelect={noop} showDevTools={showDevTools} />
                <div className="flex-1 min-w-0 flex flex-col">
                    <div className="window-drag w-full max-w-[760px] mx-auto px-4 pr-12 pt-5 pb-5 shrink-0 flex flex-col gap-1 md:px-10 md:pr-12">
                        <h1 className="settings-page-title">{title}</h1>
                    </div>
                    <div className="flex-1" aria-busy="true" />
                </div>
            </div>
        </div>
    );
}
