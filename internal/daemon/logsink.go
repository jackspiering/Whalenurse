package daemon

import (
	"context"
	"log/slog"

	"github.com/jackspiering/whalenurse/internal/engine"
)

// LogSink returns the domain-event subscriber that writes the structured log.
// "Notify" means this log line plus the domain event itself until M3 (Shoutrrr). Because it reads the same stream the store and API will, the
// log cannot disagree with them.
func LogSink(log *slog.Logger) func(engine.Event) {
	return func(e engine.Event) {
		level := slog.LevelInfo
		switch e.Type {
		case engine.Suspended, engine.NeedsRecreate, engine.NeedsDependentRestart, engine.ActionRefused,
			engine.RateLimited, engine.NotifyOnly, engine.IncidentOpened:
			level = slog.LevelWarn
		case engine.EventAttributed, engine.RestoreEvent:
			level = slog.LevelDebug
		}
		attrs := []any{"event_id", e.ID, "type", string(e.Type), "host", e.Host, "key", e.Key, "container", e.Container}
		for k, v := range e.Attrs {
			attrs = append(attrs, "attr."+k, v)
		}
		log.Log(context.Background(), level, e.Message, attrs...)
	}
}
