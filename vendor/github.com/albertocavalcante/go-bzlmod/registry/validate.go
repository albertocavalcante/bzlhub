package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// FieldError represents a validation failure for a specific field.
type FieldError struct {
	Field   string // Field path (e.g., "maintainers[0].github")
	Message string // Human-readable error message
}

func (e *FieldError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return e.Message
}

// ValidationErrors collects multiple validation errors.
type ValidationErrors struct {
	Errors []*FieldError
}

func (e *ValidationErrors) Error() string {
	if len(e.Errors) == 0 {
		return "validation failed"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d validation errors:", len(e.Errors))
	for _, err := range e.Errors {
		fmt.Fprintf(&b, "\n  - %s", err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying errors for errors.Is/As compatibility.
func (e *ValidationErrors) Unwrap() []error {
	errs := make([]error, len(e.Errors))
	for i, err := range e.Errors {
		errs[i] = err
	}
	return errs
}

// Add appends a validation error.
func (e *ValidationErrors) Add(field, message string) {
	e.Errors = append(e.Errors, &FieldError{Field: field, Message: message})
}

// AddError appends an existing FieldError.
func (e *ValidationErrors) AddError(err *FieldError) {
	e.Errors = append(e.Errors, err)
}

// HasErrors returns true if any errors were collected.
func (e *ValidationErrors) HasErrors() bool {
	return len(e.Errors) > 0
}

// ToError returns nil if no errors, otherwise returns self.
func (e *ValidationErrors) ToError() error {
	if !e.HasErrors() {
		return nil
	}
	return e
}

// Precompiled regex patterns for validation.
var (
	// GitHub username: alphanumeric and hyphens
	githubUsernamePattern = regexp.MustCompile(`^[-a-zA-Z0-9]*$`)

	// Git commit SHA: 40 hex characters
	gitCommitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

	// Date format: YYYY-MM-DD
	datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

	// SRI integrity: algorithm-base64hash
	sriPattern = regexp.MustCompile(`^(sha256|sha384|sha512)-[A-Za-z0-9+/]+=*$`)
)

// Validate checks that the Metadata conforms to BCR schema requirements.
// Returns nil if valid, or ValidationErrors containing all issues found.
func (m *Metadata) Validate() error {
	var errs ValidationErrors

	// Required fields
	if m.Homepage == "" {
		errs.Add("homepage", "required field is missing")
	}

	if len(m.Maintainers) == 0 {
		errs.Add("maintainers", "required field is missing or empty")
	} else {
		for i := range m.Maintainers {
			if err := m.Maintainers[i].validate(fmt.Sprintf("maintainers[%d]", i)); err != nil {
				var ferr *FieldError
				if errors.As(err, &ferr) {
					errs.AddError(ferr)
				}
			}
		}
	}

	if len(m.Repository) == 0 {
		errs.Add("repository", "required field is missing or empty")
	}

	if len(m.Versions) == 0 {
		errs.Add("versions", "required field is missing or empty")
	}

	// Cross-field validation: yanked versions must exist in versions
	for version := range m.YankedVersions {
		if !m.HasVersion(version) {
			errs.Add(
				fmt.Sprintf("yanked_versions[%q]", version),
				"yanked version does not exist in versions list",
			)
		}
	}

	return errs.ToError()
}

// validate checks a single maintainer's fields.
func (m *Maintainer) validate(fieldPrefix string) error {
	// GitHub username pattern validation (if provided)
	if m.GitHub != "" && !githubUsernamePattern.MatchString(m.GitHub) {
		return &FieldError{
			Field:   fieldPrefix + ".github",
			Message: "must contain only alphanumeric characters and hyphens",
		}
	}

	// At least one identifier should be present (not strictly required by schema,
	// but a maintainer with no identifiable info is suspicious)
	if m.GitHub == "" && m.Email == "" && m.Name == "" {
		return &FieldError{
			Field:   fieldPrefix,
			Message: "maintainer should have at least one of: github, email, or name",
		}
	}

	return nil
}

// Validate checks that the Source conforms to BCR schema requirements.
// Returns nil if valid, or ValidationErrors containing all issues found.
func (s *Source) Validate() error {
	var errs ValidationErrors

	if s.IsGitRepository() {
		s.validateGitRepository(&errs)
	} else {
		s.validateArchive(&errs)
	}

	// Common validation
	if s.PatchStrip < 0 {
		errs.Add("patch_strip", "must be non-negative")
	}

	return errs.ToError()
}

// validateArchive validates archive-type source fields.
func (s *Source) validateArchive(errs *ValidationErrors) {
	// Type must be empty or "archive"
	if s.Type != "" && s.Type != "archive" {
		errs.Add("type", fmt.Sprintf("expected 'archive' or empty, got %q", s.Type))
	}

	// Required fields for archive
	if s.URL == "" {
		errs.Add("url", "required field is missing")
	}

	if s.Integrity == "" {
		errs.Add("integrity", "required field is missing")
	} else if !sriPattern.MatchString(s.Integrity) {
		errs.Add("integrity", "must be a valid SRI hash (e.g., 'sha256-...')")
	}

	// Git-specific fields should not be set for archives
	if s.Remote != "" {
		errs.Add("remote", "should not be set for archive type")
	}
	if s.Commit != "" {
		errs.Add("commit", "should not be set for archive type")
	}
	if s.Tag != "" {
		errs.Add("tag", "should not be set for archive type")
	}
}

// validateGitRepository validates git_repository-type source fields.
func (s *Source) validateGitRepository(errs *ValidationErrors) {
	// Required fields for git_repository
	if s.Remote == "" {
		errs.Add("remote", "required field is missing for git_repository")
	}

	// Must have either commit or tag
	if s.Commit == "" && s.Tag == "" {
		errs.Add("commit", "either 'commit' or 'tag' is required for git_repository")
	}

	// Commit format validation
	if s.Commit != "" && !gitCommitPattern.MatchString(s.Commit) {
		errs.Add("commit", "must be a 40-character hex SHA")
	}

	// ShallowSince date format
	if s.ShallowSince != "" && !datePattern.MatchString(s.ShallowSince) {
		errs.Add("shallow_since", "must be in YYYY-MM-DD format")
	}

	// Archive-specific fields should not be set for git
	if s.URL != "" {
		errs.Add("url", "should not be set for git_repository type")
	}
	if s.Integrity != "" {
		errs.Add("integrity", "should not be set for git_repository type")
	}
	if len(s.Overlay) != 0 {
		errs.Add("overlay", "should not be set for git_repository type")
	}
}

// ValidateMetadataJSON validates raw JSON bytes as Metadata.
// This is a convenience function that unmarshals and validates in one step.
func ValidateMetadataJSON(data []byte) (*Metadata, error) {
	var m Metadata
	if err := unmarshalStrict(data, &m); err != nil {
		return nil, &FieldError{Message: fmt.Sprintf("invalid JSON: %v", err)}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// ValidateSourceJSON validates raw JSON bytes as Source.
// This is a convenience function that unmarshals and validates in one step.
func ValidateSourceJSON(data []byte) (*Source, error) {
	var s Source
	if err := unmarshalStrict(data, &s); err != nil {
		return nil, &FieldError{Message: fmt.Sprintf("invalid JSON: %v", err)}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
