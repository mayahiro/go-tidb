// Package logging provides safe logging primitives for tidbgo commands and
// runtime components.
package logging

import (
	"context"
	"log/slog"
	"strings"

	"github.com/mayahiro/go-tidb/internal/redact"
)

// RedactingHandler wraps a slog handler and removes secrets from messages and
// string attributes before forwarding records.
type RedactingHandler struct {
	next    slog.Handler
	secrets []string
}

// NewRedactingHandler wraps next with secret redaction.
func NewRedactingHandler(next slog.Handler, secrets ...string) *RedactingHandler {
	return &RedactingHandler{
		next:    next,
		secrets: append([]string(nil), secrets...),
	}
}

// Enabled delegates level filtering to the wrapped handler.
func (handler *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

// Handle redacts and forwards one record.
func (handler *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, redact.String(record.Message, handler.secrets...), record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		clean.AddAttrs(handler.redactAttribute(attribute))
		return true
	})
	return handler.next.Handle(ctx, clean)
}

// WithAttrs returns a handler with redacted preformatted attributes.
func (handler *RedactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		clean = append(clean, handler.redactAttribute(attribute))
	}
	return &RedactingHandler{next: handler.next.WithAttrs(clean), secrets: append([]string(nil), handler.secrets...)}
}

// WithGroup delegates group construction to the wrapped handler.
func (handler *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: handler.next.WithGroup(name), secrets: append([]string(nil), handler.secrets...)}
}

func (handler *RedactingHandler) redactAttribute(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if sensitiveKey(attribute.Key) {
		return slog.String(attribute.Key, redact.Replacement)
	}

	switch attribute.Value.Kind() {
	case slog.KindString:
		return slog.String(attribute.Key, redact.String(attribute.Value.String(), handler.secrets...))
	case slog.KindAny:
		if err, ok := attribute.Value.Any().(error); ok {
			return slog.String(attribute.Key, redact.Error(err, handler.secrets...))
		}
		return slog.String(attribute.Key, redact.Replacement)
	case slog.KindGroup:
		members := attribute.Value.Group()
		clean := make([]slog.Attr, 0, len(members))
		for _, member := range members {
			clean = append(clean, handler.redactAttribute(member))
		}
		return slog.Attr{Key: attribute.Key, Value: slog.GroupValue(clean...)}
	}
	return attribute
}

func sensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	switch normalized {
	case "password", "passwd", "pwd", "token", "secret", "clientsecret", "credential", "apikey", "accesskey", "privatekey", "dsn", "databaseurl", "connectionstring":
		return true
	}
	return strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "secret") ||
		strings.HasSuffix(normalized, "token") ||
		strings.HasSuffix(normalized, "dsn")
}
