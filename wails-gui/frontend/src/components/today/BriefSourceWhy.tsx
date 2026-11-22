import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronRight } from "lucide-react";
import type { BriefSourceGroup } from "@/lib/brief-sources";

/**
 * One kind of the brief's sources in "In this brief" (computer-brief v1.0.0
 * rule 6), opening its Why in place: the sources of that kind by day, as the
 * brief recorded them. Kept small on purpose: the shared Why sheet
 * (components/why/WhySheet.tsx) replaces the list once it lands. Not this is
 * not offered here yet.
 */
export function BriefSourceWhy({ group }: { group: BriefSourceGroup }) {
    const { t, i18n } = useTranslation("today");
    const tr = (key: string, options?: Record<string, unknown>) => t(key as never, options as never) as unknown as string;
    const [open, setOpen] = useState(false);
    const listId = useId();
    const kind = tr(`cbrief.kind.${group.kind}`);
    const lines = group.sources.filter((ref) => ref.label?.trim()).map((ref) => {
        const at = new Date(ref.observedAt);
        const day = Number.isNaN(at.getTime()) ? "" : at.toLocaleDateString(i18n.language, { weekday: "short" });
        return { key: ref.ref, text: day ? tr("cbrief.sourceLine", { day, kind, label: ref.label.trim() }) : `${kind} · ${ref.label.trim()}` };
    });
    return (
        <div className="flex flex-col" data-testid="brief-source-kind" data-kind={group.kind}>
            <button
                type="button"
                aria-expanded={open}
                aria-controls={listId}
                onClick={() => setOpen((was) => !was)}
                className="flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-left text-[13px] font-medium text-ink outline-none hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-border-focus"
            >
                <ChevronRight aria-hidden className={open ? "size-3.5 shrink-0 rotate-90 text-muted transition-transform" : "size-3.5 shrink-0 text-muted transition-transform"} />
                <span className="min-w-0 flex-1">{kind}</span>
                <span className="text-[12px] tabular-nums text-muted">{group.sources.length}</span>
            </button>
            {open && (
                <ul id={listId} className="flex flex-col gap-1 py-1 pl-7 pr-2.5" data-testid="brief-source-why">
                    {lines.length > 0
                        ? lines.map((line) => <li key={line.key} className="text-[12px] leading-[1.45] text-muted">{line.text}</li>)
                        : <li className="text-[12px] text-muted">{tr("cbrief.whyEmpty")}</li>}
                </ul>
            )}
        </div>
    );
}
