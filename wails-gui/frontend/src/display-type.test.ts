// @vitest-environment node
import { readFileSync, readdirSync } from 'node:fs';
import { relative, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import ts from 'typescript';

const src = __dirname;
const css = readFileSync(resolve(src, 'index.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');

function filesUnder(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
        const path = resolve(dir, entry.name);
        return entry.isDirectory() ? filesUnder(path) : [path];
    });
}

describe('PP Mori display scope', () => {
    it('bundles Book 400 normal/italic and SemiBold 600, with optional loading and no local substitution', () => {
        const faces = [...css.matchAll(/@font-face\s*\{([^}]+)\}/g)]
            .map((match) => match[1]).filter((body) => body.includes('"PP Mori"'));
        expect(faces).toHaveLength(3);
        for (const [i, [file, weight, style]] of [['Book', 400, 'normal'], ['BookItalic', 400, 'italic'], ['SemiBold', 600, 'normal']].entries()) {
            expect(faces[i]).toContain(`src: url("./assets/fonts/PPMori-${file}.woff2") format("woff2");`);
            expect(faces[i]).toContain(`font-weight: ${weight};`);
            expect(faces[i]).toContain(`font-style: ${style};`);
            expect(faces[i]).toContain('font-display: optional;');
            expect(faces[i]).not.toContain('local(');
            const bytes = readFileSync(resolve(src, `assets/fonts/PPMori-${file}.woff2`));
            expect(bytes.subarray(0, 4).toString()).toBe('wOF2');
            expect(bytes.length).toBeGreaterThan(1000);
        }
        // Instrument Sans is setup v2's display face only (onboarding/v2/setup-v2.css).
        expect(readdirSync(resolve(src, 'assets/fonts')).filter((name) => name.endsWith('.woff2')).sort())
            .toEqual(['InstrumentSans-Medium.woff2', 'InstrumentSans-SemiBold.woff2', 'PPMori-Book.woff2', 'PPMori-BookItalic.woff2', 'PPMori-SemiBold.woff2']);
        expect(css).not.toContain('PPMori-Regular');
    });

    it('keeps the system UI stack and the exact display and portrait metrics', () => {
        expect(css.match(/--font-sans:\s*([^;]+);/)![1]).not.toMatch(/Mori/);
        expect(css).toContain('--text-display: 400 30px/1.25 var(--font-display);');
        expect(css).toContain('--text-portrait: 400 16px/1.66 var(--font-display);');
        expect(css).toMatch(/\.display\s*\{\s*font: var\(--text-display\); letter-spacing: -0.01em;\s*\}/);
        expect(css).toMatch(/\.portrait\s*\{\s*font: var\(--text-portrait\); letter-spacing: 0;\s*\}/);
        expect(css).toMatch(/\.display b, \.display strong\s*\{\s*font-weight: 600;\s*\}/);
        const consumers = [...css.matchAll(/([^{}]+)\{([^{}]+)\}/g)]
            .filter(([, , body]) => /(?:font|font-family):[^;]*(?:Mori|--font-display|--text-display|--text-portrait)/.test(body))
            .map(([, selector]) => selector.trim());
        expect(consumers).toEqual(['@font-face', '@font-face', '@font-face', '.display', '.portrait']);
    });

    it('allows display classes in exactly four authored elements and no direct font escape', () => {
        const callers: string[] = [];
        const escapes: string[] = [];
        for (const path of filesUnder(src).filter((path) => /\.(tsx?|css)$/.test(path) &&
            !/\.(?:test|spec|fixture)\./.test(path) && !path.endsWith('index.css'))) {
            const text = readFileSync(path, 'utf8');
            const name = relative(src, path);
            if (path.endsWith('.css')) {
                if (/Mori|--font-display|--text-(?:display|portrait)|\.(?:display|portrait)\b/.test(text)) escapes.push(name);
                continue;
            }
            const file = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
            const visit = (node: ts.Node) => {
                if (ts.isStringLiteralLike(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) {
                    if (/Mori|--font-display|--text-(?:display|portrait)/.test(node.text)) escapes.push(name);
                    for (const kind of ['display', 'portrait']) {
                        if (node.text.split(/\s+/).includes(kind)) {
                            // Record actual JSX class declarations, not prose or unrelated variables.
                            const attr = node.parent;
                            if (ts.isJsxAttribute(attr) && attr.name.getText(file) === 'className') {
                                const tag = (attr.parent.parent as ts.JsxOpeningElement).tagName.getText(file);
                                callers.push(`${name}:${tag}.${kind}`);
                            }
                        }
                    }
                }
                ts.forEachChild(node, visit);
            };
            visit(file);
        }
        expect(escapes).toEqual([]);
        expect(callers.sort()).toEqual([
            'components/about-you/portrait.tsx:p.portrait',
            'components/activity-day-view.tsx:p.display',
            'components/onboarding/v3/IntroScreen.tsx:h1.display',
            'components/today/TodayPage.tsx:h1.display',
        ]);
    });
});
