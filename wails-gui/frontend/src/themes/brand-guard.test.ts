// @vitest-environment node
import { readdirSync, readFileSync } from 'node:fs';
import { resolve, relative } from 'node:path';
import { describe, expect, it } from 'vitest';
import retired from '../../../../docs/design-system/patina/tokens.json';
import violet from '../../../../docs/violet/ds/tokens.json';

const root = resolve(__dirname, '../../../..');
function filesUnder(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
        const path = resolve(dir, entry.name);
        return entry.isDirectory() ? (/^(?:node_modules|dist|build|\.build|\.git)$/.test(entry.name) ? [] : filesUnder(path)) : [path];
    });
}
const frontend = ['wails-gui/frontend/src', 'wails-gui/quick-chat-spotlight/frontend/src']
    .flatMap((dir) => filesUnder(resolve(root, dir)))
    .filter((path) => /\.(?:[jt]sx?|css|html|svg|json|md)$/.test(path));
const isTest = (path: string) => /\.(?:test|spec)\.[jt]sx?$/.test(path);
const values = (source: { color: { tokens: { value: Record<string, string> }[] } }) => source.color.tokens.flatMap((token) => Object.values(token.value));
const current = new Set(values(violet).map((value) => value.toLowerCase()));
const retiredHex = new Set(values(retired).filter((value) => /^#[\da-f]{6}$/i.test(value) && !current.has(value.toLowerCase()))
    .map((value) => value.slice(1).toLowerCase()));

function retiredColors(text: string): string[] {
    return [...text.matchAll(/(?:#|0x)([\da-f]{6})(?:[\da-f]{2})?(?![\da-f])/gi)]
        .filter((match) => retiredHex.has(match[1].toLowerCase())).map((match) => match[0]);
}

describe('brand violet regression guards', () => {
    it('rejects retired brand hex in either frontend, native chrome or iOS', () => {
        const native = filesUnder(resolve(root, 'wails-gui')).filter((path) =>
            /\.(?:go|m|h|html)$/.test(path) && !path.includes('/frontend/') && !path.endsWith('_test.go'));
        const ios = filesUnder(resolve(root, 'ios')).filter((path) =>
            /\.(?:swift|json)$/.test(path) && !path.includes('/.build/') && !path.includes('/YansuTests/'));
        const violations = [...frontend, ...native, ...ios].flatMap((path) => retiredColors(readFileSync(path, 'utf8'))
            .map((color) => `${relative(root, path)}: ${color}`));
        expect(violations).toEqual([]);
        // This control makes a vacuous or accidentally empty retired set fail.
        const oldAccent = retired.color.tokens.find((token) => token.name === 'color-accent')!.value.paper;
        expect(retiredColors(oldAccent)).toHaveLength(1);
        expect(retiredColors(oldAccent + '80')).toHaveLength(1);
    });

    it('never reads a retired Texture preference for rendering', () => {
        const sources = frontend.filter((path) => !isTest(path) && !path.endsWith('.md'));
        sources.push(resolve(root, 'wails-gui/host_bridge_spotlight.go'));
        const violations = sources.filter((path) => /yansu\.appearance\.texture|useTexture|applyTextureAttribute|data-texture|general\.texture\./
            .test(readFileSync(path, 'utf8'))).map((path) => relative(root, path));
        expect(violations).toEqual([]);
    });

    it('omits Texture copy in both supported languages', () => {
        for (const locale of ['en', 'zh-CN']) {
            const settings = JSON.parse(readFileSync(resolve(root, `wails-gui/frontend/src/i18n/locales/${locale}/settings.json`), 'utf8'));
            expect(settings.general).not.toHaveProperty('texture');
            expect(JSON.stringify(settings)).not.toMatch(/texture|grain|纹理|颗粒/i);
        }
    });
});
