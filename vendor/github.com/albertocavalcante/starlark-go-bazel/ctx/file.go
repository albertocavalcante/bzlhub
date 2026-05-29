// Package ctx provides the Starlark rule context (ctx) object.
//
// This package implements the ctx object passed to rule implementation functions,
// based on Bazel's StarlarkRuleContext.java.
package ctx

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
)

// File represents a Bazel File (artifact) object.
// Source: StarlarkRuleContext.java references Artifact
// See: com.google.devtools.build.lib.actions.Artifact
type File struct {
	path        string // The file path
	root        string // Root directory (bin, genfiles, source)
	isSource    bool   // Whether this is a source file
	isDirectory bool   // Whether this is a directory (tree artifact)
	isSymlink   bool   // Whether this is a symlink
	shortPath   string // Short path for display
	owner       string // Label of owning target
	frozen      bool
}

var (
	_ starlark.Value    = (*File)(nil)
	_ starlark.HasAttrs = (*File)(nil)
)

// NewFile creates a new File.
func NewFile(path, root string, isSource bool) *File {
	return &File{
		path:      path,
		root:      root,
		isSource:  isSource,
		shortPath: path,
	}
}

// NewDeclaredFile creates a new declared output file.
func NewDeclaredFile(path, root string) *File {
	return &File{
		path:      path,
		root:      root,
		isSource:  false,
		shortPath: path,
	}
}

// NewDirectory creates a new declared directory (tree artifact).
func NewDirectory(path, root string) *File {
	return &File{
		path:        path,
		root:        root,
		isSource:    false,
		isDirectory: true,
		shortPath:   path,
	}
}

// NewSymlink creates a new declared symlink.
func NewSymlink(path, root string) *File {
	return &File{
		path:      path,
		root:      root,
		isSource:  false,
		isSymlink: true,
		shortPath: path,
	}
}

// String returns the string representation.
func (f *File) String() string {
	return fmt.Sprintf("<File %s>", f.path)
}

// Type returns "File".
func (f *File) Type() string { return "File" }

// Freeze marks the file as frozen.
func (f *File) Freeze() { f.frozen = true }

// Truth returns true.
func (f *File) Truth() starlark.Bool { return true }

// Hash returns a hash for the file.
func (f *File) Hash() (uint32, error) {
	return starlark.String(f.path).Hash()
}

