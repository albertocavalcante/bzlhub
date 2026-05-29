// Pure renderer: ModuleDiffReport → PR-body-ready markdown.
//
// Sections are emitted only when non-empty. Lists are kept terse so the
// output drops cleanly into a PR description without wall-of-text.
//
// Format choices:
//   - H2 (##) for the title — most PR templates put the change title at H1
//   - H3 (###) for each surface (bazel_deps, rules, …)
//   - bold counts ("**Added (3):**") so reviewers scan totals quickly
//   - backticks for identifiers, versions, types — disambiguates from prose
//   - sigils mirror the UI (`~` changed, `+` added, `−` removed)

import type { ModuleDiffReport, DiffRules, DiffNames } from './api/types';

export function renderDiffMarkdown(r: ModuleDiffReport): string {
  const out: string[] = [];
  out.push(`## ${r.module} · \`${r.from}\` → \`${r.to}\``);
  out.push('');

  if (r.from_source === 'upstream' || r.to_source === 'upstream') {
    const transient = [
      r.from_source === 'upstream' ? r.from : null,
      r.to_source === 'upstream' ? r.to : null,
    ].filter(Boolean);
    out.push(
      `> _What-if diff: ${transient.length === 1 ? 'version' : 'versions'} \`${transient.join(', ')}\` fetched from upstream; not in the local index._`,
    );
    out.push('');
  }

  if (r.breaking && r.breaking.length > 0) {
    out.push(`### ⚠ Breaking changes (${r.breaking.length})`);
    out.push('_Consumers exercising these surfaces will need code changes to migrate._');
    out.push('');
    for (const f of r.breaking) {
      const sym = f.detail ? `\`${f.symbol}.${f.detail}\`` : `\`${f.symbol}\``;
      out.push(`- **${f.kind}** ${sym} — ${f.reason}`);
    }
    out.push('');
  }

  if (r.compatibility_level) {
    out.push(`### compatibility_level`);
    out.push(
      `**\`L${r.compatibility_level.from}\` → \`L${r.compatibility_level.to}\`** — different compatibility_levels are incompatible in Bazel; expect a hard migration.`,
    );
    out.push('');
  }

  if (r.hermeticity && ((r.hermeticity.added?.length ?? 0) + (r.hermeticity.removed?.length ?? 0)) > 0) {
    out.push(`### hermeticity`);
    if (r.hermeticity.added?.length) out.push(`- **+** ${r.hermeticity.added.map((c) => `\`${c}\``).join(', ')}`);
    if (r.hermeticity.removed?.length) out.push(`- **−** ${r.hermeticity.removed.map((c) => `\`${c}\``).join(', ')}`);
    out.push('');
  }

  const deps = r.bazel_deps;
  if (deps && ((deps.added?.length ?? 0) + (deps.removed?.length ?? 0) + (deps.changed?.length ?? 0)) > 0) {
    out.push(`### bazel_deps`);
    for (const d of deps.changed ?? []) out.push(`- ~ \`${d.name}\` \`${d.from_version}\` → \`${d.to_version}\``);
    for (const d of deps.added ?? []) out.push(`- **+** \`${d.name}@${d.version}\``);
    for (const d of deps.removed ?? []) out.push(`- **−** \`${d.name}@${d.version}\``);
    out.push('');
  }

  appendRulesSection(out, 'rules', r.rules);
  appendRulesSection(out, 'repository_rules', r.repository_rules);

  appendProvidersSection(out, r);
  appendNamesSection(out, 'macros', r.macros);
  appendModExtsSection(out, r);
  appendNamesSection(out, 'aspects', r.aspects);
  appendNamesSection(out, 'toolchains', r.toolchains);

  // Trim trailing blank lines.
  while (out.length > 0 && out[out.length - 1] === '') out.pop();
  return out.join('\n');
}

