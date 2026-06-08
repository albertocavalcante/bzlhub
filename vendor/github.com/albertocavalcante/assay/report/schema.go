package report

import _ "embed"

//go:embed schema.json
var schemaJSON []byte

// SchemaJSON returns the embedded JSON Schema (Draft 2020-12) for
// [ModuleReport]. The schema is the contract assay's downstream
// consumers (canopy, MCP tools, CI checks) regenerate TypeScript /
// validate ingested reports against. Hand-maintained at
// report/schema.json; the matching reflect-walk tests in
// report/schema_test.go catch drift between the schema and the Go
// type.
//
// Returned bytes share storage with the embedded file — do not
// mutate.
func SchemaJSON() []byte {
	return schemaJSON
}
