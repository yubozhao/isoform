import {
    useCallback,
    useEffect,
    useLayoutEffect,
    useMemo,
    useRef,
    useState,
    type KeyboardEvent,
    type PointerEvent,
} from "react";
import { useTranslation } from "react-i18next";
import {
    AcceptManagedModelConsent,
    GetModelAccountsStatus,
    OpenAssistantInstallTerminal,
    OpenAssistantSignIn,
    OpenNodeDownload,
    RefreshAssistantHistory,
    SetManagedAccountOrder,
    SetupCheckAssistant,
    SetupInstallAssistant,
    type AssistantSetupCode,
    type AssistantSetupResult,
} from "@/lib/wails-shim-app";
import type { SetupAgentProbe } from "@/lib/setup-backend";
import { SetupIcon, iconMarkup, type SetupIconId } from "../SetupIcons";
import { Run, ease, lerp, reflow, rel } from "../motion";
import { useAgentProbe } from "../useAgentProbe";
import type { SceneProps } from "../types";
import { TIMINGS } from "../timings";
import { escapeHTML } from "./NoticesScene";
import { GatewaySetupNotice } from "../../gateway-setup-notice";

// Screen 2 (handoff §4, "Screen 2 · Your AI"): what Isoform notices goes to
// the assistant on top. Choosing it is the gesture itself: drag it to the
// top, or tap it. The choice is applied the moment the card lands.

export type AssistantId = "claude" | "codex";
const IDS: AssistantId[] = ["claude", "codex"];
const ICONS: Record<AssistantId, SetupIconId> = {
    claude: "i-term",
    codex: "i-code",
};
/**
 * A card's state. Past the happy path (install, sign in) each outcome has its
 * own state with its real next step, never a bare "Try again": a card turns
 * on after an install or sign-in here only once the assistant answered a
 * one-token test through the gateway.
 */
export type CardState =
    | "on"
    | "signin"
    | "install"
    | "installing"
    | "waiting"
    | "installWaiting"
    | "checking"
    | "testing"
    | "needsNode"
    | "installFailed"
    | "notFound"
    | "expired"
    | "testFailed";
type LocalState = Exclude<CardState, "on">;

/** Each state's subline and its action (the button after the subline). */
const CARD = {
    on: { sub: "v2.ai.signedIn" },
    signin: { sub: "v2.ai.notSignedIn", action: "v2.ai.signIn" },
    install: { sub: "v2.ai.notInstalled", action: "v2.ai.install" },
    installing: { sub: "v2.ai.installing" },
    waiting: { sub: "v2.ai.finishInTerminal", action: "v2.ai.openAgain" },
    installWaiting: { sub: "v2.ai.finishInTerminal", action: "v2.ai.openAgain" },
    checking: { sub: "v2.ai.checking" },
    testing: { sub: "v2.ai.testing" },
    needsNode: { sub: "v2.ai.needsNode", action: "v2.ai.getNode" },
    installFailed: { sub: "v2.ai.notInstalledYet", action: "v2.ai.installInTerminal" },
    notFound: { sub: "v2.ai.notFound", action: "v2.ai.installInTerminal" },
    expired: { sub: "v2.ai.signInFailed", action: "v2.ai.signIn" },
    testFailed: { sub: "v2.ai.noAnswer", action: "v2.ai.testAgain" },
} as const satisfies Record<CardState, { sub: string; action?: string }>;
const BUSY: CardState[] = ["installing", "checking", "testing"];
type ActionKey = Extract<(typeof CARD)[CardState], { action: string }>["action"];
const actionOf = (state: CardState): ActionKey | undefined =>
    "action" in CARD[state] ? (CARD[state] as { action: ActionKey }).action : undefined;

