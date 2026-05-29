package types

// AttrDescriptorHolder is implemented by Starlark values that wrap an
// AttrDescriptor — specifically the values returned by `attr.string()`,
// `attr.label_list()`, etc.
//
// External consumers (assay/interp/external when deriving default
// attrs for a default-invocation of a repository_rule) need to reach
// the wrapped descriptor without depending on the concrete impl type,
// which lives in a different package and is unexported. Type-assert
// to this interface instead.
type AttrDescriptorHolder interface {
	Descriptor() *AttrDescriptor
}
