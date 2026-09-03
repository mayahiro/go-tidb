package modelmeta

import (
	"strings"
	"testing"
)

func TestSnakeCase(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"User":        "user",
		"UserID":      "user_id",
		"URLValue":    "url_value",
		"HTTP2Client": "http2_client",
	}
	for input, want := range tests {
		input, want := input, want
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if got := SnakeCase(input); got != want {
				t.Fatalf("SnakeCase(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestValidSQLIdentifier(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"users", "User2", "_private"} {
		if !ValidSQLIdentifier(value) {
			t.Fatalf("ValidSQLIdentifier(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "2users", "user-name", "利用者"} {
		if ValidSQLIdentifier(value) {
			t.Fatalf("ValidSQLIdentifier(%q) = true, want false", value)
		}
	}
}

func TestParseModelTags(t *testing.T) {
	t.Parallel()

	field, err := ParseField("UserID", ",pk,auto_random", true)
	if err != nil {
		t.Fatalf("ParseField() error = %v", err)
	}
	if field.Column != "user_id" || !field.PrimaryKey || !field.AutoRandom || field.ExplicitColumn {
		t.Fatalf("ParseField() = %#v", field)
	}
	explicit, err := ParseField("UserID", "user_id,soft_delete", true)
	if err != nil || explicit.Column != "user_id" || !explicit.ExplicitColumn || !explicit.SoftDelete {
		t.Fatalf("ParseField(explicit) = %#v, %v", explicit, err)
	}
	if _, err := ParseField("ID", ",pk,pk", true); err == nil || !strings.Contains(err.Error(), "must not be repeated") {
		t.Fatalf("ParseField(repeated) error = %v", err)
	}
	if ignored, err := ParseIgnore("-", true); err != nil || !ignored {
		t.Fatalf("ParseIgnore() = %t, %v", ignored, err)
	}
	if _, err := ParseIgnore("id,-", true); err == nil {
		t.Fatal("ParseIgnore(combined) error = nil")
	}
	if table, err := ParseTable("table=user_accounts", true); err != nil || table != "user_accounts" {
		t.Fatalf("ParseTable() = %q, %v", table, err)
	}
	if _, err := ParseTable("table=bad-name", true); err == nil {
		t.Fatal("ParseTable(invalid) error = nil")
	}
}
