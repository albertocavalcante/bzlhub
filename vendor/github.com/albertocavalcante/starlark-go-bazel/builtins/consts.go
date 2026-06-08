package builtins

// Bazel builtin function names. Centralized to satisfy the
// goconst linter and to give grep a single dispatch table.
const (
	builtinNameRule    = "rule"
	builtinNameAspect  = "aspect"
	builtinNameSelect  = "select"
	builtinNameStruct  = "struct"
	builtinNameDepset  = "depset"
	builtinNameLabel   = "Label"
	builtinKwargDoc    = "doc"
	depsetOrderDefault = "default"
)

// Bazel attribute type names (verbatim as they appear on
// attr.<type>(...) calls and AttrDescriptor.attrType).
const (
	attrTypeBool                 = "bool"
	attrTypeInt                  = "int"
	attrTypeIntList              = "int_list"
	attrTypeLabel                = "label"
	attrTypeLabelKeyedStringDict = "label_keyed_string_dict"
	attrTypeLabelList            = "label_list"
	attrTypeOutput               = "output"
	attrTypeOutputList           = "output_list"
	attrTypeString               = "string"
	attrTypeStringDict           = "string_dict"
	attrTypeStringList           = "string_list"
	attrTypeStringListDict       = "string_list_dict"
)
