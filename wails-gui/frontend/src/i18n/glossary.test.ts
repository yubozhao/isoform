import { describe, expect, it } from 'vitest';
import { resources } from './index';
import { UNTRANSLATED_TERMS } from './glossary';

// Product and feature names (Isoform, Skill, MCP, Claude Code, Codex) stay in
// English in every locale. When the English value for a key contains one of
// these terms, the Simplified Chinese value must keep the same English term
// (any case) instead of translating it away. See README "Do not translate".

function collectStrings(tree: unknown, prefix: string, out: Record<string, string>): void {
    if (typeof tree === 'string') {
        out[prefix] = tree;
        return;
    }
    if (tree && typeof tree === 'object') {
        for (const [key, value] of Object.entries(tree)) {
            collectStrings(value, prefix ? `${prefix}.${key}` : key, out);
        }
    }
}

function escapeRegExp(text: string): string {
    return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// Whole-word, case-insensitive match, so "Running skill" counts for "Skill"
// while "skills" only counts for the "Skills" entry.
function containsTerm(text: string, term: string): boolean {
    return new RegExp(`\\b${escapeRegExp(term)}\\b`, 'i').test(text);
}

// "skill" is a common noun here, not the feature name: the memory-export
// prompt lists a person's career "skill areas", which correctly translate to
// 技能领域. The glossary protects the Skill feature name only.
const COMMON_NOUN_EXCEPTIONS = new Set([
    'onboarding:memoryImport.extractionPrompt',
]);

describe('glossary terms stay in English', () => {
    it('keeps every glossary term in the zh-CN translation', () => {
        const byLanguage = resources as unknown as Record<string, Record<string, unknown>>;
        const en = byLanguage.en;
        const zh = byLanguage['zh-CN'];
        const offenders: string[] = [];

        for (const namespace of Object.keys(en)) {
            const enStrings: Record<string, string> = {};
            const zhStrings: Record<string, string> = {};
            collectStrings(en[namespace], '', enStrings);
            collectStrings(zh[namespace], '', zhStrings);

            for (const [key, enValue] of Object.entries(enStrings)) {
                if (COMMON_NOUN_EXCEPTIONS.has(`${namespace}:${key}`)) continue;
                for (const term of UNTRANSLATED_TERMS) {
                    if (!containsTerm(enValue, term)) continue;
                    const zhValue = zhStrings[key];
                    if (zhValue === undefined || !containsTerm(zhValue, term)) {
                        offenders.push(
                            `${namespace}:${key} translates "${term}" -> en "${enValue}" / zh-CN "${zhValue ?? '<missing>'}"`,
                        );
                    }
                }
            }
        }

        expect(offenders).toEqual([]);
    });
});
