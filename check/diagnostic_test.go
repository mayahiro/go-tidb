package check

import (
	"encoding/json"
	"testing"
)

func TestDiagnosticJSONUsesStableFieldNames(t *testing.T) {
	t.Parallel()

	diagnostic := Diagnostic{
		Code:     "SCH001",
		Severity: SeverityError,
		Title:    "Primary key required",
		Message:  "entity User has no primary key",
		Location: Location{Path: "db/schema/main.go", Line: 10, Column: 2},
	}

	data, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	const want = `{"code":"SCH001","severity":"error","title":"Primary key required","message":"entity User has no primary key","location":{"path":"db/schema/main.go","line":10,"column":2},"suppressible":false}`
	if string(data) != want {
		t.Fatalf("Marshal() = %s, want %s", data, want)
	}
}
