import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import i18n from '@/i18n';
import { MainChatSummary } from './main-chat-header';

afterEach(cleanup);

for (const language of ['en', 'zh-CN']) {
    describe(`compact chat summary in ${language}`, () => {
        const line = (counts: { conversations: number; finished: number; routine: number }) => {
            render(<MainChatSummary counts={counts} failed={false} onRetry={() => undefined} />);
            return screen.getByTestId('main-chat-summary').textContent;
        };

        it('has no waiting count; Tasks › Needs you is the one waiting list (K10)', async () => {
            await i18n.changeLanguage(language);
            const text = line({ conversations: 3, finished: 1, routine: 9 });
            expect(text).not.toMatch(language === 'en' ? /waiting/ : /等你处理/);
            expect(screen.getByTestId('main-chat-summary').querySelector('.text-warning')).toBeNull();
        });

        it('drops every zero count', async () => {
            await i18n.changeLanguage(language);
            expect(line({ conversations: 0, finished: 0, routine: 0 })).toBe(language === 'en' ? 'In your chat today' : '今天聊天里有');
        });

        it('shows the full line in with you, finished, routine order', async () => {
            await i18n.changeLanguage(language);
            expect(line({ conversations: 3, finished: 1, routine: 9 })).toBe(language === 'en'
                ? 'In your chat today · 3 with you · 1 finished · 9 routine, folded'
                : '今天聊天里有 · 3 段与你的聊天 · 1 项已完成 · 9 条例行更新，已折叠');
        });

        it('uses the plural translations for counts above one', async () => {
            await i18n.changeLanguage(language);
            expect(line({ conversations: 2, finished: 2, routine: 2 })).toBe(language === 'en'
                ? 'In your chat today · 2 with you · 2 finished · 2 routine, folded'
                : '今天聊天里有 · 2 段与你的聊天 · 2 项已完成 · 2 条例行更新，已折叠');
        });
    });
}
