import { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogDescription,
    DialogCloseButton,
} from '@/components/ui/dialog';

interface ConfirmDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    title: string;
    description: React.ReactNode;
    confirmLabel?: string;
    /** The dismiss button's words; Cancel when not given. */
    cancelLabel?: string;
    onConfirm: () => void;
    variant?: 'danger' | 'default';
    /** Override the default 400px width when the description needs more
     *  room to breathe (e.g. multi-paragraph explanations or numbered
     *  lists). Tailwind class. */
    widthClass?: string;
    /** Keep the dialog open after Confirm; the caller closes it once the
     *  action went through (a failed delete stays with its line). */
    closeOnConfirm?: boolean;
    confirmDisabled?: boolean;
    /** Shown above the buttons, e.g. "Didn't delete. Try again". */
    notice?: React.ReactNode;
    /** Opens with Cancel focused, for a confirm that can't be undone. */
    focusCancel?: boolean;
    /** Where focus goes when it closes, such as the row button that opened it. */
    returnFocusTo?: HTMLElement | null;
}

export function ConfirmDialog({
    open,
    onOpenChange,
    title,
    description,
    confirmLabel,
    cancelLabel,
    onConfirm,
    variant = 'default',
    widthClass = 'w-[400px]',
    closeOnConfirm = true,
    confirmDisabled = false,
    notice,
    focusCancel = false,
    returnFocusTo,
}: ConfirmDialogProps) {
    const cancelRef = useRef<HTMLButtonElement>(null);
    const { t } = useTranslation('common');
    const resolvedConfirmLabel = confirmLabel ?? t('confirm');
    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent
                className={widthClass}
                onOpenAutoFocus={focusCancel ? (event) => { event.preventDefault(); cancelRef.current?.focus(); } : undefined}
                onCloseAutoFocus={returnFocusTo ? (event) => { event.preventDefault(); returnFocusTo.focus(); } : undefined}
            >
                <DialogHeader>
                    <DialogTitle className="text-sm">{title}</DialogTitle>
                    <DialogCloseButton />
                </DialogHeader>
                <div className="px-6 py-4">
                    <DialogDescription asChild>
                        <div className="text-[13px] text-muted leading-relaxed">
                            {description}
                        </div>
                    </DialogDescription>
                </div>
                {notice && <div className="px-6 pb-3">{notice}</div>}
                <div className="flex items-center justify-end gap-2 px-6 pb-5">
                    <button
                        ref={cancelRef}
                        className="px-3 py-1.5 rounded-md text-xs font-medium text-ink bg-border/50 hover:bg-border transition-colors"
                        onClick={() => onOpenChange(false)}
                    >
                        {cancelLabel ?? t('cancel')}
                    </button>
                    <button
                        className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                            variant === 'danger'
                                ? 'bg-error text-solid-btn-fg hover:bg-error/90'
                                : 'patina-verdigris bg-solid-btn-bg text-solid-btn-fg shadow-[var(--shadow-solid-btn)] hover:opacity-90'
                        }`}
                        disabled={confirmDisabled}
                        onClick={() => {
                            onConfirm();
                            if (closeOnConfirm) onOpenChange(false);
                        }}
                    >
                        {resolvedConfirmLabel}
                    </button>
                </div>
            </DialogContent>
        </Dialog>
    );
}
