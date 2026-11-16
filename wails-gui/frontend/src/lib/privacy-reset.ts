import { EventsOn } from '../../wailsjs/runtime/runtime';

// One subscription point for the transitions that change what the computer
// permits a reader to see: Forget (`memory:withdrawn`), protecting an app
// (`sensitive-apps:updated`, the #185 protection-change event), Closed Eyes
// (`closed-eyes:changed`) and account sign-out. In-memory render-tier stores
// (chat edit drafts, highlight markers) register here and drop everything
// when any of them fires — the same events the page query cache invalidates
// on (lib/page-queries.ts).
//
// `model-accounts:updated` also fires for the 60-second quota poll
// (magpie_quota.go): the payload carries the full account status, and quota
// numbers change without any identity changing. Only an identity withdrawal
// — an account that was signed in is gone or switched off — is a privacy
// reset; an ordinary refresh must not erase anyone's unsent words.
//
// Baseline handling is fail-closed: while the signed-in identity set is
// unknown (the bootstrap status read is still pending or failed), every
// event is a possible withdrawal and resets. A status read that completes
// after the first event is stale and is discarded — it must never reinstate
// trust for accounts the event stream already saw removed.
const PRIVACY_RESET_EVENTS = ['memory:withdrawn', 'sensitive-apps:updated', 'closed-eyes:changed'];

const listeners = new Set<() => void>();
// The account-identity subset: listeners that care only about sign-out and
// account removal, not the other reset events. Shared by every reader that
// must drop observed-data state exactly when a signed-in identity is
// withdrawn — one baseline, one fence, no per-page reimplementation.
const accountWithdrawalListeners = new Set<() => void>();
let subscribed = false;

function notifyPrivacyReset(): void {
    for (const listener of listeners) listener();
}

// Account-driven resets are always both: a privacy reset and an identity
// withdrawal. The other reset events never touch the account baseline.
function notifyAccountIdentityWithdrawal(): void {
    notifyPrivacyReset();
    for (const listener of accountWithdrawalListeners) listener();
}

interface ModelAccountView {
    provider?: string;
    kind?: string;
    name?: string;
    on?: boolean;
}

function signedInIds(accounts: ModelAccountView[] | undefined): Set<string> {
    const ids = new Set<string>();
    for (const account of accounts ?? []) {
        if (account?.on) ids.add(`${account.provider ?? ''}/${account.kind ?? ''}/${account.name ?? ''}`);
    }
    return ids;
}

let knownSignedInIds: Set<string> | null = null;
let baselineStale = false;
let baselineGeneration = 0;

function checkModelAccounts(payload: unknown): void {
    const accounts = (payload as { accounts?: ModelAccountView[] } | null)?.accounts;
    if (!Array.isArray(accounts)) {
        // Shape we cannot read: privacy bias is to reset.
        notifyAccountIdentityWithdrawal();
        return;
    }
    const current = signedInIds(accounts);
    if (knownSignedInIds === null) {
        // Unknown baseline: this first event is a possible withdrawal.
        // Adopt its set going forward and fail closed — a slow or failed
        // bootstrap read must not leave private stores in place.
        knownSignedInIds = current;
        baselineStale = true;
        notifyAccountIdentityWithdrawal();
        return;
    }
    const known = knownSignedInIds;
    knownSignedInIds = current;
    for (const id of known) {
        if (!current.has(id)) {
            notifyAccountIdentityWithdrawal();
            return;
        }
    }
}

async function seedSignedInBaseline(): Promise<void> {
    const generation = baselineGeneration;
    try {
        // Dynamic, so test doubles for the shim that do not model this
        // binding simply leave the baseline to the first event.
        const { GetModelAccountsStatus } = await import('@/lib/wails-shim-app');
        const status = await GetModelAccountsStatus();
        // A newer reset or an event already established the baseline: this
        // completion is stale and must never reinstate trust or retain
        // private stores.
        if (generation !== baselineGeneration || baselineStale || knownSignedInIds !== null) return;
        knownSignedInIds = signedInIds(status?.accounts);
    } catch {
        // No status, no baseline; the first event resets and establishes it.
    }
}

function ensurePrivacyResetSubscription(): void {
    if (subscribed) return;
    subscribed = true;
    for (const name of PRIVACY_RESET_EVENTS) {
        EventsOn(name, () => notifyPrivacyReset());
    }
    EventsOn('model-accounts:updated', checkModelAccounts);
    // Seed the baseline once, so a sign-out that happened off this computer's
    // event stream before the first poll still reads as a withdrawal.
    void seedSignedInBaseline();
}

export function onPrivacyReset(listener: () => void): () => void {
    ensurePrivacyResetSubscription();
    listeners.add(listener);
    return () => { listeners.delete(listener); };
}

/** Fires only on account-identity withdrawal (a signed-in account gone or
 *  switched off, an unreadable payload, or an unknown baseline failing
 *  closed) — never on the other reset events or ordinary quota refreshes.
 *  For readers that must drop observed-data state exactly on sign-out. */
export function onAccountIdentityWithdrawal(listener: () => void): () => void {
    ensurePrivacyResetSubscription();
    accountWithdrawalListeners.add(listener);
    return () => { accountWithdrawalListeners.delete(listener); };
}

/** Fires every registered privacy listener, then re-seeds the account
 *  baseline like a cold start. Test and fixture isolation uses this so no
 *  store or baseline from a previous scenario leaks into the next; the real
 *  events call the same listeners. */
export function resetRenderTierStores(): void {
    baselineGeneration += 1;
    knownSignedInIds = null;
    baselineStale = false;
    notifyPrivacyReset();
    void seedSignedInBaseline();
}
