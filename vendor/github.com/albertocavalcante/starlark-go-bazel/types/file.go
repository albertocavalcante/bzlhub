// Package types provides core Starlark types for Bazel's dialect.
//
// File implementation based on:
// - bazel/src/main/java/com/google/devtools/build/lib/actions/Artifact.java
// - bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/FileApi.java
package types

import (
	"fmt"
	"path"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// FileRoot represents a root directory for files.
// Based on: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/FileRootApi.java
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

// String returns the Starlark representation.
func (r *FileRoot) String() string {
	return fmt.Sprintf("<root %s>", r.path)
}

// Type returns "root".
func (r *FileRoot) Type() string { return "root" }

// Freeze marks the root as frozen.
func (r *FileRoot) Freeze() { r.frozen = true }

// Truth returns true.
func (r *FileRoot) Truth() starlark.Bool { return true }

// Hash returns an error.
func (r *FileRoot) Hash() (uint32, error) {
	return starlark.String(r.path).Hash()
}

// Attr returns an attribute of the root.
// Reference: FileRootApi.java - getExecPathString()
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

// ExecPathString returns the execution path string.
func (r *FileRoot) ExecPathString() string { return r.path }

// File represents a file in the Bazel build system.
// This can be either a source file or a generated (derived) artifact.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/actions/Artifact.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/FileApi.java
type File struct {
	// path is the execution path (relative to execroot)
	// From Artifact.java: execPath field
	path string

	// shortPath is the runfiles-relative path (also called root-relative path)
	// From Artifact.java: getRootRelativePath() / getRunfilesPath()
	shortPath string

	// root is the artifact root
	// From Artifact.java: root field (ArtifactRoot)
	root *FileRoot

	// owner is the label that created this file
	// From Artifact.java: getOwnerLabel()
	owner *Label

	// isSource indicates whether this is a source file
	// From Artifact.java: isSourceArtifact()
	isSource bool

	// isDirectory indicates whether this is a tree artifact (directory)
	// From Artifact.java: isTreeArtifact() / isDirectory()
	isDir bool

	// isSymlink indicates if this was declared as a symlink
	// From Artifact.java: isSymlink()
	isSymlink bool

	frozen bool
}

var (
	_ starlark.Value      = (*File)(nil)
	_ starlark.HasAttrs   = (*File)(nil)
	_ starlark.Comparable = (*File)(nil)
)

// NewFile creates a new File.
func NewFile(path, shortPath string, root *FileRoot, owner *Label, isSource bool) *File {
	return &File{
		path:      path,
		shortPath: shortPath,
		root:      root,
		owner:     owner,
		isSource:  isSource,
	}
}

// NewSourceFile creates a new source file.
func NewSourceFile(pkg, name string) *File {
	shortPath := path.Join(pkg, name)
	return &File{
		path:      shortPath,
		shortPath: shortPath,
		root:      NewFileRoot(""),
		isSource:  true,
	}
}

// NewDerivedFile creates a new derived (generated) file.
func NewDerivedFile(rootPath, rootRelativePath string, owner *Label) *File {
	execPath := path.Join(rootPath, rootRelativePath)
	return &File{
		path:      execPath,
		shortPath: rootRelativePath,
		root:      NewFileRoot(rootPath),
		owner:     owner,
		isSource:  false,
	}
}

// NewTreeArtifact creates a new tree artifact (directory).
func NewTreeArtifact(path, shortPath string, root *FileRoot, owner *Label) *File {
	f := NewFile(path, shortPath, root, owner, false)
	f.isDir = true
	return f
}

// String returns the Starlark representation.
// Reference: Artifact.java repr() method
func (f *File) String() string {
	if f.isSource {
		return fmt.Sprintf("<source file %s>", f.shortPath)
	}
	return fmt.Sprintf("<generated file %s>", f.shortPath)
}

// Type returns "File".
func (f *File) Type() string { return "File" }

// Freeze marks the file as frozen.
func (f *File) Freeze() { f.frozen = true }

// Truth returns true.
func (f *File) Truth() starlark.Bool { return true }

// Hash returns the hash of the file path.
func (f *File) Hash() (uint32, error) {
	return starlark.String(f.path).Hash()
}

// Attr returns an attribute of the file.
// Reference: FileApi.java interface methods
func (f *File) Attr(name string) (starlark.Value, error) {
	switch name {
	case "path":
		// From FileApi.java: getExecPathStringForStarlark()
		// "The execution path of this file, relative to the workspace's execution directory"
		return starlark.String(f.path), nil

	case "short_path":
		// From FileApi.java: getRunfilesPathString()
		// "The path of this file relative to its root"
		return starlark.String(f.shortPath), nil

	case "basename":
		// From FileApi.java: getFilename()
		// "The base name of this file"
		return starlark.String(path.Base(f.path)), nil

	case "dirname":
		// From FileApi.java: getDirnameForStarlark()
		// "The name of the directory containing this file"
		dir := path.Dir(f.path)
		if dir == "." {
			dir = ""
		}
		return starlark.String(dir), nil

	case "extension":
		// From FileApi.java: getExtension()
		// "The file extension following (not including) the rightmost period"
		base := path.Base(f.path)
		if idx := strings.LastIndex(base, "."); idx >= 0 {
			return starlark.String(base[idx+1:]), nil
		}
		return starlark.String(""), nil

	case "root":
		// From FileApi.java: getRootForStarlark()
		// "The root beneath which this file resides"
		return f.root, nil

	case "owner":
		// From FileApi.java: getOwnerLabel()
		// "A label of a target that produces this File"
		if f.owner == nil {
			return starlark.None, nil
		}
		return f.owner, nil

	case "is_source":
		// From FileApi.java: isSourceArtifact()
		// "Returns true if this is a source file"
		return starlark.Bool(f.isSource), nil

	case "is_directory":
		// From FileApi.java: isDirectory()
		// "Returns true if this is a directory"
		return starlark.Bool(f.isDir), nil

	case "is_symlink":
		// From FileApi.java: isSymlink()
		// "Returns true if this was declared as a symlink"
		return starlark.Bool(f.isSymlink), nil

	case "tree_relative_path":
		// From FileApi.java: getTreeRelativePathString()
		// Only valid for tree artifact children
		return nil, fmt.Errorf("tree_relative_path not allowed for files that are not tree artifact files")

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
		"is_symlink",
		"owner",
		"path",
		"root",
		"short_path",
	}
}

// CompareSameType implements comparison.
// Reference: Artifact.java compareTo() using EXEC_PATH_COMPARATOR
func (f *File) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	other := y.(*File)
	cmp := strings.Compare(f.path, other.path)
	switch op {
	case syntax.EQL:
		return cmp == 0 && f.root.path == other.root.path, nil
	case syntax.NEQ:
		return cmp != 0 || f.root.path != other.root.path, nil
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

// Path returns the execution path.
func (f *File) Path() string { return f.path }

// ShortPath returns the root-relative path.
func (f *File) ShortPath() string { return f.shortPath }

// Root returns the file root.
func (f *File) Root() *FileRoot { return f.root }

// Owner returns the owner label.
func (f *File) Owner() *Label { return f.owner }

// IsSource returns true if this is a source file.
func (f *File) IsSource() bool { return f.isSource }

// IsDirectory returns true if this is a tree artifact.
func (f *File) IsDirectory() bool { return f.isDir }

// IsSymlink returns true if this was declared as a symlink.
func (f *File) IsSymlink() bool { return f.isSymlink }

// SetDirectory marks this file as a directory (tree artifact).
func (f *File) SetDirectory(isDir bool) {
	if f.frozen {
		return
	}
	f.isDir = isDir
}

// SetSymlink marks this file as a symlink.
func (f *File) SetSymlink(isSymlink bool) {
	if f.frozen {
		return
	}
	f.isSymlink = isSymlink
}

// SymlinkEntry represents a single runfiles symlink.
// Reference: bazel/src/main/java/com/google/devtools/build/lib/analysis/SymlinkEntry.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/SymlinkEntryApi.java
type SymlinkEntry struct {
	// path is the symlink path in the runfiles tree
	path string
	// target is the target file
	target *File
	frozen bool
}

var (
	_ starlark.Value    = (*SymlinkEntry)(nil)
	_ starlark.HasAttrs = (*SymlinkEntry)(nil)
)

// NewSymlinkEntry creates a new SymlinkEntry.
func NewSymlinkEntry(linkPath string, target *File) *SymlinkEntry {
	return &SymlinkEntry{
		path:   linkPath,
		target: target,
	}
}

// String returns the Starlark representation.
// Reference: SymlinkEntry.java repr() method
func (s *SymlinkEntry) String() string {
	return fmt.Sprintf("SymlinkEntry(path = %q, target_file = %s)", s.path, s.target.String())
}

// Type returns "SymlinkEntry".
func (s *SymlinkEntry) Type() string { return "SymlinkEntry" }

// Freeze marks the symlink entry as frozen.
func (s *SymlinkEntry) Freeze() {
	if s.frozen {
		return
	}
	s.frozen = true
	s.target.Freeze()
}

// Truth returns true.
func (s *SymlinkEntry) Truth() starlark.Bool { return true }

// Hash returns an error.
func (s *SymlinkEntry) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: SymlinkEntry")
}

// Attr returns an attribute of the symlink entry.
// Reference: SymlinkEntryApi.java interface methods
func (s *SymlinkEntry) Attr(name string) (starlark.Value, error) {
	switch name {
	case "path":
		// From SymlinkEntryApi.java: getPathString()
		return starlark.String(s.path), nil
	case "target_file":
		// From SymlinkEntryApi.java: getArtifact()
		return s.target, nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("SymlinkEntry has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (s *SymlinkEntry) AttrNames() []string {
	return []string{"path", "target_file"}
}

// PathString returns the symlink path.
func (s *SymlinkEntry) PathString() string { return s.path }

// Target returns the target file.
func (s *SymlinkEntry) Target() *File { return s.target }
