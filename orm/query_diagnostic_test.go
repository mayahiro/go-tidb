package orm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mayahiro/go-tidb/check"
)

func TestSelectQueryDiagnosticsReturnsNonNilEmptyForSafeBuilder(t *testing.T) {
	t.Parallel()

	diagnostics := Query[scanModel]().
		Select("ID", "Name").
		Where(Equal("ID", uint64(7))).
		OrderBy(Asc("ID")).
		Diagnostics()
	if diagnostics == nil || len(diagnostics) != 0 {
		t.Fatalf("Diagnostics() = %#v, want non-nil empty", diagnostics)
	}
}

func TestSelectQueryDiagnosticsConvertsBuildError(t *testing.T) {
	t.Parallel()

	diagnostics := Query[scanModel]().Select("Missing").Diagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("Diagnostics() = %#v, want one", diagnostics)
	}
	diagnostic := diagnostics[0]
	if diagnostic.Code != codeInvalidQuery || diagnostic.Severity != check.SeverityError || diagnostic.Suppressible {
		t.Fatalf("diagnostic = %#v, want non-suppressible QRY001 error", diagnostic)
	}
	if !strings.Contains(diagnostic.Message, "not a mapped scalar field") {
		t.Fatalf("message = %q", diagnostic.Message)
	}
}

func TestNilSelectQueryDiagnosticsConvertsBuildError(t *testing.T) {
	t.Parallel()

	var query *SelectQuery[scanModel]
	diagnostics := query.Diagnostics()
	if len(diagnostics) != 1 || diagnostics[0].Code != codeInvalidQuery || !strings.Contains(diagnostics[0].Message, "nil SELECT query") {
		t.Fatalf("Diagnostics() = %#v, want nil-query QRY001", diagnostics)
	}
}

func TestSelectQueryDiagnosticsReportsOffsetAndUnorderedPagination(t *testing.T) {
	t.Parallel()

	diagnostics := Query[scanModel]().Limit(50).Offset(10).Diagnostics()
	wantCodes := []string{codeOffsetPagination, codeUnorderedPagination}
	if got := queryDiagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if !strings.Contains(diagnostics[0].Message, "skips 10 rows") {
		t.Fatalf("offset message = %q", diagnostics[0].Message)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != check.SeverityWarning || !diagnostic.Suppressible || diagnostic.Reference != paginationReference {
			t.Fatalf("diagnostic = %#v, want suppressible pagination warning", diagnostic)
		}
	}
}

func TestSelectQueryDiagnosticsHandlesExplicitZeroPagination(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]*SelectQuery[scanModel]{
		"zero limit and offset": Query[scanModel]().Limit(0).Offset(100),
		"ordered first page":    Query[scanModel]().OrderBy(Asc("ID")).Limit(50).Offset(0),
	} {
		if diagnostics := query.Diagnostics(); len(diagnostics) != 0 {
			t.Fatalf("%s Diagnostics() = %#v, want none", name, diagnostics)
		}
	}
}

func TestSelectQueryDiagnosticsReportsEveryLeadingWildcardWithoutValues(t *testing.T) {
	t.Parallel()

	diagnostics := Query[preloadUser]().Where(
		Or(
			Contains("Email", "private-email"),
			Has("Orders", Not(HasSuffix("Total", "private-total"))),
		),
	).Diagnostics()
	wantCodes := []string{codeLeadingWildcardFilter, codeLeadingWildcardFilter}
	if got := queryDiagnosticCodes(diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %#v, want %#v", got, wantCodes)
	}
	if !strings.Contains(diagnostics[0].Message, "preloadUser.Email") || !strings.Contains(diagnostics[1].Message, "preloadUser.Orders.Total") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "private-") {
			t.Fatalf("diagnostic exposed bind value: %#v", diagnostic)
		}
		if diagnostic.Reference != likeReference {
			t.Fatalf("reference = %q, want %q", diagnostic.Reference, likeReference)
		}
	}
}

func TestSelectQueryDiagnosticsDoesNotReportPrefixPattern(t *testing.T) {
	t.Parallel()

	diagnostics := Query[scanModel]().Where(HasPrefix("Name", "Ada")).Diagnostics()
	if len(diagnostics) != 0 {
		t.Fatalf("Diagnostics() = %#v, want none", diagnostics)
	}
}

func TestSelectQueryDiagnosticsReturnsDetachedValues(t *testing.T) {
	t.Parallel()

	query := Query[scanModel]().Limit(10)
	first := query.Diagnostics()
	first[0].Code = "CHANGED"
	second := query.Diagnostics()
	if second[0].Code != codeUnorderedPagination {
		t.Fatalf("second Diagnostics() = %#v", second)
	}
}

func BenchmarkSelectQueryDiagnostics(b *testing.B) {
	query := Query[scanModel]().
		Select("ID", "Name").
		Where(Contains("Name", "Ada")).
		Limit(100).
		Offset(20)
	var diagnostics []check.Diagnostic
	b.ReportAllocs()
	for b.Loop() {
		diagnostics = query.Diagnostics()
	}
	queryDiagnosticSink = diagnostics
}

var queryDiagnosticSink []check.Diagnostic

func queryDiagnosticCodes(diagnostics []check.Diagnostic) []string {
	codes := make([]string, len(diagnostics))
	for index := range diagnostics {
		codes[index] = diagnostics[index].Code
	}
	return codes
}
