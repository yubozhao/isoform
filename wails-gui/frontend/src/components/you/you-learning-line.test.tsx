import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import enMemory from '@/i18n/locales/en/memory.json';
import { YouLearningLine } from './you-learning-line';

const fixture = vi.hoisted(() => ({ bindings: new Map<string, ReturnType<typeof vi.fn>>() }));

vi.mock('@/lib/wails-shim-app', () => new Proxy({}, {
    has: () => true,
    get: (_target, name: string) => {
        if (name === 'then') return undefined;
        if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue(undefined));
        return fixture.bindings.get(name);
    },
}));

function binding(name: string) {
    if (!fixture.bindings.has(name)) fixture.bindings.set(name, vi.fn().mockResolvedValue(undefined));
    return fixture.bindings.get(name)!;
}

const words = enMemory.you;

describe('Learning from your day, at the foot of a You tab (S18)', () => {
    beforeEach(() => {
        fixture.bindings.clear();
        binding('GetModelAccountsStatus').mockResolvedValue({ mode: 'managed', accountOrder: ['claude'] });
        binding('GetAIProviderConfig').mockResolvedValue({ provider: '' });
    });

    it('says learning is on with the assistant, and Turn off saves off for that tab only', async () => {
        binding('LoadUIState').mockResolvedValue('');
        binding('SaveUIState').mockResolvedValue(undefined);
        await act(async () => { render(<YouLearningLine feature="memory" />); });
        expect(binding('LoadUIState')).toHaveBeenCalledWith('triage_enable_memory');
        await screen.findByText(words.learning.replace('{{assistant}}', 'Claude Code'));
        await act(async () => { fireEvent.click(screen.getByRole('button', { name: words.turnOff })); });
        expect(binding('SaveUIState')).toHaveBeenCalledWith('triage_enable_memory', 'false');
        expect(screen.getByText(words.learningOff)).toBeTruthy();
        expect(screen.getByRole('button', { name: words.turnOn })).toBeTruthy();
    });

    it('reads How you do things from its own switch, and shows off when off', async () => {
        binding('LoadUIState').mockResolvedValue('false');
        await act(async () => { render(<YouLearningLine feature="knowledge" />); });
        expect(binding('LoadUIState')).toHaveBeenCalledWith('triage_enable_knowledge');
        expect(await screen.findByText(words.learningOff)).toBeTruthy();
    });

    it('says so when the read fails, and Try again reads again', async () => {
        binding('LoadUIState').mockRejectedValueOnce(new Error('locked')).mockResolvedValue('true');
        await act(async () => { render(<YouLearningLine feature="memory" />); });
        expect(await screen.findByText(words.learningReadFailed)).toBeTruthy();
        expect(screen.queryByText(words.learningOff)).toBeNull();
        await act(async () => { fireEvent.click(screen.getByRole('button', { name: /try again/i })); });
        await waitFor(() => expect(binding('LoadUIState')).toHaveBeenCalledTimes(2));
        await screen.findByRole('button', { name: words.turnOff });
    });

    it('puts the switch back and says so when the save fails', async () => {
        binding('LoadUIState').mockResolvedValue('true');
        binding('SaveUIState').mockRejectedValue(new Error('locked'));
        await act(async () => { render(<YouLearningLine feature="memory" />); });
        await act(async () => { fireEvent.click(await screen.findByRole('button', { name: words.turnOff })); });
        expect(await screen.findByText(words.learningSaveFailed)).toBeTruthy();
        expect(screen.getByRole('button', { name: words.turnOff })).toBeTruthy();
    });
});
