// Package registry provides types and validation for Bazel Central Registry (BCR) data.
//
// This package implements the JSON schemas used by BCR for module metadata,
// source specifications, and registry configuration. It enables:
//
//   - Type-safe access to registry data with proper validation
//   - JSON Schema validation against official BCR schemas
//   - Interoperability with Bazel's registry protocol
//
// # Registry Structure
//
// A Bazel registry follows a standard layout:
//
//	registry/
//	├── bazel_registry.json       # Registry configuration
//	└── modules/
//	    └── {name}/
//	        ├── metadata.json     # Module metadata (versions, maintainers)
//	        └── {version}/
//	            ├── MODULE.bazel  # Module file
//	            └── source.json   # Source location (archive/git)
//
// # Usage
//
// Fetch and validate module metadata:
//
//	client := registry.NewClient("https://bcr.bazel.build")
//	metadata, err := client.GetMetadata(ctx, "rules_go")
//	if err != nil {
//	    // Handle validation or network errors
//	}
//
// Validate arbitrary JSON against BCR schemas:
//
//	validator := registry.NewValidator()
//	err := validator.ValidateMetadata(jsonData)
package registry
