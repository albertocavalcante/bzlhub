package types

import (
	"fmt"

	"go.starlark.net/starlark"
)

// TagClass is the captured definition of a tag_class() — the schema
// for one named tag in a module_extension. Lightweight: just an attr
// schema + doc string. Tag instances themselves are concrete struct
// values constructed at MODULE.bazel evaluation time.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/TagClass.java
type TagClass struct {
	name   string
	attrs  map[string]starlark.Value
	doc    string
	frozen bool
}

var _ starlark.Value = (*TagClass)(nil)

func NewTagClass(attrs map[string]starlark.Value, doc string) *TagClass {
	return &TagClass{attrs: attrs, doc: doc}
}

func (t *TagClass) String() string {
	if t.name != "" {
		return fmt.Sprintf("<tag_class %s>", t.name)
	}
	return "<tag_class>"
}

func (t *TagClass) Type() string                       { return "tag_class" }
func (t *TagClass) Freeze()                            { t.frozen = true }
func (t *TagClass) Truth() starlark.Bool               { return true }
func (t *TagClass) Hash() (uint32, error)              { return 0, fmt.Errorf("unhashable: tag_class") }
func (t *TagClass) Name() string                       { return t.name }
func (t *TagClass) SetName(name string)                { t.name = name }
func (t *TagClass) Attrs() map[string]starlark.Value   { return t.attrs }
func (t *TagClass) Doc() string                        { return t.doc }
