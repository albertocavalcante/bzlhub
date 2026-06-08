// colour-discipline.test.ts — the UI-side counterpart to bzlhub's
// Go-side egress lint (internal/egress/lint_test.go).
//
// Rule: no raw oklch(...) colour literals outside ui/src/app.css.
// Tokens belong in @theme; components reference them via
// var(--color-<token>) or Tailwind utility classes that resolve
// through @theme.
//
// Why a vitest test instead of stylelint:
//   1. Zero new tooling dep — vitest already runs in CI.
//   2. Self-documenting — the rule, the rationale, and the
//      exemption list (Plan 19 Idea I migration backlog) live in
//      one file.
//
// Exemption list shrinks as cleanup commits land. Final state:
// empty.

import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { describe, test } from 'vitest';

/**
 * Files that STILL contain raw oklch(...) literals, with the
 * rationale + planned cleanup. Each entry retires when the source
 * is migrated to tokens.
 */
const EXEMPTIONS = new Set<string>([
  // External-source palette duplicated across airgap + external
  // pages: 10+ literals for an "external source identity" family
  // (bcr / maven / pypi / npm / go-proxy / github-* / gitlab-*).
  // Migration needs a new --color-source-* family in @theme and
  // a shared helper to eliminate the duplication. Tracked as a
  // separate cleanup commit — too large for Plan 28 M2 PR1.
  //
  // Paths are relative to ui/src/ (the scan root).
  'routes/modules/[name]/[version]/airgap/+page.svelte',
  'routes/modules/[name]/[version]/external/+page.svelte',
  // Consumers page uses three literals at the same hue (0.74 0.18
  // 80 = matches --color-warn). Direct migration available; deferred
  // to keep PR1 scope tight to the tokens M2 needs.
  'routes/modules/[name]/[version]/consumers/[symbol]/+page.svelte',
]);

// Vitest runs from the ui/ working directory, so cwd + 'src' is the
// scan root. Using cwd-relative paths instead of import.meta.url
// because the latter is not a `file:` URL under the jsdom environment.
const SRC_ROOT = path.join(process.cwd(), 'src');

async function walk(dir: string): Promise<string[]> {
  const entries = await readdir(dir, { withFileTypes: true });
  const files: string[] = [];
  for (const e of entries) {
    if (e.name === 'node_modules' || e.name === 'build' || e.name === '.svelte-kit') {
      continue;
    }
    const full = path.join(dir, e.name);
    if (e.isDirectory()) {
      files.push(...(await walk(full)));
    } else if (e.name.endsWith('.svelte') || e.name.endsWith('.ts')) {
      // Skip the @theme file itself — that's where the tokens live.
      if (e.name === 'app.css' || e.name.endsWith('.test.ts')) continue;
      files.push(full);
    }
  }
  return files;
}

// Catches `oklch(<digit>` and `oklch(0.` (number-first literals).
// Does NOT catch `color-mix(in oklch, var(--color-x) ...)` — the
// `oklch` there is the colorspace identifier, not a colour literal.
// That distinction is the whole point: composition over tokens is
// permitted; raw colour decisions in components are not.
const RAW_OKLCH_LITERAL = /oklch\(\s*[0-9.]/;

describe('colour discipline', () => {
  test('no raw oklch(...) literals outside app.css', async () => {
    const files = await walk(SRC_ROOT);
    const violations: string[] = [];

    for (const f of files) {
      const rel = path.relative(SRC_ROOT, f);
      const text = await readFile(f, 'utf8');
      if (!RAW_OKLCH_LITERAL.test(text)) continue;

      if (EXEMPTIONS.has(rel)) {
        // Logged but not failed — tech debt tracked by EXEMPTIONS.
        continue;
      }
      // Capture the matched lines for the failure diagnostic.
      const lines = text.split('\n');
      for (let i = 0; i < lines.length; i++) {
        if (RAW_OKLCH_LITERAL.test(lines[i])) {
          violations.push(`${rel}:${i + 1}  ${lines[i].trim()}`);
        }
      }
    }

    if (violations.length > 0) {
      const detail = violations.map((v) => `  ${v}`).join('\n');
      throw new Error(
        `Found ${violations.length} raw oklch(...) literal(s) outside app.css:\n${detail}\n\n` +
          `Add a token to ui/src/app.css @theme and reference it via var(--color-<name>).\n` +
          `See docs/plans/19-ui-improvements.md §"Idea I" for the token taxonomy.`,
      );
    }
  });
});