/** Where each setup result code leaves the card, and the line it shows. */
const CODE_STATE: Partial<Record<AssistantSetupCode, LocalState>> = {
    needsNode: "needsNode",
    installDenied: "installFailed",
    installTimeout: "installFailed",
    installFailed: "installFailed",
    notFound: "notFound",
    signedOut: "signin",
    testSignedOut: "expired",
    testFailed: "testFailed",
    gatewayDown: "testFailed",
};
const CODE_NOTICE = {
    needsNode: "v2.ai.fineNeedsNode",
    installDenied: "v2.ai.fineInstallDenied",
    installTimeout: "v2.ai.fineInstallTimeout",
    installFailed: "v2.ai.fineInstallFailed",
    notFound: "v2.ai.fineNotFound",
    testSignedOut: "v2.ai.fineTestSignedOut",
    testFailed: "v2.ai.fineTestFailed",
    gatewayDown: "v2.ai.fineGatewayDown",
    testLimit: "v2.ai.fineLimit",
} as const satisfies Partial<Record<AssistantSetupCode, string>>;
type NoticeKey =
    | (typeof CODE_NOTICE)[keyof typeof CODE_NOTICE]
    | "v2.ai.errorSetUp"
    | "v2.ai.fineWaitSignIn"
    | "v2.ai.fineWaitInstall"
    | "v2.ai.fineNoTerminal";

interface Notice {
    key: NoticeKey;
    id?: AssistantId;
    command?: string;
    error: boolean;
}

const T = TIMINGS.ai;

/** What each card shows, from the launch check plus what is under way here. */
export function cardStates(
    probe: SetupAgentProbe | null,
    local: Partial<Record<AssistantId, LocalState>>,
    verified: Partial<Record<AssistantId, boolean>> = {},
): Record<AssistantId, CardState> {
    const out = {} as Record<AssistantId, CardState>;
    for (const id of IDS) {
        const agent = probe?.agents.find((a) => a.id === id);
        const mine = local[id];
        // A sign-in finished in Terminal: the test call runs next.
        if (mine === "waiting" && agent?.signedIn) out[id] = "testing";
        else if (mine) out[id] = mine;
        else if (verified[id] || agent?.signedIn) out[id] = "on";
        else if (agent?.installed) out[id] = "signin";
        else out[id] = "install";
    }
    return out;
}

type FlowKey = "lenaSeen" | "lenaMade" | "deckSeen" | "deckMade" | "priyaSeen" | "priyaMade";
const FLOW: Array<[SetupIconId, FlowKey, FlowKey]> = [
    ["i-mail", "lenaSeen", "lenaMade"],
    ["i-screen", "deckSeen", "deckMade"],
    ["i-chat", "priyaSeen", "priyaMade"],
];

/** The frozen gallery states (handoff §9). */
const STILL_STATES: Record<
    string,
    { st: Record<AssistantId, CardState>; top: AssistantId | null; bk: AssistantId | null }
> = {
    "ai-empty": { st: { claude: "on", codex: "on" }, top: null, bk: null },
    "ai-picked": { st: { claude: "on", codex: "on" }, top: "claude", bk: "codex" },
    "ai-one": { st: { claude: "on", codex: "install" }, top: "claude", bk: null },
    "ai-none": { st: { claude: "signin", codex: "install" }, top: null, bk: null },
};
const STILL_PLANS: Record<AssistantId, string> = { claude: "Max", codex: "Plus" };
/** The commands a line can name (assistant_setup.go returns the same). */
const INSTALL: Record<AssistantId, string> = {
    claude: "curl -fsSL https://claude.ai/install.sh | bash",
    codex: "curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh",
};
const SIGN_IN: Record<AssistantId, string> = { claude: "claude auth login", codex: "codex login" };

