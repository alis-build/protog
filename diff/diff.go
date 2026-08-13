// Package diff helps to build up the changes needed to resolve a diff between two maps of strings.
package diff

import (
	"strconv"
	"strings"
)

type Diff struct {
	Ignore []string
	Create []string
	Update []string
	Delete []string
}

func New(current map[string]struct{}, desired map[string]struct{}, packageIDs []string) *Diff {
	diff := &Diff{}
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

func (d *Diff) Print(opts *PrintOptions) {
	if opts.PrintIgnored {
		for _, topic := range d.Ignore {
			println("\033[37mIGNORE " + topic + "\033[0m")
		}
	}
	for _, item := range d.Create {
		println("➕ CREATE " + item)
	}
	if !opts.NoUpdates {
		for _, item := range d.Update {
			println("✏️ UPDATE " + item)
		}
	}
	for _, item := range d.Delete {
		println("🗑️ DELETE " + item)
	}
	println()
	println("TOTAL IGNORED: " + strconv.Itoa(len(d.Ignore)))
	println("TOTAL TO CREATE: " + strconv.Itoa(len(d.Create)))
	if !opts.NoUpdates {
		println("TOTAL TO UPDATE: " + strconv.Itoa(len(d.Update)))
	}
	println("TOTAL TO DELETE: " + strconv.Itoa(len(d.Delete)))
}
