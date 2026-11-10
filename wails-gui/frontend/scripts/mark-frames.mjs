// Generates src/components/mark-motion/mark-frames.json from design's cut kit
// (docs/handoff-2026-11-09/mark-motion-1.0.0/mark-kit.js), the same way design
// made mark-frames.json: every gesture stepped at twelve frames a second, each
// frame the whole state of the mark.
//
// The one change is handoff rule 24: the kit's rest S is redrawn to the logo
// (LOGO_S below, fitted to LogoIcon.tsx's outline at 20x by pixel difference),
// and the shapes that share the logo's tail (cap, peek, groucho) move with it.
// The I is redrawn in the renderer (mark-geometry.ts), not here.
//
//   node scripts/mark-frames.mjs          write the app's frames
//   node scripts/mark-frames.mjs --check  regenerate design's own frames with
//                                         the kit unchanged and compare them
//                                         with design's mark-frames.json.
//                                         Design sampled its frames on a live
//                                         clock (a few repeat), so each
//                                         sequence's first and last frames
//                                         must match and its length be within
//                                         two frames.
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const KIT_DIR = path.resolve(HERE, '../../../docs/handoff-2026-11-09/mark-motion-1.0.0');
const OUT = path.resolve(HERE, '../src/components/mark-motion/mark-frames.json');
const check = process.argv.includes('--check');

// The logo's S, in kit units: fitted to the logo with the kit's stroke (21).
const LOGO_S = [77.5, 206.9, 93.75, 224, 113.65, 232.5, 134.5, 220, 150.1, 206.1, 157.5, 195, 162, 170.9,
    174.35, 130.4, 183.65, 116, 211.1, 107.9, 219.35, 105.25, 241.25, 106, 253, 114.35];

function loadKit() {
    const FRAME = 1000 / 12;
    let now = 0;
    let queue = [];
    const sandbox = {
        performance: { now: () => now },
        requestAnimationFrame: (cb) => { queue.push(cb); return queue.length; },
        Math, Promise, Object, Array,
    };
    sandbox.window = sandbox;
    vm.createContext(sandbox);
    vm.runInContext(fs.readFileSync(path.join(KIT_DIR, 'mark-kit.js'), 'utf8'), sandbox);
    const kit = sandbox.MarkKit;
    const flush = () => new Promise((res) => setImmediate(res));
    // One step of the clock: every waiting frame callback runs once, a hair
    // over a twelfth of a second later, so each tween and sleep moves exactly
    // one frame.
    const step = () => { now += FRAME + 1e-6; const q = queue; queue = []; for (const cb of q) cb(now); };
    return { kit, flush, step, pending: () => queue.length };
}

function applyLogo(kit) {
    const delta = LOGO_S.map((v, i) => v - kit.SH.logo[i]);
    for (const name of ['cap', 'peek', 'groucho']) {
        kit.SH[name].forEach((v, i) => { kit.SH[name][i] = Math.round((v + delta[i]) * 100) / 100; });
    }
    LOGO_S.forEach((v, i) => { kit.SH.logo[i] = v; });
}

const round = (v) => (typeof v === 'number' ? Math.round(v * 100) / 100 : Array.isArray(v) ? v.map(round) : v);
const snap = (st) => Object.fromEntries(Object.entries(st).map(([k, v]) => [k, round(v)]));

async function record(env, setup, run) {
    const { kit, flush, step, pending } = env;
    const m = { st: kit.HOME(), render() {} };
    if (setup) setup(m);
    const inst = { stop: false };
    let done = false;
    run(m, inst).then(() => { done = true; });
    await flush();
    const frames = [snap(m.st)];
    for (let guard = 0; !done && guard < 2000; guard++) {
        if (!pending()) throw new Error('kit stalled');
        step();
        await flush();
        frames.push(snap(m.st));
    }
    if (!done) throw new Error('sequence did not end');
    return frames;
}

