// Package bzl provides a parser for the Bzl dialect (Starlark + Bazel builtins).
//
// This package is automatically synced from bazelbuild/buildtools and is
// optimized for parsing BUILD, .bzl, and MODULE.bazel files. It preserves
// comments in a CST-like manner, attaching them to AST nodes.
//
// Basic usage:
//
//	content := []byte(`load("@rules_go//go:def.bzl", "go_library")`)
//	file, err := bzl.Parse("BUILD.bazel", content)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for _, stmt := range file.Stmt {
//	    // Process statements...
//	}
//
// Source: https://github.com/bazelbuild/buildtools
// License: Apache 2.0
package bzl
