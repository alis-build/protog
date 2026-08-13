package diff

import "testing"

func TestNewScopesUpdatesToRequestedPackages(t *testing.T) {
	current := names(
		"dealade.dl.marketplace.v1.Buyer",
		"dealade.dl.marketplace.v1.Buyer.View",
		"dealade.dl.projects.v1.Project",
		"google.protobuf.Timestamp",
	)
	desired := names(
		"dealade.dl.marketplace.v1.Buyer",
		"dealade.dl.marketplace.v1.Buyer.View",
		"dealade.dl.projects.v1.Project",
		"google.protobuf.Timestamp",
	)

	got := New(current, desired, []string{"dealade.dl.marketplace.v1"})

	assertNames(t, "update", got.Update,
		"dealade.dl.marketplace.v1.Buyer",
		"dealade.dl.marketplace.v1.Buyer.View",
	)
	assertNames(t, "ignore", got.Ignore,
		"dealade.dl.projects.v1.Project",
		"google.protobuf.Timestamp",
	)
}

func TestNewCreatesMissingImportedTypes(t *testing.T) {
	got := New(nil, names(
		"dealade.dl.marketplace.v1.Buyer",
		"google.protobuf.Timestamp",
	), []string{"dealade.dl.marketplace.v1"})

	assertNames(t, "create", got.Create,
		"dealade.dl.marketplace.v1.Buyer",
		"google.protobuf.Timestamp",
	)
}

func TestNewScopesDeletesToTopLevelRequestedPackage(t *testing.T) {
	got := New(names(
		"dealade.dl.marketplace.v1.Obsolete",
		"dealade.dl.marketplace.v1.internal.Obsolete",
		"dealade.dl.marketplace.v10.Obsolete",
	), nil, []string{"dealade.dl.marketplace.v1"})

	assertNames(t, "delete", got.Delete, "dealade.dl.marketplace.v1.Obsolete")
	assertNames(t, "ignore", got.Ignore,
		"dealade.dl.marketplace.v1.internal.Obsolete",
		"dealade.dl.marketplace.v10.Obsolete",
	)
}

func TestNewWithoutPackageScopePreservesExistingBehavior(t *testing.T) {
	got := New(
		names("existing.Type", "obsolete.Type"),
		names("existing.Type", "new.Type"),
		nil,
	)

	assertNames(t, "create", got.Create, "new.Type")
	assertNames(t, "update", got.Update, "existing.Type")
	assertNames(t, "ignore", got.Ignore, "obsolete.Type")
}

func names(items ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		result[item] = struct{}{}
	}
	return result
}

func assertNames(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	gotSet := names(got...)
	wantSet := names(want...)
	if len(gotSet) != len(wantSet) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for item := range wantSet {
		if _, ok := gotSet[item]; !ok {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}
