package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/api/paths"
)

func (h *handler) apiSearch(w http.ResponseWriter, r *http.Request) {
	q := api.Query{
		Text:  r.URL.Query().Get("q"),
		Attr:  r.URL.Query().Get("attr"),
		Kind:  r.URL.Query().Get("kind"),
		Limit: atoiOrDefault(r.URL.Query().Get("limit"), 50),
	}
	for _, c := range r.URL.Query()["hermeticity"] {
		q.Hermeticity = append(q.Hermeticity, report.HermeticityClass(c))
	}
	results, err := h.c.Search(r.Context(), q)
	if err != nil {
		h.apiError(w, err)
		return
	}
	// Empty results must marshal as `"hits":[]`, not `"hits":null`.
	// UI consumers deref `.hits.length` and crash on the latter.
	if results != nil && results.Hits == nil {
		results.Hits = []api.Hit{}
	}
	writeJSON(w, http.StatusOK, results)
}

// apiGetModule serves the per-module HoverCard preview. Returns the
// same ModuleSummary shape as one row of apiListModules; 404 maps
// to api.ErrModuleNotFound so the hover UI can render a "not
// indexed here" hint without parsing error strings.
func (h *handler) apiGetModule(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "module")
	mod, err := h.c.GetModule(r.Context(), name)
	if err != nil {
		if errors.Is(err, api.ErrModuleNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "module " + name + " not indexed in this canopy",
			})
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mod)
}

// apiListModules serves the corpus-overview page: one row per
// indexed module with the latest version + count. Powers the
// /modules SvelteKit route where users browse without having to
// know what to search for. Empty result still serializes as
// `{"modules":[]}` to keep clients dereferencing .length safe.
func (h *handler) apiListModules(w http.ResponseWriter, r *http.Request) {
	mods, err := h.c.ListModules(r.Context())
	if err != nil {
		h.apiError(w, err)
		return
	}
	if mods == nil {
		mods = []api.ModuleSummary{}
	}
	// Plan 14 Layer 1: curl-parity with the /modules UI view.
	//   ?q=<text>     - name substring (case-insensitive)
	//   ?source=true  - only modules with a non-empty source index
	//   ?sort=<key>   - name | usage | maintainers | versions
	//                   (default = usage, matches UI)
	mods = filterModuleList(mods, r)
	resp := map[string]any{"modules": mods}
	// Best-effort corpus stats - feeds the home-page dashboard
	// counters. Failures degrade gracefully (the home page just
	// hides the documented-symbols stat).
	if h.helper != nil {
		if stats, err := h.helper.ComputeCorpusStats(r.Context()); err == nil {
			resp["corpus_stats"] = stats
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func filterModuleList(mods []api.ModuleSummary, r *http.Request) []api.ModuleSummary {
	q := strings.ToLower(paths.QueryString(r, "q"))
	onlyWithSource := paths.QueryBool(r, "source")
	sortKey := paths.QueryString(r, "sort")
	if sortKey == "" {
		sortKey = "usage"
	}

	out := mods[:0]
	for _, m := range mods {
		if q != "" && !strings.Contains(strings.ToLower(m.Name), q) {
			continue
		}
		if onlyWithSource && !m.HasSourceIndex {
			continue
		}
		out = append(out, m)
	}
	sortModuleList(out, sortKey)
	return out
}

func sortModuleList(mods []api.ModuleSummary, key string) {
	// Mirror the UI's compareBy: usage/maintainers/versions sort DESC
	// (popularity-style); name sorts ASC; tie-break always by name.
	sort.SliceStable(mods, func(i, j int) bool {
		a, b := mods[i], mods[j]
		switch key {
		case "name":
			return a.Name < b.Name
		case "maintainers":
			if a.MaintainerCount != b.MaintainerCount {
				return a.MaintainerCount > b.MaintainerCount
			}
		case "versions":
			if a.VersionCount != b.VersionCount {
				return a.VersionCount > b.VersionCount
			}
		default: // "usage"
			if a.UsageCount != b.UsageCount {
				return a.UsageCount > b.UsageCount
			}
		}
		return a.Name < b.Name
	})
}
