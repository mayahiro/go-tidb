package queryshape

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBoundJSONPreservesOnlyPresenceAndPositiveClassification(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Bound{Set: true, Positive: true, Value: 500})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if got, want := string(encoded), `{"set":true,"positive":true}`; got != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
	if strings.Contains(string(encoded), "500") {
		t.Fatalf("JSON exposed bound value: %s", encoded)
	}

	var decoded Bound
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !decoded.Set || !decoded.Positive || decoded.Value != 0 {
		t.Fatalf("decoded Bound = %#v", decoded)
	}
}