function appendRulesSection(out: string[], title: string, data: DiffRules | undefined): void {
  if (!data) return;
  const total = (data.added?.length ?? 0) + (data.removed?.length ?? 0) + (data.changed?.length ?? 0);
  if (total === 0) return;
  out.push(`### ${title}`);
  if (data.added?.length) out.push(`**Added (${data.added.length}):** ${data.added.map((n) => `\`${n}\``).join(', ')}`);
  if (data.removed?.length) out.push(`**Removed (${data.removed.length}):** ${data.removed.map((n) => `\`${n}\``).join(', ')}`);
  if (data.changed?.length) {
    out.push('');
    out.push(`**Changed (${data.changed.length}):**`);
    for (const ch of data.changed) {
      const parts: string[] = [];
      if (ch.attrs_added?.length) {
        parts.push(`+${ch.attrs_added.length} attr${ch.attrs_added.length === 1 ? '' : 's'} (${ch.attrs_added.map((a) => `\`${a.name}: ${a.type || 'any'}${a.mandatory ? ' [required]' : ''}\``).join(', ')})`);
      }
      if (ch.attrs_removed?.length) {
        parts.push(`−${ch.attrs_removed.length} attr${ch.attrs_removed.length === 1 ? '' : 's'} (${ch.attrs_removed.map((a) => `\`${a.name}\``).join(', ')})`);
      }
      if (ch.attrs_changed?.length) {
        const details = ch.attrs_changed
          .map((a) => {
            const sub: string[] = [];
            if (a.from_type || a.to_type) sub.push(`type \`${a.from_type || '—'}\`→\`${a.to_type || '—'}\``);
            if (a.from_default !== undefined && a.to_default !== undefined) sub.push(`default \`${a.from_default || '—'}\`→\`${a.to_default || '—'}\``);
            if (a.mandatory_flip) sub.push(`mandatory \`${a.from_mandatory ? 'yes' : 'no'}\`→\`${a.to_mandatory ? 'yes' : 'no'}\``);
            return `\`${a.name}\` (${sub.join('; ')})`;
          })
          .join(', ');
        parts.push(`~${ch.attrs_changed.length} attr${ch.attrs_changed.length === 1 ? '' : 's'}: ${details}`);
      }
      out.push(`- \`${ch.name}\` — ${parts.join(' · ')}`);
    }
  }
  out.push('');
}

function appendProvidersSection(out: string[], r: ModuleDiffReport): void {
  const p = r.providers;
  if (!p) return;
  const total = (p.added?.length ?? 0) + (p.removed?.length ?? 0) + (p.changed?.length ?? 0);
  if (total === 0) return;
  out.push(`### providers`);
  if (p.added?.length) out.push(`**Added (${p.added.length}):** ${p.added.map((n) => `\`${n}\``).join(', ')}`);
  if (p.removed?.length) out.push(`**Removed (${p.removed.length}):** ${p.removed.map((n) => `\`${n}\``).join(', ')}`);
  for (const ch of p.changed ?? []) {
    const parts: string[] = [];
    if (ch.fields_added?.length) parts.push(`+fields: ${ch.fields_added.map((f) => `\`${f}\``).join(', ')}`);
    if (ch.fields_removed?.length) parts.push(`−fields: ${ch.fields_removed.map((f) => `\`${f}\``).join(', ')}`);
    out.push(`- ~ \`${ch.name}\` — ${parts.join(' · ')}`);
  }
  out.push('');
}

function appendNamesSection(out: string[], title: string, data: DiffNames | undefined): void {
  if (!data) return;
  const total = (data.added?.length ?? 0) + (data.removed?.length ?? 0);
  if (total === 0) return;
  out.push(`### ${title}`);
  if (data.added?.length) out.push(`**Added (${data.added.length}):** ${data.added.map((n) => `\`${n}\``).join(', ')}`);
  if (data.removed?.length) out.push(`**Removed (${data.removed.length}):** ${data.removed.map((n) => `\`${n}\``).join(', ')}`);
  out.push('');
}

function appendModExtsSection(out: string[], r: ModuleDiffReport): void {
  const e = r.module_extensions;
  if (!e) return;
  const total = (e.added?.length ?? 0) + (e.removed?.length ?? 0) + (e.changed?.length ?? 0);
  if (total === 0) return;
  out.push(`### module_extensions`);
  out.push(`_use_extension surface — highest-impact change for Bzlmod consumers._`);
  if (e.added?.length) out.push(`**Added (${e.added.length}):** ${e.added.map((n) => `\`${n}\``).join(', ')}`);
  if (e.removed?.length) out.push(`**Removed (${e.removed.length}):** ${e.removed.map((n) => `\`${n}\``).join(', ')}`);
  for (const ch of e.changed ?? []) {
    const parts: string[] = [];
    if (ch.tag_classes_added?.length) parts.push(`+tag_classes: ${ch.tag_classes_added.map((t) => `\`${t}\``).join(', ')}`);
    if (ch.tag_classes_removed?.length) parts.push(`−tag_classes: ${ch.tag_classes_removed.map((t) => `\`${t}\``).join(', ')}`);
    out.push(`- ~ \`${ch.name}\` — ${parts.join(' · ')}`);
  }
  out.push('');
}
