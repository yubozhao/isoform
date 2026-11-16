import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { WindowSetTitle } from '../../wailsjs/runtime/runtime';
import { isMainChatSession } from '@/lib/stream-helpers';
import type { Session } from '@/types';
import type { AppMode } from '@/components/sidebar';

const pageKeys = {
    today: 'sidebar.today', chat: 'sidebar.chat', activity: 'sidebar.activity', agents: 'sidebar.agents',
    you: 'sidebar.you', memory: 'userMenu.memory', knowledge: 'userMenu.knowledge', people: 'userMenu.people',
} as const satisfies Record<AppMode, string>;

export function usePageWindowTitle(mode: AppMode, session?: Pick<Session, 'kind' | 'label'> | null) {
    const { t } = useTranslation('app');
    const mainChat = session ? isMainChatSession(session) : false;
    const label = session?.label?.trim();
    useEffect(() => {
        const page = mode === 'chat'
            ? mainChat ? t('sidebar.mainChat') : label || t('sidebar.chat')
            : t(pageKeys[mode]);
        WindowSetTitle(t('windowTitle', { page }));
    }, [mode, mainChat, label, t]);
}
