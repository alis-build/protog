package output

import (
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// Human reproduces protog's legacy output: progress lines on stderr, listing
// results one item per line on stdout.
type Human struct {
	mu      sync.Mutex
	results io.Writer
	events  io.Writer
}

func NewHuman(results, events io.Writer) *Human {
	return &Human{results: results, events: events}
}

func (h *Human) line(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintln(h.events, s)
}

// Version prints nothing: the legacy output has no version banner.
func (h *Human) Version(string) {}

func (h *Human) Log(message string) { h.line(message) }

func (h *Human) RefreshStart(target Resource) { h.line(refreshMessage(target)) }

// RefreshComplete prints nothing: the legacy output has no such line.
func (h *Human) RefreshComplete(Resource, int) {}

func (h *Human) FdsParsed(path string, _, _ int) { h.line("Parsing fds file " + path) }

func (h *Human) PlannedChange(action Action, resource Resource) {
	switch action {
	case ActionNoop:
		h.line("\033[37mIGNORE " + resource.Name + "\033[0m")
	case ActionCreate:
		h.line("➕ CREATE " + resource.Name)
	case ActionUpdate:
		h.line("✏️ UPDATE " + resource.Name)
	case ActionDelete:
		h.line("🗑️ DELETE " + resource.Name)
	}
}

func (h *Human) ChangeSummary(s Summary) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintln(h.events)
	fmt.Fprintln(h.events, "TOTAL IGNORED: "+strconv.Itoa(s.Ignore))
	fmt.Fprintln(h.events, "TOTAL TO CREATE: "+strconv.Itoa(s.Add))
	if !s.SkipUpdates {
		fmt.Fprintln(h.events, "TOTAL TO UPDATE: "+strconv.Itoa(s.Change))
	}
	fmt.Fprintln(h.events, "TOTAL TO DELETE: "+strconv.Itoa(s.Remove))
}

func (h *Human) ApplyStart(action Action, resource Resource, detail string) {
	// Per-topic starts stay silent like the legacy output; the batch headers
	// arrive via Log. Spanner prints its legacy line plus the DDL statement.
	if resource.Kind != "spanner_database" {
		return
	}
	h.line(applyStartMessage(action, resource))
	if detail != "" {
		h.line(detail)
	}
}

func (h *Human) ApplyProgress(_ Action, resource Resource, _ string, percent int32, throttled bool, _ time.Duration) {
	msg := fmt.Sprintf("Progress: %d%%", percent)
	if throttled {
		msg += " (throttled)"
	}
	h.line(msg)
}

func (h *Human) ApplyComplete(action Action, resource Resource, _ string, _ time.Duration) {
	// Per-topic completions stay silent like the legacy output.
	if resource.Kind != "spanner_database" {
		return
	}
	h.line(applyCompleteMessage(action, resource))
}

func (h *Human) ApplyErrored(action Action, resource Resource, err error, _ time.Duration) {
	h.line(applyErroredMessage(action, resource, err))
}

func (h *Human) AuthRetry(string, int) {
	h.line("Credentials rejected; retrying with fresh credentials...")
}

func (h *Human) Diagnostic(severity, summary, detail string) {
	msg := severity + ": " + summary
	if detail != "" {
		msg += ": " + detail
	}
	h.line(msg)
}

// Result prints listing results (view, fds) one item per line on stdout;
// plan/deploy results print nothing extra because their change lines already
// rendered via PlannedChange/ChangeSummary.
func (h *Human) Result(v any) {
	l, ok := v.(Lister)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, line := range l.Lines() {
		fmt.Fprintln(h.results, line)
	}
}

// ResultError prints nothing: fang renders the returned error on stderr.
func (h *Human) ResultError(error) {}
