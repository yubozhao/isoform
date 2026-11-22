// @vitest-environment node
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, relative } from 'node:path';
import { describe, expect, it } from 'vitest';
import ts from 'typescript';
import tokens from '../../../../docs/design-system/tokens.json';
import sourceTokens from '../../../../docs/violet/ds/tokens.json';
import { getTheme } from './registry';
import type { TerminalTheme, ThemeId } from './types';

const src = resolve(__dirname, '..');
const css = readFileSync(resolve(src, 'index.css'), 'utf8');
const sourceCSS = readFileSync(resolve(src, '../../../docs/violet/ds/tokens.css'), 'utf8');
const themeIds: ThemeId[] = ['paper', 'onedark'];
const normalize = (value: string) => value.replace(/\s+/g, ' ').trim();

function declarations(body: string): Record<string, string> {
    return Object.fromEntries([...body.matchAll(/(--[\w-]+):\s*(url\(".*?"\)|[^;]+);/gs)]
        .map((match) => [match[1], normalize(match[2])]));
}

// Follow the actual cascade, including inherited root tokens and later overrides.
function palette(stylesheet: string, theme: ThemeId): Record<string, string> {
    const result: Record<string, string> = {};
    for (const [, selectorText, body] of stylesheet.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
        const selectors = selectorText.trim().split(',').map((selector) => selector.trim());
        if (selectors.some((selector) => selector.endsWith('@theme') || selector === ':root' ||
            selector === `[data-theme="${theme}"]` || selector === `:root[data-theme="${theme}"]`)) {
            Object.assign(result, declarations(body));
        }
    }
    return result;
}

function filesUnder(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
        const path = resolve(dir, entry.name);
        return entry.isDirectory() ? filesUnder(path) : [path];
    });
}

// Generated Wails bindings live outside src, but allow their generated subtree
// explicitly if generation moves it here. No authored component is allowlisted.
const generated = /^generated\/wails\//;
const literalColor = /#[\da-f]{3,4}\b|#[\da-f]{6}(?:[\da-f]{2})?\b|\b(?:rgb|hsl)a?\(\s*[\d.]/gi;
const paletteUtility = /\b(?:bg|text|border|ring|fill|stroke|from|via|to|shadow|divide|outline|decoration)-(?:gray|zinc|slate|neutral|stone|red|green|blue|indigo|violet|purple|pink|rose|amber|orange|yellow|emerald|teal|cyan|sky|lime|fuchsia)(?:-\d{2,3})\b|\b(?:bg|text|border|ring|fill|stroke|from|via|to)-(?:black|white)\b/g;

function scanContent(path: string): string {
    const text = readFileSync(path, 'utf8');
    if (!/\.[jt]sx?$/.test(path)) return text;
    // Inspect authored strings and JSX, not comments containing issue numbers.
    const file = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
    const strings: string[] = [];
    const visit = (node: ts.Node) => {
        if (ts.isStringLiteralLike(node) || ts.isTemplateHead(node) ||
            ts.isTemplateMiddle(node) || ts.isTemplateTail(node) || ts.isJsxText(node)) {
            strings.push(node.getText(file));
        }
        ts.forEachChild(node, visit);
    };
    visit(file);
    return strings.join('\n');
}

