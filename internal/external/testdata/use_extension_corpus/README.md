# `use_extension_corpus`

Snapshots of real-world `MODULE.bazel` files used by
`TestScanUseExtensions_RealCorpus` (see `../../use_extension_scan_test.go`)
to assert the scanner survives upstream syntax variation.

## Sources

| File | Source | Notes |
| --- | --- | --- |
| `rules_go.MODULE.bazel` | https://raw.githubusercontent.com/bazel-contrib/rules_go/master/MODULE.bazel | go SDK extension, gazelle extension, MODULE.tools deps |
| `rules_python.MODULE.bazel` | https://raw.githubusercontent.com/bazelbuild/rules_python/main/MODULE.bazel | python toolchain extension, pip extension, internal_deps |

## Refresh

These are committed verbatim. To refresh against the latest upstream:

```sh
curl -sSfL https://raw.githubusercontent.com/bazel-contrib/rules_go/master/MODULE.bazel \
    > internal/external/testdata/use_extension_corpus/rules_go.MODULE.bazel
curl -sSfL https://raw.githubusercontent.com/bazelbuild/rules_python/main/MODULE.bazel \
    > internal/external/testdata/use_extension_corpus/rules_python.MODULE.bazel
go test ./internal/external/...
```

If the scanner regresses against a refreshed snapshot, that's a real
signal the upstream syntax changed — investigate before just bumping
the snapshot.
