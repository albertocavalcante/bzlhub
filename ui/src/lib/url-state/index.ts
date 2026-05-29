/**
 * URL-state binding library.
 *
 * Plan 14's "URL = state" principle: every filter, sort, tab, search
 * box, expansion that the user might want to share lives in the URL.
 * This module owns the read/write contract; per-page consumers wire
 * it via `$state` + `$effect`.
 *
 * Two layers:
 *   - codecs.ts — pure parse/serialize pairs per value type
 *     (string, list, tristate, sort, int)
 *   - url.ts    — readParam / writeParam with history-mode handling
 *
 * Mirrors the API filter-param conventions on the Go side (Plan 14
 * Layer 1, to be landed in follow-up commits). UI URL params and
 * API query params share the same names and serialization — paste
 * a shared URL into curl, get the equivalent JSON.
 */

export {
  stringField,
  stringList,
  tristate,
  boolField,
  intField,
  sortField,
  type Codec,
  type Sort,
  type Tristate,
} from './codecs';

export { readParam, writeParam } from './url';
