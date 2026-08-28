// Package check defines diagnostics shared by schema, query, plan, runtime,
// and migration checks.
package check

// Severity describes how a diagnostic affects command execution.
type Severity string

const (
	// SeverityInfo reports useful information that does not block execution.
	SeverityInfo Severity = "info"
	// SeverityWarning reports a potential problem that does not block execution
	// under the default policy.
	SeverityWarning Severity = "warning"
	// SeverityError reports a problem that blocks execution under the default
	// policy.
	SeverityError Severity = "error"
)

// Location identifies a source position associated with a diagnostic.
// Line and Column are one-based when present and zero when unknown.
type Location struct {
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// Evidence records one fact supporting a diagnostic.
type Evidence struct {
	Message  string   `json:"message"`
	Location Location `json:"location,omitempty"`
}

// Diagnostic is the stable base representation produced by tidbgo checks.
type Diagnostic struct {
	Code         string     `json:"code"`
	Severity     Severity   `json:"severity"`
	Title        string     `json:"title"`
	Message      string     `json:"message"`
	Evidence     []Evidence `json:"evidence,omitempty"`
	Suggestion   string     `json:"suggestion,omitempty"`
	Location     Location   `json:"location,omitempty"`
	Suppressible bool       `json:"suppressible"`
	Reference    string     `json:"reference,omitempty"`
}
