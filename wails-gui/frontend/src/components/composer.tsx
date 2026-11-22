import { Button } from "@/components/ui/button";
import {
    type ReactNode,
    useRef,
    useCallback,
    useEffect,
    useMemo,
    useState,
    useLayoutEffect,
} from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import {
    ArrowUp,
    Loader2,
    Mic,
    Plus,
    RefreshCw,
    Square,
    X,
} from "lucide-react";
import { VoiceRecordingIndicator } from "@/components/voice-recording-indicator";
import type { VoiceInputState } from "@/hooks/useVoiceInput";
import type { ExecutorGroup, ExecutorType } from "@/lib/stream-helpers";
import type { AgentCommand, FileAttachment, FileCategory, ImageAttachment } from "@/types";
import type { QueuedMessage } from "@/hooks/useComposerInput";
import type { TodayChatPreview } from "@/lib/today-chat-navigation";
import { TodayContextPreview } from "./today-context-status";
import { ComposerSuggestions } from "./composer-suggestions";
import { QueuedMessageList } from "./composer-queue";
import { useComposerSuggestions } from "@/hooks/useComposerSuggestions";
import { FileAttachmentList } from "./file-attachment-preview";
import { toast } from "sonner";

export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

export const ACCEPTED_FILE_EXTENSIONS = [
    ".pdf", ".doc", ".docx", ".rtf", ".odt",
    ".xls", ".xlsx", ".csv", ".tsv", ".ods",
    ".ppt", ".pptx",
    ".txt", ".md", ".markdown", ".log",
    ".json", ".xml", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".env",
    ".gitignore", ".dockerignore",
    ".js", ".ts", ".jsx", ".tsx", ".py", ".go", ".java", ".c", ".cpp", ".h", ".hpp",
    ".rs", ".rb", ".php", ".swift", ".kt", ".scala", ".sql",
    ".html", ".htm", ".css", ".scss", ".less",
    ".sh", ".bash", ".zsh", ".ps1", ".diff", ".patch",
];

export const MAX_FILE_SIZE = 20 * 1024 * 1024; // 20 MB

export function isImageFile(file: File): boolean {
    return IMAGE_TYPES.includes(file.type) || file.type.startsWith("image/");
}

export function isAcceptedFile(file: File): boolean {
    if (isImageFile(file)) return true;
    const ext = "." + file.name.split(".").pop()?.toLowerCase();
    if (ACCEPTED_FILE_EXTENSIONS.includes(ext)) return true;
    if (file.type.startsWith("text/")) return true;
    if (file.type === "application/pdf") return true;
    if (file.type === "application/json") return true;
    if (file.type.includes("officedocument") || file.type.includes("msword") || file.type.includes("ms-excel")) return true;
    return false;
}

export function detectCategory(file: File): FileCategory {
    const ext = "." + file.name.split(".").pop()?.toLowerCase();
    if (isImageFile(file)) return "image";
    if ([".pdf", ".doc", ".docx", ".rtf", ".odt"].includes(ext)) return "document";
    if ([".xls", ".xlsx", ".csv", ".tsv", ".ods"].includes(ext)) return "spreadsheet";
    if ([".pptx", ".ppt"].includes(ext)) return "presentation";
    if ([".zip", ".tar", ".gz", ".7z", ".rar"].includes(ext)) return "archive";
    if ([".js", ".ts", ".jsx", ".tsx", ".py", ".go", ".java", ".c", ".cpp", ".h", ".hpp",
         ".rs", ".rb", ".php", ".swift", ".kt", ".scala", ".sql",
         ".html", ".htm", ".css", ".scss", ".less",
         ".sh", ".bash", ".zsh", ".ps1", ".diff", ".patch"].includes(ext)) return "code";
    return "text";
}
const SLASH_POPUP_MAX_HEIGHT = 208; // matches old max-h-52
const SLASH_POPUP_MIN_HEIGHT = 64;
const SLASH_POPUP_GAP = 8;
const SLASH_POPUP_VIEWPORT_PADDING = 12;

