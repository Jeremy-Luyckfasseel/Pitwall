// Package logging provides the service's single structured-JSON logger
// (blueprint §Observability). Every line carries timestamp, level, service and
// correlationId. There is intentionally no other logging entrypoint in the
// service — no bare fmt.Print*/println to stdout (AC5, NFR20). Duplicated from
// services/timing/internal/logging; the libs/go-pitwall extraction is Story 2.1.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New builds a JSON logger writing to w, tagged with the service name and a
// lifecycle correlationId carried on every line. levelStr is one of
// debug|info|warn|error (anything else falls back to info).
func New(w io.Writer, serviceName, correlationID, levelStr string) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: parseLevel(levelStr),
		// Rename the default keys to the contract-mandated field names:
		// "time" -> "timestamp", "msg" -> "message". "level" already matches.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				a.Key = "timestamp"
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	})
	return slog.New(handler).With(
		slog.String("service", serviceName),
		slog.String("correlationId", correlationID),
	)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
