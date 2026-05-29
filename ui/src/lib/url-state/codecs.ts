/**
 * Codecs for serializing UI state into URL query parameters and back.
 *
 * Each codec is a pair of `parse(raw: string|null) -> T` and
 * `serialize(value: T) -> string|null`. A serialize result of `null`
 * means "omit this key from the URL" — empty/default state keeps the
 * URL clean (no `?class=&host=` cruft, see Plan 14 principle 6).
 *
 * Conventions locked in by docs/plans/14-permalinks.md:
 *   - String lists are comma-separated, not repeated keys.
 *     `?class=a,b` not `?class=a&class=b`.
 *   - Tristate booleans use explicit "only"/"exclude"/absent;
 *     truthy/falsy strings are not accepted.
 *   - Sort fields use `name` (asc default) and `-name` (desc).
 *   - Pagination is 1-based offsets (`?page=2`), not cursors —
 *     stable sort + offset is the v1 trade-off.
 *
 * The same conventions are mirrored on the Go API side (Plan 15
 * landed the path layer; Plan 14 Layer 1 will land matching filter
 * parsers in handlers).
 */

export interface Codec<T> {
  /** Read the raw query-string value (or null when absent). */
  parse: (raw: string | null) => T;
  /** Returns the URL value, or `null` to omit the key entirely. */
  serialize: (value: T) => string | null;
  /** Default value when the key is absent from the URL. */
  defaultValue: T;
}

/** Free-form string (`?q=cc_library`). Empty string → key omitted. */
export const stringField: Codec<string> = {
  parse: (raw) => raw ?? '',
  serialize: (v) => (v === '' ? null : v),
  defaultValue: '',
};

/**
 * Comma-separated list (`?class=github-archive,vendor-http`). Empty
 * array → key omitted. Whitespace around commas is trimmed; empty
 * segments dropped.
 */
export const stringList: Codec<string[]> = {
  parse: (raw) =>
    raw
      ? raw
          .split(',')
          .map((s) => s.trim())
          .filter((s) => s.length > 0)
      : [],
  serialize: (v) => (v.length === 0 ? null : v.join(',')),
  defaultValue: [],
};

/**
 * Tristate filter (`?tainted=only` / `?tainted=exclude` / absent).
 * Invalid values are tolerated as "absent" — we never 4xx a URL.
 */
export type Tristate = 'only' | 'exclude' | null;
export const tristate: Codec<Tristate> = {
  parse: (raw) => (raw === 'only' || raw === 'exclude' ? raw : null),
  serialize: (v) => v,
  defaultValue: null,
};

/**
 * Plain boolean toggle (`?recursive=true` only when set). Default
 * `false` → key omitted. Bare `?recursive` (no value) is also
 * accepted as `true` for ergonomics.
 */
export const boolField: Codec<boolean> = {
  parse: (raw) => raw === 'true' || raw === '',
  serialize: (v) => (v ? 'true' : null),
  defaultValue: false,
};

/**
 * Integer field (`?page=2`). Invalid values fall back to the default.
 */
export function intField(defaultValue: number): Codec<number> {
  return {
    parse: (raw) => {
      if (raw === null) return defaultValue;
      const n = parseInt(raw, 10);
      return Number.isFinite(n) ? n : defaultValue;
    },
    serialize: (v) => (v === defaultValue ? null : String(v)),
    defaultValue,
  };
}

/**
 * Sort direction encoded as `?sort=name` (asc) or `?sort=-name`
 * (desc). Returns a tuple of (field, direction).
 *
 * `defaultField` is the implicit sort when the URL has no `sort=`
 * key; default direction is asc. To make a different default
 * explicit, callers can compare against the returned tuple.
 */
export interface Sort {
  field: string;
  direction: 'asc' | 'desc';
}
export function sortField(defaultField: string, defaultDirection: 'asc' | 'desc' = 'asc'): Codec<Sort> {
  const defaultValue: Sort = { field: defaultField, direction: defaultDirection };
  return {
    parse: (raw) => {
      if (!raw) return defaultValue;
      if (raw.startsWith('-')) return { field: raw.slice(1), direction: 'desc' };
      return { field: raw, direction: 'asc' };
    },
    serialize: (v) => {
      if (v.field === defaultField && v.direction === defaultDirection) return null;
      return v.direction === 'desc' ? `-${v.field}` : v.field;
    },
    defaultValue,
  };
}
