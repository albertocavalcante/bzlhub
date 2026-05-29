package modulediff

import (
	"sort"

	"github.com/albertocavalcante/assay/report"
)

func providersDiff(a, b []report.ProviderSpec) ProvidersDiff {
	aByName := map[string]report.ProviderSpec{}
	for _, p := range a {
		aByName[p.Name] = p
	}
	bByName := map[string]report.ProviderSpec{}
	for _, p := range b {
		bByName[p.Name] = p
	}
	var out ProvidersDiff
	for name, bp := range bByName {
		if ap, ok := aByName[name]; ok {
			ch := compareProvider(ap, bp)
			if ch != nil {
				out.Changed = append(out.Changed, *ch)
			}
		} else {
			out.Added = append(out.Added, name)
		}
	}
	for name := range aByName {
		if _, ok := bByName[name]; !ok {
			out.Removed = append(out.Removed, name)
		}
	}
	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	sort.Slice(out.Changed, func(i, j int) bool { return out.Changed[i].Name < out.Changed[j].Name })
	return out
}

func compareProvider(a, b report.ProviderSpec) *ChangedProvider {
	aSet := map[string]bool{}
	for _, f := range a.Fields {
		aSet[f] = true
	}
	bSet := map[string]bool{}
	for _, f := range b.Fields {
		bSet[f] = true
	}
	c := &ChangedProvider{Name: a.Name}
	for f := range bSet {
		if !aSet[f] {
			c.FieldsAdded = append(c.FieldsAdded, f)
		}
	}
	for f := range aSet {
		if !bSet[f] {
			c.FieldsRemoved = append(c.FieldsRemoved, f)
		}
	}
	if len(c.FieldsAdded) == 0 && len(c.FieldsRemoved) == 0 {
		return nil
	}
	sort.Strings(c.FieldsAdded)
	sort.Strings(c.FieldsRemoved)
	return c
}
