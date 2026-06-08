// Package artifactory implements an httpstore.Layout against JFrog
// Artifactory's storage API.
//
// Artifactory's "Generic" repository type doesn't expose a directory
// listing as HTML autoindex; instead it provides a structured JSON
// API at /api/storage/<repo>/<path> that returns a children[] array
// with folder vs file discrimination. This package wires go-bcr-
// httpstore's Layout interface to that API so a Backend pointed at
// an Artifactory generic repo can enumerate BCR modules + versions.
//
// # Quickstart
//
//	backend, err := httpstore.New(httpstore.NewOptions{
//	    BaseURL: "https://artifactory.example.com/artifactory",
//	    Auth: httpstore.CustomHeaderAuth{
//	        HeaderName: "X-JFrog-Art-Api",
//	        Value:      os.Getenv("ARTIFACTORY_API_KEY"),
//	    },
//	    HTTP:   &http.Client{Timeout: 30 * time.Second},
//	    Layout: artifactory.New("bcr-mirror"), // your Artifactory repo name
//	})
//
// Then backend.ListModules / ListVersions enumerate via Artifactory's
// API; backend.ReadMetadataJSON / ReadSourceJSON / etc read BCR-shape
// content from the Artifactory repo verbatim (Artifactory serves
// generic-repo content on the bare path, no API prefix needed).
//
// # Auth pairing
//
// Artifactory deployments typically use API-key auth via the
// X-JFrog-Art-Api header — pair with go-bcr-httpstore's
// CustomHeaderAuth{HeaderName: "X-JFrog-Art-Api"} type. Bearer tokens,
// Basic auth (username + password), and reference tokens also work
// via the corresponding httpstore.Auth implementations.
//
// # Why a separate library
//
// JFrog Artifactory's APIs (storage listing, properties, build
// promotion) are vendor-specific surface that doesn't translate to
// other substrates. Keeping this code in its own library — rather
// than as a sub-package of go-bcr-httpstore — preserves httpstore's
// substrate-agnostic posture and lets Artifactory-specific releases
// ship on their own cadence.
//
// # Scope
//
// v0.1.0 ships the listing surface (Layout: ListModules /
// ListVersions). Properties API (?properties=k=v) and build promotion
// API are planned for later slices when canopy production code
// consumes them — landing in v0.2.x or v0.3.x.
package artifactory
