package modulediff

import (
	"sort"

	"github.com/albertocavalcante/assay/report"
)

func rulesDiff(a, b []report.RuleSpec) RulesDiff {
	aByName := map[string]report.RuleSpec{}
	for _, r := range a {
		aByName[r.Name] = r
	}
	bByName := map[string]report.RuleSpec{}
	for _, r := range b {
		bByName[r.Name] = r
	}
	var out RulesDiff
	for name, bRule := range bByName {
		if aRule, ok := aByName[name]; ok {
			ch := compareRule(aRule, bRule)
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

func compareRule(a, b report.RuleSpec) *ChangedRule {
	aByAttr := map[string]report.AttrSpec{}
	for _, at := range a.Attrs {
		aByAttr[at.Name] = at
	}
	bByAttr := map[string]report.AttrSpec{}
	for _, at := range b.Attrs {
		bByAttr[at.Name] = at
	}
	ch := &ChangedRule{Name: a.Name}
	for n, ba := range bByAttr {
		if aa, ok := aByAttr[n]; ok {
			if c := compareAttr(aa, ba); c != nil {
				ch.AttrsChg = append(ch.AttrsChg, *c)
			}
		} else {
			ch.AttrsAdd = append(ch.AttrsAdd, ba)
		}
	}
	for n, aa := range aByAttr {
		if _, ok := bByAttr[n]; !ok {
			ch.AttrsRem = append(ch.AttrsRem, aa)
		}
	}
	if len(ch.AttrsAdd) == 0 && len(ch.AttrsRem) == 0 && len(ch.AttrsChg) == 0 {
		return nil
	}
	sort.Slice(ch.AttrsAdd, func(i, j int) bool { return ch.AttrsAdd[i].Name < ch.AttrsAdd[j].Name })
	sort.Slice(ch.AttrsRem, func(i, j int) bool { return ch.AttrsRem[i].Name < ch.AttrsRem[j].Name })
	sort.Slice(ch.AttrsChg, func(i, j int) bool { return ch.AttrsChg[i].Name < ch.AttrsChg[j].Name })
	return ch
}

func compareAttr(a, b report.AttrSpec) *AttrChange {
	c := &AttrChange{Name: a.Name}
	changed := false
	if a.Type != b.Type {
		c.FromType, c.ToType = a.Type, b.Type
		changed = true
	}
	if a.Default != b.Default {
		c.FromDefault, c.ToDefault = a.Default, b.Default
		changed = true
	}
	if a.Mandatory != b.Mandatory {
		c.FromMandatory = a.Mandatory
		c.ToMandatory = b.Mandatory
		c.MandatoryFlip = true
		changed = true
	}
	if !changed {
		return nil
	}
	return c
}
