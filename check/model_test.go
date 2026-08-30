package check

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/model"
)

type checkedModel struct {
	model.Meta `tidbgo:"table=checked_models"`
	ID         int64  `tidbgo:"id,pk,auto_random"`
	Name       string `tidbgo:"name"`
}

type invalidCheckedModel struct {
	First       string `tidbgo:"same"`
	Second      string `tidbgo:"same"`
	Unsupported map[string]string
}

type taggedCheckedModel struct {
	ID      int64  `db:"legacy_id" tidbgo:",pk"`
	Option  string `tidbgo:"pkk"`
	private string `tidbgo:"private"`
}

type explicitlyNamedCheckedModel struct {
	ID  int64  `tidbgo:",pk"`
	PKK string `tidbgo:"pkk"`
}

type relatedCheckedModel struct {
	ID              int64 `tidbgo:",pk"`
	CheckedModelID  int64
	CheckedModelRef *checkedModel `tidbgo:"belongs_to,join=CheckedModelID:ID"`
}

type keylessCheckedModel struct {
	Name string
}

type scannerOnlyCheckedValue struct{}

func (*scannerOnlyCheckedValue) Scan(any) error {
	panic("check.Model must not call Scan")
}

type valuerOnlyCheckedValue struct{}

func (valuerOnlyCheckedValue) Value() (driver.Value, error) {
	panic("check.Model must not call Value")
}

type capabilityCheckedModel struct {
	ID       int64 `tidbgo:",pk"`
	Readable scannerOnlyCheckedValue
	Writable valuerOnlyCheckedValue
	Computed scannerOnlyCheckedValue `tidbgo:",computed"`
}

func TestModelReturnsNoDiagnosticsForValidModel(t *testing.T) {
	t.Parallel()

	for name, diagnostics := range map[string][]Diagnostic{
		"value":   Model[checkedModel](),
		"pointer": Model[**checkedModel](),
		"type":    ModelType(reflect.TypeFor[checkedModel]()),
	} {
		if diagnostics == nil || len(diagnostics) != 0 {
			t.Fatalf("%s diagnostics = %#v, want non-nil empty", name, diagnostics)
		}
	}
}

func TestModelConvertsEveryMetadataIssueToAnErrorDiagnostic(t *testing.T) {
	t.Parallel()

	diagnostics := Model[invalidCheckedModel]()
	if got, want := diagnosticCodes(diagnostics), []string{codeInvalidModel, codeInvalidModel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %#v, want %#v", got, want)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != SeverityError || diagnostic.Suppressible {
			t.Fatalf("diagnostic = %#v, want non-suppressible error", diagnostic)
		}
		if len(diagnostic.Evidence) != 1 || !strings.HasPrefix(diagnostic.Evidence[0].Message, "Go field: invalidCheckedModel.") {
			t.Fatalf("evidence = %#v", diagnostic.Evidence)
		}
	}
}

func TestModelRejectsInvalidModelTypeAsDiagnostic(t *testing.T) {
	t.Parallel()

	for name, diagnostics := range map[string][]Diagnostic{
		"scalar": Model[int](),
		"nil":    ModelType(nil),
	} {
		if len(diagnostics) != 1 || diagnostics[0].Code != codeInvalidModel || diagnostics[0].Severity != SeverityError {
			t.Fatalf("%s diagnostics = %#v, want one invalid-model error", name, diagnostics)
		}
	}
}

func TestModelReportsIgnoredAndLikelyMisplacedTagsInDeclarationOrder(t *testing.T) {
	t.Parallel()

	diagnostics := Model[taggedCheckedModel]()
	wantCodes := []string{codeIgnoredDBTag, codeLikelyMisplacedOption, codeUnexportedModelTag}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if got := diagnostics[1].Message; !strings.Contains(got, `uses "pkk" as its column name`) || !strings.Contains(got, `"pk" option`) {
		t.Fatalf("misplaced-option message = %q", got)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != SeverityWarning || !diagnostic.Suppressible {
			t.Fatalf("diagnostic = %#v, want suppressible warning", diagnostic)
		}
	}
}

func TestModelDoesNotGuessExplicitDefaultColumnOrRelationTag(t *testing.T) {
	t.Parallel()

	for name, diagnostics := range map[string][]Diagnostic{
		"explicit default": Model[explicitlyNamedCheckedModel](),
		"relation":         Model[relatedCheckedModel](),
	} {
		if len(diagnostics) != 0 {
			t.Fatalf("%s diagnostics = %#v, want none", name, diagnostics)
		}
	}
}

func TestModelReportsMissingPrimaryKeyAsInformation(t *testing.T) {
	t.Parallel()

	diagnostics := Model[keylessCheckedModel]()
	if len(diagnostics) != 1 || diagnostics[0].Code != codeMissingPrimaryKey || diagnostics[0].Severity != SeverityInfo || !diagnostics[0].Suppressible {
		t.Fatalf("diagnostics = %#v, want one suppressible info diagnostic", diagnostics)
	}
}

func TestModelReportsOneWayCustomFieldCapabilities(t *testing.T) {
	t.Parallel()

	diagnostics := Model[capabilityCheckedModel]()
	wantCodes := []string{codeReadOnlyCustomField, codeWriteOnlyCustomField}
	if got := diagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if !strings.Contains(diagnostics[0].Message, "capabilityCheckedModel.Readable") {
		t.Fatalf("read-only message = %q", diagnostics[0].Message)
	}
	if !strings.Contains(diagnostics[1].Message, "capabilityCheckedModel.Writable") {
		t.Fatalf("write-only message = %q", diagnostics[1].Message)
	}
}

func TestModelReturnsDetachedDiagnostics(t *testing.T) {
	t.Parallel()

	first := Model[taggedCheckedModel]()
	first[0].Code = "CHANGED"
	second := Model[taggedCheckedModel]()
	if second[0].Code != codeIgnoredDBTag {
		t.Fatalf("second diagnostics = %#v", second)
	}
}

func TestWithinOneEdit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "equal", left: "pk", right: "pk", want: true},
		{name: "substitution", left: "p_", right: "pk", want: true},
		{name: "insertion", left: "pkk", right: "pk", want: true},
		{name: "deletion", left: "p", right: "pk", want: true},
		{name: "transposition is two edits", left: "kp", right: "pk"},
		{name: "different", left: "column", right: "pk"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := withinOneEdit(test.left, test.right); got != test.want {
				t.Fatalf("withinOneEdit(%q, %q) = %t, want %t", test.left, test.right, got, test.want)
			}
		})
	}
}

func BenchmarkModel(b *testing.B) {
	var diagnostics []Diagnostic
	b.ReportAllocs()
	for b.Loop() {
		diagnostics = Model[checkedModel]()
	}
	modelDiagnosticSink = diagnostics
}

var modelDiagnosticSink []Diagnostic

func diagnosticCodes(diagnostics []Diagnostic) []string {
	codes := make([]string, len(diagnostics))
	for index := range diagnostics {
		codes[index] = diagnostics[index].Code
	}
	return codes
}
