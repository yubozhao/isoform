// Chat-error classifier.
//
// Errors arrive at the frontend as opaque strings — sometimes a Go error
// (`anthropic: HTTP 429: {"code":"USAGE_LIMIT_EXCEEDED",...}`), sometimes a
// JSON-RPC blob, sometimes a friendly sentence that streaming.go already
// rewrote. This module is the single place that maps any of those into a
// typed `ChatError` so the renderer can pick the right copy and action.

import i18n from '@/i18n';

export type QuotaPeriod = 'daily' | 'weekly' | 'monthly';

export type ChatErrorKind =
    | 'quota_period'      // recognised period quota (daily/weekly/monthly)
    | 'quota_generic'     // 429 / rate-limit with no recognised period
    | 'subscription'      // subscription required / billing
    | 'no_assistant'      // proven: no AI configured at send (AgentEvent.code)
    | 'signed_out'        // proven: HTTP 401/403 or the CLI's own sign-in check (AgentEvent.code)
    | 'runner_unavailable' // proven: a list item's runner isn't available (AgentEvent.code)
    | 'assistant_error'   // proven: the AI answered with an error (AgentEvent.code)
    | 'no_reply'          // proven: the turn ended with no answer; the hub stored the row (desktop_turn_keeper.go)
    | 'server'            // 5xx / upstream temporarily unavailable
    | 'network'           // connection refused / dns / timeout
    | 'request_too_large' // request body over the upstream byte limit (e.g. too many/too large screenshots)
    | 'unknown';          // anything we couldn't classify

export interface ChatError {
    kind: ChatErrorKind;
    period?: QuotaPeriod;
    // Cleaned single-line message extracted from the raw payload, for the
    // unknown branch's body or the "Show details" affordance on classified
    // errors. Empty when there is nothing useful to surface.
    detail: string;
}

// Detects JSON-RPC style error payloads that sometimes leak into assistant or
// result messages instead of being delivered as proper error events.
export function isJsonRpcError(raw: string | undefined | null): boolean {
    if (!raw) return false;
    const trimmed = raw.trim();
    if (!trimmed.startsWith('{')) return false;
    try {
        const parsed = JSON.parse(trimmed);
        return (
            typeof parsed === 'object' &&
            parsed !== null &&
            typeof parsed.message === 'string' &&
            ('code' in parsed || 'error' in parsed)
        );
    } catch {
        return false;
    }
}

// Unwrap a JSON-RPC blob to its inner `message`, otherwise return the input
// unchanged. Strips `Internal error:` / `API Error:` prefixes and trailing
// Go `metadata=map[...]` suffix so detail strings don't leak runtime noise.
function unwrapDetail(raw: string): string {
    let text = raw.trim();
    try {
        const parsed = JSON.parse(text);
        if (parsed && typeof parsed.message === 'string') {
            text = parsed.message;
        }
    } catch {
        // not JSON, use as-is
    }
    text = text.replace(/^Internal error:\s*/i, '');
    text = text.replace(/^API Error:\s*/i, '');
    text = text.replace(/\s*metadata=map\[.*?\]\s*$/, '');
    return text.trim();
}

const PERIOD_PATTERNS: Array<{ re: RegExp; period: QuotaPeriod }> = [
    { re: /daily.*(quota|limit|exceeded|exhausted)/i, period: 'daily' },
    { re: /weekly.*(quota|limit|exceeded|exhausted)/i, period: 'weekly' },
    { re: /monthly.*(quota|limit|exceeded|exhausted)/i, period: 'monthly' },
    { re: /(quota|limit|exceeded|exhausted).*daily/i, period: 'daily' },
    { re: /(quota|limit|exceeded|exhausted).*weekly/i, period: 'weekly' },
    { re: /(quota|limit|exceeded|exhausted).*monthly/i, period: 'monthly' },
];

// Codes the backend sets from proof (AgentEvent.code, chat_cause.go). A code
// always wins over the words in the raw text.
const PROVEN_CODES: ReadonlySet<string> = new Set(['no_assistant', 'signed_out', 'runner_unavailable', 'assistant_error', 'server', 'no_reply']);

