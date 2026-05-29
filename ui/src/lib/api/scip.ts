// Parsing helpers for the SCIP symbol string canopy emits.
//
// Wire shape: `bzlmod <module>@<version> <relpath>#<name>`
// Example:    `bzlmod rules_python@0.40.0 python/defs.bzl#py_library`
//
// The backend produces this string + already exposes the parsed parts
// in ConsumersResult / SymbolLookupResult / etc. The parser here is
// for the one case where the UI only has the raw string: a URL path
// parameter on /modules/<m>/<v>/consumers/<symbol>. Everywhere the
// API result is available, prefer those structured fields.

/**
 * ParsedScipSymbol holds the four canopy-defined parts of a SCIP
 * symbol. Null on any malformed input — callers should fall back to
 * showing the raw string.
 */
export interface ParsedScipSymbol {
  module: string;
  version: string;
  file: string;
  name: string;
}

/**
 * parseScipSymbol splits a canopy-emitted SCIP symbol into its
 * four components. Returns null when the input doesn't match the
 * shape — be lenient about whitespace, but require both the
 * `<module>@<version>` and the `<relpath>#<name>` halves.
 */
export function parseScipSymbol(s: string | null | undefined): ParsedScipSymbol | null {
  if (!s) return null;
  // Strip the leading "bzlmod " scheme if present; some callers may
  // pass the bare `<m>@<v> <file>#<name>` half-decoded form.
  const trimmed = s.startsWith('bzlmod ') ? s.slice('bzlmod '.length) : s;
  // Split on the first space: left half is `<module>@<version>`,
  // right half is `<relpath>#<name>`. Either half missing → no parse.
  const spaceIdx = trimmed.indexOf(' ');
  if (spaceIdx <= 0) return null;
  const left = trimmed.slice(0, spaceIdx);
  const right = trimmed.slice(spaceIdx + 1);

  const atIdx = left.indexOf('@');
  if (atIdx <= 0 || atIdx === left.length - 1) return null;
  const module = left.slice(0, atIdx);
  const version = left.slice(atIdx + 1);

  // The relpath itself may contain # (rare but legal in unix paths),
  // so split on the LAST # so `<name>` is the trailing identifier.
  const hashIdx = right.lastIndexOf('#');
  if (hashIdx <= 0 || hashIdx === right.length - 1) return null;
  const file = right.slice(0, hashIdx);
  const name = right.slice(hashIdx + 1);

  return { module, version, file, name };
}
