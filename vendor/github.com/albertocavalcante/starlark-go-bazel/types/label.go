// Package types provides core Starlark types for Bazel's dialect.
package types

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Label represents a Bazel label like //pkg:target or @repo//pkg:target.
type Label struct {
	repo   string // Repository name (empty for main repo)
	pkg    string // Package path
	name   string // Target name
	frozen bool
}

var (
	_ starlark.Value      = (*Label)(nil)
	_ starlark.HasAttrs   = (*Label)(nil)
	_ starlark.Comparable = (*Label)(nil)
)

// NewLabel creates a Label from components.
func NewLabel(repo, pkg, name string) *Label {
	return &Label{repo: repo, pkg: pkg, name: name}
}

// ParseLabel parses a label string like "//pkg:target" or "@repo//pkg:target".
func ParseLabel(s string) (*Label, error) {
	l := &Label{}

	// Handle repository prefix
	if strings.HasPrefix(s, "@") {
		idx := strings.Index(s, "//")
		if idx == -1 {
			return nil, fmt.Errorf("invalid label %q: missing //", s)
		}
		l.repo = s[1:idx]
		s = s[idx:]
	}

	// Must start with //
	if !strings.HasPrefix(s, "//") {
		return nil, fmt.Errorf("invalid label %q: must start with // or @", s)
	}
	s = s[2:]

	// Split package and target
	if idx := strings.LastIndex(s, ":"); idx != -1 {
		l.pkg = s[:idx]
		l.name = s[idx+1:]
	} else {
		// No colon means target name equals last component of package
		l.pkg = s
		if idx := strings.LastIndex(s, "/"); idx != -1 {
			l.name = s[idx+1:]
		} else {
			l.name = s
		}
	}

	return l, nil
}

// String returns the Starlark representation.
func (l *Label) String() string {
	var sb strings.Builder
	if l.repo != "" {
		sb.WriteString("@")
		sb.WriteString(l.repo)
	}
	sb.WriteString("//")
	sb.WriteString(l.pkg)
	sb.WriteString(":")
	sb.WriteString(l.name)
	return sb.String()
}

// Type returns "Label".
func (l *Label) Type() string { return "Label" }

// Freeze marks the label as frozen.
func (l *Label) Freeze() { l.frozen = true }

// Truth returns true (labels are always truthy).
func (l *Label) Truth() starlark.Bool { return true }

// Hash returns a hash for the label.
func (l *Label) Hash() (uint32, error) {
	return starlark.String(l.String()).Hash()
}

// CompareSameType implements comparison.
func (l *Label) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	other := y.(*Label)
	cmp := strings.Compare(l.String(), other.String())
	switch op {
	case syntax.EQL:
		return cmp == 0, nil
	case syntax.NEQ:
		return cmp != 0, nil
	case syntax.LT:
		return cmp < 0, nil
	case syntax.LE:
		return cmp <= 0, nil
	case syntax.GT:
		return cmp > 0, nil
	case syntax.GE:
		return cmp >= 0, nil
	default:
		return false, fmt.Errorf("unsupported comparison: %s", op)
	}
}

// Attr returns an attribute of the label.
func (l *Label) Attr(name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(l.name), nil
	case "package":
		return starlark.String(l.pkg), nil
	case "workspace_name":
		return starlark.String(l.repo), nil
	case "workspace_root":
		if l.repo == "" {
			return starlark.String(""), nil
		}
		return starlark.String("external/" + l.repo), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("Label has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (l *Label) AttrNames() []string {
	return []string{"name", "package", "workspace_name", "workspace_root"}
}

// Repo returns the repository name.
func (l *Label) Repo() string { return l.repo }

// Pkg returns the package path.
func (l *Label) Pkg() string { return l.pkg }

// Name returns the target name.
func (l *Label) Name() string { return l.name }

// LabelBuiltin is the Label() constructor for Starlark.
func LabelBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackArgs("Label", args, kwargs, "label", &s); err != nil {
		return nil, err
	}
	return ParseLabel(s)
}

// ParseLabelRelative parses a label string relative to a given package context.
// It handles:
//   - Absolute labels: "//pkg:target" or "@repo//pkg:target"
//   - Package-relative labels: ":target" -> "//currentPkg:target"
//   - Bare target names: "target" -> "//currentPkg:target"
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/cmdline/Label.java
func ParseLabelRelative(s string, currentRepo string, currentPkg string) (*Label, error) {
	// Handle absolute labels with repo
	if strings.HasPrefix(s, "@") {
		return ParseLabel(s)
	}

	// Handle absolute labels without repo
	if strings.HasPrefix(s, "//") {
		l, err := ParseLabel(s)
		if err != nil {
			return nil, err
		}
		// Use current repo if not specified
		if l.repo == "" {
			l.repo = currentRepo
		}
		return l, nil
	}

	// Handle package-relative labels (":target")
	if strings.HasPrefix(s, ":") {
		return &Label{
			repo: currentRepo,
			pkg:  currentPkg,
			name: s[1:],
		}, nil
	}

	// Handle bare target names ("target")
	// These are relative to the current package
	return &Label{
		repo: currentRepo,
		pkg:  currentPkg,
		name: s,
	}, nil
}