// Only actual opaque color tokens count as backing, not bg-cover or bg-none.
const backingPalettes = themeIds.map((theme) => palette(css, theme));
const opaqueFillUtilities = new Set(Object.keys(backingPalettes[0])
    .filter((name) => name.startsWith('--color-') && backingPalettes.every((colors) => /^#[\da-f]{6}$/i.test(colors[name] ?? '')))
    .map((name) => name.replace('--color-', 'bg-')));

// Any alpha utility can leak grain, regardless of its token or opacity syntax.
const alphaFill = /\bbg-(?:[\w-]+|\[[^\]]+\])\/(?:\d+(?:\.\d+)?|\[(?:0?\.\d+)\])/g;
const alphaTokenUtilities = new Set(Object.keys(backingPalettes[0])
    .filter((name) => name.startsWith('--color-') && backingPalettes.some((colors) => /^rgba\(/.test(colors[name] ?? '') && rgba(colors[name])[3] < 1))
    .map((name) => name.replace('--color-', 'bg-')));
function alphaFills(text: string): string[] {
    return [...new Set([...text.matchAll(alphaFill)].map((match) => match[0])
        .concat([...text.matchAll(/\bbg-[\w-]+\b/g)].map((match) => match[0]).filter((name) => alphaTokenUtilities.has(name))))];
}
function staticClassText(node: ts.Node | undefined): string {
    if (!node) return '';
    if (ts.isStringLiteralLike(node)) return node.text;
    if (ts.isJsxExpression(node)) return staticClassText(node.expression);
    if (ts.isTemplateExpression(node)) return node.head.text + ' ' + node.templateSpans.map((span) => span.literal.text).join(' ');
    if (ts.isCallExpression(node)) return node.arguments.map(staticClassText).join(' ');
    return '';
}
function alphaBackground(value: string): boolean {
    if (/\bcolor-mix\(/.test(value) && /\btransparent\b/.test(value)) return true;
    const colors = [...value.matchAll(/\b(?:rgba|hsla)\([\s\S]*?[,/]\s*([\d.]+%?)\s*\)/g),
        ...value.matchAll(/\b(?:rgb|hsl)\([\s\S]*?\/\s*([\d.]+%?)\s*\)/g)];
    return colors.some((match) => parseFloat(match[1]) < (match[1].endsWith('%') ? 100 : 1)) ||
        [...alphaTokenUtilities].some((name) => value.includes(`var(${name.replace('bg-', '--color-')})`));
}
function unbackedAlphaFills(text: string): string[] {
    const file = ts.createSourceFile('ground.tsx', text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
    const classAttr = (node: ts.JsxOpeningElement | ts.JsxSelfClosingElement) => node.attributes.properties
        .filter(ts.isJsxAttribute).find((attr) => attr.name.getText(file) === 'className')?.initializer;
    const backgrounds = (node: ts.JsxOpeningElement | ts.JsxSelfClosingElement): string[] => {
        const attr = node.attributes.properties.filter(ts.isJsxAttribute).find((a) => a.name.getText(file) === 'style');
        const expression = attr?.initializer && ts.isJsxExpression(attr.initializer) ? attr.initializer.expression : undefined;
        if (!expression || !ts.isObjectLiteralExpression(expression)) return [];
        return expression.properties.filter(ts.isPropertyAssignment)
            .filter((property) => /^(?:background|backgroundColor)$/.test(property.name.getText(file).replace(/["']/g, '')))
            .map((property) => ts.isStringLiteralLike(property.initializer) ? property.initializer.text : property.initializer.getText(file));
    };
    const paints = (node: ts.JsxOpeningElement | ts.JsxSelfClosingElement) => alphaFills(classAttr(node)?.getText(file) ?? '')
        .concat(backgrounds(node).filter(alphaBackground).map((value) => `style: ${value}`));
    const opaque = (node: ts.JsxOpeningElement | ts.JsxSelfClosingElement) => {
        const classes = staticClassText(classAttr(node));
        if (/\bpatina-ground\b/.test(classes) || paints(node).length) return false;
        return classes.split(/\s+/).some((name) => opaqueFillUtilities.has(name)) ||
            backgrounds(node).some((value) => opaqueFillUtilities.has(value.replace(/^var\(--color-([\w-]+)\)$/, 'bg-$1'))) ||
            /^(DialogContent|PopoverContent|DropdownMenuContent|SheetContent|SelectContent)$/.test(node.tagName.getText(file));
    };
    const violations: string[] = [];
    const visit = (node: ts.Node) => {
        if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
            const attr = classAttr(node);
            const fills = paints(node);
            // A modal scrim deliberately reveals the scene below it. Its actual
            // dialog/card children still need their own opaque panel backing.
            const scrim = node.tagName.getText(file) === 'div' && /\bfixed\s+inset-0\b/.test(staticClassText(attr)) &&
                fills.every((fill) => fill === 'bg-code-bg/60');
            if (fills.length && !scrim) {
                let backed = false;
                for (let parent = node.parent; parent; parent = parent.parent) {
                    if (ts.isJsxElement(parent) && parent.openingElement !== node) {
                        if (/\bpatina-ground\b/.test(staticClassText(classAttr(parent.openingElement)))) break;
                        if (opaque(parent.openingElement)) { backed = true; break; }
                    }
                }
                if (!backed) violations.push(`${node.tagName.getText(file)}: ${fills.join(', ')}`);
            }
        }
        ts.forEachChild(node, visit);
    };
    visit(file);
    return violations;
}

function colorViolations(text: string): string[] {
    // Ignore issue references, not color literals. They occur in copy and fixtures.
    const withoutIssues = text.replace(/\b(?:PR |pr:)#[\da-f]+\b/gi, 'issue');
    return [...withoutIssues.matchAll(literalColor), ...withoutIssues.matchAll(paletteUtility)].map((match) => match[0]);
}

type RGBA = [number, number, number, number];
function rgba(value: string): RGBA {
    if (value.startsWith(String.fromCharCode(35))) {
        const channels = value.slice(1).match(/../g)!.map((part) => parseInt(part, 16));
        return [channels[0], channels[1], channels[2], 1];
    }
    const channels = value.match(/[\d.]+/g)!.map(Number);
    return [channels[0], channels[1], channels[2], channels[3]];
}
function over(top: RGBA, bottom: RGBA): RGBA {
    return [0, 1, 2].map((i) => top[i] * top[3] + bottom[i] * (1 - top[3])).concat(1) as RGBA;
}
function rgbContrast(a: RGBA, b: RGBA): number {
    const luminance = (color: RGBA) => color.slice(0, 3).map((v) => v / 255)
        .map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
        .reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0);
    const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
    return (light + 0.05) / (dark + 0.05);
}

function luminance(hex: string): number {
    const rgb = hex.slice(1).match(/../g)!.map((part) => parseInt(part, 16) / 255)
        .map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4);
    return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
}
function contrast(a: string, b: string): number {
    const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x);
    return (light + 0.05) / (dark + 0.05);
}

// The direction's page, raised, recessed and user-message pairs, plus the other
// background/surface aliases used by shared components and their hover states.
const surfaces = tokens.color.tokens.filter(({ name }) =>
    /^(color-background(?:-accent)?|color-panel(?:-warm|-code|-result|-user)?|color-card-(?:warm|cool)(?:-alt)?|color-composer-bg|color-chip-bg|color-surface-(?:hover|active)|color-active-row-bg)$/.test(name));

describe('Violet source and app parity', () => {
    it('publishes an exact copy of the source JSON', () => {
        expect(tokens).toEqual(sourceTokens);
    });
    for (const theme of themeIds) {
        it(`keeps every color, shadow, spacing, radius and type name in ${theme}`, () => {
            const actual = palette(css, theme);
            const source = palette(sourceCSS, theme);
            for (const token of [...tokens.color.tokens, ...tokens.shadow.tokens]) {
                const expected = token.value[theme].replace(/\{([^}]+)\}/g, 'var(--$1)');
                expect(actual[`--${token.name}`], token.name).toBe(normalize(expected));
                // The supplied CSS retains earlier compatibility chip labels. Tooling
                // JSON is authoritative for those unused app-tint roles.
                if (!/^color-app-.*-text$/.test(token.name)) {
                    expect(source[`--${token.name}`], `source CSS: ${token.name}`).toBe(normalize(expected));
                }
            }
            for (const token of [...tokens.spacing.tokens, ...tokens.radius.tokens]) {
                expect(actual[`--${token.name}`], token.name).toBe(token.value);
            }
            for (const [name, family] of Object.entries(tokens.type.families)) {
                const expanded = actual[`--font-${name}`]?.replace('var(--font-sans)', tokens.type.families.sans);
                expect(expanded, name).toBe(normalize(family));
            }
            for (const group of tokens.type.groups) {
                for (const style of group.styles) {
                    // Tailwind text-* utilities consume sizes; display/portrait
                    // are font shorthands used only by the registered classes.
                    const expected = ['display', 'portrait'].includes(style.name)
                        ? source[`--text-${style.name}`] : style.fontSize;
                    expect(actual[`--text-${style.name}`], style.name).toBe(expected);
                }
            }
        });
        it(`keeps inert texture variables in ${theme}`, () => {
            const actual = palette(css, theme);
            const source = palette(sourceCSS, theme);
            for (const name of Object.keys(source).filter((name) => name.startsWith('--texture-'))) {
                expect(actual[name], name).toBe(source[name]);
            }
        });
        it(`keeps every ground and control flat in ${theme}`, () => {
            const colors = palette(css, theme);
            for (const name of ['grain', 'mottle-hi', 'mottle-lo', 'copper-hi']) {
                expect(colors[`--texture-${name}`]).toBe('none');
            }
            expect(css).not.toMatch(/data:image|background-image:\s*var\(--texture-/);
            for (const surface of ['background', 'panel', 'background-accent']) {
                for (const name of ['ink', 'muted']) {
                    expect(contrast(colors[`--color-${name}`], colors[`--color-${surface}`])).toBeGreaterThanOrEqual(4.5);
                }
            }
        });
        it(`resolves terminal canvas colors from ${theme} tokens`, () => {
            const terminal = getTheme(theme).terminal;
            const color = (name: string) => tokens.color.tokens.find((token) => token.name === `color-${name}`)!.value[theme];
            expect(terminal.background).toBe(color('background'));
            expect(terminal.foreground).toBe(color('ink'));
            expect(terminal.selectionBackground).toBe(color('selection-bg'));
            expect(terminal.red).toBe(color('error'));
            expect(terminal.green).toBe(color('success'));
            expect(terminal.yellow).toBe(color('warning'));
            for (const value of Object.values(terminal)) {
                expect(tokens.color.tokens.some((token) => token.value[theme] === value)).toBe(true);
            }
        });
        it(`keeps all sixteen ANSI text colors readable on the terminal in ${theme}`, () => {
            const terminal = getTheme(theme).terminal;
            const ansi: (keyof TerminalTheme)[] = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
                'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'];
            for (const name of ansi) {
                expect(contrast(terminal[name], terminal.background), `${theme} ANSI ${name}`).toBeGreaterThanOrEqual(4.5);
            }
        });
        it(`keeps selected pill ink and its outline readable in ${theme}`, () => {
            const colors = palette(css, theme);
            const surface = colors['--color-surface-active'];
            expect(contrast(colors['--color-ink'], surface)).toBeGreaterThanOrEqual(4.5);
            // The outline distinguishes selection when the unselected self pill has the same fill.
            expect(contrast(colors['--color-accent'], surface)).toBeGreaterThanOrEqual(3);
        });
        it(`keeps the folded waiting badge on readable Violet copper in ${theme}`, () => {
            const colors = palette(css, theme);
            expect(colors['--color-waiting-badge']).toBe('var(--color-warning)');
            expect(colors['--color-waiting-badge-fg']).toBe('var(--color-background)');
            expect(contrast(colors['--color-background'], colors['--color-warning'])).toBeGreaterThanOrEqual(4.5);
        });
        for (const surface of surfaces) {
            it(`secondary text on ${surface.name} in ${theme} is at least 4.5:1`, () => {
                const actual = palette(css, theme);
                expect(contrast(actual['--color-muted'], actual[`--${surface.name}`])).toBeGreaterThanOrEqual(4.5);
            });
        }
        it(`keeps the primary button foreground above the direction's contrast target in ${theme}`, () => {
            const actual = palette(css, theme);
            expect(contrast(actual['--color-solid-btn-bg'], actual['--color-solid-btn-fg']))
                .toBeGreaterThanOrEqual(4.5);
        });
    }
    it('registers optional display type and confines texture helpers to their intended callers', () => {
        expect(css).toMatch(/\.display\s*\{\s*font: var\(--text-display\); letter-spacing: -0.01em;/);
        expect(css).toMatch(/\.portrait\s*\{\s*font: var\(--text-portrait\)/);
        expect(css).toContain('font-display: optional;');
        expect(css).not.toMatch(/font-display: swap;/);
        expect(css).toContain('.patina-ground, .patina-verdigris { background-image: none; }');
        expect(css).not.toContain('data-texture');
        const expected = {
            'patina-ground': ['App.tsx', 'components/sidebar.tsx', 'components/today/TodayDayStrip.tsx', 'components/today/TodayPage.tsx'],
            'patina-verdigris': [
                'App.tsx',
                'components/composer.tsx',
                'components/confirm-dialog.tsx',
                'components/contacts/contact-editor.tsx',
                'components/memory-dream-dialog.tsx',
                'components/onboarding/components/AgentChoice.tsx',
                'components/permission-question-card.tsx',
                'components/plan-review-card.tsx',
                'components/settings/pages/assistant-page.tsx',
                'components/settings/pages/memory-debug-page.tsx',
                'components/sops/CreateSopDialog.tsx',
                'components/sops/PlaybookDeliveryPanel.tsx',
                'components/sops/PlaybookSuggestionCard.tsx',
                'components/today/BriefAudioPlayer.tsx',
                'components/ui/button.tsx',
            ],
        };
        const callers = filesUnder(src).filter((path) => /\.[jt]sx?$/.test(path) && !path.includes('.test.'));
        for (const [helper, allowed] of Object.entries(expected)) {
            const actual = callers.filter((path) => readFileSync(path, 'utf8').includes(helper))
                .map((path) => relative(src, path)).sort();
            expect(actual, helper).toEqual(allowed);
        }
        for (const [path, tags] of [['App.tsx', ['div', 'main']], ['components/sidebar.tsx', ['aside']], ['components/today/TodayDayStrip.tsx', ['div']], ['components/today/TodayPage.tsx', ['div']]] as const) {
            const source = readFileSync(resolve(src, path), 'utf8');
            const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true);
            const textured: string[] = [];
            const visit = (node: ts.Node) => {
                if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
                    const attr = node.attributes.properties.filter(ts.isJsxAttribute).find((a) => a.name.getText(file) === 'className');
                    if (attr?.getText(file).includes('patina-ground')) textured.push(node.tagName.getText(file));
                }
                ts.forEachChild(node, visit);
            };
            visit(file);
            expect(textured, path).toEqual([...tags]);
        }
    });
    it('starts both WebView documents flat before any renderer or storage read', () => {
        for (const path of ['../index.html', '../../quick-chat-spotlight/frontend/index.html']) {
            const html = readFileSync(resolve(src, path), 'utf8');
            expect(html.match(/<html\b[^>]*>/)?.[0], path).not.toContain('data-texture');
        }
    });
    it('covers native primary actions and retains compatibility class names', () => {
        const native: string[] = [];
        const shared: string[] = [];
        const nonActions: string[] = [];
        for (const path of filesUnder(src).filter((path) => path.endsWith('.tsx') && !path.includes('.test.'))) {
            const source = readFileSync(path, 'utf8');
            const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true);
            const visit = (node: ts.Node) => {
                if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
                    const attr = node.attributes.properties.filter(ts.isJsxAttribute).find((a) => a.name.getText(file) === 'className');
                    const classes = attr?.getText(file) ?? '';
                    if (node.tagName.getText(file) === 'Button') shared.push(relative(src, path));
                    if (classes.includes('bg-solid-btn-bg')) {
                        const tag = node.tagName.getText(file);
                        if (tag === 'button') {
                            native.push(relative(src, path));
                            expect(classes, relative(src, path)).toContain('patina-verdigris');
                        } else if (tag !== 'Button' && tag !== 'span') {
                            nonActions.push(relative(src, path));
                            expect(classes, relative(src, path)).not.toContain('patina-verdigris');
                        }
                    }
                }
                ts.forEachChild(node, visit);
            };
            visit(file);
        }
        // Setup now shares Button instead of duplicating native primary markup.
        // The computer-use setup dialog and its chips went with the bundled driver.
        expect(native.length).toBeGreaterThanOrEqual(16);
        for (const path of ['components/onboarding/models-panel.tsx']) expect(shared).toContain(path);
        expect(native).toContain('components/confirm-dialog.tsx');
        expect(nonActions).toContain('components/sops/CreateSopDialog.tsx');
        expect(nonActions).toContain('components/activity-page.tsx');
    });
    it('keeps main page roots transparent so they expose the shared app ground', () => {
        const routes = [
            ['components/activity-page.tsx', 'ActivityPage'],
            ['components/automations/AutomationDetailPane.tsx', 'AutomationDetailPane'],
            ['components/deliverables/DeliverablesPage.tsx', 'DeliverablesPage'],
            ['components/goals/GoalsPage.tsx', 'GoalsPage'],
            ['components/today/TodayPage.tsx', 'TodayPage'],
            ['components/new-chat-welcome.tsx', 'NewChatWelcome'],
        ];
        for (const [path, name] of routes) {
            const file = ts.createSourceFile(path, readFileSync(resolve(src, path), 'utf8'), ts.ScriptTarget.Latest, true);
            const component = file.statements.find((node): node is ts.FunctionDeclaration => ts.isFunctionDeclaration(node) && node.name?.text === name)!;
            expect(component, path).toBeDefined();
            let roots = 0;
            const visit = (node: ts.Node) => {
                if (ts.isFunctionLike(node) && node !== component) return;
                if (ts.isReturnStatement(node) && node.expression) {
                    let expression = node.expression;
                    while (ts.isParenthesizedExpression(expression)) expression = expression.expression;
                    if (ts.isJsxElement(expression) || ts.isJsxSelfClosingElement(expression)) {
                        roots++;
                        const opening = ts.isJsxElement(expression) ? expression.openingElement : expression;
                        const attr = opening.attributes.properties.filter(ts.isJsxAttribute).find((a) => a.name.getText(file) === 'className');
                        expect(attr?.getText(file) ?? '', path).not.toMatch(/\bbg-(?:background|panel)\b/);
                    }
                }
                ts.forEachChild(node, visit);
            };
            visit(component);
            expect(roots, path).toBeGreaterThan(0);
        }
    });
    it('detects every token alpha syntax through transparent ancestors and accepts opaque backing', () => {
        for (const fill of ['bg-border/30', 'bg-ink/[0.02]', 'bg-warning/50', 'dark:bg-accent-2/[.07]', 'hover:bg-panel/70', 'bg-success-bg']) {
            const unbacked = `<main className="patina-ground bg-background"><div className="flex"><input className="${fill}" /></div></main>`;
            expect(unbackedAlphaFills(unbacked), fill).toHaveLength(1);
            expect(unbackedAlphaFills(`<div className="bg-panel">${unbacked}</div>`), fill).toHaveLength(1);
            expect(unbackedAlphaFills(unbacked.replace('className="flex"', 'className="bg-panel"')), fill).toEqual([]);
            expect(unbackedAlphaFills(unbacked.replace('className="flex"', 'className="bg-cover"')), fill).toHaveLength(1);
        }
    });
    it.each(['bg-[var(--color-border)]/30', 'bg-[var(--color-ink)]/[0.02]'])('rejects arbitrary-value bypass %s', (fill) => {
        const tree = `<main className="patina-ground bg-background"><div className="${fill}" /></main>`;
        expect(unbackedAlphaFills(tree)).toHaveLength(1);
        expect(unbackedAlphaFills(tree.replace('<div className=', '<section className="bg-panel"><div className=').replace('/></main>', '/></section></main>'))).toEqual([]);
    });
    it('rejects classless inline color-mix and rgba backgrounds and recognizes inline opaque backing', () => {
        const rgbaFill = 'rgba(' + [0, 0, 0, 0.02].join(', ') + ')';
        for (const value of ['color-mix(in oklab, var(--color-border) 30%, transparent)', rgbaFill]) {
            const tree = `<main className="patina-ground bg-background"><section><div style={{ backgroundColor: '${value}' }} /></section></main>`;
            expect(unbackedAlphaFills(tree), value).toHaveLength(1);
            expect(unbackedAlphaFills(tree.replace('<section>', '<section style={{ backgroundColor: "var(--color-panel)" }}>')), value).toEqual([]);
        }
    });
    it('gives raised panels over app grounds opaque token backings', () => {
        const groundPanels = [
            'activity-day-view',
            'activity-meeting-notes', 'activity-page', 'call-transcript',
            'entry-content', 'stream-card', 'compaction-banner',
            'composer-suggestions', 'chat-error-preview', 'plan-review-card',
            'permission-question-card', 'first-chat-welcome', 'onboarding/components/AgentChoice',
            'onboarding/models-panel',
        ];
        for (const name of groundPanels) {
            const text = readFileSync(resolve(src, `components/${name}.tsx`), 'utf8');
            expect(unbackedAlphaFills(text), name).toEqual([]);
        }
        for (const name of ['plan-review-card', 'permission-question-card']) {
            const text = readFileSync(resolve(src, `components/${name}.tsx`), 'utf8');
            expect(text, name).toContain('bg-panel overflow-hidden');
        }
        for (const name of ['onboarding/components/AgentChoice']) {
            const text = readFileSync(resolve(src, `components/${name}.tsx`), 'utf8');
            expect(text, name).toContain('border-accent bg-surface-active');
        }
        // Prove the general guard catches the exact reported regressions.
        const activity = readFileSync(resolve(src, 'components/activity-page.tsx'), 'utf8');
        expect(unbackedAlphaFills(activity.replace('rounded-md bg-panel text-ink placeholder:', 'rounded-md bg-border/30 text-ink placeholder:')))
            .toContain('input: bg-border/30');
        const day = readFileSync(resolve(src, 'components/activity-day-view.tsx'), 'utf8');
        expect(day.match(/<ul className="[^"]*bg-panel[^"]*"/g)).toHaveLength(1);
        // Remaining translucent panels sit inside opaque dialogs or panels.
        // Media overlays and hover marks are not raised paper surfaces.
        // The WebKit fixture checks actual ground siblings.
    });
    it('rejects literals and non-token Tailwind palettes throughout authored src', () => {
        const violations = filesUnder(src).filter((path) => /\.(?:[jt]sx?|css|svg|json|md)$/.test(path))
            .filter((path) => path !== resolve(src, 'index.css') && !generated.test(relative(src, path)))
            .flatMap((path) => colorViolations(scanContent(path)).map((value) => `${relative(src, path)}: ${value}`));
        expect(violations).toEqual([]);
    });
    it('keeps essential small labels on the contrast-checked secondary token', () => {
        const violations = filesUnder(src).filter((path) => /\.[jt]sx$/.test(path) && !path.includes('.test.'))
            .filter((path) => /text-\[(?:[0-9]|1[01])(?:\.\d+)?px\][^"'`]*text-(?:subtle|label|faint)|text-(?:subtle|label|faint)[^"'`]*text-\[(?:[0-9]|1[01])(?:\.\d+)?px\]/.test(scanContent(path)))
            .map((path) => relative(src, path));
        expect(violations).toEqual([]);
        expect(css).toContain('--s-muted-soft: var(--color-muted);');
    });
    it('allows faint only on hidden decoration across every authored caller', () => {
        const violations: string[] = [];
        for (const path of filesUnder(src).filter((path) => /\.[jt]sx$/.test(path) && !path.includes('.test.'))) {
            const file = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true);
            const visit = (node: ts.Node) => {
                if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
                    const attrs = node.attributes.properties.filter(ts.isJsxAttribute);
                    const faint = attrs.some((attr) => /text-faint|var\(--color-faint\)/.test(attr.getText(file)));
                    const hidden = attrs.some((attr) => attr.name.getText(file) === 'aria-hidden' &&
                        (!attr.initializer || /^(?:"true"|'true'|\{true\})$/.test(attr.initializer.getText(file))));
                    if (faint && !hidden) violations.push(relative(src, path));
                }
                ts.forEachChild(node, visit);
            };
            visit(file);
        }
        expect(violations).toEqual([]);
    });

    it('keeps every phone and widget role on the published tokens in both themes', () => {
        for (const path of ['ios/Yansu/Theme.swift', 'ios/YansuWidget/WidgetTheme.swift']) {
            const source = readFileSync(resolve(src, '../../..', path), 'utf8');
            const roles = [...source.matchAll(/static let (\w+) = dynamic\(light: (0x[\da-f]+)(?:, ([\d.]+))?, dark: (0x[\da-f]+)(?:, ([\d.]+))?\) \/\/ (color-[\w-]+|acc-bg2)/g)];
            expect(roles.length).toBe(source.split('= dynamic(').length - 1);
            expect(roles.length).toBeGreaterThan(0);
            for (const role of roles) {
                if (role[6] === 'acc-bg2') {
                    expect(role.slice(1, 6)).toEqual(['accentWaiting', '0x4f5ed8', '0.22', '0x7a8aff', '0.26']);
                    continue;
                }
                const token = tokens.color.tokens.find((token) => token.name === role[6]);
                expect(token, role[6]).toBeDefined();
                for (const [theme, hex, alpha] of [['paper', role[2], role[3]], ['onedark', role[4], role[5]]] as const) {
                    const actual = rgba(String.fromCharCode(35) + hex.slice(2));
                    actual[3] = Number(alpha ?? 1);
                    expect(actual, `${path}: ${role[1]} ${theme}`).toEqual(rgba(token!.value[theme]));
                }
            }
        }
    });

    it('keeps both floating tab bars on secondary or selected accent', () => {
        const root = readFileSync(resolve(src, '../../../ios/Yansu/RootView.swift'), 'utf8');
        const narrow = readFileSync(resolve(src, '../../../ios/Yansu/TabRootBar.swift'), 'utf8');
        const wide = readFileSync(resolve(src, '../../../ios/Yansu/DuoWideRootView.swift'), 'utf8');
        expect(root).toContain('.toolbar(.hidden, for: .tabBar)');
        expect(root).toContain('FloatingTabBar(router: router, chat: environment.chat, tasks: environment.tasks)');
        expect(narrow).toContain('selected ? YansuTheme.accent : YansuTheme.secondary');
        expect(wide).toContain('.foregroundStyle(isSelected ? YansuTheme.accent : YansuTheme.secondary)');
    });

    it('keeps phone and widget caption metadata readable at smaller Dynamic Type sizes', () => {
        const smallTertiary = /\.font\(\.caption(?:2)?\b[^\n]*\)[\s\S]{0,180}?\.foregroundStyle\([^\n]*(?:YansuTheme|WidgetTheme)\.tertiary/g;
        // Positive/negative controls ensure caption weight and conditional selection are covered.
        expect('.font(.caption.weight(.medium))\n.foregroundStyle(selected ? YansuTheme.ink : YansuTheme.tertiary)')
            .toMatch(smallTertiary);
        expect('.font(.caption)\n.foregroundStyle(WidgetTheme.tertiary)').toMatch(smallTertiary);
        expect('.font(.caption)\n.foregroundStyle(YansuTheme.secondary)').not.toMatch(smallTertiary);
        const callers = ['ios/Yansu', 'ios/YansuWidget'].flatMap((dir) => filesUnder(resolve(src, '../../..', dir)))
            .filter((path) => path.endsWith('.swift'));
        const violations = callers.flatMap((path) => [...readFileSync(path, 'utf8').matchAll(smallTertiary)]
            .map(() => relative(resolve(src, '../../..'), path)));
        expect(violations).toEqual([]);
    });

    it('keeps screenshot badges opaque and readable over light and dark shots in both themes', () => {
        const path = resolve(src, 'components/activity-page.tsx');
        const file = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true);
        const badges = new Map<string, string>();
        const visit = (node: ts.Node) => {
            if (ts.isJsxElement(node)) {
                const attr = node.openingElement.attributes.properties.filter(ts.isJsxAttribute)
                    .find((attr) => attr.name.getText(file) === 'className');
                const classes = attr?.initializer && ts.isStringLiteral(attr.initializer) ? attr.initializer.text : '';
                if (classes.includes('absolute top-2 left-2')) {
                    for (const key of ['session.usedInTriage', 'session.siblingOfTriage', 'SKIP_REASON_I18N_KEY']) {
                        if (node.getText(file).includes(key)) badges.set(key, classes);
                    }
                }
            }
            ts.forEachChild(node, visit);
        };
        visit(file);
        expect([...badges.keys()].sort()).toEqual(['SKIP_REASON_I18N_KEY', 'session.siblingOfTriage', 'session.usedInTriage']);
        for (const theme of themeIds) {
            const colors = palette(css, theme);
            for (const [name, classes] of badges) {
                const paint = (kind: string) => {
                    const match = classes.match(new RegExp(`(?:^| )${kind}-([a-z][\\w-]*)(?:/(\\d+))?(?= |$)`))!;
                    const color = rgba(colors[`--color-${match[1]}`]);
                    color[3] *= Number(match[2] ?? 100) / 100;
                    return color;
                };
                const bg = paint('bg');
                const fg = paint('text');
                expect(bg[3], `${name}: opaque backing`).toBe(1);
                // Use the actual light raised surface and dark code surface as contrasting screenshots.
                for (const shot of [rgba(palette(css, 'paper')['--color-panel']), rgba(palette(css, 'onedark')['--color-code-bg'])]) {
                    const backing = over(bg, shot);
                    const text = over(fg, backing);
                    expect(rgbContrast(text, backing), `${name} in ${theme}`).toBeGreaterThanOrEqual(4.5);
                }
            }
        }
    });

    it('keeps considered and skipped timeline markers neutral and visible in both themes', () => {
        const path = resolve(src, 'components/activity-page.tsx');
        const file = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true);
        const markers: { kind: string; classes: string }[] = [];
        const visit = (node: ts.Node) => {
            if (ts.isJsxSelfClosingElement(node) && node.tagName.getText(file) === 'button') {
                const attrs = node.attributes.properties.filter(ts.isJsxAttribute);
                const key = attrs.find((attr) => attr.name.getText(file) === 'key')?.initializer?.getText(file) ?? '';
                const attr = attrs.find((attr) => attr.name.getText(file) === 'className');
                const classes = attr?.initializer && ts.isStringLiteral(attr.initializer) ? attr.initializer.text : '';
                for (const kind of ['sibling', 'skipped']) {
                    if (key.includes(`${kind}-`) && classes.includes('absolute top-1/2')) markers.push({ kind, classes });
                }
            }
            ts.forEachChild(node, visit);
        };
        visit(file);
        expect(markers.map((marker) => marker.kind).sort()).toEqual(['sibling', 'skipped']);
        for (const marker of markers) {
            const classes = marker.classes.split(/\s+/);
            expect(classes).toContain(marker.kind === 'sibling' ? 'bg-muted' : 'ring-muted');
            expect(marker.classes).not.toMatch(/(?:bg|ring)-(?:error|warning|success|accent)\b/);
        }
        for (const theme of themeIds) {
            const colors = palette(css, theme);
            for (const surface of ['background', 'panel', 'panel-warm', 'panel-user']) {
                expect(contrast(colors['--color-muted'], colors[`--color-${surface}`]),
                    `${theme} neutral marker on ${surface}`).toBeGreaterThanOrEqual(3);
            }
        }
    });

    it('detects hex, RGB, HSL, arbitrary colors and Tailwind variants, while permitting tokens and issue refs', () => {
        const hash = String.fromCharCode(35);
        expect(colorViolations(`text-[${hash}123456] dark:bg-${'gray'}-500 rgba(${'1,2,3'},0.5) hsl(${'20 30% 40%'})`)).toHaveLength(4);
        expect(colorViolations(`bg-panel text-muted var(--color-ink) PR ${hash}212 pr:${hash}212`)).toEqual([]);
    });
});


describe('Polish 6 interaction roles', () => {
    it('keeps inline links on accent across Today, Chat, Activity, Settings and onboarding', () => {
        expect(readFileSync(resolve(src, 'components/ui/button.tsx'), 'utf8')).toContain("link: 'text-accent underline-offset-4");
        const callers = [
            'components/today/TodayCaptureLine.tsx', 'components/today/TodayBriefSection.tsx',
            'components/main-chat-proactive-card.tsx',
            'components/settings/keep-times-sheet.tsx',
        ];
        for (const path of callers) {
            const source = readFileSync(resolve(src, path), 'utf8');
            const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true);
            const visit = (node: ts.Node) => {
                if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
                    const attrs = node.attributes.properties.filter(ts.isJsxAttribute);
                    const classes = attrs.find((a) => a.name.getText(file) === 'className')?.initializer?.getText(file) ?? '';
                    if (/underline/.test(classes) && ['button', 'Button', 'a'].includes(node.tagName.getText(file))) {
                        expect(classes, path).toMatch(/text-accent(?:-2)?/);
                        expect(classes, path).not.toMatch(/(?:^|[ "'])text-muted(?:[ "']|$)/);
                    }
                }
                ts.forEachChild(node, visit);
            };
            visit(file);
        }
    });
    it('removes the floating new-message action from the stream', () => {
        const stream = readFileSync(resolve(src, 'components/stream-body.tsx'), 'utf8');
        expect(stream).not.toContain('absolute bottom-4 left-1/2');
        expect(stream).not.toContain("t('stream.newMessages')");
    });
    it('keeps only one scope picker and no round overlay alongside the composer', () => {
        const composer = readFileSync(resolve(src, 'components/composer.tsx'), 'utf8');
        expect(composer.match(/\{scopeControl\}/g)).toHaveLength(1);
        expect(composer.indexOf('{scopeControl}')).toBeGreaterThan(composer.indexOf('data-testid="composer-toolbar"'));
        const stream = readFileSync(resolve(src, 'components/stream-body.tsx'), 'utf8');
        expect(stream).not.toMatch(/absolute[^"\n]*rounded-full/);
    });
    it('does not announce page-visible summary and saved-settings state with success toasts', () => {
        const app = readFileSync(resolve(src, 'App.tsx'), 'utf8');
        expect(app).not.toContain('Activity summary for ${label} is ready');
        for (const path of ['components/settings/pages/recording-storage.ts', 'components/settings/keep-times-sheet.tsx']) {
            const source = readFileSync(resolve(src, path), 'utf8');
            expect(source, path).not.toMatch(/toast.success\(t\(["'](?:activityRetention|sensitiveApps|voiceInputApps|storage.screenshots)/);
        }
        const memory = readFileSync(resolve(src, 'components/settings/pages/memory-debug-page.tsx'), 'utf8');
        for (const key of ['embedding.testPassedToast', 'embedding.testFailedToast']) {
            expect(memory).not.toContain(key);
        }
    });
    for (const theme of themeIds) {
        it(`keeps Activity work and resume roles distinct and text readable in ${theme}`, () => {
            const colors = palette(css, theme);
            const activity = readFileSync(resolve(src, 'components/activity-stitched.tsx'), 'utf8');
            expect(activity).toContain("item.interruption ? 'bg-faint' : 'bg-ink'");
            expect(activity).toContain("stretch.interruption ? 'bg-faint' : 'bg-ink'");
            expect(activity).toContain('bg-ink px-1.5 text-[11.5px] text-background');
            expect(new Set(['ink', 'faint', 'accent'].map((name) => colors[`--color-${name}`])).size).toBe(3);
            expect(contrast(colors['--color-ink'], colors['--color-background'])).toBeGreaterThanOrEqual(4.5);
            for (const surface of ['background', 'panel', 'panel-warm']) {
                expect(contrast(colors['--color-accent'], colors[`--color-${surface}`])).toBeGreaterThanOrEqual(4.5);
            }
        });
    }
});
