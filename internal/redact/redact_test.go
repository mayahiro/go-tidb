package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestDSNAlwaysRedactsTheCompleteValue(t *testing.T) {
	t.Parallel()

	const dsn = "app:super-secret@tcp(example.invalid:4000)/production"
	if got := DSN(dsn); got != Replacement {
		t.Fatalf("DSN() = %q, want %q", got, Replacement)
	}
}

func TestStringRedactsExplicitSecrets(t *testing.T) {
	t.Parallel()

	const (
		dsn      = "app:super-secret@tcp(example.invalid:4000)/production"
		password = "super-secret"
	)
	got := String("connection failed for "+dsn+" password="+password, dsn, password)
	if strings.Contains(got, dsn) || strings.Contains(got, password) {
		t.Fatalf("String() disclosed a secret: %q", got)
	}
	if !strings.Contains(got, Replacement) {
		t.Fatalf("String() = %q, want replacement marker", got)
	}
}

func TestStringRedactsStructuredCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		secret string
	}{
		{name: "password", input: "password=hunter2", secret: "hunter2"},
		{name: "token", input: `{"token":"token-value"}`, secret: "token-value"},
		{name: "prefixed token", input: "oauth_access_token=access-token-value", secret: "access-token-value"},
		{name: "quoted punctuation", input: `{"client_secret":"alpha,beta;gamma"}`, secret: "alpha,beta;gamma"},
		{name: "escaped quote", input: `{"token":"alpha\"beta"}`, secret: `alpha\"beta`},
		{name: "single quoted", input: "credential='alpha beta;gamma'", secret: "alpha beta;gamma"},
		{name: "api key", input: "api_key: abc123", secret: "abc123"},
		{name: "URL", input: "mysql://app:url-password@example.invalid/database", secret: "url-password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := String(tt.input)
			if strings.Contains(got, tt.secret) {
				t.Fatalf("String() disclosed %q in %q", tt.secret, got)
			}
			if !strings.Contains(got, Replacement) {
				t.Fatalf("String() = %q, want replacement marker", got)
			}
		})
	}
}

func TestError(t *testing.T) {
	t.Parallel()

	if got := Error(nil); got != "" {
		t.Fatalf("Error(nil) = %q", got)
	}
	const secret = "private-value"
	if got := Error(errors.New("failed with "+secret), secret); strings.Contains(got, secret) {
		t.Fatalf("Error() disclosed a secret: %q", got)
	}
}
