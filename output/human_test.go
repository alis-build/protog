package output

import (
	"bytes"
	"testing"
)

func TestHumanPlannedChangesKeepLegacyStrings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := NewHuman(&stdout, &stderr)

	item := SpannerType("projects/p/instances/i/databases/d")
	h.PlannedChange(ActionNoop, item.Named("google.protobuf.Timestamp"))
	h.PlannedChange(ActionCreate, item.Named("a.b.v1.Book"))
	h.PlannedChange(ActionUpdate, item.Named("a.b.v1.Author"))
	h.PlannedChange(ActionDelete, item.Named("a.b.v1.Old"))
	h.ChangeSummary(Summary{Add: 1, Change: 1, Remove: 1, Ignore: 1})

	want := "\033[37mIGNORE google.protobuf.Timestamp\033[0m\n" +
		"➕ CREATE a.b.v1.Book\n" +
		"✏️ UPDATE a.b.v1.Author\n" +
		"🗑️ DELETE a.b.v1.Old\n" +
		"\n" +
		"TOTAL IGNORED: 1\n" +
		"TOTAL TO CREATE: 1\n" +
		"TOTAL TO UPDATE: 1\n" +
		"TOTAL TO DELETE: 1\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Errorf("progress must not reach stdout, got %q", stdout.String())
	}
}

func TestHumanSummarySkipsUpdatesLikeLegacyNoUpdates(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := NewHuman(&stdout, &stderr)
	h.ChangeSummary(Summary{Add: 2, Ignore: 3, SkipUpdates: true})

	want := "\nTOTAL IGNORED: 3\nTOTAL TO CREATE: 2\nTOTAL TO DELETE: 0\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

type listing []string

func (l listing) Lines() []string { return l }

func TestHumanResultListsToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	h := NewHuman(&stdout, &stderr)

	h.Result(listing{"a.b.v1.Book", "a.b.v1.Author"})
	if stdout.String() != "a.b.v1.Book\na.b.v1.Author\n" {
		t.Errorf("stdout = %q", stdout.String())
	}

	h.Result(struct{ Operation string }{"plan"})
	if stdout.String() != "a.b.v1.Book\na.b.v1.Author\n" {
		t.Errorf("non-listing results must print nothing, stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("results must not reach stderr, got %q", stderr.String())
	}
}

func TestPackageOf(t *testing.T) {
	cases := map[string]string{
		"alis.aa.library.v1.Book":         "alis.aa.library.v1",
		"alis.aa.library.v1.Book.Chapter": "alis.aa.library.v1",
		"google.protobuf.Timestamp":       "google.protobuf",
		"Book":                            "",
	}
	for name, want := range cases {
		if got := packageOf(name); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", name, got, want)
		}
	}
}