// Attr returns an attribute of the file.
// Source: FileApi in starlarkbuildapi
func (f *File) Attr(name string) (starlark.Value, error) {
	switch name {
	case "path":
		// The full path to this file, relative to the workspace root
		return starlark.String(filepath.Join(f.root, f.path)), nil
	case "short_path":
		// The short path of this file, relative to its root
		return starlark.String(f.shortPath), nil
	case "basename":
		// The basename of the file
		return starlark.String(filepath.Base(f.path)), nil
	case "dirname":
		// The directory containing the file
		dir := filepath.Dir(f.path)
		if f.root != "" {
			dir = filepath.Join(f.root, dir)
		}
		return starlark.String(dir), nil
	case "extension":
		// The file extension
		ext := filepath.Ext(f.path)
		if len(ext) > 0 && ext[0] == '.' {
			ext = ext[1:]
		}
		return starlark.String(ext), nil
	case "root":
		// The root of the file (ArtifactRoot)
		return &FileRoot{path: f.root}, nil
	case "is_source":
		// Whether this file is a source file
		return starlark.Bool(f.isSource), nil
	case "is_directory":
		// Whether this file is a directory (tree artifact)
		return starlark.Bool(f.isDirectory), nil
	case "owner":
		// The label of the target that owns this file
		if f.owner != "" {
			return starlark.String(f.owner), nil
		}
		return starlark.None, nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("File has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (f *File) AttrNames() []string {
	return []string{
		"basename",
		"dirname",
		"extension",
		"is_directory",
		"is_source",
		"owner",
		"path",
		"root",
		"short_path",
	}
}

// Path returns the full path.
func (f *File) Path() string {
	if f.root != "" {
		return filepath.Join(f.root, f.path)
	}
	return f.path
}

// ShortPath returns the short path.
func (f *File) ShortPath() string { return f.shortPath }

// Basename returns the file basename.
func (f *File) Basename() string { return filepath.Base(f.path) }

// IsSource returns whether this is a source file.
func (f *File) IsSource() bool { return f.isSource }

// IsDirectory returns whether this is a directory (tree artifact).
func (f *File) IsDirectory() bool { return f.isDirectory }

// IsSymlink returns whether this is a symlink.
func (f *File) IsSymlink() bool { return f.isSymlink }

// SetOwner sets the owner label.
func (f *File) SetOwner(owner string) { f.owner = owner }

// FileRoot represents an artifact root (bin, genfiles, source).
// Source: ArtifactRoot in starlarkbuildapi/FileRootApi
type FileRoot struct {
	path   string
	frozen bool
}

var (
	_ starlark.Value    = (*FileRoot)(nil)
	_ starlark.HasAttrs = (*FileRoot)(nil)
)

// NewFileRoot creates a new FileRoot.
func NewFileRoot(path string) *FileRoot {
	return &FileRoot{path: path}
}

// String returns the string representation.
func (r *FileRoot) String() string {
	return fmt.Sprintf("<root %s>", r.path)
}

// Type returns "root".
func (r *FileRoot) Type() string { return "root" }

// Freeze marks the root as frozen.
func (r *FileRoot) Freeze() { r.frozen = true }

// Truth returns true.
func (r *FileRoot) Truth() starlark.Bool { return true }

// Hash returns an error (roots are not hashable).
func (r *FileRoot) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: root")
}

// Attr returns an attribute of the root.
func (r *FileRoot) Attr(name string) (starlark.Value, error) {
	switch name {
	case "path":
		return starlark.String(r.path), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("root has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (r *FileRoot) AttrNames() []string {
	return []string{"path"}
}

// Path returns the root path.
func (r *FileRoot) Path() string { return r.path }

// Runfiles represents a runfiles object.
// Source: StarlarkRuleContext.runfiles() and Runfiles.java
type Runfiles struct {
	files           []*File
	transitiveFiles []*File
	symlinks        map[string]*File
	rootSymlinks    map[string]*File
	frozen          bool
}

var (
	_ starlark.Value    = (*Runfiles)(nil)
	_ starlark.HasAttrs = (*Runfiles)(nil)
)

// NewRunfiles creates a new Runfiles object.
func NewRunfiles() *Runfiles {
	return &Runfiles{
		symlinks:     make(map[string]*File),
		rootSymlinks: make(map[string]*File),
	}
}

// String returns the string representation.
func (r *Runfiles) String() string {
	return fmt.Sprintf("<runfiles files=%d>", len(r.files))
}

// Type returns "runfiles".
func (r *Runfiles) Type() string { return "runfiles" }

// Freeze marks the runfiles as frozen.
func (r *Runfiles) Freeze() { r.frozen = true }

// Truth returns true.
func (r *Runfiles) Truth() starlark.Bool { return true }

// Hash returns an error.
func (r *Runfiles) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: runfiles")
}

// Attr returns an attribute.
func (r *Runfiles) Attr(name string) (starlark.Value, error) {
	switch name {
	case "files":
		items := make([]starlark.Value, len(r.files))
		for i, f := range r.files {
			items[i] = f
		}
		return starlark.NewList(items), nil
	case "empty_filenames":
		return starlark.NewList(nil), nil
	case "symlinks":
		d := starlark.NewDict(len(r.symlinks))
		for k, v := range r.symlinks {
			_ = d.SetKey(starlark.String(k), v)
		}
		return d, nil
	case "root_symlinks":
		d := starlark.NewDict(len(r.rootSymlinks))
		for k, v := range r.rootSymlinks {
			_ = d.SetKey(starlark.String(k), v)
		}
		return d, nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("runfiles has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (r *Runfiles) AttrNames() []string {
	return []string{"empty_filenames", "files", "root_symlinks", "symlinks"}
}

// AddFile adds a file to runfiles.
func (r *Runfiles) AddFile(f *File) {
	r.files = append(r.files, f)
}

// AddTransitiveFile adds a transitive file.
func (r *Runfiles) AddTransitiveFile(f *File) {
	r.transitiveFiles = append(r.transitiveFiles, f)
}

// AddSymlink adds a symlink.
func (r *Runfiles) AddSymlink(path string, f *File) {
	r.symlinks[path] = f
}

// AddRootSymlink adds a root symlink.
func (r *Runfiles) AddRootSymlink(path string, f *File) {
	r.rootSymlinks[path] = f
}

// Merge merges another Runfiles into this one.
func (r *Runfiles) Merge(other *Runfiles) {
	if other == nil {
		return
	}
	r.files = append(r.files, other.files...)
	r.transitiveFiles = append(r.transitiveFiles, other.transitiveFiles...)
	for k, v := range other.symlinks {
		r.symlinks[k] = v
	}
	for k, v := range other.rootSymlinks {
		r.rootSymlinks[k] = v
	}
}

// expandLocation expands $(location ...) patterns in a string.
// Source: StarlarkRuleContext.expandLocation()
func expandLocation(input string, labelMap map[string][]*File) (string, error) {
	result := input

	// Find all $(location ...) and $(locations ...) patterns
	for {
		start := strings.Index(result, "$(location")
		if start == -1 {
			break
		}

		// Find the closing paren
		end := strings.Index(result[start:], ")")
		if end == -1 {
			return "", fmt.Errorf("unmatched parenthesis in $(location ...)")
		}
		end += start

		// Extract the pattern and label
		pattern := result[start : end+1]
		var label string
		var isPlural bool

		if strings.HasPrefix(pattern, "$(locations ") {
			label = strings.TrimSpace(pattern[12 : len(pattern)-1])
			isPlural = true
		} else if strings.HasPrefix(pattern, "$(location ") {
			label = strings.TrimSpace(pattern[11 : len(pattern)-1])
			isPlural = false
		} else {
			// Not a location pattern, skip
			result = result[:start] + result[start+2:]
			continue
		}

		// Look up the label
		files, ok := labelMap[label]
		if !ok {
			return "", fmt.Errorf("label %q not found in location expansion", label)
		}

		if len(files) == 0 {
			return "", fmt.Errorf("label %q has no files", label)
		}

		if !isPlural && len(files) > 1 {
			return "", fmt.Errorf("label %q expands to multiple files, use $(locations ...) instead", label)
		}

		// Build the replacement
		var paths []string
		for _, f := range files {
			paths = append(paths, f.Path())
		}
		replacement := strings.Join(paths, " ")

		result = result[:start] + replacement + result[end+1:]
	}

	return result, nil
}
