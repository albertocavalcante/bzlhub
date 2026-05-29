package store

import (
	"context"
	"fmt"
)

// ModuleExtensionSource is one stored .bzl source file from a producer
// ruleset — specifically a file that declares at least one
// module_extension(). Persisted at ingest so the airgap analyzer can
// re-drive the extension at query time with corpus-derived ModuleSpecs
// without re-fetching the producer's tarball.
type ModuleExtensionSource struct {
	File    string // module-relative path, e.g. "go/extensions.bzl"
	Content []byte // raw .bzl bytes
}

// WriteModuleExtensionSources replaces all stored extension-impl
// sources for (module, version) atomically. Empty input clears prior
// rows (typical when the module no longer declares any extensions
// in a re-ingest).
func (s *Store) WriteModuleExtensionSources(ctx context.Context, name, version string, sources []ModuleExtensionSource) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM module_extension_sources WHERE module_name = ? AND version = ?`,
		name, version,
	); err != nil {
		return fmt.Errorf("delete prior sources: %w", err)
	}
	for _, src := range sources {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO module_extension_sources (module_name, version, file, content) VALUES (?,?,?,?)`,
			name, version, src.File, src.Content,
		); err != nil {
			return fmt.Errorf("insert source %s: %w", src.File, err)
		}
	}
	return tx.Commit()
}

// GetModuleExtensionSources returns every stored extension-impl source
// file for (module, version), ordered by file path for deterministic
// callers.
func (s *Store) GetModuleExtensionSources(ctx context.Context, name, version string) ([]ModuleExtensionSource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file, content FROM module_extension_sources
		  WHERE module_name = ? AND version = ?
		  ORDER BY file`,
		name, version,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	var out []ModuleExtensionSource
	for rows.Next() {
		var src ModuleExtensionSource
		if err := rows.Scan(&src.File, &src.Content); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, src)
	}
	return out, rows.Err()
}
