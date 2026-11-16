import { EventsOn } from '../../wailsjs/runtime/runtime';

// memory:withdrawn is a forget or the person's own memory change; a plain
// memory:updated (index pass, background learning) only marks reads stale.
const PRIVACY_SWEEP_EVENTS = ['memory:withdrawn', 'closed-eyes:changed', 'background-listening:changed', 'permissions:changed', 'sensitive-apps:updated'];
type WithdrawalListener = (event?: string, payload?: unknown) => void;
const listeners = new Set<WithdrawalListener>();
const cacheListeners = new Set<() => void>();
const cleanups: Array<() => void> = [];
let installed = false;

interface ModelAccountIdentity { provider?: string; kind?: string; name?: string; on?: boolean }
function signedInIds(accounts: ModelAccountIdentity[] | undefined): Set<string> {
    const ids = new Set<string>();
    for (const account of accounts ?? []) {
        if (account?.on) ids.add(`${account.provider ?? ''}/${account.kind ?? ''}/${account.name ?? ''}`);
    }
    return ids;
}
let knownSignedInIds: Set<string> | null = null;

function withdraw(event?: string, payload?: unknown) {
    for (const listener of [...listeners]) listener(event, payload);
    for (const listener of [...cacheListeners]) listener();
}

function checkModelAccounts(payload: unknown) {
    const accounts = (payload as { accounts?: ModelAccountIdentity[] } | null)?.accounts;
    const current = Array.isArray(accounts) ? signedInIds(accounts) : null;
    const known = knownSignedInIds;
    if (current !== null) knownSignedInIds = current;
    if (known === null) {
        // No first snapshot can prove that no earlier account was withdrawn.
        withdraw();
        return;
    }
    if (current === null || current.size === 0) {
        if (known.size > 0) withdraw();
        return;
    }
    for (const id of known) {
        if (!current.has(id)) {
            withdraw();
            return;
        }
    }
}

function installPrivacyEvents() {
    if (installed) return;
    installed = true;
    for (const event of PRIVACY_SWEEP_EVENTS) cleanups.push(EventsOn(event, (payload: unknown) => withdraw(event, payload)));
    cleanups.push(EventsOn('model-accounts:updated', checkModelAccounts));
}

export function onPagePrivacyWithdrawal(listener: WithdrawalListener): () => void {
    installPrivacyEvents();
    listeners.add(listener);
    return () => { listeners.delete(listener); };
}

export function onPagePrivacyCacheWithdrawal(listener: () => void): () => void {
    installPrivacyEvents();
    cacheListeners.add(listener);
    return () => { cacheListeners.delete(listener); };
}

export function resetPagePrivacyWithdrawal() {
    cleanups.splice(0).forEach((off) => off());
    installed = false;
    knownSignedInIds = null;
}
