package scipstarlark

// scope is a lexical scope frame in a Starlark file. Scopes nest via parent.
// The top-level scope's parent is nil. Lookup walks parent chain; bind only
// modifies the current frame.
type scope struct {
	parent   *scope
	bindings map[string]string // identifier name -> SCIP symbol
	// localCounter is non-nil only on the top-level scope; child scopes share
	// the same pointer to keep local-symbol IDs unique within a file.
	localCounter *int
}

// newRootScope makes a top-level scope with a shared local-symbol counter.
func newRootScope() *scope {
	counter := 0
	return &scope{
		bindings:     map[string]string{},
		localCounter: &counter,
	}
}

// push returns a child scope whose parent is s.
func (s *scope) push() *scope {
	return &scope{
		parent:       s,
		bindings:     map[string]string{},
		localCounter: s.localCounter,
	}
}

// bind installs (name -> sym) in the current frame.
func (s *scope) bind(name, sym string) {
	s.bindings[name] = sym
}

// lookup returns the symbol bound to name in the innermost scope that has
// it, or "" if none does.
func (s *scope) lookup(name string) string {
	for cur := s; cur != nil; cur = cur.parent {
		if sym, ok := cur.bindings[name]; ok {
			return sym
		}
	}
	return ""
}

// nextLocalID returns a monotonically increasing local-symbol ID unique
// within the file the root scope belongs to.
func (s *scope) nextLocalID() int {
	*s.localCounter++
	return *s.localCounter
}