export function parseChatError(raw: string | undefined | null, code?: string): ChatError {
    const text = (raw ?? '').toString();
    const detail = unwrapDetail(text);
    const probe = (text + ' ' + detail).toLowerCase();

    if (code && PROVEN_CODES.has(code)) {
        return { kind: code as ChatErrorKind, detail };
    }

    for (const { re, period } of PERIOD_PATTERNS) {
        if (re.test(text) || re.test(detail)) {
            return { kind: 'quota_period', period, detail };
        }
    }

    // Request body over the upstream byte ceiling — distinct from token /
    // context-window limits. The Kimi/Moonshot upstream we proxy to 400s
    // with `total message size N exceeds limit 2097152` when a request
    // (typically image-heavy: cua_driver screenshots, large attachments)
    // exceeds 2 MiB. Matched before the generic buckets so it gets its own
    // actionable copy instead of falling through to "Something went wrong".
    // Deliberately narrow so token-limit errors ("exceeded model token
    // limit") do NOT land here.
    if (
        probe.includes('total message size') ||
        probe.includes('request too large') ||
        probe.includes('request entity too large') ||
        probe.includes('payload too large') ||
        probe.includes('content too large') ||
        (probe.includes('message size') && probe.includes('exceed'))
    ) {
        return { kind: 'request_too_large', detail };
    }

    if (
        probe.includes('subscription required') ||
        probe.includes('group_not_allowed') ||
        (probe.includes('billing') && (probe.includes('error') || probe.includes('required')))
    ) {
        return { kind: 'subscription', detail };
    }

    if (
        probe.includes('quota') ||
        probe.includes('spend') ||
        (probe.includes('rate') && probe.includes('limit')) ||
        probe.includes('too many requests') ||
        probe.includes('429')
    ) {
        return { kind: 'quota_generic', detail };
    }

    if (
        probe.includes('502') ||
        probe.includes('503') ||
        probe.includes('504') ||
        probe.includes('bad gateway') ||
        probe.includes('service unavailable') ||
        probe.includes('gateway timeout') ||
        probe.includes('internal server error') ||
        probe.includes(' 500 ') ||
        probe.includes(' 500:')
    ) {
        return { kind: 'server', detail };
    }

    if (
        probe.includes('no such host') ||
        probe.includes('connection refused') ||
        probe.includes('dial tcp') ||
        probe.includes('network is unreachable') ||
        probe.includes('eof') ||
        probe.includes('timeout') ||
        probe.includes('context deadline exceeded') ||
        probe.includes('econnreset')
    ) {
        return { kind: 'network', detail };
    }

    return { kind: 'unknown', detail };
}

// One-line helpers for surfaces that show a plain string instead of a
// ChatErrorCard (CronJobDetail's run history). They use the same chat.errors
// title and body as the card, so a quota or billing error points to the
// Assistant page and never mentions an Isoform plan. New code should prefer
// `parseChatError` and render via `ChatErrorCard`.
export function parseQuotaError(errorMsg: string): { message: string } | null {
    const err = parseChatError(errorMsg);
    switch (err.kind) {
        case 'quota_period':
        case 'quota_generic':
        case 'subscription':
        case 'server':
            return { message: errorLine(err) };
        default:
            return null;
    }
}

export function friendlyErrorMessage(raw: string): string {
    const err = parseChatError(raw);
    switch (err.kind) {
        case 'quota_period':
        case 'quota_generic':
        case 'subscription':
        case 'server':
            return errorLine(err);
        default:
            return err.detail || errorLine(err);
    }
}

function periodLabels(period: QuotaPeriod): { period: string; periodLower: string } {
    switch (period) {
        case 'daily':
            return { period: i18n.t('chat:errors.period.daily'), periodLower: i18n.t('chat:errors.period.dailyLower') };
        case 'weekly':
            return { period: i18n.t('chat:errors.period.weekly'), periodLower: i18n.t('chat:errors.period.weeklyLower') };
        case 'monthly':
            return { period: i18n.t('chat:errors.period.monthly'), periodLower: i18n.t('chat:errors.period.monthlyLower') };
    }
}

function errorLine(err: ChatError): string {
    switch (err.kind) {
        case 'quota_period': {
            const labels = periodLabels(err.period ?? 'daily');
            return i18n.t('chat:errors.line', {
                title: i18n.t('chat:errors.quotaPeriod.title', labels),
                body: i18n.t('chat:errors.quotaPeriod.body', labels),
            });
        }
        case 'quota_generic':
            return i18n.t('chat:errors.line', {
                title: i18n.t('chat:errors.quotaGeneric.title'),
                body: i18n.t('chat:errors.quotaGeneric.body'),
            });
        case 'subscription':
            return i18n.t('chat:errors.line', {
                title: i18n.t('chat:errors.subscription.title'),
                body: i18n.t('chat:errors.subscription.body'),
            });
        case 'server':
            return i18n.t('chat:errors.line', {
                title: i18n.t('chat:errors.server.title'),
                body: i18n.t('chat:errors.server.body'),
            });
        default:
            return i18n.t('chat:errors.line', {
                title: i18n.t('chat:errors.unknown.title'),
                body: i18n.t('chat:errors.unknown.body'),
            });
    }
}

/** An error row's proven cause, stored as its toolInput ({code, assistant}). */
export function chatErrorCauseOf(item: { toolInput?: unknown }): { code?: string; assistant?: string } {
    const raw = item.toolInput;
    if (!raw || typeof raw !== 'object') return {};
    const { code, assistant } = raw as { code?: unknown; assistant?: unknown };
    return {
        code: typeof code === 'string' ? code : undefined,
        assistant: typeof assistant === 'string' ? assistant : undefined,
    };
}