interface ComposerProps {
    scopeControl?: ReactNode;
    /** Replaces the box's usual placeholder (the main chat on the first day). */
    placeholder?: string;
    /** What the message is attached to (phone-chat v1.0.0 rule 10): the
     *  chip "About: {title} ×" above the field. × or Backspace in an empty
     *  field takes it off; nothing is sent until the person types. */
    about?: { title: string; onRemove: () => void };
    prompt: string;
    todayPreview?: TodayChatPreview;
    onPromptChange: (value: string) => void;
    onSend: () => void;
    onCancel: () => void;
    onKeyDown: (event: React.KeyboardEvent<HTMLTextAreaElement>) => void;
    executorGroups: ExecutorGroup[];
    streaming: boolean;
    ready: boolean;
    hasProject: boolean;
    contextError?: string;
    images: ImageAttachment[];
    onImagesChange: (images: ImageAttachment[]) => void;
    files: FileAttachment[];
    onFilesChange: (files: FileAttachment[]) => void;
    executorType: ExecutorType;
    awaitingPlanApproval?: boolean;
    queuedMessages: QueuedMessage[];
    onRemoveQueuedMessage: (index: number) => void;
    onEditQueuedMessage: (index: number) => void;
    availableCommands: AgentCommand[];
    slashCommandsLoading?: boolean;
    onTyping?: () => void;
    voiceState?: VoiceInputState;
    voiceAudioLevel?: number;
    voiceStatusMessage?: string | null;
    voiceError?: string | null;
    voiceDownloadProgress?: {
        fileName: string;
        downloaded: number;
        total: number;
        bytesPerSec: number;
        percent: number;
        speed: string;
    } | null;
    voiceNeedsPermission?: boolean;
    onVoiceStart?: () => void;
    onVoiceStop?: () => Promise<string>;
    onVoiceCancel?: () => void;
    onVoiceRetry?: () => void;
    onVoiceOpenSettings?: () => Promise<void>;
    /** Called when the composer is clicked but no session exists. */
    onRequestSession?: () => void;
    /** Welcome mode: gradient border on send button, rounded-26px container. */
    welcomeMode?: boolean;
    /** When true, render activity-driven suggestion chips below the composer. */
    showSuggestions?: boolean;
    /** Invoked when a suggestion is clicked: the caller puts its words in
     *  the box (a click prepares; only Send sends). */
    onSelectSuggestion?: (prompt: string) => void;
    /** A short line under the box, such as "Nothing is sent until you press
     *  Send." after a click prepared the words. */
    note?: string;
    /** Opens the handoff reader for the active-handoff chip. */
    onOpenHandoff?: (id: string) => void;
    wideMode?: boolean;
}

interface SlashToken {
    query: string;
    tokenEnd: number;
}

function getSlashToken(prompt: string, cursorPos: number): SlashToken | null {
    if (!prompt.startsWith("/")) {
        return null;
    }
    const firstWhitespace = prompt.search(/\s/);
    const tokenEnd = firstWhitespace === -1 ? prompt.length : firstWhitespace;
    if (cursorPos > tokenEnd) {
        return null;
    }
    return {
        query: prompt.slice(1, tokenEnd).toLowerCase(),
        tokenEnd,
    };
}

export function readFileAsBase64(file: File): Promise<ImageAttachment> {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => {
            const dataUrl = reader.result as string;
            // Strip "data:<mime>;base64," prefix
            const base64 = dataUrl.split(",")[1];
            resolve({ mediaType: file.type, dataBase64: base64, fileName: file.name });
        };
        reader.onerror = () => reject(reader.error);
        reader.readAsDataURL(file);
    });
}

function formatBytes(bytes: number): string {
    if (bytes >= 1024 * 1024 * 1024) {
        return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
    }
    if (bytes >= 1024 * 1024) {
        return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    }
    if (bytes >= 1024) {
        return `${(bytes / 1024).toFixed(0)} KB`;
    }
    return `${bytes} B`;
}

