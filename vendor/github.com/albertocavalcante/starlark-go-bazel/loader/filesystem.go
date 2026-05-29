// Package loader provides filesystem abstractions and .bzl module loading.
//
// This package implements the loading of Starlark .bzl files, following
// the semantics of Bazel's BzlLoadFunction.java and BzlLoadValue.java.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/skyframe/BzlLoadFunction.java
package loader

import (
	"io/fs"
	"path/filepath"
	"time"
)

// MemoryFileSystem implements FileSystem using an in-memory map.
// Useful for WASM environments and testing.
type MemoryFileSystem struct {
	files map[string][]byte
}

// NewMemoryFileSystem creates an empty in-memory filesystem.
func NewMemoryFileSystem() *MemoryFileSystem {
	return &MemoryFileSystem{
		files: make(map[string][]byte),
	}
}

// AddFile adds a file to the in-memory filesystem.
func (f *MemoryFileSystem) AddFile(path string, content []byte) {
	f.files[path] = content
}

// ReadFile reads a file from memory.
func (f *MemoryFileSystem) ReadFile(path string) ([]byte, error) {
	content, ok := f.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
	}
	return content, nil
}

// Stat returns file info for an in-memory file.
func (f *MemoryFileSystem) Stat(path string) (fs.FileInfo, error) {
	content, ok := f.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return &memFileInfo{
		name: filepath.Base(path),
		size: int64(len(content)),
	}, nil
}

// Glob matches files in the in-memory filesystem.
// Supports basic glob patterns.
func (f *MemoryFileSystem) Glob(pattern string) ([]string, error) {
	var matches []string
	for path := range f.files {
		match, err := filepath.Match(pattern, path)
		if err != nil {
			return nil, err
		}
		if match {
			matches = append(matches, path)
		}
	}
	return matches, nil
}

// memFileInfo implements fs.FileInfo for in-memory files.
type memFileInfo struct {
	name string
	size int64
}

func (fi *memFileInfo) Name() string       { return fi.name }
func (fi *memFileInfo) Size() int64        { return fi.size }
func (fi *memFileInfo) Mode() fs.FileMode  { return 0644 }
func (fi *memFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *memFileInfo) IsDir() bool        { return false }
func (fi *memFileInfo) Sys() any           { return nil }

// Join implements FileSystem.Join for MemoryFileSystem.
func (f *MemoryFileSystem) Join(elem ...string) string {
	return filepath.Join(elem...)
}

// Abs implements FileSystem.Abs for MemoryFileSystem.
func (f *MemoryFileSystem) Abs(path string) (string, error) {
	// In-memory paths are already "absolute" in the sense that they're complete
	return path, nil
}
