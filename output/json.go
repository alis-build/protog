package output

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/status"
)

// JSON emits terraform-style NDJSON events on the events writer (stderr) and
// exactly one result object on the results writer (stdout).
type JSON struct {
	mu         sync.Mutex
	events     *json.Encoder
	results    io.Writer
	resultOnce sync.Once
	now        func() time.Time
}

func NewJSON(results, events io.Writer) *JSON {
	return &JSON{
		events:  json.NewEncoder(events),
		results: results,
		now:     time.Now,
	}
}

type envelope struct {
	Level      string             `json:"@level"`
	Message    string             `json:"@message"`
	Timestamp  string             `json:"@timestamp"`
	Type       string             `json:"type"`
	Protog     string             `json:"protog,omitempty"`
	UI         string             `json:"ui,omitempty"`
	Fds        *fdsPayload        `json:"fds,omitempty"`
	Refresh    *refreshPayload    `json:"refresh,omitempty"`
	Retry      *retryPayload      `json:"retry,omitempty"`
	Change     *changePayload     `json:"change,omitempty"`
	Changes    *Summary           `json:"changes,omitempty"`
	Hook       *hookPayload       `json:"hook,omitempty"`
	Diagnostic *diagnosticPayload `json:"diagnostic,omitempty"`
}

type fdsPayload struct {
	Path   string `json:"path"`
	Types  int    `json:"types,omitempty"`
	Events int    `json:"events,omitempty"`
}

type refreshPayload struct {
	Target   Resource `json:"target"`
	Existing *int     `json:"existing,omitempty"`
}

type retryPayload struct {
	Reason  string `json:"reason"`
	Attempt int    `json:"attempt"`
}

type changePayload struct {
	Resource Resource `json:"resource"`
	Action   Action   `json:"action"`
}

type hookPayload struct {
	Resource       Resource `json:"resource"`
	Action         Action   `json:"action,omitempty"`
	Operation      string   `json:"operation,omitempty"`
	Detail         string   `json:"detail,omitempty"`
	Percent        *int32   `json:"percent,omitempty"`
	Throttled      bool     `json:"throttled,omitempty"`
	ElapsedSeconds *float64 `json:"elapsed_seconds,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type diagnosticPayload struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (j *JSON) emit(e envelope) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e.Level == "" {
		e.Level = "info"
	}
	e.Timestamp = j.now().UTC().Format(time.RFC3339)
	// Encoding errors mean the progress stream is gone (closed pipe); the
	// final result write will surface the same condition.
	_ = j.events.Encode(e)
}

func (j *JSON) Version(version string) {
	j.emit(envelope{
		Message: "protog " + version,
		Type:    "version",
		Protog:  version,
		UI:      UISchemaVersion,
	})
}

func (j *JSON) Log(message string) {
	j.emit(envelope{Message: message, Type: "log"})
}

func (j *JSON) RefreshStart(target Resource) {
	j.emit(envelope{
		Message: refreshMessage(target),
		Type:    "refresh_start",
		Refresh: &refreshPayload{Target: target},
	})
}

func (j *JSON) RefreshComplete(target Resource, existing int) {
	j.emit(envelope{
		Message: refreshCompleteMessage(target, existing),
		Type:    "refresh_complete",
		Refresh: &refreshPayload{Target: target, Existing: &existing},
	})
}

func (j *JSON) FdsParsed(path string, types, events int) {
	j.emit(envelope{
		Message: "Parsing fds file " + path,
		Type:    "fds_parsed",
		Fds:     &fdsPayload{Path: path, Types: types, Events: events},
	})
}

func (j *JSON) PlannedChange(action Action, resource Resource) {
	label := strings.ToUpper(string(action))
	if action == ActionNoop {
		label = "IGNORE"
	}
	j.emit(envelope{
		Message: label + " " + resource.Name,
		Type:    "planned_change",
		Change:  &changePayload{Resource: resource, Action: action},
	})
}

func (j *JSON) ChangeSummary(s Summary) {
	j.emit(envelope{
		Message: summaryMessage(s),
		Type:    "change_summary",
		Changes: &s,
	})
}

func (j *JSON) ApplyStart(action Action, resource Resource, detail string) {
	j.emit(envelope{
		Message: applyStartMessage(action, resource),
		Type:    "apply_start",
		Hook:    &hookPayload{Resource: resource, Action: action, Detail: detail},
	})
}

func (j *JSON) ApplyProgress(action Action, resource Resource, operation string, percent int32, throttled bool, elapsed time.Duration) {
	j.emit(envelope{
		Message: fmt.Sprintf("%s: %d%%", applyStartMessage(action, resource), percent),
		Type:    "apply_progress",
		Hook: &hookPayload{
			Resource:       resource,
			Action:         action,
			Operation:      operation,
			Percent:        &percent,
			Throttled:      throttled,
			ElapsedSeconds: elapsedSeconds(elapsed),
		},
	})
}

func (j *JSON) ApplyComplete(action Action, resource Resource, operation string, elapsed time.Duration) {
	j.emit(envelope{
		Message: applyCompleteMessage(action, resource),
		Type:    "apply_complete",
		Hook: &hookPayload{
			Resource:       resource,
			Action:         action,
			Operation:      operation,
			ElapsedSeconds: elapsedSeconds(elapsed),
		},
	})
}

func (j *JSON) ApplyErrored(action Action, resource Resource, err error, elapsed time.Duration) {
	j.emit(envelope{
		Level:   "error",
		Message: applyErroredMessage(action, resource, err),
		Type:    "apply_errored",
		Hook: &hookPayload{
			Resource:       resource,
			Action:         action,
			ElapsedSeconds: elapsedSeconds(elapsed),
			Error:          err.Error(),
		},
	})
}

func (j *JSON) AuthRetry(reason string, attempt int) {
	j.emit(envelope{
		Level:   "warn",
		Message: "Credentials rejected; retrying with fresh credentials...",
		Type:    "auth_retry",
		Retry:   &retryPayload{Reason: reason, Attempt: attempt},
	})
}

func (j *JSON) Diagnostic(severity, summary, detail string) {
	level := severity
	if level != "error" && level != "warn" {
		level = "info"
	}
	msg := summary
	if detail != "" {
		msg += ": " + detail
	}
	j.emit(envelope{
		Level:      level,
		Message:    msg,
		Type:       "diagnostic",
		Diagnostic: &diagnosticPayload{Severity: severity, Summary: summary, Detail: detail},
	})
}

func (j *JSON) Result(v any) {
	j.writeResult(v)
}

func (j *JSON) ResultError(err error) {
	j.writeResult(errorEnvelope{Error: errorBody{
		Code:    errorCode(err),
		Message: err.Error(),
	}})
}

func (j *JSON) writeResult(v any) {
	j.resultOnce.Do(func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		enc := json.NewEncoder(j.results)
		_ = enc.Encode(v)
	})
}

// errorCode maps an error to a snake_case code: the gRPC status code when the
// error carries one, "unknown" otherwise (status.Code returns Unknown for
// plain errors).
func errorCode(err error) string {
	return camelToSnake(status.Code(err).String())
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func elapsedSeconds(d time.Duration) *float64 {
	s := math.Round(d.Seconds()*1000) / 1000
	return &s
}
