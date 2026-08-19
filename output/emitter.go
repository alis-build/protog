// Package output renders progress events and command results in either
// human-readable or machine-readable (--json) form.
//
// The contract mirrors terraform's machine-readable UI and the alis CLI:
// stderr carries progress (NDJSON events under --json, plain lines otherwise)
// and stdout carries exactly one final result object under --json.
package output

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// UISchemaVersion identifies the NDJSON event schema. Increment the minor
// version for backward-compatible additions, the major version for breaking
// changes.
const UISchemaVersion = "1.0"

// Action classifies a planned or applied change.
type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
	ActionNoop   Action = "noop"
)

// Resource addresses the unit a change or event applies to, protog's analog
// of terraform's resource address.
type Resource struct {
	Kind     string `json:"kind"`
	Database string `json:"database,omitempty"`
	Project  string `json:"project,omitempty"`
	Name     string `json:"name,omitempty"`
	Package  string `json:"package,omitempty"`
}

func SpannerDatabase(database string) Resource {
	return Resource{Kind: "spanner_database", Database: database}
}

func SpannerType(database string) Resource {
	return Resource{Kind: "spanner_proto_bundle_type", Database: database}
}

func PubsubProject(project string) Resource {
	return Resource{Kind: "pubsub_project", Project: project}
}

func PubsubTopic(project string) Resource {
	return Resource{Kind: "pubsub_topic", Project: project}
}

// Named returns a copy of the resource identifying one fully-qualified proto
// type or topic, with its proto package derived from the name.
func (r Resource) Named(name string) Resource {
	r.Name = name
	r.Package = packageOf(name)
	return r
}

// packageOf returns the proto package of a fully-qualified type name: the
// leading lowercase identifiers. An uppercase identifier starts the type
// (nested types keep their outer message out of the package).
func packageOf(fullName string) string {
	parts := strings.Split(fullName, ".")
	i := 0
	for i < len(parts) {
		if len(parts[i]) > 0 && parts[i][0] >= 'A' && parts[i][0] <= 'Z' {
			break
		}
		i++
	}
	return strings.Join(parts[:i], ".")
}

// Summary carries the diff counts reported by the change_summary event and
// the final result objects.
type Summary struct {
	Add       int    `json:"add"`
	Change    int    `json:"change"`
	Remove    int    `json:"remove"`
	Ignore    int    `json:"ignore"`
	Operation string `json:"operation,omitempty"`
	// SkipUpdates suppresses the update total in human output, matching the
	// legacy NoUpdates rendering; it never appears in JSON.
	SkipUpdates bool `json:"-"`
}

// Lister is implemented by result objects whose human rendering is one line
// per item on stdout (view and fds listings).
type Lister interface {
	Lines() []string
}

// Emitter renders progress events (stderr) and the final result (stdout).
// Implementations are safe for concurrent use.
type Emitter interface {
	Version(version string)
	Log(message string)
	RefreshStart(target Resource)
	RefreshComplete(target Resource, existing int)
	// FdsParsed reports a parsed file descriptor set; pass 0 for whichever of
	// types/events was not counted and it is omitted from the payload.
	FdsParsed(path string, types, events int)
	PlannedChange(action Action, resource Resource)
	ChangeSummary(s Summary)
	ApplyStart(action Action, resource Resource, detail string)
	ApplyProgress(action Action, resource Resource, operation string, percent int32, throttled bool, elapsed time.Duration)
	ApplyComplete(action Action, resource Resource, operation string, elapsed time.Duration)
	ApplyErrored(action Action, resource Resource, err error, elapsed time.Duration)
	AuthRetry(reason string, attempt int)
	Diagnostic(severity, summary, detail string)
	Result(v any)
	ResultError(err error)
}

var (
	current     Emitter = NewHuman(os.Stdout, os.Stderr)
	jsonMode    bool
	initialized bool
)

