package bcrmirror

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// LogChanges returns commits affecting modules/<module>/ in the
// half-open range (fromSHA, toSHA]. Each CommitInfo carries the
// author + time + message + the SUBSET of changed files that fall
// inside modules/<module>/.
//
// Empty result means "no changes affecting this module in the range"
// — the basis for canopy drift's "behind by N" calculation (one
// entry per upstream commit that touched the module).
//
// Both SHAs must be commits already present in the local mirror.
// fromSHA may be empty to mean "from repo birth" (the initial
// commit; full history of this module).
//
// Per-commit change detection is FIRST-PARENT only: for merge
// commits, only changes against the first parent are surfaced.
// This matches BCR's de-facto linear-history convention; histories
// with non-trivial merges may under-report changes here.
//
// Returns a wrapped error when either SHA is unknown or the walk
// fails. Returns ErrInvalidName when module contains unsafe path
// characters.
//
// Mirror must be Open()ed (or freshly Clone()d) before LogChanges.
func (m *Mirror) LogChanges(ctx context.Context, module, fromSHA, toSHA string) ([]CommitInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := m.requireOpenRepo()
	if err != nil {
		return nil, err
	}
	if err := validateModuleName(module); err != nil {
		return nil, err
	}
	if toSHA == "" {
		return nil, fmt.Errorf("bcrmirror.LogChanges: empty toSHA")
	}

	toHash := plumbing.NewHash(toSHA)
	fromHash := plumbing.NewHash(fromSHA) // zero hash when fromSHA == ""

	modulePrefix := "modules/" + module + "/"

	iter, err := repo.Log(&git.LogOptions{From: toHash})
	if err != nil {
		return nil, fmt.Errorf("bcrmirror.LogChanges: open log from %s: %w", toSHA, err)
	}
	defer iter.Close()

	var out []CommitInfo
	// Sentinel sentinel — pointer-equality compared below; see
	// sync.go::countCommitsBetween for the same pattern + rationale.
	stopErr := errors.New("stop")
	walkErr := iter.ForEach(func(c *object.Commit) error {
		if fromSHA != "" && c.Hash == fromHash {
			return stopErr
		}

		files, err := changedFilesUnder(c, modulePrefix)
		if err != nil {
			// Don't abort the whole log on one commit's file
			// resolution error — skip + continue. Operators see
			// the gap in audit logs if they care.
			return nil
		}
		if len(files) == 0 {
			return nil
		}

		out = append(out, CommitInfo{
			SHA:         c.Hash.String(),
			AuthorName:  c.Author.Name,
			AuthorEmail: c.Author.Email,
			AuthorAt:    c.Author.When,
			Message:     c.Message,
			Files:       files,
		})
		return nil
	})
	if walkErr != nil && walkErr != stopErr { //nolint:errorlint // see comment above
		return nil, fmt.Errorf("bcrmirror.LogChanges: walk: %w", walkErr)
	}
	return out, nil
}

// MetadataAt returns modules/<module>/metadata.json content at a
// specific commit SHA, without changing the working tree. Used by
// drift computation: compare current metadata with metadata at the
// sync-runner's last-pulled SHA to derive "what's new upstream?"
//
// sha can be a tag, branch, or full commit hash. Empty sha returns
// an explicit error.
//
// Returns ErrModuleNotFound when the file isn't present at that
// commit.
func (m *Mirror) MetadataAt(ctx context.Context, module, sha string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := m.requireOpenRepo()
	if err != nil {
		return nil, err
	}
	if err := validateModuleName(module); err != nil {
		return nil, err
	}
	if sha == "" {
		return nil, fmt.Errorf("bcrmirror.MetadataAt: empty sha")
	}

	hash, err := repo.ResolveRevision(plumbing.Revision(sha))
	if err != nil {
		return nil, fmt.Errorf("bcrmirror.MetadataAt: resolve %s: %w", sha, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, fmt.Errorf("bcrmirror.MetadataAt: commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("bcrmirror.MetadataAt: tree at %s: %w", hash, err)
	}

	relPath := "modules/" + module + "/metadata.json"
	file, err := tree.File(relPath)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s at %s", ErrModuleNotFound, module, sha)
		}
		return nil, fmt.Errorf("bcrmirror.MetadataAt: lookup %s: %w", relPath, err)
	}

	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("bcrmirror.MetadataAt: read %s: %w", relPath, err)
	}
	return []byte(content), nil
}

// changedFilesUnder returns the changed files in commit c that have
// a path starting with prefix. Uses the diff between the commit and
// its first parent. The first commit (no parent) treats every file
// in its tree as a "change."
func changedFilesUnder(c *object.Commit, prefix string) ([]string, error) {
	var parentTree *object.Tree
	if c.NumParents() > 0 {
		parent, err := c.Parent(0)
		if err != nil {
			return nil, err
		}
		parentTree, err = parent.Tree()
		if err != nil {
			return nil, err
		}
	}
	currentTree, err := c.Tree()
	if err != nil {
		return nil, err
	}

	// First commit: enumerate all files under prefix.
	if parentTree == nil {
		var out []string
		err := currentTree.Files().ForEach(func(f *object.File) error {
			if strings.HasPrefix(f.Name, prefix) {
				out = append(out, f.Name)
			}
			return nil
		})
		return out, err
	}

	// Subsequent commits: diff against parent.
	changes, err := parentTree.Diff(currentTree)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ch := range changes {
		// Pick the To name if non-empty (covers Add + Modify),
		// otherwise From name (covers Delete).
		name := ch.To.Name
		if name == "" {
			name = ch.From.Name
		}
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out, nil
}
