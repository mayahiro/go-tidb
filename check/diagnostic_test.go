package check

import (
	"encoding/json"
	"testing"
)

func TestDiagnosticJSONUsesStableFieldNames(t *testing.T) {
	t.Parallel()

	diagnostic := Diagnostic{
		Code:     "TEST001",
		Severity: SeverityError,
		Title:    "Example diagnostic",
		Message:  "example diagnostic message",
		Location: Location{Path: "models/user.go", Line: 10, Column: 2},
	}

	data, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	const want = `{"code":"TEST001","severity":"error","title":"Example diagnostic","message":"example diagnostic message","location":{"path":"models/user.go","line":10,"column":2},"suppressible":false}`
	if string(data) != want {
		t.Fatalf("Marshal() = %s, want %s", data, want)
	}
}
