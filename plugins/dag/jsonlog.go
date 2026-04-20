package reactdag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// ---------------------------------------------------------------------------
// JSONLogger — structured NDJSON event hook
// ---------------------------------------------------------------------------

// JSONLogEntry is one line of structured build log output.
type JSONLogEntry struct {
	Timestamp string         `json:"ts"`
	Event     string         `json:"event"`
	VertexID  string         `json:"vertex,omitempty"`
	State     string         `json:"state,omitempty"`
	DurationMS int64         `json:"duration_ms,omitempty"`
	CacheTier string         `json:"cache_tier,omitempty"`
	Error     string         `json:"error,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// JSONLogger subscribes to the EventBus and writes one NDJSON line per event
// to the provided writer. Safe for concurrent use.
//
// Wire it up:
//
//	logger := dag.NewJSONLogger(os.Stderr)
//	defer logger.Unsubscribe()
//	logger.Subscribe(sched.EventBus())
type JSONLogger struct {
	w     io.Writer
	unsub []func()
}

// NewJSONLogger constructs a JSONLogger writing to w.
func NewJSONLogger(w io.Writer) *JSONLogger {
	return &JSONLogger{w: w}
}

// Subscribe wires the logger to the EventBus.
func (l *JSONLogger) Subscribe(bus *EventBus) {
	l.unsub = []func(){
		bus.Subscribe(EventBuildStart, l.onBuildStart),
		bus.Subscribe(EventBuildEnd, l.onBuildEnd),
		bus.Subscribe(EventExecutionStart, l.onExecutionStart),
		bus.Subscribe(EventExecutionEnd, l.onExecutionEnd),
		bus.Subscribe(EventCacheHit, l.onCacheHit),
		bus.Subscribe(EventCacheMiss, l.onCacheMiss),
		bus.Subscribe(EventStateChanged, l.onStateChanged),
		bus.Subscribe(EventInvalidated, l.onInvalidated),
	}
}

// Unsubscribe removes all EventBus subscriptions.
func (l *JSONLogger) Unsubscribe() {
	for _, u := range l.unsub {
		u()
	}
}

// ---------------------------------------------------------------------------
// Event handlers
// ---------------------------------------------------------------------------

func (l *JSONLogger) onBuildStart(_ context.Context, e Event) {
	l.write(JSONLogEntry{Event: "build_start", VertexID: e.VertexID})
}

func (l *JSONLogger) onBuildEnd(_ context.Context, e Event) {
	entry := JSONLogEntry{
		Event:    "build_end",
		VertexID: e.VertexID,
		Extra:    make(map[string]any),
	}
	if ms, ok := e.Payload["duration_ms"].(int64); ok {
		entry.DurationMS = ms
	}
	if failed, ok := e.Payload["failed"].(int); ok {
		entry.Extra["failed"] = failed
	}
	l.write(entry)
}

func (l *JSONLogger) onExecutionStart(_ context.Context, e Event) {
	l.write(JSONLogEntry{Event: "exec_start", VertexID: e.VertexID})
}

func (l *JSONLogger) onExecutionEnd(_ context.Context, e Event) {
	entry := JSONLogEntry{Event: "exec_end", VertexID: e.VertexID}
	if ms, ok := e.Payload["duration_ms"].(int64); ok {
		entry.DurationMS = ms
	}
	if errStr, ok := e.Payload["error"].(string); ok {
		entry.Error = errStr
	}
	l.write(entry)
}

func (l *JSONLogger) onCacheHit(_ context.Context, e Event) {
	tier, _ := e.Payload["tier"].(string)
	l.write(JSONLogEntry{Event: "cache_hit", VertexID: e.VertexID, CacheTier: tier})
}

func (l *JSONLogger) onCacheMiss(_ context.Context, e Event) {
	l.write(JSONLogEntry{Event: "cache_miss", VertexID: e.VertexID})
}

func (l *JSONLogger) onStateChanged(_ context.Context, e Event) {
	to, _ := e.Payload["to"].(string)
	l.write(JSONLogEntry{Event: "state_changed", VertexID: e.VertexID, State: to})
}

func (l *JSONLogger) onInvalidated(_ context.Context, e Event) {
	reason, _ := e.Payload["reason"].(string)
	entry := JSONLogEntry{
		Event:    "invalidated",
		VertexID: e.VertexID,
		Extra:    map[string]any{"reason": reason},
	}
	l.write(entry)
}

// write serialises entry to a single NDJSON line.
func (l *JSONLogger) write(entry JSONLogEntry) {
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	fmt.Fprintf(l.w, "%s\n", data)
}
