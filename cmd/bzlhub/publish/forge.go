package publish

import (
	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bzlhub/cmd/bzlhub/forge"
	"github.com/albertocavalcante/bzlhub/internal/version"
)

// buildForge constructs a bigorna.Forge from the resolved config.
func buildForge(cfg publishConfig) (bigorna.Forge, error) {
	return forge.New(cfg.forge, cfg.repo, cfg.baseURL, cfg.token, "canopy/"+version.Version)
}
