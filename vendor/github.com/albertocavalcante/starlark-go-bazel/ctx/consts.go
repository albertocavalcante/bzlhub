package ctx

// Bazel ctx attribute and field name constants. These mirror the
// StarlarkRuleContextApi.java surface and are referenced from the
// per-method Attr() switch dispatches.
const (
	attrName       = "name"
	attrPath       = "path"
	attrFiles      = "files"
	attrLabel      = "label"
	attrAttr       = "attr"
	attrStruct     = "struct"
	attrActions    = "actions"
	attrExecutable = "executable"
)
