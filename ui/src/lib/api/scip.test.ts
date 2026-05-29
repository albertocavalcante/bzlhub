import { describe, expect, test } from 'vitest';
import { parseScipSymbol } from './scip';

describe('parseScipSymbol', () => {
  test('canonical shape with bzlmod prefix', () => {
    expect(parseScipSymbol('bzlmod rules_python@0.40.0 python/defs.bzl#py_library')).toEqual({
      module: 'rules_python',
      version: '0.40.0',
      file: 'python/defs.bzl',
      name: 'py_library',
    });
  });

  test('bzlmod prefix is optional (half-decoded form)', () => {
    expect(parseScipSymbol('rules_go@0.50.1 go/def.bzl#go_library')).toEqual({
      module: 'rules_go',
      version: '0.50.1',
      file: 'go/def.bzl',
      name: 'go_library',
    });
  });

  test('4-component canopy variant version', () => {
    expect(parseScipSymbol('bzlmod platforms@0.0.4.1 platforms.bzl#constraint_setting')).toEqual({
      module: 'platforms',
      version: '0.0.4.1',
      file: 'platforms.bzl',
      name: 'constraint_setting',
    });
  });

  test('relpath with multiple # picks the last as name boundary', () => {
    // Pathological but legal: file path containing #.
    expect(parseScipSymbol('bzlmod x@1.0 a#b.bzl#py_test')).toEqual({
      module: 'x',
      version: '1.0',
      file: 'a#b.bzl',
      name: 'py_test',
    });
  });

  test('null / empty / malformed returns null', () => {
    expect(parseScipSymbol(null)).toBeNull();
    expect(parseScipSymbol(undefined)).toBeNull();
    expect(parseScipSymbol('')).toBeNull();
    // Missing version.
    expect(parseScipSymbol('bzlmod rules_go file.bzl#x')).toBeNull();
    // Missing name (trailing #).
    expect(parseScipSymbol('bzlmod x@1 file.bzl#')).toBeNull();
    // Missing module (leading @).
    expect(parseScipSymbol('bzlmod @1.0 file.bzl#x')).toBeNull();
    // No file/name half.
    expect(parseScipSymbol('bzlmod x@1.0')).toBeNull();
    // No # in right half.
    expect(parseScipSymbol('bzlmod x@1.0 file.bzl')).toBeNull();
  });
});
