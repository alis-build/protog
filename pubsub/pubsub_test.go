package pubsub

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRetryUnauthenticated(t *testing.T) {
	calls := 0
	got, err := retryUnauthenticated(func() (string, error) {
		calls++
		if calls == 1 {
			return "", status.Error(codes.Unauthenticated, "expired token")
		}
		return "topics", nil
	})
	if err != nil {
		t.Fatalf("retryUnauthenticated() error = %v", err)
	}
	if got != "topics" {
		t.Fatalf("retryUnauthenticated() = %q, want topics", got)
	}
	if calls != 2 {
		t.Fatalf("retryUnauthenticated() calls = %d, want 2", calls)
	}
}

func TestRetryUnauthenticatedDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	wantErr := errors.New("permission denied")
	_, err := retryUnauthenticated(func() (string, error) {
		calls++
		return "", wantErr
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
	_, err := retryUnauthenticated(func() (string, error) {
		calls++
		return "", status.Error(codes.Unauthenticated, "expired token")
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("retryUnauthenticated() error = %v, want Unauthenticated", err)
	}
	if calls != 2 {
		t.Fatalf("retryUnauthenticated() calls = %d, want 2", calls)
	}
}
