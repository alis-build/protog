// Package diff helps to build up the changes needed to resolve a diff between two maps of strings.
package diff

import (
	"slices"
	"strings"

	"github.com/alis-build/protog/output"
)

type Diff struct {
	Ignore []string `json:"ignore"`
	Create []string `json:"create"`
	Update []string `json:"update"`
	Delete []string `json:"delete"`
}

func New(current map[string]struct{}, desired map[string]struct{}, packageIDs []string) *Diff {
	diff := &Diff{
		Ignore: []string{},
		Create: []string{},
		Update: []string{},
		Delete: []string{},
	}
	for item := range desired {
		if _, ok := current[item]; !ok {
			// Keep creating missing imported types because a target package can
			// depend on them. Existing imported types do not need to be updated.
			diff.Create = append(diff.Create, item)
		} else if len(packageIDs) == 0 || inPackage(item, packageIDs) {
			diff.Update = append(diff.Update, item)
		} else {
			diff.Ignore = append(diff.Ignore, item)
		}
	}
	for item := range current {
		if _, ok := desired[item]; !ok {
			if !inPackage(item, packageIDs) {
				diff.Ignore = append(diff.Ignore, item)
				continue
			}
			diff.Delete = append(diff.Delete, item)
		}
	}
	slices.Sort(diff.Ignore)
	slices.Sort(diff.Create)
	slices.Sort(diff.Update)
	slices.Sort(diff.Delete)
	return diff
}

func inPackage(item string, packageIDs []string) bool {
	for _, packageID := range packageIDs {
		if suffix, ok := strings.CutPrefix(item, packageID+"."); ok {
			// An uppercase first identifier denotes a type in the package. A
			// lowercase identifier belongs to a nested proto package instead.
			if len(suffix) > 0 && suffix[0] >= 'A' && suffix[0] <= 'Z' {
				return true
			}
		}
	}
	return false
}

type PrintOptions struct {
	PrintIgnored bool
	NoUpdates    bool
}

// Summary returns the diff counts for the given operation ("plan" or
// "deploy").
func (d *Diff) Summary(operation string) output.Summary {
	return output.Summary{
		Add:       len(d.Create),
		Change:    len(d.Update),
		Remove:    len(d.Delete),
		Ignore:    len(d.Ignore),
		Operation: operation,
	}
}

// Emit reports the diff as planned_change events followed by a
// change_summary. item is the resource template for a single entry (kind plus
// database/project); each entry fills in its name.
func (d *Diff) Emit(em output.Emitter, item output.Resource, operation string, opts *PrintOptions) {
	if opts.PrintIgnored {
		for _, name := range d.Ignore {
			em.PlannedChange(output.ActionNoop, item.Named(name))
		}
	}
	for _, name := range d.Create {
		em.PlannedChange(output.ActionCreate, item.Named(name))
	}
	if !opts.NoUpdates {
		for _, name := range d.Update {
			em.PlannedChange(output.ActionUpdate, item.Named(name))
		}
	}
	for _, name := range d.Delete {
		em.PlannedChange(output.ActionDelete, item.Named(name))
	}
	s := d.Summary(operation)
	if opts.NoUpdates {
		s.Change = 0
		s.SkipUpdates = true
	}
	em.ChangeSummary(s)
}
