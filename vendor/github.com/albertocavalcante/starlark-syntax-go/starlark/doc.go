// Package starlark provides the official Google Starlark parser.
//
// This package is automatically synced from google/starlark-go and provides
// full Starlark language parsing with indentation support.
//
// Basic usage:
//
//	content := []byte(`def greet(name): return "Hello, " + name`)
//	file, err := starlark.Parse("example.star", content, 0)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// Source: https://github.com/google/starlark-go
// License: BSD-3-Clause
package starlark
