package lockfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// lockfilePermissions is the file permission mode for lockfiles.
// Using 0600 for security (owner read/write only).
const lockfilePermissions = 0o600

// ReadFile reads and parses a lockfile from the given path.
func ReadFile(path string) (*Lockfile, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- intentional file read by caller-provided path
	if err != nil {
		return nil, fmt.Errorf("failed to read lockfile: %w", err)
	}
	return Parse(data)
}

// Parse parses lockfile JSON data.
func Parse(data []byte) (*Lockfile, error) {
	var lf Lockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("failed to parse lockfile JSON: %w", err)
	}

	// Initialize nil maps to empty maps for consistency
	if lf.RegistryFileHashes == nil {
		lf.RegistryFileHashes = make(map[string]*string)
	}
	if lf.SelectedYankedVersions == nil {
		lf.SelectedYankedVersions = make(map[string]string)
	}
	if lf.ModuleExtensions == nil {
		lf.ModuleExtensions = make(map[string]ModuleExtensionEntry)
	}
	if lf.Facts == nil {
		lf.Facts = make(map[string]json.RawMessage)
	}

	// Normalize repo-rule identifier across schema versions:
	//   - v13/v14 emit BzlFile + RuleClassName as separate fields.
	//   - v18+ emits the consolidated RepoRuleID = "<BzlFile>%<RuleClassName>".
	// Synthesize whichever direction is missing so callers can rely
	// on both representations being populated regardless of source
	// version. Split on the LAST '%' since BzlFile may itself contain
	// other characters but never '%' per Bazel's label grammar.
	for _, entry := range lf.ModuleExtensions {
		for _, scope := range entry {
			for repoName, spec := range scope.GeneratedRepoSpecs {
				changed := false
				if spec.RepoRuleID == "" && spec.BzlFile != "" && spec.RuleClassName != "" {
					spec.RepoRuleID = spec.BzlFile + "%" + spec.RuleClassName
					changed = true
				}
				if spec.RepoRuleID != "" && (spec.BzlFile == "" || spec.RuleClassName == "") {
					if i := strings.LastIndexByte(spec.RepoRuleID, '%'); i > 0 && i < len(spec.RepoRuleID)-1 {
						if spec.BzlFile == "" {
							spec.BzlFile = spec.RepoRuleID[:i]
						}
						if spec.RuleClassName == "" {
							spec.RuleClassName = spec.RepoRuleID[i+1:]
						}
						changed = true
					}
				}
				if changed {
					scope.GeneratedRepoSpecs[repoName] = spec
				}
			}
		}
	}

	return &lf, nil
}

// WriteFile writes the lockfile to the given path with deterministic formatting.
func (l *Lockfile) WriteFile(path string) error {
	data, err := l.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, lockfilePermissions)
}

// WriteTo writes the lockfile to the given writer.
func (l *Lockfile) WriteTo(w io.Writer) (int64, error) {
	data, err := l.Marshal()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	return int64(n), err
}

// Marshal serializes the lockfile to JSON with deterministic key ordering.
func (l *Lockfile) Marshal() ([]byte, error) {
	// Use a custom marshaling approach for deterministic output
	return marshalDeterministic(l)
}

// MarshalIndent serializes the lockfile with indentation for readability.
func (l *Lockfile) MarshalIndent(prefix, indent string) ([]byte, error) {
	data, err := l.Marshal()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, data, prefix, indent); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// marshalDeterministic produces JSON with sorted keys for reproducibility.
func marshalDeterministic(l *Lockfile) ([]byte, error) {
	// Create ordered representation
	ordered := orderedLockfile{
		Version:                l.Version,
		RegistryFileHashes:     sortedNullableStringMap(l.RegistryFileHashes),
		SelectedYankedVersions: sortedStringMap(l.SelectedYankedVersions),
		ModuleExtensions:       sortedExtensions(l.ModuleExtensions),
		Facts:                  sortedFacts(l.Facts),
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(ordered); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// orderedLockfile is used for deterministic JSON output.
type orderedLockfile struct {
	Version                int                      `json:"lockFileVersion"`
	RegistryFileHashes     orderedNullableStringMap `json:"registryFileHashes"`
	SelectedYankedVersions orderedStringMap         `json:"selectedYankedVersions"`
	ModuleExtensions       orderedExtensionMap      `json:"moduleExtensions"`
	Facts                  orderedRawMessageMap     `json:"facts"`
}

// orderedNullableStringMap maintains insertion order for JSON marshaling.
type orderedNullableStringMap struct {
	keys   []string
	values map[string]*string
}

func sortedNullableStringMap(m map[string]*string) orderedNullableStringMap {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return orderedNullableStringMap{keys: keys, values: m}
}

func (o orderedNullableStringMap) MarshalJSON() ([]byte, error) {
	if len(o.keys) == 0 {
		return []byte("{}"), nil
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		valJSON, _ := json.Marshal(o.values[k])
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// orderedStringMap maintains insertion order for JSON marshaling.
type orderedStringMap struct {
	keys   []string
	values map[string]string
}

func sortedStringMap(m map[string]string) orderedStringMap {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return orderedStringMap{keys: keys, values: m}
}

func (o orderedStringMap) MarshalJSON() ([]byte, error) {
	if len(o.keys) == 0 {
		return []byte("{}"), nil
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		valJSON, _ := json.Marshal(o.values[k])
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// orderedExtensionMap maintains insertion order for module extensions.
type orderedExtensionMap struct {
	keys   []string
	values map[string]ModuleExtensionEntry
}

func sortedExtensions(m map[string]ModuleExtensionEntry) orderedExtensionMap {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return orderedExtensionMap{keys: keys, values: m}
}

func (o orderedExtensionMap) MarshalJSON() ([]byte, error) {
	if len(o.keys) == 0 {
		return []byte("{}"), nil
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		valJSON, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// orderedRawMessageMap maintains insertion order for facts.
type orderedRawMessageMap struct {
	keys   []string
	values map[string]json.RawMessage
}

func sortedFacts(m map[string]json.RawMessage) orderedRawMessageMap {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return orderedRawMessageMap{keys: keys, values: m}
}

func (o orderedRawMessageMap) MarshalJSON() ([]byte, error) {
	if len(o.keys) == 0 {
		return []byte("{}"), nil
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(o.values[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Exists returns true if a lockfile exists at the given path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DefaultPath returns the default lockfile path relative to a workspace root.
func DefaultPath(workspaceRoot string) string {
	if workspaceRoot == "" {
		return "MODULE.bazel.lock"
	}
	return workspaceRoot + "/MODULE.bazel.lock"
}
