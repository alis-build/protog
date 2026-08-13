package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestJSON() (*JSON, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	j := NewJSON(&stdout, &stderr)
	j.now = func() time.Time { return time.Date(2026, 8, 13, 13, 36, 17, 0, time.UTC) }
	return j, &stdout, &stderr
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for line := range strings.Lines(buf.String()) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}
		event := map[string]any{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestJSONEventStream(t *testing.T) {
	j, stdout, stderr := newTestJSON()

	database := "projects/p/instances/i/databases/d"
	target := SpannerDatabase(database)
	j.Version("v1.2.3")
	j.RefreshStart(target)
	j.RefreshComplete(target, 17)
	j.FdsParsed("test/fds", 42, 0)
	j.PlannedChange(ActionCreate, SpannerType(database).Named("alis.aa.library.v1.Book"))
	j.ChangeSummary(Summary{Add: 1, Ignore: 2, Operation: "plan"})
	j.ApplyStart(ActionUpdate, target, "ALTER PROTO BUNDLE")
	j.ApplyProgress(target, "op-1", 60, true, 1500*time.Millisecond)
	j.ApplyComplete(ActionUpdate, target, "op-1", 3*time.Second)
	j.ApplyErrored(ActionCreate, PubsubTopic("proj").Named("a.b.v1.XEvent"), errors.New("boom"), time.Second)
	j.AuthRetry("cached credential rejected", 2)
	j.Diagnostic("error", "deploy failed", "details")

	if stdout.Len() != 0 {
		t.Fatalf("events must not write to the results writer, got %q", stdout.String())
	}
	events := decodeLines(t, stderr)
	wantTypes := []string{
		"version", "refresh_start", "refresh_complete", "fds_parsed",
		"planned_change", "change_summary", "apply_start", "apply_progress",
		"apply_complete", "apply_errored", "auth_retry", "diagnostic",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d", len(events), len(wantTypes))
	}
	for i, event := range events {
		if event["type"] != wantTypes[i] {
			t.Errorf("event %d type = %v, want %s", i, event["type"], wantTypes[i])
		}
		for _, key := range []string{"@level", "@message", "@timestamp"} {
			if _, ok := event[key].(string); !ok {
				t.Errorf("event %d missing %s", i, key)
			}
		}
		if _, err := time.Parse(time.RFC3339, event["@timestamp"].(string)); err != nil {
			t.Errorf("event %d @timestamp: %v", i, err)
		}
	}

	version := events[0]
	if version["protog"] != "v1.2.3" || version["ui"] != UISchemaVersion {
		t.Errorf("version event = %v", version)
	}
	change := events[4]["change"].(map[string]any)
	resource := change["resource"].(map[string]any)
	if change["action"] != "create" ||
		resource["kind"] != "spanner_proto_bundle_type" ||
		resource["name"] != "alis.aa.library.v1.Book" ||
		resource["package"] != "alis.aa.library.v1" {
		t.Errorf("planned_change = %v", change)
	}
	progress := events[7]["hook"].(map[string]any)
	if progress["percent"] != float64(60) || progress["throttled"] != true ||
		progress["operation"] != "op-1" || progress["elapsed_seconds"] != 1.5 {
		t.Errorf("apply_progress hook = %v", progress)
	}
	errored := events[9]
	if errored["@level"] != "error" || errored["hook"].(map[string]any)["error"] != "boom" {
		t.Errorf("apply_errored = %v", errored)
	}
}

func TestJSONResultWrittenOnce(t *testing.T) {
	j, stdout, _ := newTestJSON()
	j.Result(map[string]string{"operation": "plan"})
	j.ResultError(errors.New("late failure"))

	results := decodeLines(t, stdout)
	if len(results) != 1 {
		t.Fatalf("stdout carries %d objects, want exactly 1: %q", len(results), stdout.String())
	}
	if results[0]["operation"] != "plan" {
		t.Fatalf("first result must win, got %v", results[0])
	}
}

func TestJSONResultErrorEnvelope(t *testing.T) {
	j, stdout, _ := newTestJSON()
	j.ResultError(status.Error(codes.NotFound, "database missing"))

	results := decodeLines(t, stdout)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	errBody := results[0]["error"].(map[string]any)
	if errBody["code"] != "not_found" || !strings.Contains(errBody["message"].(string), "database missing") {
		t.Fatalf("error envelope = %v", errBody)
	}
}

func TestJSONPlainErrorCodeIsUnknown(t *testing.T) {
	j, stdout, _ := newTestJSON()
	j.ResultError(errors.New("plain failure"))
	results := decodeLines(t, stdout)
	if code := results[0]["error"].(map[string]any)["code"]; code != "unknown" {
		t.Fatalf("code = %v, want unknown", code)
	}
}

func TestJSONConcurrentEventsStayLineDelimited(t *testing.T) {
	j, _, stderr := newTestJSON()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 50 {
				item := PubsubTopic("proj").Named(fmt.Sprintf("a.b.v1.Topic%dx%dEvent", g, i))
				j.ApplyStart(ActionCreate, item, "")
				j.ApplyComplete(ActionCreate, item, "", time.Millisecond)
			}
		})
	}
	wg.Wait()
	events := decodeLines(t, stderr)
	if len(events) != 8*50*2 {
		t.Fatalf("got %d events, want %d", len(events), 8*50*2)
	}
}
