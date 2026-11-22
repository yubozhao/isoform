import { expect, it } from 'vitest';

// Snappy 1.0, "Done when": with the window hidden and the person idle, no UI
// timer fires. These screens' timers run through activeInterval (lib/
// app-activity.ts), which stops them while the window is hidden, the Mac is
// locked or asleep, and catches up on show; its own tests cover that. A raw
// setInterval here would keep firing while hidden.
const sources = import.meta.glob([
    '../hooks/useTodayBeforeYouTalk.ts',
    '../hooks/useMainChatView.ts',
    '../hooks/useTodayDayShape.ts',
    '../hooks/useTodayQuietLines.ts',
    '../hooks/useTodayTopics.ts',
    '../hooks/useLatestBrief.ts',
    '../components/timeline/activity-session-cache.ts',
    '../components/activity-day-view.tsx',
    '../components/people-page.tsx',
    '../components/today/TodayDayStrip.tsx',
    '../components/today/TodayBriefSection.tsx',
    '../components/about-you/new-this-week.tsx',
    '../components/settings-page.tsx',
    '../components/settings/pages/observation-page.tsx',
    '../components/settings/keep-times-sheet.tsx',
    '../components/settings/pages/chat-apps/bot-setups.tsx',
    '../components/mobile-connection-settings.tsx',
    '../components/model-accounts-card.tsx',
    '../components/agents/ConnectAgentDialog.tsx',
], { query: '?raw', import: 'default', eager: true }) as Record<string, string>;

it('keeps every converted screen off raw intervals', () => {
    expect(Object.keys(sources)).toHaveLength(19);
    for (const [path, source] of Object.entries(sources)) {
        expect(source.match(/\bsetInterval\(/g) ?? [], path).toEqual([]);
    }
});
