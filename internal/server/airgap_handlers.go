package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/api/paths"
)

func (h *handler) apiGetAirgapDownloaderConfig(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	opts := api.DownloaderConfigOptions{
		MirrorBase: r.URL.Query().Get("mirror"),
		Recursive:  r.URL.Query().Get("recursive") == "true",
	}
	resp, err := h.c.AirgapDownloaderConfig(r.Context(), module, version, opts)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(err.Error(), "mirror:") || strings.HasPrefix(err.Error(), "registry:") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.apiError(w, err)
		return
	}
	// Default to text/plain so curl piped to a file works. Add
	// ?format=json to get the metadata-wrapped JSON shape.
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="canopy-downloader-config-%s-%s.txt"`, module, version))
	_, _ = w.Write([]byte(resp.Text))
}

func (h *handler) apiGetAirgapModuleMirrors(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	opts := api.ModuleMirrorsOptions{
		MirrorBase: r.URL.Query().Get("mirror"),
		Registry:   r.URL.Query().Get("registry"),
	}
	resp, err := h.c.AirgapModuleMirrors(r.Context(), module, version, opts)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(err.Error(), "mirror:") || strings.HasPrefix(err.Error(), "registry:") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.apiError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="canopy-module-mirrors-%s-%s.bazelrc"`, module, version))
	_, _ = w.Write([]byte(resp.Text))
}

func (h *handler) apiGetAirgapSurface(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	resp, err := h.c.AirgapSurface(r.Context(), module, version)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	// Plan 14 Layer 1: same filter contract as the per-module
	// external endpoint, so a UI URL with ?class=/?host=/?tainted=
	// is curl-equivalent for the closure view too. ?module= is
	// accepted but currently a no-op -- see filterClosureRefs.
	filterClosureRefs(resp, r)
	writeJSONWithETag(w, r, http.StatusOK, resp)
}

func (h *handler) apiGetExternalSurface(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	resp, err := h.c.ExternalSurface(r.Context(), module, version)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	// Plan 14 Layer 1: curl-parity with the UI's URL state. Filter
	// params (?class=, ?host=, ?tainted=, ?mutability=) match the
	// UI's codec conventions so a shared URL is curl-equivalent.
	// Filtering happens here post-fetch rather than in the store
	// layer because (a) refs lists are small (<100 typical), so the
	// perf gain of a SQL-WHERE filter is microseconds, and (b)
	// keeping the service interface stable means MCP + future Go
	// callers continue to receive the full surface.
	filterExternalRefs(resp, r)
	writeJSONWithETag(w, r, http.StatusOK, resp)
}

// filterExternalRefs trims an ExternalSurfaceResponse in place per
// the Plan 14 filter contract. Empty filters are no-ops.
func filterExternalRefs(resp *api.ExternalSurfaceResponse, r *http.Request) {
	resp.Refs, resp.ClassCounts = applyRefFilters(resp.Refs, r)
}

// filterClosureRefs trims a ClosureSurfaceResponse in place per the
// Plan 14 filter contract. Same filter shape as the per-module
// external endpoint, plus ?module=<m1,m2,...> which scopes to refs
// whose closure source-module ID matches (formatted as
// "<module>@<version>"; populated by AirgapSurface).
func filterClosureRefs(resp *api.ClosureSurfaceResponse, r *http.Request) {
	modules := paths.QueryList(r, "module")
	if len(modules) > 0 {
		modSet := setFromList(modules)
		// Pre-filter refs by SourceModule. Tolerate both fully-
		// qualified "name@version" and bare "name" inputs; the UI
		// chip emits the bare name; an operator drilling in via the
		// closure node list might paste "name@version".
		kept := resp.Refs[:0]
		for _, ref := range resp.Refs {
			if ref.SourceModule == "" {
				continue
			}
			if modSet[ref.SourceModule] {
				kept = append(kept, ref)
				continue
			}
			// Try the bare-name form (split on '@').
			if i := strings.IndexByte(ref.SourceModule, '@'); i > 0 {
				if modSet[ref.SourceModule[:i]] {
					kept = append(kept, ref)
				}
			}
		}
		resp.Refs = kept
	}
	resp.Refs, resp.ClassCounts = applyRefFilters(resp.Refs, r)
}

// applyRefFilters is the shared filter pass. Returns the kept refs
// (reusing the input backing array) + a recomputed ClassCounts map so
// the filtered response stays self-consistent. Empty filters return
// the input refs with recomputed counts.
func applyRefFilters(refs []api.ExternalRef, r *http.Request) ([]api.ExternalRef, map[string]int) {
	classes := paths.QueryList(r, "class")
	hosts := paths.QueryList(r, "host")
	mutabilities := paths.QueryList(r, "mutability")
	tainted := paths.QueryTristate(r, "tainted")

	if len(classes) == 0 && len(hosts) == 0 && len(mutabilities) == 0 && tainted == paths.TristateUnset {
		counts := make(map[string]int)
		for _, ref := range refs {
			if ref.Class != "" {
				counts[ref.Class]++
			}
		}
		return refs, counts
	}

	classSet := setFromList(classes)
	hostSet := setFromList(hosts)
	mutSet := setFromList(mutabilities)

	kept := refs[:0]
	counts := make(map[string]int)
	for _, ref := range refs {
		if len(classSet) > 0 && !classSet[ref.Class] {
			continue
		}
		if len(hostSet) > 0 && !hostSet[ref.Host] {
			continue
		}
		if len(mutSet) > 0 && !mutSet[ref.Mutability] {
			continue
		}
		if tainted == paths.TristateOnly && !ref.Tainted {
			continue
		}
		if tainted == paths.TristateExclude && ref.Tainted {
			continue
		}
		kept = append(kept, ref)
		if ref.Class != "" {
			counts[ref.Class]++
		}
	}
	return kept, counts
}

func setFromList(xs []string) map[string]bool {
	if len(xs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}
