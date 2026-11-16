// Small pieces the owner's Agent of You screens share: the linked row with a
// chevron, the dialog frame, the copper "waiting on you" status line and the
// gray chip for something taken out of a line.
import type { ComponentType, ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

export const AOY_LINE = "text-xs leading-relaxed text-muted";
export const AOY_LINK = "text-accent underline-offset-2 hover:underline disabled:opacity-50";
export const AOY_FIELD =
    "min-w-0 rounded-md border border-border bg-panel px-2 text-sm text-ink placeholder:text-ink/40 focus:outline-none focus:ring-1 focus:ring-accent disabled:opacity-50";
export const AOY_LABEL = "text-[11px] font-semibold uppercase tracking-wide text-muted";
export const AOY_TAG = "inline-flex items-center rounded-full bg-accent/10 px-2 py-0.5 text-[11px] font-semibold text-accent";

/** A row that opens a screen: icon, title, an optional tag, a value line and a chevron. */
export function AoyNavRow({
    icon: Icon,
    title,
    tag,
    value,
    trailing,
    onClick,
    testId,
}: {
    icon?: ComponentType<{ className?: string }>;
    title: string;
    tag?: string;
    value?: ReactNode;
    trailing?: string;
    onClick: () => void;
    testId?: string;
}) {
    return (
        <button
            type="button"
            data-testid={testId}
            onClick={onClick}
            className="settings-row w-full py-3 text-left"
        >
            <span className="flex min-w-0 items-center gap-3">
                {Icon && <Icon className="h-4 w-4 shrink-0 text-muted" />}
                <span className="flex min-w-0 flex-col">
                    <span className="flex items-center gap-2">
                        <span className="settings-row-title truncate">{title}</span>
                        {tag && <span className={AOY_TAG}>{tag}</span>}
                    </span>
                    {value && <span className="settings-row-desc">{value}</span>}
                </span>
            </span>
            <span className="flex shrink-0 items-center gap-2 text-sm text-muted">
                {trailing}
                <ChevronRight className="h-4 w-4" aria-hidden />
            </span>
        </button>
    );
}

/** The copper "waiting on you" line with its dot. */
export function AoyWaiting({ children }: { children: ReactNode }) {
    return (
        <p className="flex items-center gap-2 text-sm font-semibold text-copper">
            <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-copper" />
            {children}
        </p>
    );
}

/** The gray chip that stands where a name, number or date was taken out. */
export function AoyChip({ children }: { children: ReactNode }) {
    return <span className="mx-0.5 inline-flex items-center rounded-md bg-ink/[0.06] px-1.5 py-px text-[0.92em] text-muted">{children}</span>;
}

/** A 520px decision dialog: title, description, body, then the buttons (primary last). */
export function AoyDialog({
    open,
    onOpenChange,
    title,
    description,
    children,
    footer,
    testId,
}: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    title: string;
    description?: ReactNode;
    children?: ReactNode;
    footer: ReactNode;
    testId?: string;
}) {
    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent className="w-[520px] max-w-[90vw]" data-testid={testId}>
                <DialogHeader>
                    <DialogTitle>{title}</DialogTitle>
                    {description && <DialogDescription className="mt-1">{description}</DialogDescription>}
                </DialogHeader>
                {children && <div className="min-h-0 space-y-3 overflow-y-auto px-6 py-5 text-sm text-ink">{children}</div>}
                <div className="flex justify-end gap-2 border-t border-border px-6 py-4">{footer}</div>
            </DialogContent>
        </Dialog>
    );
}

/** Wordless rows the size of the real ones, while the first cached read lands. */
export function AoyPlaceholder({ rows = 3, testId }: { rows?: number; testId?: string }) {
    return (
        <div aria-hidden data-testid={testId} className="flex flex-col gap-2">
            {Array.from({ length: rows }, (_, index) => (
                <div key={index} className="h-10 rounded-md bg-ink/[0.04]" />
            ))}
        </div>
    );
}

/** A figure tile: the amount large, its label under it. */
export function AoyTile({ value, label }: { value: string; label: string }) {
    return (
        <div className="settings-card settings-card--padded flex flex-col gap-1">
            <span className="text-xl font-semibold text-ink tabular-nums">{value}</span>
            <span className="text-xs text-muted">{label}</span>
        </div>
    );
}
