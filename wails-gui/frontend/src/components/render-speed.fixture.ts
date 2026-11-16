import type { StreamItem } from '@/types';
import type { ActivityThreadView, ActivityView } from '@/lib/wails-shim-app';

// Realistic render fixtures for the opt-in render-speed harness
// (render-speed-fixture.test.tsx). All content is synthetic and public; no
// real activity, chat, or people data.

const PARAGRAPH =
    'The render path now batches token flushes through a single scheduler tick, so the stream stays at a steady cadence even when the model emits uneven bursts. Each paragraph keeps its own memoized block, which means a growing tail never reparses the sealed sections above it.\n\n';

const CODE_BLOCK = '```ts\n' +
    Array.from({ length: 24 }, (_, i) =>
        `export function step${i}(input: State${i}): State${i + 1} {\n    return { ...input, index: input.index + 1, ok: true };\n}`
    ).join('\n') +
    '\n```\n\n';

const TABLE = '| Step | Owner | Status |\n| --- | --- | --- |\n' +
    Array.from({ length: 8 }, (_, i) => `| Phase ${i} | Team ${i % 3} | ${i % 2 ? 'done' : 'running'} |`).join('\n') +
    '\n\n';

const LIST = Array.from({ length: 10 }, (_, i) => `- Finding ${i}: the commit walks every mounted row, so memo boundaries decide what actually re-renders.`).join('\n') + '\n\n';

/** A long assistant answer (~3 KB) with the block mix real answers have:
 *  prose, a fenced code block, a table, a list and a quote. */
export function longMarkdownAnswer(seed: number): string {
    return (
        `## Summary of pass ${seed}\n\n` +
        PARAGRAPH +
        CODE_BLOCK +
        `### Numbers\n\n` + TABLE +
        `> Keep the diff minimal; the scroll anchor must not move for the reader.\n\n` +
        LIST +
        PARAGRAPH
    );
}

/** Chat rows: `pairs` turns of user + thinking + tool + assistant, spread
 *  across five local days so the main chat builds five day sections. Every
 *  assistant answer is the long markdown document above. The original
 *  500-row fixture (125 turns) exercises the staged recent-only path; 200
 *  turns crosses the 150-row windowing threshold for the split path. */
export function chatStreamFixture(pairs = 200): StreamItem[] {
    const items: StreamItem[] = [];
    const dayMs = 24 * 60 * 60 * 1000;
    const base = new Date('2026-10-19T09:00:00').getTime();
    let sort = 0;
    for (let turn = 0; turn < pairs; turn++) {
        const stamp = base + Math.floor(turn / 40) * dayMs + (turn % 40) * 7 * 60_000;
        const localDay = new Date(stamp).toISOString().slice(0, 10);
        const put = (kind: StreamItem['kind'], content: string, extra: Partial<StreamItem> = {}) => {
            items.push({ id: `row-${sort}`, kind, content, sortOrder: sort, localDay, ...extra });
            sort += 1;
        };
        put('user', `Pass ${turn}: walk me through commit ${turn} and what changed in the renderer.`);
        put('thinking', `Comparing the tree before and after pass ${turn} to isolate the rows that actually changed.`);
        put('tool', '', { toolName: 'Read', toolInput: { file_path: `/src/render/pass-${turn}.ts` }, toolUseId: `tool-${turn}` });
        put('assistant', longMarkdownAnswer(turn));
    }
    return items;
}

/** A full Activity day: 300 moments and 20 threads with stretches and gaps,
 *  08:00-20:00. Most moments stay ungrouped (they land in the stitched
 *  aggregate lanes); 20 threads of 2-3 moments each own the rest. Shapes
 *  mirror activity-stitched.fixture.ts. */
export function busyDayFixture(): { activities: ActivityView[]; threads: ActivityThreadView[] } {
    const dayStart = new Date('2026-10-22T08:00:00').getTime();
    const activities: ActivityView[] = [];
    for (let i = 0; i < 300; i++) {
        const start = dayStart + i * 144_000; // one every 2.4 minutes across 12 hours
        const end = start + 100_000 + (i % 5) * 20_000;
        activities.push({
            id: `moment-${i}`,
            status: 'settled',
            title: `Fixture moment ${i}`,
            description: `Public fixture work block ${i}`,
            latestChange: i % 3 === 0 ? `Edited file-${i % 40}.ts` : '',
            category: 'build',
            categoryColor: '',
            entities: [],
            apps: [{ appKey: '', appName: ['VS Code', 'GitHub', 'Slack', 'Mail'][i % 4], activeMs: end - start }],
            activeMs: end - start,
            startMs: start,
            endMs: end,
            runCount: 1,
            thumbnails: [],
            inProgress: false,
        });
    }
    const base = { kind: 'work', userTitled: false, reviewed: false, unfinished: false, interruption: false };
    const threads: ActivityThreadView[] = [];
    for (let tIdx = 0; tIdx < 20; tIdx++) {
        const owned = activities.slice(tIdx * 3, tIdx * 3 + 3);
        const stretches = [0, 1].map((s) => {
            const slice = s === 0 ? owned.slice(0, 2) : owned.slice(2);
            const start = slice[0].startMs;
            const end = slice[slice.length - 1].endMs;
            return {
                start,
                end,
                activeMs: end - start,
                activityIds: slice.map((a) => a.id),
                resumePoint: s === 0 ? null : { kind: 'path' as const, label: `file-${tIdx}-${s}.ts` },
            };
        });
        threads.push({
            ...base,
            id: `thread-${tIdx}`,
            title: `Fixture thread ${tIdx}`,
            startMs: stretches[0].start,
            endMs: stretches[stretches.length - 1].end,
            timeSpentMs: stretches.reduce((sum, s) => sum + s.activeMs, 0),
            activeMs: stretches.reduce((sum, s) => sum + s.activeMs, 0),
            activityIds: owned.map((a) => a.id),
            stretches,
            gaps: [
                { start: stretches[0].end, end: stretches[1].start, minutes: 4, label: `Fixture moment ${tIdx * 3 + 2}`, threadId: '' },
            ],
            linkReason: { kind: 'rule', keys: [`file:file-${tIdx}-1.ts`], text: '', resumedWithinMinutes: 4 },
        });
    }
    return { activities, threads };
}