async function generate(patch) {
    const env = loadKit();
    const { kit } = env;
    if (patch) applyLogo(kit);
    const pose = (name) => (m) => kit.HELD[name].pose(m);
    const poses = { rest: snap(kit.HOME()) };
    for (const name of ['working', 'listening', 'paused']) {
        const m = { st: kit.HOME(), render() {} };
        kit.HELD[name].pose(m);
        poses[name] = snap(m.st);
    }
    const seq = {};
    for (const name of ['working', 'listening', 'paused']) {
        const held = kit.HELD[name];
        seq[`${name}.enter`] = await record(env, null, (m, i) => held.enter(m, i));
        if (held.loop) seq[`${name}.loop`] = await record(env, pose(name), (m, i) => held.loop(m, i));
        if (held.loopLarge) seq[`${name}.loopLarge`] = await record(env, pose(name), (m, i) => held.loopLarge(m, i));
        if (held.peek) seq[`${name}.peek`] = await record(env, pose(name), (m, i) => held.peek(m, i));
        seq[`${name}.leave`] = await record(env, pose(name), (m, i) => held.leave(m, i));
    }
    seq.blink = await record(env, null, (m, i) => kit.GEST.blink(m, i));
    seq.hello = await record(env, null, (m, i) => kit.GEST.hello(m, i));
    seq.needs = await record(env, null, (m, i) => kit.GEST.needs(m, i));
    seq.briefMorning = await record(env, null, (m, i) => kit.GEST.brief(m, i, {}));
    seq.briefEvening = await record(env, null, (m, i) => kit.GEST.brief(m, i, { evening: true }));
    seq.sent = await record(env, null, (m, i) => kit.GEST.sent(m, i));
    seq.retry = await record(env, null, (m, i) => kit.GEST.retry(m, i));
    return { poses, seq };
}

function compare(a, b, where) {
    if (typeof a === 'number' && typeof b === 'number') {
        if (Math.abs(a - b) > 0.02) throw new Error(`${where}: ${a} != ${b}`);
        return;
    }
    if (Array.isArray(a) && Array.isArray(b)) {
        if (a.length !== b.length) throw new Error(`${where}: length ${a.length} != ${b.length}`);
        a.forEach((v, i) => compare(v, b[i], `${where}[${i}]`));
        return;
    }
    if (a && b && typeof a === 'object' && typeof b === 'object') {
        for (const k of new Set([...Object.keys(a), ...Object.keys(b)])) compare(a[k], b[k], `${where}.${k}`);
        return;
    }
    if (a !== b) throw new Error(`${where}: ${a} != ${b}`);
}

if (check) {
    const design = JSON.parse(fs.readFileSync(path.join(KIT_DIR, 'mark-frames.json'), 'utf8'));
    const ours = await generate(false);
    compare(ours.poses, design.poses, 'poses');
    for (const [name, frames] of Object.entries(design.seq)) {
        const mine = ours.seq[name];
        if (!mine) throw new Error(`missing ${name}`);
        if (Math.abs(mine.length - frames.length) > 2) throw new Error(`${name}: ${mine.length} frames, design ${frames.length}`);
        compare(mine[0], frames[0], `${name}[first]`);
        compare(mine[mine.length - 1], frames[frames.length - 1], `${name}[last]`);
    }
    console.log(`design's frames reproduced: ${Object.keys(ours.seq).length} sequences`);
} else {
    const { poses, seq } = await generate(true);
    // Stored compactly: each pose and each sequence's first frame as its
    // fields that differ from rest, every later frame as its fields that
    // differ from the frame before. mark-frames.ts expands them.
    const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
    const diff = (from, to) => Object.fromEntries(Object.entries(to).filter(([k, v]) => !same(v, from[k])));
    const out = {
        fps: 12, box: 19,
        fit: 'scale(18.215/196.5) translate(-67,-68): kit units to the 19-unit logo box',
        note: "Made by scripts/mark-frames.mjs from design's mark-kit.js, with the rest S redrawn to the logo (rule 24). Each frame is the whole state of the mark, 1/12 s apart; stored as changes from rest (poses, first frames) and from the frame before.",
        rest: poses.rest,
        poses: Object.fromEntries(Object.entries(poses).filter(([k]) => k !== 'rest').map(([k, v]) => [k, diff(poses.rest, v)])),
        seq: Object.fromEntries(Object.entries(seq).map(([k, frames]) => [k, frames.map((f, i) => diff(i ? frames[i - 1] : poses.rest, f))])),
    };
    fs.writeFileSync(OUT, JSON.stringify(out) + '\n');
    console.log(`wrote ${path.relative(process.cwd(), OUT)}: ${Object.keys(seq).length} sequences`);
}