// Init selects the process-wide emitter and emits the version event. It is a
// no-op once initialized, so the fang error handler can call it as a fallback
// for failures that occur before cobra's PersistentPreRun.
func Init(json bool, version string) {
	if initialized {
		return
	}
	initialized = true
	jsonMode = json
	if json {
		current = NewJSON(os.Stdout, os.Stderr)
	}
	current.Version(version)
}

func JSONMode() bool { return jsonMode }

// Get returns the process-wide emitter.
func Get() Emitter { return current }

func Log(message string)                     { current.Log(message) }
func RefreshStart(target Resource)           { current.RefreshStart(target) }
func RefreshComplete(t Resource, n int)      { current.RefreshComplete(t, n) }
func FdsParsed(path string, types, ev int)   { current.FdsParsed(path, types, ev) }
func ChangeSummary(s Summary)                { current.ChangeSummary(s) }
func AuthRetry(reason string, attempt int)   { current.AuthRetry(reason, attempt) }
func Diagnostic(sev, summary, detail string) { current.Diagnostic(sev, summary, detail) }
func Result(v any)                           { current.Result(v) }
func ResultError(err error)                  { current.ResultError(err) }

func PlannedChange(action Action, resource Resource) { current.PlannedChange(action, resource) }

func ApplyStart(action Action, r Resource, detail string) { current.ApplyStart(action, r, detail) }

func ApplyProgress(action Action, r Resource, op string, pct int32, throttled bool, elapsed time.Duration) {
	current.ApplyProgress(action, r, op, pct, throttled, elapsed)
}

func ApplyComplete(action Action, r Resource, op string, elapsed time.Duration) {
	current.ApplyComplete(action, r, op, elapsed)
}

func ApplyErrored(action Action, r Resource, err error, elapsed time.Duration) {
	current.ApplyErrored(action, r, err, elapsed)
}

// Shared human-readable message builders so @message under --json matches the
// human-mode wording.

func refreshMessage(target Resource) string {
	if target.Kind == "pubsub_project" {
		return "Fetching current topics..."
	}
	return "Fetching current proto bundles..."
}

func refreshCompleteMessage(target Resource, existing int) string {
	if target.Kind == "pubsub_project" {
		return fmt.Sprintf("Found %d existing topics", existing)
	}
	return fmt.Sprintf("Found %d existing proto bundle types", existing)
}

func applyStartMessage(action Action, r Resource) string {
	if r.Kind == "spanner_database" {
		if action == ActionDelete {
			return "Destroying proto bundles..."
		}
		return "Deploying proto bundles..."
	}
	return applyVerb(action, "Creating", "Updating", "Deleting") + " topic " + r.Name
}

func applyCompleteMessage(action Action, r Resource) string {
	if r.Kind == "spanner_database" {
		if action == ActionDelete {
			return "Proto bundles destroyed"
		}
		return "Proto bundles deployed"
	}
	return applyVerb(action, "Created", "Updated", "Deleted") + " topic " + r.Name
}

func applyErroredMessage(action Action, r Resource, err error) string {
	if r.Kind == "spanner_database" {
		if action == ActionDelete {
			return fmt.Sprintf("Error destroying proto bundles: %v", err)
		}
		return fmt.Sprintf("Error deploying proto bundles: %v", err)
	}
	return fmt.Sprintf("Error %s topic %s: %v",
		strings.ToLower(applyVerb(action, "Creating", "Updating", "Deleting")), r.Name, err)
}

func applyVerb(action Action, create, update, del string) string {
	switch action {
	case ActionCreate:
		return create
	case ActionDelete:
		return del
	default:
		return update
	}
}

func summaryMessage(s Summary) string {
	title := "Plan"
	switch s.Operation {
	case "deploy":
		title = "Deploy"
	case "destroy":
		title = "Destroy"
	case "plandestroy":
		title = "Plan destroy"
	}
	if s.SkipUpdates {
		return fmt.Sprintf("%s: %d to create, %d to delete (%d ignored)",
			title, s.Add, s.Remove, s.Ignore)
	}
	return fmt.Sprintf("%s: %d to create, %d to update, %d to delete (%d ignored)",
		title, s.Add, s.Change, s.Remove, s.Ignore)
}
