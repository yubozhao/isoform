#!/usr/bin/env node
// Stamps every local CSS and JS reference in the website's pages with
// ?v={first 8 hex of the file's SHA-256}, so a changed file gets a new URL
// and browsers never pair new HTML with a cached old stylesheet.
// Usage: node scripts/website-stamp-assets.mjs [--check]
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const site = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'website');
export const pages = ['index.html', 'about/index.html', 'agents/index.html', '404.html'];
// Server code that writes asset tags into pages it serves (Agent of You chat).
export const fragments = ['functions/_lib/aoy-hire-route.js', 'functions/_lib/aoy-owner-route.js'];
const ref = /\b(href|src)="((?!https?:|\/\/)[^"?#]+\.(?:css|js))(?:\?v=[0-9a-f]*)?"/g;

export function hashOf(file) {
  return createHash('sha256').update(readFileSync(file)).digest('hex').slice(0, 8);
}

export function assetPath(page, url) {
  return url.startsWith('/') ? join(site, url) : join(site, dirname(page), url);
}

export function stamp(page, html) {
  return html.replace(ref, (_, attr, url) => `${attr}="${url}?v=${hashOf(assetPath(page, url))}"`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const check = process.argv.includes('--check');
  let stale = 0;
  for (const page of [...pages, ...fragments]) {
    const file = join(site, page);
    const html = readFileSync(file, 'utf8');
    const next = stamp(page, html);
    if (next === html) continue;
    stale++;
    if (check) console.error(`${page}: asset versions are out of date`);
    else writeFileSync(file, next);
  }
  if (check && stale) process.exit(1);
}
