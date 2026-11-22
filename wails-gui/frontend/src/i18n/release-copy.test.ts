import { expect, it } from 'vitest';
import { resources } from './index';

// Technical exceptions name the actual permission system, native APIs,
// platform restrictions and notification diagnostics.
const technical = new Set([
    'activity.session.hoverPreviewSourceHint', 'chat.setup.requesting', 'common.relaunch.body',
    'onboarding.setupWelcome.checked', 'onboarding.setupPermissions.description',
    'onboarding.setupPermissions.sequence', 'onboarding.setupPermissions.items.screen_recording.description',
    'onboarding.agent.consent', 'onboarding.permissions.items.clipboard.description',
    'people.contactsImportPermissionGrantHint', 'today.todayBrief.blindBody',
    'today.todayBrief.blindScreenDenied',
    'settings.modelAccounts.consentKeychain',
    'settings.textMessages.permissionNeeded', 'settings.textMessages.grantFailed', 'settings.textMessages.errorNotAllowed',
    'settings.proactiveNotifications.macPermission.description', 'settings.proactiveNotifications.macPermission.actionHint',
    'settings.proactiveNotifications.macPermission.failed', 'settings.axParser.unsupportedDescription',
    'settings.notifications.cardDescription', 'settings.notifications.desktopTest.title',
    'settings.notifications.desktopTest.description', 'settings.notifications.desktopTest.button',
    'settings.notifications.windowPopRepro.description', 'settings.notifications.windowPopRepro.armedBody',
    'settings.audioCapture.suspension.desktop_locked', 'settings.observation.reminders.description',
    'settings.observation.reminders.readingOff', 'settings.observation.mail.description',
    'settings.observation.files.description',
    'settings.observation.files.notAvailable', 'settings.plugins.builtins.reminders-sync.description',
]);

// Setup pairs Apple's iPhone, so its product name is allowed there.
const namesTheDevices = (key: string) => key.startsWith('onboarding.v2.');

// "From your iPhone" is design's exact caption for a message sent from the phone (handoff
// 2026-11-06-1744-ios-pr-batch-review-v1.0.5, #635). The caption names the phone on purpose, in English and zh-CN.
const namesThePhone = new Set(['chat.fromPhone']);

for (const [language, namespaces] of Object.entries(resources)) {
    it('keeps release copy free of emoji and generic device brands (' + language + ')', () => {
        function check(value: unknown, key: string) {
            if (typeof value === 'string') {
                expect(value, key).not.toMatch(/\p{Extended_Pictographic}|\uFE0F|\u20E3/u);
                expect(value, key).not.toMatch(/\bMac\b(?! App Store\b)/);
                if (key === 'app.tasks.claudeCode') {
                    expect(value).toBe(language === 'en' ? 'Claude Code on this computer' : '此电脑上的 Claude Code');
                }
                if (!namesTheDevices(key) && !namesThePhone.has(key)) {
                    expect(value, key).not.toMatch(/\b(?:iPhone|iOS)\b/);
                }
                if (/\bmacOS\b/.test(value)) expect(technical.has(key), key).toBe(true);
            } else if (value && typeof value === 'object') {
                for (const [child, item] of Object.entries(value)) check(item, key ? key + '.' + child : child);
            }
        }
        check(namespaces, '');
    });
}
