package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Validator validates registry JSON data against BCR schema rules.
// This is a zero-dependency implementation that validates the same rules
// as the official BCR JSON schemas.
type Validator struct{}

// NewValidator creates a validator for BCR metadata and source files.
func NewValidator() *Validator {
	return &Validator{}
}

// ValidateMetadata validates JSON data against metadata.json schema rules.
func (v *Validator) ValidateMetadata(data []byte) error {
	var m Metadata
	if err := unmarshalStrict(data, &m); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return m.Validate()
}

// ValidateSource validates JSON data against source.json schema rules.
func (v *Validator) ValidateSource(data []byte) error {
	var s Source
	if err := unmarshalStrict(data, &s); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return s.Validate()
}

// ValidateMetadataStruct validates a Metadata struct.
func (v *Validator) ValidateMetadataStruct(m *Metadata) error {
	return m.Validate()
}

// ValidateSourceStruct validates a Source struct.
func (v *Validator) ValidateSourceStruct(s *Source) error {
	return s.Validate()
}

// unmarshalStrict unmarshals JSON with strict settings (disallow unknown fields).
func unmarshalStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