export function Composer({
    scopeControl,
    placeholder,
    about,
    prompt,
    todayPreview,
    onPromptChange,
    onSend,
    onCancel,
    onKeyDown,
    streaming,
    ready,
    hasProject,
    contextError,
    images,
    onImagesChange,
    files,
    onFilesChange,
    awaitingPlanApproval,
    queuedMessages,
    onRemoveQueuedMessage,
    onEditQueuedMessage,
    availableCommands,
    slashCommandsLoading = false,
    onTyping,
    voiceState = "idle",
    voiceAudioLevel = 0,
    voiceStatusMessage,
    voiceError,
    voiceDownloadProgress,
    voiceNeedsPermission = false,
    onVoiceStart,
    onVoiceStop,
    onVoiceCancel: _onVoiceCancel,
    onVoiceRetry,
    onVoiceOpenSettings,
    onRequestSession,
    welcomeMode = false,
    showSuggestions = false,
    onSelectSuggestion,
    note,
    onOpenHandoff,
    wideMode = false,
}: ComposerProps) {
    const { t } = useTranslation("app");
    const { t: tChat } = useTranslation("chat");
    const inputDisabled = !ready || !hasProject;
    const voiceBusy =
        voiceState === "recording" || voiceState === "transcribing";
    const canSend =
        ready &&
        hasProject &&
        !voiceBusy &&
        todayPreview?.status !== "loading" &&
        (prompt.trim().length > 0 || images.length > 0 || files.length > 0);
    const hasInput = prompt.trim().length > 0 || images.length > 0 || files.length > 0;
    const voicePermissionBlocked = voiceNeedsPermission;
    const voiceErrorTitle = voicePermissionBlocked
        ? t("composer.voice.microphonePermissionRequired")
        : t("composer.voice.unavailable");
    const voiceErrorDescription = voicePermissionBlocked
        ? t("composer.voice.permissionDescription")
        : t("composer.voice.setupFailed");

    const fileInputRef = useRef<HTMLInputElement>(null);
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const promptRef = useRef(prompt);
    const [isFocused, setIsFocused] = useState(false);
    const [cursorPos, setCursorPos] = useState(0);

    const {
        suggestions: composerSuggestions,
        isLoading: suggestionsLoading,
        isRefreshing: suggestionsRefreshing,
        refresh: refreshSuggestions, // TODO(debug-only): remove with refresh button
        activeHandoffs,
    } = useComposerSuggestions();
    const suggestionsActive = showSuggestions && !streaming;
    const suggestionsCollapsed = prompt.trim().length > 0;
    const [slashIndex, setSlashIndex] = useState(0);
    const [slashDismissed, setSlashDismissed] = useState(false);
    const [slashPlacement, setSlashPlacement] = useState<"bottom" | "top">(
        "bottom",
    );
    const [slashPopupMaxHeight, setSlashPopupMaxHeight] = useState<number>(
        SLASH_POPUP_MAX_HEIGHT,
    );

    useEffect(() => {
        promptRef.current = prompt;
    }, [prompt]);

    const handleVoiceStop = useCallback(() => {
        if (voiceState === "recording" && onVoiceStop) {
            void onVoiceStop().then((text) => {
                if (text) {
                    const currentPrompt = promptRef.current;
                    const next = currentPrompt
                        ? currentPrompt + " " + text
                        : text;
                    onPromptChange(next);
                    onTyping?.();
                }
            });
        }
    }, [voiceState, onVoiceStop, onPromptChange, onTyping]);

    const handleVoiceToggle = useCallback(() => {
        if (voicePermissionBlocked && onVoiceOpenSettings) {
            void onVoiceOpenSettings();
            return;
        }
        if (voiceState === "idle" && onVoiceStart) {
            onVoiceStart();
        } else if (voiceState === "recording") {
            handleVoiceStop();
        }
    }, [
        voicePermissionBlocked,
        onVoiceOpenSettings,
        voiceState,
        onVoiceStart,
        handleVoiceStop,
    ]);

    const slashToken = useMemo(
        () => getSlashToken(prompt, cursorPos),
        [prompt, cursorPos],
    );
    const slashCandidates = useMemo(() => {
        if (!slashToken) {
            return [];
        }
        if (slashToken.query === "") {
            return availableCommands;
        }
        const startsWith: AgentCommand[] = [];
        const includes: AgentCommand[] = [];
        for (const command of availableCommands) {
            const name = command.name.toLowerCase();
            if (name.startsWith(slashToken.query)) {
                startsWith.push(command);
            } else if (name.includes(slashToken.query)) {
                includes.push(command);
            }
        }
        return [...startsWith, ...includes];
    }, [availableCommands, slashToken]);
    const slashActive =
        availableCommands.length > 0 &&
        isFocused &&
        !inputDisabled &&
        !slashDismissed &&
        slashToken !== null;
    const showSlashSuggestions = slashActive && slashCandidates.length > 0;
    const showSlashEmptyState = slashActive && slashCandidates.length === 0;
    const slashEmptyLabel = useMemo(() => {
        if (!showSlashEmptyState) {
            return "";
        }
        if (slashCommandsLoading && availableCommands.length === 0) {
            return t("composer.slash.loadingCommands");
        }
        if (availableCommands.length === 0) {
            return t("composer.slash.noCommands");
        }
        return t("composer.slash.noMatches");
    }, [
        availableCommands.length,
        showSlashEmptyState,
        slashCommandsLoading,
        t,
    ]);

    useEffect(() => {
        if (import.meta.env.DEV && prompt.startsWith("/")) {
            console.debug("[slash-debug] composer slash state", {
                availableCount: availableCommands.length,
                candidateCount: slashCandidates.length,
                show: showSlashSuggestions,
                focused: isFocused,
                inputDisabled,
                cursorPos,
                query: slashToken?.query ?? "",
                loading: slashCommandsLoading,
                placement: slashPlacement,
                maxHeight: slashPopupMaxHeight,
            });
        }
    }, [
        availableCommands.length,
        cursorPos,
        inputDisabled,
        isFocused,
        prompt,
        slashPlacement,
        slashPopupMaxHeight,
        slashCommandsLoading,
        showSlashSuggestions,
        slashCandidates.length,
        slashToken?.query,
    ]);

    useEffect(() => {
        setSlashIndex(0);
    }, [slashToken?.query, slashCandidates.length]);

    const updateSlashPopupLayout = useCallback(() => {
        if (!slashActive) {
            return;
        }
        const ta = textareaRef.current;
        if (!ta) {
            return;
        }

        const rect = ta.getBoundingClientRect();
        const viewportHeight = window.innerHeight;
        const spaceBelow = Math.max(
            0,
            viewportHeight - rect.bottom - SLASH_POPUP_VIEWPORT_PADDING,
        );
        const spaceAbove = Math.max(0, rect.top - SLASH_POPUP_VIEWPORT_PADDING);

        const expectedHeight = showSlashSuggestions ? 220 : 52;
        const shouldFlip =
            spaceBelow < expectedHeight && spaceAbove > spaceBelow;
        const placement: "bottom" | "top" = shouldFlip ? "top" : "bottom";

        const usableSpace =
            (placement === "bottom" ? spaceBelow : spaceAbove) -
            SLASH_POPUP_GAP;
        const nextMaxHeight = Math.round(
            Math.max(
                SLASH_POPUP_MIN_HEIGHT,
                Math.min(SLASH_POPUP_MAX_HEIGHT, usableSpace),
            ),
        );

        setSlashPlacement((prev) => (prev === placement ? prev : placement));
        setSlashPopupMaxHeight((prev) =>
            prev === nextMaxHeight ? prev : nextMaxHeight,
        );
    }, [showSlashSuggestions, slashActive]);

    useLayoutEffect(() => {
        if (!slashActive) {
            return;
        }
        updateSlashPopupLayout();

        const onViewportChange = () => {
            updateSlashPopupLayout();
        };
        window.addEventListener("resize", onViewportChange);
        window.addEventListener("scroll", onViewportChange, true);

        let ro: ResizeObserver | null = null;
        if (typeof ResizeObserver !== "undefined" && textareaRef.current) {
            ro = new ResizeObserver(onViewportChange);
            ro.observe(textareaRef.current);
        }

        return () => {
            window.removeEventListener("resize", onViewportChange);
            window.removeEventListener("scroll", onViewportChange, true);
            ro?.disconnect();
        };
    }, [slashActive, updateSlashPopupLayout]);

    const syncCursorPos = useCallback(() => {
        const pos = textareaRef.current?.selectionStart ?? prompt.length;
        setCursorPos(pos);
    }, [prompt.length]);

    const applySlashCommand = useCallback(
        (command: AgentCommand) => {
            const token = getSlashToken(prompt, cursorPos);
            if (!token) {
                return;
            }
            const replacement = `/${command.name}`;
            const suffix = prompt.slice(token.tokenEnd);
            const nextPrompt =
                suffix.length > 0
                    ? `${replacement}${suffix}`
                    : `${replacement} `;
            onPromptChange(nextPrompt);
            onTyping?.();
            setSlashDismissed(false);

            const nextCursorPos =
                suffix.length > 0 ? replacement.length : replacement.length + 1;
            requestAnimationFrame(() => {
                const ta = textareaRef.current;
                if (!ta) {
                    return;
                }
                ta.focus();
                ta.setSelectionRange(nextCursorPos, nextCursorPos);
                setCursorPos(nextCursorPos);
            });
        },
        [cursorPos, onPromptChange, onTyping, prompt],
    );

    const handlePromptValueChange = useCallback(
        (value: string) => {
            setSlashDismissed(false);
            onPromptChange(value);
            onTyping?.();
        },
        [onPromptChange, onTyping],
    );

    const handleTextareaKeyDown = useCallback(
        (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
            // Skip IME composition so neither the slash-command capture below
            // nor the upstream send handler fires while the user is selecting
            // a Pinyin candidate. See useComposerInput for the keyCode 229
            // fallback rationale.
            if (
                event.nativeEvent.isComposing ||
                event.nativeEvent.keyCode === 229
            ) {
                return;
            }
            if (about && event.key === "Backspace" && prompt === "") {
                event.preventDefault();
                about.onRemove();
                return;
            }
            if (showSlashSuggestions) {
                if (event.key === "ArrowDown") {
                    event.preventDefault();
                    setSlashIndex((current) =>
                        Math.min(current + 1, slashCandidates.length - 1),
                    );
                    return;
                }
                if (event.key === "ArrowUp") {
                    event.preventDefault();
                    setSlashIndex((current) => Math.max(current - 1, 0));
                    return;
                }
                const plainEnter =
                    event.key === "Enter" &&
                    !event.shiftKey &&
                    !event.metaKey &&
                    !event.ctrlKey &&
                    !event.altKey;
                const plainTab =
                    event.key === "Tab" &&
                    !event.shiftKey &&
                    !event.metaKey &&
                    !event.ctrlKey &&
                    !event.altKey;
                if (plainEnter || plainTab) {
                    event.preventDefault();
                    const selected = slashCandidates[slashIndex];
                    if (selected) {
                        applySlashCommand(selected);
                    }
                    return;
                }
                if (event.key === "Escape") {
                    event.preventDefault();
                    setSlashDismissed(true);
                    return;
                }
            }
            onKeyDown(event);
        },
        [
            about,
            applySlashCommand,
            onKeyDown,
            prompt,
            showSlashSuggestions,
            slashCandidates,
            slashIndex,
        ],
    );

    // Auto-resize textarea based on content
    useEffect(() => {
        const ta = textareaRef.current;
        if (ta) {
            ta.style.height = "auto";
            ta.style.height = `${Math.min(ta.scrollHeight, 200)}px`;
        }
    }, [prompt]);

    const addFiles = useCallback(
        async (incoming: FileList | File[]) => {
            const allFiles = Array.from(incoming);
            const accepted = allFiles.filter(
                (f) => isAcceptedFile(f) && f.size <= MAX_FILE_SIZE,
            );
            const unsupported = allFiles.filter((f) => !isAcceptedFile(f));
            const oversized = allFiles.filter(
                (f) => isAcceptedFile(f) && f.size > MAX_FILE_SIZE,
            );
            if (unsupported.length > 0) {
                toast.error(t("composer.attachments.unsupportedType"));
            }
            if (oversized.length > 0) {
                toast.error(t("composer.attachments.fileTooLarge"));
            }
            if (accepted.length === 0) return;

            const imageFiles = accepted.filter(isImageFile);
            const nonImageFiles = accepted.filter((f) => !isImageFile(f));

            if (imageFiles.length > 0) {
                const newImages = await Promise.all(
                    imageFiles.map(readFileAsBase64),
                );
                onImagesChange([...images, ...newImages]);
            }

            if (nonImageFiles.length > 0) {
                const newFiles = await Promise.all(
                    nonImageFiles.map(async (f): Promise<FileAttachment> => {
                        const base64 = await new Promise<string>((resolve, reject) => {
                            const reader = new FileReader();
                            reader.onload = () => resolve((reader.result as string).split(",")[1]);
                            reader.onerror = () => reject(reader.error);
                            reader.readAsDataURL(f);
                        });
                        return {
                            id: `file-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
                            fileName: f.name,
                            fileSize: f.size,
                            mediaType: f.type || "application/octet-stream",
                            category: detectCategory(f),
                            dataBase64: base64,
                        };
                    }),
                );
                onFilesChange([...files, ...newFiles]);
            }
        },
        [images, onImagesChange, files, onFilesChange, t],
    );

    const removeImage = useCallback(
        (index: number) => {
            onImagesChange(images.filter((_, i) => i !== index));
        },
        [images, onImagesChange],
    );

    const removeFile = useCallback(
        (id: string) => {
            onFilesChange(files.filter((f) => f.id !== id));
        },
        [files, onFilesChange],
    );

    const handlePaste = useCallback(
        (e: React.ClipboardEvent) => {
            const items = e.clipboardData?.items;
            if (!items) return;
            const pastedFiles: File[] = [];
            for (const item of items) {
                if (item.kind === "file") {
                    const file = item.getAsFile();
                    if (file) pastedFiles.push(file);
                }
            }
            if (pastedFiles.length > 0) {
                e.preventDefault();
                addFiles(pastedFiles);
            }
        },
        [addFiles],
    );

    const handleDrop = useCallback(
        (e: React.DragEvent) => {
            e.preventDefault();
            if (inputDisabled) return;
            const files = e.dataTransfer?.files;
            if (files) addFiles(files);
        },
        [addFiles, inputDisabled],
    );

    const handleDragOver = useCallback((e: React.DragEvent) => {
        e.preventDefault();
    }, []);

    return (
        <footer
            className={
                welcomeMode
                    ? "shrink-0 w-full"
                    : `shrink-0 px-6 pb-4 w-full${wideMode ? "" : " max-w-3xl mx-auto"}`
            }
        >
            <div
                className={cn(
                    "composer-surface flex flex-col border transition-all min-h-[108px] px-5 py-2.5 justify-between rounded-2xl bg-composer-bg",
                    isFocused
                        ? "border-border-focus shadow-[var(--shadow-composer-focus)]"
                        : welcomeMode
                          ? "border-composer-border shadow-[var(--shadow-composer)]"
                          : "border-composer-border shadow-[var(--shadow-composer)] hover:border-border-focus",
                )}
                onDrop={handleDrop}
                onDragOver={handleDragOver}
                onMouseDown={(e) => {
                    if (inputDisabled) return;
                    const target = e.target as HTMLElement | null;
                    if (!target) return;
                    if (
                        target.closest(
                            'button, input, textarea, select, a, [role="button"], [contenteditable="true"], [data-radix-select-trigger]',
                        )
                    ) {
                        return;
                    }
                    if (target === textareaRef.current) return;
                    e.preventDefault();
                    textareaRef.current?.focus();
                }}
            >
                {/* Hidden file input */}
                <input
                    ref={fileInputRef}
                    type="file"
                    accept={[...IMAGE_TYPES, ...ACCEPTED_FILE_EXTENSIONS].join(",")}
                    multiple
                    className="hidden"
                    onChange={(e) => {
                        if (e.target.files) addFiles(e.target.files);
                        e.target.value = "";
                    }}
                />

                {/* Image previews */}
                {images.length > 0 && (
                    <div className="flex flex-wrap gap-2 mb-2">
                        {images.map((img, i) => {
                            const removeLabel = img.fileName
                                ? t("composer.attachments.removeFile", { file: img.fileName })
                                : t("composer.attachments.removeImage");
                            return (
                                <div key={i} className="relative group">
                                    <img
                                        src={`data:${img.mediaType};base64,${img.dataBase64}`}
                                        alt={t("composer.attachments.alt", {
                                            number: i + 1,
                                        })}
                                        className="h-16 w-16 rounded-lg border border-border object-cover"
                                    />
                                    <button
                                        type="button"
                                        aria-label={removeLabel}
                                        className="absolute -top-1.5 -right-1.5 h-5 w-5 rounded-full bg-solid text-solid-fg flex items-center justify-center opacity-0 group-hover:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus transition-opacity cursor-pointer"
                                        onClick={() => removeImage(i)}
                                        title={removeLabel}
                                    >
                                        <X className="h-3 w-3" />
                                    </button>
                                </div>
                            );
                        })}
                    </div>
                )}

                {/* File attachment previews */}
                {files.length > 0 && (
                    <FileAttachmentList
                        files={files}
                        onRemove={removeFile}
                        className="mb-2"
                    />
                )}

                {/* Context error */}
                {contextError && (
                    <p className="mb-2 px-3 py-2 rounded-lg bg-error-bg text-error text-xs border border-error-border">
                        {contextError}
                    </p>
                )}

                {about && (
                    <div className="mt-1 min-w-0">
                        <span className="inline-flex min-h-6 max-w-full items-center gap-1 rounded-md border border-border bg-panel-warm pl-2.5 pr-0.5 text-[12px] text-ink" data-testid="composer-about-chip">
                            <span className="min-w-0 max-w-[360px] whitespace-normal [overflow-wrap:anywhere]">{tChat("about.item", { title: about.title })}</span>
                            <Button size="icon" variant="quiet" className="size-5 rounded-md" aria-label={tChat("about.remove", { title: about.title })} onClick={about.onRemove}>
                                <X className="size-3" />
                            </Button>
                        </span>
                    </div>
                )}

                {/* Pending queued messages */}
                <QueuedMessageList
                    messages={queuedMessages}
                    onEdit={onEditQueuedMessage}
                    onRemove={onRemoveQueuedMessage}
                    className="mb-2"
                />

                {/* Text input */}
                <div
                    className={cn(
                        "relative",
                        "mt-2.5",
                        !hasProject &&
                            ready &&
                            onRequestSession &&
                            "cursor-text",
                    )}
                    onClick={() => {
                        // When clicked with no session, auto-create one.
                        if (!hasProject && ready && onRequestSession) {
                            onRequestSession();
                        }
                    }}
                >
                    <textarea
                        ref={textareaRef}
                        data-composer-textarea
                        value={prompt}
                        onChange={(e) => {
                            handlePromptValueChange(e.target.value);
                            setCursorPos(
                                e.target.selectionStart ??
                                    e.target.value.length,
                            );
                        }}
                        onKeyDown={handleTextareaKeyDown}
                        onKeyUp={syncCursorPos}
                        onSelect={syncCursorPos}
                        onPaste={handlePaste}
                        onFocus={() => {
                            setIsFocused(true);
                            syncCursorPos();
                        }}
                        onBlur={() => setIsFocused(false)}
                        placeholder={
                            awaitingPlanApproval
                                ? t("composer.placeholder.planFeedback")
                                : placeholder ?? t("composer.placeholder.default")
                        }
                        rows={1}
                        disabled={inputDisabled}
                        spellCheck={false}
                        autoCorrect="off"
                        autoCapitalize="off"
                        className={cn(
                            "w-full resize-none bg-transparent text-sm text-ink placeholder:text-muted focus:outline-none min-h-[24px] max-h-[200px]",
                            inputDisabled && "opacity-50",
                            inputDisabled &&
                                onRequestSession &&
                                "pointer-events-none",
                        )}
                    />
                    {slashActive && (
                        <div
                            className={cn(
                                "absolute left-4 right-4 z-30 overflow-hidden rounded-xl border border-border bg-panel shadow-[var(--shadow-card)]",
                                slashPlacement === "bottom"
                                    ? "top-full mt-2"
                                    : "bottom-full mb-2",
                            )}
                        >
                            {showSlashSuggestions ? (
                                <div
                                    className="overflow-y-auto p-1"
                                    style={{
                                        maxHeight: `${slashPopupMaxHeight}px`,
                                    }}
                                >
                                    {slashCandidates.map((command, index) => (
                                        <button
                                            key={command.name}
                                            className={cn(
                                                "flex w-full flex-col items-start gap-0.5 rounded-lg px-2.5 py-2 text-left transition-colors cursor-pointer",
                                                index === slashIndex
                                                    ? "bg-accent/[0.08]"
                                                    : "hover:bg-ink/[0.04]",
                                            )}
                                            onMouseDown={(e) =>
                                                e.preventDefault()
                                            }
                                            onMouseEnter={() =>
                                                setSlashIndex(index)
                                            }
                                            onClick={() =>
                                                applySlashCommand(command)
                                            }
                                        >
                                            <span className="text-xs font-semibold text-ink">
                                                /{command.name}
                                            </span>
                                            {(command.description ||
                                                command.input_hint) && (
                                                <span className="text-[11px] text-muted line-clamp-1">
                                                    {command.description ||
                                                        command.input_hint}
                                                </span>
                                            )}
                                        </button>
                                    ))}
                                </div>
                            ) : (
                                <div className="px-3 py-2 text-xs text-muted">
                                    {slashEmptyLabel}
                                </div>
                            )}
                        </div>
                    )}
                </div>

                <TodayContextPreview preview={todayPreview} />

                {/* Bottom toolbar */}
                <div className="flex flex-wrap items-center gap-x-2 gap-y-2" data-testid="composer-toolbar">
                    {/* Left: attach. The button's box sits in the padding so
                        the + lines up with the text. */}
                    <div className="flex max-w-full shrink-0 flex-wrap items-center gap-1 [&>button]:shrink-0">
                        <Button
                            size="icon"
                            variant="quiet"
                            className="-ml-2 text-ink"
                            disabled={inputDisabled}
                            onClick={() => fileInputRef.current?.click()}
                            aria-label={t("composer.attachments.attachFiles")}
                            title={t("composer.attachments.attachFiles")}
                        >
                            <Plus className="size-4" />
                        </Button>
                        {scopeControl}

                    </div>

                    {/* Right: voice + stop + send */}
                    <div className="ml-auto flex max-w-full shrink-0 flex-wrap items-center gap-1.5 [&>button]:shrink-0 [&>div]:shrink-0">
                        {/* Voice input: click to toggle recording */}
                        {onVoiceStart && voiceState !== "recording" && (
                            <div className="relative group/voice">
                                <button
                                    aria-label={t("composer.voice.input")}
                                    className={cn(
                                        "flex size-8 items-center justify-center rounded-lg transition-colors",
                                        voiceError
                                            ? "bg-error/10 text-error"
                                            : voiceState === "transcribing"
                                              ? "bg-accent/10 text-accent"
                                              : voiceState === "idle"
                                                ? "text-muted hover:text-ink hover:bg-ink/[0.06] cursor-pointer"
                                                : "text-muted/50",
                                    )}
                                    disabled={
                                        inputDisabled ||
                                        voiceState === "transcribing" ||
                                        voiceState === "initializing"
                                    }
                                    onClick={handleVoiceToggle}
                                >
                                    {voiceState === "transcribing" ? (
                                        <Loader2 className="size-4 animate-spin" />
                                    ) : voiceState === "initializing" &&
                                      voiceDownloadProgress ? (
                                        <div className="relative size-5">
                                            <svg
                                                className="size-5 -rotate-90"
                                                viewBox="0 0 20 20"
                                            >
                                                <circle
                                                    cx="10"
                                                    cy="10"
                                                    r="8"
                                                    fill="none"
                                                    stroke="currentColor"
                                                    strokeWidth="2"
                                                    className="opacity-20"
                                                />
                                                <circle
                                                    cx="10"
                                                    cy="10"
                                                    r="8"
                                                    fill="none"
                                                    stroke="currentColor"
                                                    strokeWidth="2"
                                                    strokeDasharray={`${2 * Math.PI * 8}`}
                                                    strokeDashoffset={`${2 * Math.PI * 8 * (1 - (voiceDownloadProgress.percent ?? 0) / 100)}`}
                                                    className="text-accent transition-all duration-300"
                                                    strokeLinecap="round"
                                                />
                                            </svg>
                                            <Mic className="size-2.5 absolute inset-0 m-auto text-accent" />
                                        </div>
                                    ) : voiceState === "initializing" ? (
                                        <Loader2 className="size-4 animate-spin text-muted" />
                                    ) : (
                                        <Mic className="size-4" />
                                    )}
                                </button>
                                {/* Hover tooltip for non-ready states */}
                                {(voiceState !== "idle" || !!voiceError) && (
                                    <div
                                        className={cn(
                                            "absolute bottom-full right-0 mb-2 z-40",
                                            voiceError
                                                ? "block"
                                                : "hidden group-hover/voice:block group-focus-within/voice:block",
                                        )}
                                    >
                                        <div className="w-64 rounded-lg border border-border bg-panel shadow-[var(--shadow-card)] p-2.5 space-y-1.5">
                                            <div className="text-[11px] font-medium text-ink">
                                                {voiceError
                                                    ? voiceErrorTitle
                                                    : voiceState ===
                                                        "transcribing"
                                                      ? t(
                                                            "composer.voice.transcribing",
                                                        )
                                                      : t(
                                                            "composer.voice.notReady",
                                                        )}
                                            </div>
                                            <div className="text-[10px] text-muted">
                                                {voiceError
                                                    ? voiceErrorDescription
                                                    : voiceState ===
                                                        "transcribing"
                                                      ? t(
                                                            "composer.voice.converting",
                                                        )
                                                      : voiceStatusMessage
                                                        ? voiceStatusMessage
                                                        : voiceDownloadProgress
                                                          ? t(
                                                                "composer.voice.downloadingModel",
                                                            )
                                                          : t(
                                                                "composer.voice.settingUp",
                                                            )}
                                            </div>
                                            {!voiceError &&
                                                voiceDownloadProgress && (
                                                    <>
                                                        <div className="w-full h-1.5 bg-border/50 rounded-full overflow-hidden">
                                                            <div
                                                                className="h-full bg-accent rounded-full transition-all duration-300"
                                                                style={{
                                                                    width: `${Math.min(100, voiceDownloadProgress.percent)}%`,
                                                                }}
                                                            />
                                                        </div>
                                                        <div className="flex items-center justify-between text-[10px] text-muted">
                                                            <span>
                                                                {
                                                                    voiceDownloadProgress.percent
                                                                }
                                                                % —{" "}
                                                                {formatBytes(
                                                                    voiceDownloadProgress.downloaded,
                                                                )}
                                                                {voiceDownloadProgress.total >
                                                                0
                                                                    ? ` / ${formatBytes(voiceDownloadProgress.total)}`
                                                                    : ""}
                                                            </span>
                                                            <span>
                                                                {
                                                                    voiceDownloadProgress.speed
                                                                }
                                                            </span>
                                                        </div>
                                                    </>
                                                )}
                                            {voiceError && (
                                                <div className="flex items-center justify-between gap-2">
                                                    <span className="text-[10px] text-muted line-clamp-2">
                                                        {voiceError}
                                                    </span>
                                                    {voicePermissionBlocked &&
                                                        onVoiceOpenSettings && (
                                                            <button
                                                                className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-[10px] font-medium text-ink hover:bg-ink/[0.04] shrink-0 cursor-pointer"
                                                                onClick={
                                                                    onVoiceOpenSettings
                                                                }
                                                            >
                                                                {t(
                                                                    "composer.voice.openSettings",
                                                                )}
                                                            </button>
                                                        )}
                                                    {!voicePermissionBlocked &&
                                                        onVoiceRetry && (
                                                            <button
                                                                className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-[10px] font-medium text-ink hover:bg-ink/[0.04] shrink-0 cursor-pointer"
                                                                onClick={
                                                                    onVoiceRetry
                                                                }
                                                            >
                                                                <RefreshCw className="size-3" />
                                                                {t(
                                                                    "composer.voice.retry",
                                                                )}
                                                            </button>
                                                        )}
                                                </div>
                                            )}
                                        </div>
                                    </div>
                                )}
                            </div>
                        )}
                        {onVoiceStop && voiceState === "recording" && (
                            <VoiceRecordingIndicator
                                audioLevel={voiceAudioLevel}
                                onClick={handleVoiceToggle}
                            />
                        )}

                        {streaming && (
                            <button
                                className="flex size-8 items-center justify-center rounded-lg bg-solid text-solid-fg cursor-pointer hover:bg-solid/80 transition-colors"
                                onClick={onCancel}
                                title={
                                    queuedMessages.length > 0
                                        ? t(
                                              "composer.actions.stopStreamingQueued",
                                              {
                                                  count: queuedMessages.length,
                                              },
                                          )
                                        : t("composer.actions.stopStreaming")
                                }
                            >
                                <Square className="size-3.5 fill-current" />
                            </button>
                        )}

                        {(!streaming || hasInput) &&
                            voiceState !== "recording" && (
                                <div
                                    className={cn(
                                        "rounded-[10px] p-0.5",
                                        welcomeMode && canSend
                                            ? "bg-accent"
                                            : "bg-transparent",
                                    )}
                                >
                                    <button
                                        className={cn(
                                            "flex size-8 items-center justify-center rounded-lg transition-colors",
                                            canSend
                                                ? "patina-verdigris bg-solid-btn-bg text-solid-btn-fg shadow-[var(--shadow-solid-btn)] cursor-pointer hover:bg-solid-btn-bg/80"
                                                : "bg-border text-muted cursor-not-allowed",
                                        )}
                                        onClick={onSend}
                                        disabled={!canSend}
                                        title={
                                            streaming
                                                ? t(
                                                      "composer.actions.queueMessage",
                                                  )
                                                : t(
                                                      "composer.actions.sendMessage",
                                                  )
                                        }
                                    >
                                        <ArrowUp className="size-4" />
                                    </button>
                                </div>
                            )}
                    </div>

                </div>
            </div>
            {note && (
                <p aria-live="polite" className="mt-1.5 px-1 text-[12px] text-muted" data-testid="composer-note">
                    {note}
                </p>
            )}
            {suggestionsActive && (
                <ComposerSuggestions
                    suggestions={composerSuggestions}
                    isLoading={
                        suggestionsLoading && composerSuggestions.length === 0
                    }
                    isRefreshing={suggestionsRefreshing}
                    collapsed={suggestionsCollapsed}
                    onSelect={(text) => onSelectSuggestion?.(text)}
                    onRefresh={() => void refreshSuggestions()}
                    handoffs={activeHandoffs}
                    onOpenHandoff={onOpenHandoff}
                />
            )}
        </footer>
    );
}