export function YourAIScene({ onNext, reduced, still, enter }: SceneProps) {
    const { t } = useTranslation("onboarding");
    const frozen = still ? STILL_STATES[still] : undefined;
    const { probe, ready, refresh } = useAgentProbe(!frozen);
    const gatewayNeeded = !frozen && !!probe?.magpie && probe.magpie.required !== false && !probe.magpie.healthy;
    const [local, setLocal] = useState<Partial<Record<AssistantId, LocalState>>>({});
    const [verified, setVerified] = useState<Partial<Record<AssistantId, boolean>>>({});
    const st = frozen?.st ?? cardStates(probe, local, verified);
    const [top, setTop] = useState<AssistantId | null>(frozen?.top ?? null);
    const [bk, setBk] = useState<AssistantId | null>(frozen?.bk ?? null);
    const [notice, setNotice] = useState<Notice | null>(null);
    const error = notice?.error ? notice : null;
    const [restored, setRestored] = useState(!!frozen);
    const available = IDS.filter((id) => st[id] === "on");
    const availableKey = available.join(",");
    const anyOn = available.length > 0;

    const root = useRef<HTMLDivElement>(null);
    const slotTop = useRef<HTMLDivElement>(null);
    const slotBk = useRef<HTMLDivElement>(null);
    const coach = useRef<HTMLDivElement>(null);
    const tile = useRef<HTMLSpanElement>(null);
    const cards = useRef<Partial<Record<AssistantId, HTMLDivElement | null>>>({});
    const touched = useRef(false);
    const flips = useRef(new Map<AssistantId, DOMRect>());
    const applying = useRef(0);

    const plan = (id: AssistantId) =>
        frozen ? STILL_PLANS[id] : (probe?.agents.find((a) => a.id === id)?.plan ?? "");

    // Back keeps the choice: an order chosen before (consent given) is shown
    // as already made, with no animation.
    useEffect(() => {
        if (frozen) return;
        let live = true;
        void GetModelAccountsStatus()
            .then((status) => {
                if (!live || !status.consent?.accepted) return;
                const order = status.accountOrder.filter(
                    (id): id is AssistantId => id === "claude" || id === "codex",
                );
                if (order.length) {
                    touched.current = true;
                    setTop((cur) => cur ?? order[0]);
                    setBk((cur) => cur ?? order[1] ?? null);
                }
            })
            .catch(() => {})
            .finally(() => {
                if (live) setRestored(true);
            });
        return () => {
            live = false;
        };
    }, [frozen]);

    const capture = (ids: AssistantId[]) => {
        for (const id of ids) {
            const el = cards.current[id];
            if (el) flips.current.set(id, el.getBoundingClientRect());
        }
    };

    // FLIP: each card that changed place springs from where it was.
    useLayoutEffect(() => {
        flips.current.forEach((from, id) => {
            const el = cards.current[id];
            if (!el) return;
            el.style.transition = "none";
            el.style.transform = "";
            if (reduced) return;
            const to = el.getBoundingClientRect();
            el.style.transform = `translate(${from.left - to.left}px, ${from.top - to.top}px)`;
            reflow(el);
            el.style.transition = "transform .45s var(--spring)";
            el.style.transform = "";
        });
        flips.current.clear();
    }, [top, bk, reduced]);

    const apply = useCallback(
        async (order: AssistantId[], prev: [AssistantId | null, AssistantId | null]) => {
            const mine = ++applying.current;
            try {
                let status = await SetManagedAccountOrder(order);
                if (!status.consent.accepted)
                    status = await AcceptManagedModelConsent(status.consent.version);
            } catch {
                if (mine !== applying.current) return;
                capture(IDS);
                setTop(prev[0]);
                setBk(prev[1]);
                setNotice({ key: "v2.ai.errorSetUp", error: true });
            }
        },
        [],
    );

    const select = useCallback(
        (id: AssistantId) => {
            if (st[id] !== "on") return;
            const other = IDS.find((x) => x !== id && st[x] === "on") ?? null;
            capture(IDS);
            const prev: [AssistantId | null, AssistantId | null] = [top, bk];
            setTop(id);
            setBk(other);
            setNotice(null);
            if (!frozen) void apply(other ? [id, other] : [id], prev);
        },
        // eslint-disable-next-line react-hooks/exhaustive-deps
        [availableKey, top, bk, frozen, apply],
    );

    const tap = (id: AssistantId) => {
        if (st[id] !== "on") return;
        touched.current = true;
        if (top !== id) select(id);
    };

    // Only one found: it moves into Answers first by itself after 1 s. A
    // sign-in that lands while this screen is open lights its card and moves
    // it into the free slot.
    const prevAvailable = useRef<string | null>(null);
    useEffect(() => {
        if (frozen || !ready || !restored) return;
        const before = prevAvailable.current;
        prevAvailable.current = availableKey;
        const run = new Run(reduced);
        if (!top && available.length === 1) {
            const first = before === null;
            run.go(async () => {
                await run.wait(reduced ? 0 : first ? T.autoPickFirst : T.autoPickLater);
                select(available[0]);
            });
        } else if (top && !bk && before !== null) {
            const added = available.find(
                (id) => id !== top && !before.split(",").includes(id),
            );
            if (added) {
                capture([added]);
                setBk(added);
                void apply([top, added], [top, null]);
            }
        }
        return () => run.cancel();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [availableKey, ready, restored, frozen]);

    // What Isoform noticed, travelling to the assistant and back.
    useEffect(() => {
        const ai = root.current;
        const tileEl = tile.current;
        const slot = slotTop.current;
        if (!ai || !tileEl || !slot || frozen || reduced || !ready) return;
        const run = new Run(reduced);
        const toks: HTMLElement[] = [];
        const tok = (html: string) => {
            const el = document.createElement("span");
            el.className = "chip tok";
            el.innerHTML = html;
            ai.appendChild(el);
            toks.push(el);
            return el;
        };
        const at = (el: HTMLElement, x: number, y: number) => {
            el.style.transform = `translate(${x}px, ${y}px) translate(-50%, -50%)`;
        };
        const glide = (el: HTMLElement, x0: number, y0: number, x1: number, y1: number, ms: number) =>
            run.tween(ms, (p) => {
                const e = ease(p);
                at(el, lerp(x0, x1, e), lerp(y0, y1, e));
            });
        const place = (el: HTMLElement, x: number, y: number) => {
            el.style.transform = `translate(${x}px, ${y}px)`;
        };
        const ghostDrag = async () => {
            const c = cards.current.claude;
            const hand = coach.current;
            if (!c || !hand) return run.wait(T.waitHold);
            const p = rel(c, ai);
            const cx = p.x + c.offsetWidth / 2;
            const cy = p.y + 22;
            let hx = cx + 30;
            let hy = cy + 46;
            place(hand, hx, hy);
            hand.style.opacity = "1";
            await run.tween(T.coachIn, (q) => {
                const e = ease(q);
                place(hand, lerp(hx, cx, e), lerp(hy, cy, e));
            });
            hx = cx;
            hy = cy;
            if (touched.current) return;
            c.style.transition = "none";
            c.dataset.dragging = "";
            const tx = 180 - cx;
            const ty = 92 - cy;
            await run.tween(T.coachDrag, (q) => {
                if (touched.current) return;
                const e = ease(q);
                c.style.transform = `translate(${tx * e}px, ${ty * e}px)`;
                place(hand, cx + tx * e, cy + ty * e);
                if (q > 0.7) slot.dataset.over = "";
                else delete slot.dataset.over;
            });
            await run.wait(T.coachHold);
            delete c.dataset.dragging;
            delete slot.dataset.over;
            if (!touched.current) {
                c.style.transition = "transform .35s cubic-bezier(.2,1.2,.4,1)";
                c.style.transform = "";
            }
            hand.style.opacity = "0";
            await run.wait(T.coachOut);
        };
        const bothFound = availableKey === "claude,codex";
        run.go(async () => {
            await run.wait(T.start);
            for (let k = 0; ; k++) {
                const [icon, seenKey, madeKey] = FLOW[k % 3];
                const useBk = !!(top && bk && k % 3 === 2);
                const o = tok(iconMarkup(icon) + escapeHTML(t(`v2.ai.flow.${seenKey}`)));
                const w = o.offsetWidth;
                at(o, 206 + w / 2, 20);
                tileEl.classList.remove("saw");
                reflow(tileEl);
                tileEl.classList.add("saw");
                await run.wait(T.tokenIn);
                o.style.opacity = "1";
                await run.wait(T.tokenHold);
                await glide(o, 206 + w / 2, 20, 180, 52, T.toSlot);
                if (!top) {
                    o.classList.add("wait");
                    await glide(o, 180, 52, 180, 64, T.toWait);
                    slot.dataset.waiting = "";
                    if (bothFound && !touched.current) await ghostDrag();
                    else await run.wait(T.waitHold);
                    o.style.opacity = "0";
                    delete slot.dataset.waiting;
                    await run.wait(T.waitFade);
                    o.remove();
                    await run.wait(T.waitRest);
                    continue;
                }
                const topC = cards.current[top];
                const bkC = bk ? cards.current[bk] : null;
                if (!topC) return;
                let dest: HTMLElement = topC;
                let dy = 92;
                if (useBk && bkC) {
                    topC.dataset.limit = "";
                    await glide(o, 180, 52, 180, 92, T.toLimit);
                    await run.wait(T.limitHold);
                    dest = bkC;
                    dy = 160;
                    await glide(o, 180, 92, 180, 160, T.toBackup);
                } else await glide(o, 180, 52, 180, 92, T.toCard);
                o.style.opacity = "0";
                dest.dataset.thinking = "";
                await run.wait(T.thinking);
                delete dest.dataset.thinking;
                o.remove();
                const r = tok(
                    `${iconMarkup("i-check")}${escapeHTML(t(`v2.ai.flow.${madeKey}`))}<span class="st ready">${escapeHTML(t("v2.ai.flow.ready"))}</span>`,
                );
                const rw = r.offsetWidth;
                at(r, 180, dy);
                r.style.opacity = "1";
                await glide(r, 180, dy, 154 - rw / 2, 20, T.back);
                if (useBk) delete topC.dataset.limit;
                await run.wait(T.readyHold);
                r.style.opacity = "0";
                await run.wait(T.readyFade);
                r.remove();
                await run.wait(T.rest);
            }
        });
        return () => {
            run.cancel();
            toks.forEach((el) => el.remove());
            delete slot.dataset.waiting;
            delete slot.dataset.over;
            if (coach.current) coach.current.style.opacity = "0";
            for (const id of IDS) {
                const c = cards.current[id];
                if (!c) continue;
                delete c.dataset.thinking;
                delete c.dataset.limit;
                delete c.dataset.dragging;
            }
        };
    }, [top, bk, availableKey, ready, frozen, reduced, t]);

    const hit = (el: HTMLElement | null, x: number, y: number) => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;
    };

    const onPointerDown = (id: AssistantId) => (e: PointerEvent<HTMLDivElement>) => {
        const c = e.currentTarget;
        if (st[id] !== "on" || e.button > 0) return;
        if ((e.target as HTMLElement).closest(".cact")) return;
        touched.current = true;
        if (coach.current) coach.current.style.opacity = "0";
        const x0 = e.clientX;
        const y0 = e.clientY;
        let moved = false;
        try {
            c.setPointerCapture(e.pointerId);
        } catch {
            /* Capture is a nicety. */
        }
        c.style.transition = "none";
        c.dataset.dragging = "";
        const over = (el: HTMLElement | null, on: boolean) => {
            if (!el) return;
            if (on) el.dataset.over = "";
            else delete el.dataset.over;
        };
        const move = (ev: globalThis.PointerEvent) => {
            const dx = ev.clientX - x0;
            const dy = ev.clientY - y0;
            if (Math.abs(dx) + Math.abs(dy) > 5) moved = true;
            c.style.transform = `translate(${dx}px, ${dy}px)`;
            over(slotTop.current, hit(slotTop.current, ev.clientX, ev.clientY));
            over(slotBk.current, !!top && hit(slotBk.current, ev.clientX, ev.clientY));
        };
        const up = (ev: globalThis.PointerEvent) => {
            c.removeEventListener("pointermove", move);
            c.removeEventListener("pointerup", up);
            c.removeEventListener("pointercancel", up);
            delete c.dataset.dragging;
            over(slotTop.current, false);
            over(slotBk.current, false);
            if (!moved) {
                c.style.transform = "";
                tap(id);
                return;
            }
            if (hit(slotTop.current, ev.clientX, ev.clientY) && top !== id) select(id);
            else if (top === id && bk && hit(slotBk.current, ev.clientX, ev.clientY))
                select(bk);
            else {
                c.style.transition = "transform .35s cubic-bezier(.2,1.2,.4,1)";
                c.style.transform = "";
            }
        };
        c.addEventListener("pointermove", move);
        c.addEventListener("pointerup", up);
        c.addEventListener("pointercancel", up);
    };

    const alive = useRef(false);
    useEffect(() => {
        alive.current = true;
        return () => {
            alive.current = false;
        };
    }, []);
    const setCard = (id: AssistantId, state?: LocalState) =>
        setLocal((cur) => {
            const next = { ...cur };
            if (state) next[id] = state;
            else delete next[id];
            return next;
        });

    // Where a setup result leaves the card. Signed in and answering turns it
    // on; anything else names the real next step.
    const settle = (id: AssistantId, r: AssistantSetupResult) => {
        if (!alive.current) return;
        const key = (CODE_NOTICE as Partial<Record<AssistantSetupCode, NoticeKey>>)[r.code];
        if (r.working) {
            setVerified((cur) => ({ ...cur, [id]: true }));
            setCard(id);
            setNotice(key ? { key, id, error: false } : null);
            return;
        }
        setCard(id, CODE_STATE[r.code] ?? "testFailed");
        setNotice(key ? { key, id, command: r.command, error: true } : null);
    };

    // One tiny real call through the gateway, the way chat reaches it.
    // The test call, tried again for a while when the gateway is still
    // starting (screen 2 can open before it is up).
    const testCall = async (id: AssistantId) => {
        let r = await SetupCheckAssistant(id, true);
        for (let i = 1; r.code === "gatewayDown" && i < T.testTries && alive.current; i++) {
            await new Promise((done) => setTimeout(done, T.testRetry));
            r = await SetupCheckAssistant(id, true);
        }
        return r;
    };

    const testing = useRef(new Set<AssistantId>());
    const verify = async (id: AssistantId) => {
        // The re-check and the launch check can both see the sign-in land.
        if (testing.current.has(id)) return;
        testing.current.add(id);
        setCard(id, "testing");
        setNotice(null);
        try {
            settle(id, await testCall(id));
        } catch {
            settle(id, { id, code: "gatewayDown" } as AssistantSetupResult);
        } finally {
            testing.current.delete(id);
        }
    };

    const install = async (id: AssistantId) => {
        setNotice(null);
        setCard(id, "installing");
        let r: AssistantSetupResult;
        try {
            r = await SetupInstallAssistant(id);
        } catch {
            r = { id, code: "installFailed" } as AssistantSetupResult;
        }
        if (!alive.current) return;
        // Installed: a sign-in it already had goes straight to the test.
        if (r.signedIn) void verify(id);
        else settle(id, r);
    };

    const signIn = async (id: AssistantId) => {
        setNotice({ key: "v2.ai.fineWaitSignIn", id, error: false });
        setCard(id, "waiting");
        try {
            await OpenAssistantSignIn(id);
        } catch {
            setCard(id, "signin");
            setNotice({ key: "v2.ai.fineNoTerminal", id, command: SIGN_IN[id], error: true });
        }
    };

    const installInTerminal = async (id: AssistantId) => {
        setNotice({ key: "v2.ai.fineWaitInstall", id, command: INSTALL[id], error: false });
        setCard(id, "installWaiting");
        try {
            await OpenAssistantInstallTerminal(id);
        } catch {
            setCard(id, "installFailed");
            setNotice({ key: "v2.ai.fineNoTerminal", id, command: INSTALL[id], error: true });
        }
    };

    const getNode = (id: AssistantId) => {
        nodeAsked.current.add(id);
        void OpenNodeDownload().catch(() => {});
    };

    const act = (id: AssistantId, state: CardState) => {
        switch (state) {
            case "install":
                return install(id);
            case "signin":
            case "expired":
            case "waiting":
                return signIn(id);
            case "needsNode":
                return getNode(id);
            case "installFailed":
            case "notFound":
            case "installWaiting":
                return installInTerminal(id);
            case "testFailed":
                return verify(id);
        }
    };

    // While Terminal is open (a sign-in, an install) keep re-checking with a
    // fresh PATH, on a short timer and whenever this window comes back.
    const nodeAsked = useRef(new Set<AssistantId>());
    const checking = useRef(new Set<AssistantId>());
    const recheck = async (id: AssistantId) => {
        if (checking.current.has(id)) return;
        checking.current.add(id);
        try {
            const r = await SetupCheckAssistant(id, false);
            if (!alive.current) return;
            if (r.signedIn) void verify(id);
            else if (r.installed && local[id] === "installWaiting") {
                setCard(id, "signin");
                setNotice(null);
            }
        } catch {
            /* The next tick checks again. */
        } finally {
            checking.current.delete(id);
        }
    };
    const watchKey = frozen
        ? ""
        : IDS.filter((id) => st[id] === "waiting" || st[id] === "installWaiting").join(",");
    const nodeKey = frozen ? "" : IDS.filter((id) => st[id] === "needsNode").join(",");
    useEffect(() => {
        if (!watchKey && !nodeKey) return;
        const watched = watchKey ? (watchKey.split(",") as AssistantId[]) : [];
        const tick = () => watched.forEach((id) => void recheck(id));
        // Back from the Node.js download: the install the user asked for
        // runs again by itself.
        const back = () => {
            tick();
            if (document.visibilityState === "hidden") return;
            for (const id of nodeKey ? (nodeKey.split(",") as AssistantId[]) : []) {
                if (nodeAsked.current.delete(id)) void install(id);
            }
        };
        const timer = watched.length ? setInterval(tick, T.recheck) : undefined;
        window.addEventListener("focus", back);
        document.addEventListener("visibilitychange", back);
        return () => {
            if (timer) clearInterval(timer);
            window.removeEventListener("focus", back);
            document.removeEventListener("visibilitychange", back);
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [watchKey, nodeKey]);

    // The launch check saw a sign-in finish in Terminal: test it.
    const signedInKey = IDS.filter(
        (id) => local[id] === "waiting" && probe?.agents.find((a) => a.id === id)?.signedIn,
    ).join(",");
    useEffect(() => {
        if (frozen || !signedInKey) return;
        for (const id of signedInKey.split(",") as AssistantId[]) void verify(id);
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [signedInKey, frozen]);

    // Signed in at launch: the card shows as it always did, the test runs in
    // the background and never holds Continue. It turns the card to Working,
    // or to the failure's own state.
    const backgrounded = useRef(new Set<AssistantId>());
    const backgroundTest = async (id: AssistantId) => {
        testing.current.add(id);
        try {
            const r = await testCall(id);
            if (!alive.current) return;
            if (r.working) {
                setVerified((cur) => ({ ...cur, [id]: true }));
                if (r.code === "testLimit")
                    setNotice({ key: "v2.ai.fineLimit", id, error: false });
            } else if (r.code !== "signedOut") settle(id, r);
        } catch {
            /* The card stays as the launch check showed it. */
        } finally {
            testing.current.delete(id);
        }
    };
    const launchSignedIn = frozen || gatewayNeeded
        ? ""
        : IDS.filter((id) => probe?.agents.find((a) => a.id === id)?.signedIn).join(",");
    useEffect(() => {
        if (!launchSignedIn) return;
        for (const id of launchSignedIn.split(",") as AssistantId[]) {
            if (backgrounded.current.has(id) || testing.current.has(id) || local[id] || verified[id])
                continue;
            backgrounded.current.add(id);
            void backgroundTest(id);
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [launchSignedIn]);

    const subline = (id: AssistantId) => {
        const state = st[id];
        if (state === "on") {
            const p = plan(id);
            if (verified[id]) return p ? t("v2.ai.planWorking", { plan: p }) : t("v2.ai.working");
            return p ? t("v2.ai.planSignedIn", { plan: p }) : t("v2.ai.signedIn");
        }
        return t(CARD[state].sub);
    };

    const renderCard = (id: AssistantId) => {
        const state = st[id];
        const on = state === "on";
        const name = t(`agent.names.${id}`);
        const sub = subline(id);
        return (
            <div
                key={id}
                ref={(el) => {
                    cards.current[id] = el;
                }}
                className={`as${on ? "" : " off"}${BUSY.includes(state) ? " installing" : ""}`}
                data-testid={`setup-v2-card-${id}`}
                role="button"
                tabIndex={on ? 0 : -1}
                aria-label={t("v2.ai.cardLabel", { name, state: sub })}
                onPointerDown={onPointerDown(id)}
                onKeyDown={(e: KeyboardEvent<HTMLDivElement>) => {
                    if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        tap(id);
                    }
                }}
            >
                <span className="aic">
                    <SetupIcon id={ICONS[id]} />
                </span>
                <span>
                    <b>{name}</b>
                    <small
                        className={
                            state === "waiting" || state === "installWaiting" ? "wait" : undefined
                        }
                    >
                        {sub}
                        {actionOf(state) ? (
                            <>
                                {" · "}
                                <button
                                    className="cact"
                                    type="button"
                                    data-testid={`setup-v2-card-${id}-action`}
                                    onClick={(e) => {
                                        e.stopPropagation();
                                        void act(id, state);
                                    }}
                                >
                                    {t(actionOf(state)!)}
                                </button>
                            </>
                        ) : null}
                    </small>
                </span>
                <span className="think">
                    <i />
                    <i />
                    <i />
                </span>
                <span className="lim">{t("v2.ai.atLimit")}</span>
                <span className="bar">
                    <i />
                </span>
            </div>
        );
    };

    const showCards = ready || !!frozen;
    const trayIds = useMemo(
        () => (showCards ? IDS.filter((id) => id !== top && id !== bk) : []),
        [showCards, top, bk],
    );
    const selected = !!top;
    const fine = notice
        ? t(notice.key, {
              name: notice.id ? t(`agent.names.${notice.id}`) : "",
              command: notice.command ?? "",
          })
        : selected
          ? t("v2.ai.finePlans")
          : anyOn || !showCards
            ? t("v2.ai.fineDrag")
            : t("v2.ai.fineSignIn");
    // Setup v2.1 §7: with Claude Code or Codex on this computer, say that
    // Isoform reads their last 30 days, and start the read when screen 2 ends.
    const historyFrom = available.length === 2
        ? t("v2.ai.assistantsBoth")
        : available.length === 1
          ? t(`agent.names.${available[0]}`)
          : null;
    const finish = () => {
        if (historyFrom && !frozen) Promise.resolve().then(() => RefreshAssistantHistory()).catch(() => {});
        onNext();
    };

    return (
        <section className={`scene ${enter}`} data-testid="setup-v2-ai">
            <h2 className="t">{t("v2.ai.title")}</h2>
            <p className="s">{t("v2.ai.line")}</p>
            <div className="ill">
                <div className="ai" ref={root}>
                    <div className="maczone">
                        <span className="zl">
                            <SetupIcon id="i-lock" />
                            {t("v2.ai.zone")}
                        </span>
                    </div>
                    <div className="isorow">
                        <span className="itile" ref={tile} title={t("v2.ai.isoform")}>
                            <SetupIcon id="mk" />
                        </span>
                    </div>
                    <div className="wire" />
                    <div
                        className={`slot first${top ? " filled" : ""}`}
                        ref={slotTop}
                        data-testid="setup-v2-slot-first"
                    >
                        <span className="sl">{t("v2.ai.answersFirst")}</span>
                        <span className="hint">{t("v2.ai.dragHere")}</span>
                        {top && showCards ? renderCard(top) : null}
                    </div>
                    <div className={`slot bk${bk ? " filled" : ""}`} ref={slotBk}>
                        <span className="sl">{t("v2.ai.backup")}</span>
                        <span className="hint">
                            {selected && !bk ? t("v2.ai.noBackup") : t("v2.ai.otherStepsIn")}
                        </span>
                        {bk && showCards ? renderCard(bk) : null}
                    </div>
                    <div className={`tray${selected && trayIds.length === 0 ? " gone" : ""}`}>
                        <div className="tl2">
                            {selected ? t("v2.ai.addBackup") : t("v2.ai.found")}
                        </div>
                        <div className="trow">{trayIds.map(renderCard)}</div>
                    </div>
                    <p className={`ainote${top && bk ? " show" : ""}`}>
                        {top && bk
                            ? t("v2.ai.note", {
                                  backup: t(`agent.names.${bk}`),
                                  first: t(`agent.names.${top}`),
                              })
                            : ""}
                    </p>
                    <div className="cur you coach" ref={coach}>
                        <SetupIcon id="i-arrow" />
                    </div>
                </div>
            </div>
            <div className="act">
                {!gatewayNeeded && <p className={`fine wide${error ? " error" : ""}`} aria-live="polite">
                    {fine}
                </p>}
                {gatewayNeeded && probe?.magpie && <GatewaySetupNotice gateway={probe.magpie} onReady={() => void refresh()} />}
                {historyFrom && !error && !gatewayNeeded ? (
                    <p className="fine" data-testid="setup-v2-ai-history">
                        {t("v2.ai.fineHistory", { assistants: historyFrom })}
                    </p>
                ) : null}
                {!gatewayNeeded && <button
                    className="btn primary"
                    type="button"
                    data-testid="setup-v2-continue"
                    disabled={!selected}
                    onClick={finish}
                >
                    {t("v2.continue")}
                </button>}
                {showCards && (!anyOn || gatewayNeeded) ? (
                    <button className="btn quiet" type="button" onClick={onNext}>
                        {t("v2.ai.setUpLater")}
                    </button>
                ) : null}
            </div>
        </section>
    );
}
