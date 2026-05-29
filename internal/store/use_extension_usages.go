package store

import (
	"context"
	"fmt"
)

// UseExtensionUsage is one stored row of the cross-module
// use_extension index. See schema.sql for the table's role.
type UseExtensionUsage struct {
	ConsumerModule  string
	ConsumerVersion string
	ExtensionFile   string
	ExtensionName   string
	TagIndex        int
	TagName         string
	TagAttrsJSON    string // raw JSON; consumers decode as needed
	DevDependency   bool
	Isolate         bool
}

// WriteUseExtensionUsages replaces all stored use_extension usages
// for (consumerModule, consumerVersion) atomically. Empty usages is
// valid — clears prior rows for that consumer (typical when re-ingest
// finds the consumer no longer uses any extensions).
func (s *Store) WriteUseExtensionUsages(ctx context.Context, consumerModule, consumerVersion string, usages []UseExtensionUsage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM module_extension_usages WHERE consumer_module = ? AND consumer_version = ?`,
		consumerModule, consumerVersion,
	); err != nil {
		return fmt.Errorf("delete prior usages: %w", err)
	}

	for _, u := range usages {
		dev := 0
		if u.DevDependency {
			dev = 1
		}
		iso := 0
		if u.Isolate {
			iso = 1
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO module_extension_usages
                (consumer_module, consumer_version, extension_file, extension_name,
                 tag_index, tag_name, tag_attrs_json, dev_dependency, isolate)
            VALUES (?,?,?,?,?,?,?,?,?)`,
			consumerModule, consumerVersion,
			u.ExtensionFile, u.ExtensionName,
			u.TagIndex, u.TagName, u.TagAttrsJSON,
			dev, iso,
		); err != nil {
			return fmt.Errorf("insert usage %s.%s[%d]: %w",
				u.ExtensionFile, u.ExtensionName, u.TagIndex, err)
		}
	}
	return tx.Commit()
}

// GetUseExtensionUsagesForExtension returns every stored tag invocation
// across the corpus for the named extension. Ordered by
// (consumer_module, consumer_version, tag_index) so a caller iterating
// gets deterministic ModuleSpec construction order.
func (s *Store) GetUseExtensionUsagesForExtension(ctx context.Context, extensionFile, extensionName string) ([]UseExtensionUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT consumer_module, consumer_version, extension_file, extension_name,
               tag_index, tag_name, tag_attrs_json, dev_dependency, isolate
          FROM module_extension_usages
         WHERE extension_file = ? AND extension_name = ?
         ORDER BY consumer_module, consumer_version, tag_index`,
		extensionFile, extensionName,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []UseExtensionUsage
	for rows.Next() {
		var u UseExtensionUsage
		var dev, iso int
		if err := rows.Scan(&u.ConsumerModule, &u.ConsumerVersion,
			&u.ExtensionFile, &u.ExtensionName,
			&u.TagIndex, &u.TagName, &u.TagAttrsJSON,
			&dev, &iso); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		u.DevDependency = dev != 0
		u.Isolate = iso != 0
		out = append(out, u)
	}
	return out, rows.Err()
}
