import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { ToolGroupItem } from './tool-group-item';
import { TodoChecklist, parseTodoInput } from './todo-checklist';
import { AnimatedEllipsis } from './animated-ellipsis';
import { getToolMeta } from '@/lib/tool-icons';
import type { StreamItem } from '@/types';
import type { ToolResultsMap } from './grouped-stream-list';

interface ToolGroupRowProps {
    toolName: string;
    items: StreamItem[];
    /** tool_use_id → matching tool_result, used by debug-mode hover popovers. */
    resultsByToolUseId?: ToolResultsMap;
    /**
     * True when the surrounding session is currently streaming. The row only
     * shows the "Running..." present-tense phrase when streaming AND at least
     * one tool in the group is still missing its paired tool_result. Frozen
     * historical groups always render in past tense regardless of this prop.
     */
    streaming?: boolean;
}

/**
 * Renders a single "tool group" in the litterbird style:
 *   [icon] Group phrase [...] [chip] [chip] [chip]
 *
 * The header switches between present continuous ("Reading files…") while
 * the agent is still working and past tense ("Read files") once every tool
 * call in the group has produced a result.
 */
export function ToolGroupRow({
    toolName,
    items,
    resultsByToolUseId,
    streaming = false,
}: ToolGroupRowProps) {
    const { t } = useTranslation('chat');
    const primaryTool = toolName.split(', ')[0] ?? toolName;
    const meta = useMemo(() => getToolMeta(primaryTool), [primaryTool]);
    const Icon = meta.icon;

    // A tool call is considered "in flight" when the session is streaming
    // and no paired tool_result has arrived yet. Once every item has a
    // result (or streaming has stopped) the group flips to past tense.
    const inFlight = useMemo(() => {
        if (!streaming) return false;
        if (!resultsByToolUseId) return true;
        return items.some(
            (it) => !it.toolUseId || !resultsByToolUseId.has(it.toolUseId)
        );
    }, [items, resultsByToolUseId, streaming]);

    const phrase = useMemo(() => {
        const tense = inFlight ? 'running' : 'done';
        if (meta.needsToolNameSuffix) {
            const name = meta.humanizedName;
            const subKey = meta.phraseKey === 'mcp' ? 'mcp' : 'default';
            if (!name) {
                return t(`tools.${tense}.defaultNoName`);
            }
            return t(`tools.${tense}.${subKey}`, { name });
        }
        return t(`tools.${tense}.${meta.phraseKey}`);
    }, [inFlight, meta.humanizedName, meta.needsToolNameSuffix, meta.phraseKey, t]);

    // TodoWrite carries the whole todo list (with statuses) in its input,
    // so render it as an inline checklist instead of the (empty) summary
    // chip. Every call rewrites the full list, so the newest snapshot in
    // the group is the current state of the plan.
    const todos = useMemo(() => {
        for (let i = items.length - 1; i >= 0; i--) {
            if (items[i].toolName !== 'TodoWrite') continue;
            const parsed = parseTodoInput(items[i].toolInput);
            if (parsed.length > 0) return parsed;
        }
        return [];
    }, [items]);

    return (
        <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
                <div className="inline-flex shrink-0 items-center gap-1.5">
                    <Icon className={`size-3.5 shrink-0 ${meta.color}`} />
                    <span className="inline-flex items-baseline gap-0.5 text-xs font-medium text-muted">
                        <span>{phrase}</span>
                        {inFlight && <AnimatedEllipsis className="ml-0.5 text-muted" />}
                    </span>
                </div>
                {items.map((item, idx) => {
                    // The checklist below supersedes TodoWrite's chip (whose
                    // summary is empty anyway — it would only render the
                    // "preparing" placeholder while the call is in flight).
                    if (todos.length > 0 && item.toolName === 'TodoWrite') {
                        return null;
                    }
                    return (
                        <ToolGroupItem
                            key={item.id}
                            item={item}
                            index={idx}
                            pending={
                                streaming &&
                                (!item.toolUseId ||
                                    !resultsByToolUseId?.has(item.toolUseId))
                            }
                            result={
                                item.toolUseId
                                    ? resultsByToolUseId?.get(item.toolUseId)
                                    : undefined
                            }
                        />
                    );
                })}
            </div>
            {todos.length > 0 && <TodoChecklist todos={todos} />}
        </div>
    );
}
