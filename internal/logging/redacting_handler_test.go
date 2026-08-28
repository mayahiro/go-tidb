package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactingHandlerRemovesSecrets(t *testing.T) {
	t.Parallel()

	const (
		dsn      = "app:dsn-password@tcp(example.invalid:4000)/app"
		password = "attribute-password"
		token    = "message-token"
	)
	var output bytes.Buffer
	handler := NewRedactingHandler(slog.NewJSONHandler(&output, nil), dsn, token)
	logger := slog.New(handler)
	logger.ErrorContext(
		context.Background(),
		"connection failed with "+token,
		"dsn", dsn,
		"password", password,
		"cause", errors.New("driver error for "+dsn),
		"metadata", slog.GroupValue(slog.String("api_key", "nested-key")),
		"config", struct{ DSN string }{DSN: dsn},
	)
	logger.With("auth_token", "with-attribute-secret").WithGroup("request").Info("safe message", "query_count", 2)

	logged := output.String()
	for _, secret := range []string{dsn, password, token, "nested-key", "dsn-password", "with-attribute-secret"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log output disclosed %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "[REDACTED]") {
		t.Fatalf("log output has no redaction marker: %s", logged)
	}
}

func TestRedactingHandlerPreservesSafeAttributes(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewTextHandler(&output, nil)))
	logger.Info("query completed", "fingerprint", "sha256:abc", "query_count", 3)

	logged := output.String()
	if !strings.Contains(logged, "fingerprint=sha256:abc") || !strings.Contains(logged, "query_count=3") {
		t.Fatalf("safe attributes were not preserved: %s", logged)
	}
}
