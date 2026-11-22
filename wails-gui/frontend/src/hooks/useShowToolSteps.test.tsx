import { render, renderHook, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import i18n from '@/i18n';
import { ToolGroupItem } from '@/components/tool-group-item';
import { useShowToolSteps } from './useShowToolSteps';

// Tool steps' Settings switch went (answers v1.0.0 T2): the default, off,
// always applies, and a value saved before has no effect.
describe('useShowToolSteps', () => {
    beforeEach(async () => {
        localStorage.clear();
        await i18n.changeLanguage('en');
    });

    it('ignores a saved On from before its switch went', () => {
        localStorage.setItem('yansu.show-tool-steps', '1');
        localStorage.setItem('yansu.show-tool-steps-migrated', '1');
        const { result } = renderHook(() => useShowToolSteps());
        expect(result.current[0]).toBe(false);
    });

    it('shows no raw tool input and output, whatever was saved', () => {
        localStorage.setItem('yansu.show-tool-steps', '1');
        localStorage.setItem('yansu.show-tool-steps-migrated', '1');
        render(
            <ToolGroupItem
                item={{ id: 'tool-1', kind: 'tool', toolName: 'Bash', toolInput: { command: 'pwd' }, toolUseId: 'call-1' }}
                index={0}
                result={{ id: 'result-1', kind: 'result', toolUseId: 'call-1', content: '/tmp/project' }}
            />,
        );
        expect(screen.queryByRole('button', { name: 'Show raw tool input and output' })).not.toBeInTheDocument();
    });
});
