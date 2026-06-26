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
	for event := range desired {
		if _, ok := current[event]; !ok {
			diff.Create = append(diff.Create, event)
		} else {
			diff.Update = append(diff.Update, event)
		}
	}
	for topic := range current {
		if _, ok := desired[topic]; !ok {
			var foundPackage bool
			for _, packageID := range packageIDs {
				if strings.HasPrefix(topic, packageID) {
					foundPackage = true
					break
				}
			}
			if !foundPackage {
				diff.Ignore = append(diff.Ignore, topic)
				continue
			}
			diff.Delete = append(diff.Delete, topic)
		}
	}
	return diff
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
