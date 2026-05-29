package modulediff

import (
	"sort"

	"github.com/albertocavalcante/assay/report"
)

func modExtsDiff(a, b []report.ModuleExtSpec) ModExtsDiff {
	aByName := map[string]report.ModuleExtSpec{}
	for _, e := range a {
		aByName[e.Name] = e
	}
	bByName := map[string]report.ModuleExtSpec{}
	for _, e := range b {
		bByName[e.Name] = e
	}
	var out ModExtsDiff
	for name, be := range bByName {
		if ae, ok := aByName[name]; ok {
			if ch := compareModExt(ae, be); ch != nil {
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

func compareModExt(a, b report.ModuleExtSpec) *ChangedModExt {
	aSet := map[string]bool{}
	for _, t := range a.TagClasses {
		aSet[t] = true
	}
	bSet := map[string]bool{}
	for _, t := range b.TagClasses {
		bSet[t] = true
	}
	ch := &ChangedModExt{Name: a.Name}
	for t := range bSet {
		if !aSet[t] {
			ch.TagClassesAdded = append(ch.TagClassesAdded, t)
		}
	}
	for t := range aSet {
		if !bSet[t] {
			ch.TagClassesRemoved = append(ch.TagClassesRemoved, t)
		}
	}
	if len(ch.TagClassesAdded) == 0 && len(ch.TagClassesRemoved) == 0 {
		return nil
	}
	sort.Strings(ch.TagClassesAdded)
	sort.Strings(ch.TagClassesRemoved)
	return ch
}
