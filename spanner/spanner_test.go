package spanner

import (
	"errors"
	"fmt"
	"testing"

	"github.com/alis-build/protog/diff"
	"github.com/alis-build/protog/output"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRetryUnauthenticatedRefreshesAndRetriesOnce(t *testing.T) {
	calls := 0
	refreshes := 0
	got, err := retryUnauthenticated(func() (string, error) {
		calls++
		if calls == 1 {
			return "", fmt.Errorf("get database DDL: %w", status.Error(codes.Unauthenticated, "expired token"))
		}
		return "bundles", nil
	}, func() error {
		refreshes++
		return nil
	})
	if err != nil {
		t.Fatalf("retryUnauthenticated() error = %v", err)
	}
	if got != "bundles" {
		t.Fatalf("retryUnauthenticated() = %q, want bundles", got)
	}
	if calls != 2 || refreshes != 1 {
		t.Fatalf("retryUnauthenticated() calls = %d, refreshes = %d; want 2, 1", calls, refreshes)
	}
}

func TestRetryUnauthenticatedDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	refreshes := 0
	wantErr := errors.New("permission denied")
	_, err := retryUnauthenticated(func() (string, error) {
		calls++
		return "", wantErr
	}, func() error {
		refreshes++
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("retryUnauthenticated() error = %v, want %v", err, wantErr)
	}
	if calls != 1 || refreshes != 0 {
		t.Fatalf("retryUnauthenticated() calls = %d, refreshes = %d; want 1, 0", calls, refreshes)
	}
}

func TestRetryUnauthenticatedReturnsRefreshError(t *testing.T) {
	calls := 0
	wantErr := errors.New("new client failed")
	_, err := retryUnauthenticated(func() (string, error) {
		calls++
		return "", status.Error(codes.Unauthenticated, "expired token")
	}, func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("retryUnauthenticated() error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("retryUnauthenticated() calls = %d, want 1", calls)
	}
}

func TestRetryUnauthenticatedStopsAfterOneRetry(t *testing.T) {
	calls := 0
	refreshes := 0
	_, err := retryUnauthenticated(func() (string, error) {
		calls++
		return "", status.Error(codes.Unauthenticated, "expired token")
	}, func() error {
		refreshes++
		return nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("retryUnauthenticated() error = %v, want Unauthenticated", err)
	}
	if calls != 2 || refreshes != 1 {
		t.Fatalf("retryUnauthenticated() calls = %d, refreshes = %d; want 2, 1", calls, refreshes)
	}
}

func destroyPlan(existing int, delete ...string) *Plan {
	return &Plan{
		ExistingTypes: existing,
		Destroy:       true,
		Diff:          &diff.Diff{Delete: delete},
	}
}

func TestDestroyStatementDeletesRequestedTypes(t *testing.T) {
	p := destroyPlan(3, "alis.aa.library.v1.Book", "alis.aa.library.v1.Author")
	want := "ALTER PROTO BUNDLE DELETE(`alis.aa.library.v1.Book`,`alis.aa.library.v1.Author`)"
	if got := p.Statement(); got != want {
		t.Fatalf("Statement() = %q, want %q", got, want)
	}
	if got := p.action(); got != output.ActionDelete {
		t.Fatalf("action() = %q, want %q", got, output.ActionDelete)
	}
}

func TestDestroyStatementNeverDropsTheBundle(t *testing.T) {
	// Removing every remaining type still deletes members; the bundle itself
	// is never dropped.
	p := destroyPlan(2, "alis.aa.library.v1.Book", "alis.aa.library.v1.Author")
	want := "ALTER PROTO BUNDLE DELETE(`alis.aa.library.v1.Book`,`alis.aa.library.v1.Author`)"
	if got := p.Statement(); got != want {
		t.Fatalf("Statement() = %q, want %q", got, want)
	}
}

func TestDestroyStatementEmptyWhenNothingToDelete(t *testing.T) {
	if got := destroyPlan(2).Statement(); got != "" {
		t.Fatalf("Statement() = %q, want empty", got)
	}
}
