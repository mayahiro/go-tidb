package check

import (
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type schemaViaParent struct {
	model.Meta `tidbgo:"table=via_parents"`
	ID         int64             `tidbgo:",pk"`
	Edges      []schemaViaEdge   `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []schemaViaTarget `tidbgo:"many_to_many,via=Edges.Target"`
}
type schemaViaEdge struct {
	model.Meta `tidbgo:"table=via_edges"`
	ID         int64 `tidbgo:",pk,auto_random"`
	ParentID   int64
	TargetID   int64
	Priority   int
	DeletedAt  *time.Time       `tidbgo:",soft_delete"`
	Target     *schemaViaTarget `tidbgo:"belongs_to"`
}
type schemaViaTarget struct {
	model.Meta `tidbgo:"table=via_targets"`
	ID         int64 `tidbgo:",pk"`
}
type schemaPureParent struct {
	model.Meta `tidbgo:"table=via_parents"`
	ID         int64             `tidbgo:",pk"`
	Targets    []schemaViaTarget `tidbgo:"many_to_many,through=via_edges,source=ID:parent_id,target=target_id:ID"`
}

const schemaViaSQL = `
CREATE TABLE via_parents (id BIGINT PRIMARY KEY);
CREATE TABLE via_targets (id BIGINT PRIMARY KEY);
CREATE TABLE via_edges (
 id BIGINT PRIMARY KEY AUTO_RANDOM,
 parent_id BIGINT NOT NULL,
 target_id BIGINT NOT NULL,
 priority BIGINT NOT NULL,
 deleted_at DATETIME NULL,
 KEY parent_position (parent_id, priority)
);`

func TestSchemaViaAcceptsRequiredPayloadWithoutPairUniqueness(t *testing.T) {
	t.Parallel()
	catalog := parseSchemaCheckCatalog(t, schemaViaSQL)
	if diagnostics := Schema[schemaViaParent](catalog); len(diagnostics) != 0 {
		t.Fatalf("parent = %#v", diagnostics)
	}
	if diagnostics := Schema[schemaViaEdge](catalog); len(diagnostics) != 0 {
		t.Fatalf("edge = %#v", diagnostics)
	}
	pure := Schema[schemaPureParent](catalog)
	codes := strings.Join(diagnosticCodes(pure), ",")
	if !strings.Contains(codes, codeJunctionPairNotUnique) || !strings.Contains(codes, codeRequiredJunctionColumn) {
		t.Fatalf("pure junction = %#v", pure)
	}
}

func TestSchemaViaStillValidatesUsedColumnsAndIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ from, to, code string }{
		{"deleted_at DATETIME NULL", "removed_at DATETIME NULL", codeMissingPhysicalColumn},
		{"deleted_at DATETIME NULL", "deleted_at BIGINT NULL", codeIncompatibleColumnType},
		{"target_id BIGINT", "target_id VARCHAR(20)", codeIncompatibleColumnType},
		{"via_targets (id BIGINT PRIMARY KEY)", "via_targets (id BIGINT NOT NULL)", codeRelationTargetNotUnique},
		{"KEY parent_position (parent_id, priority)", "KEY target_position (target_id, priority)", codeMissingRelationIndex},
	} {
		catalog := parseSchemaCheckCatalog(t, strings.Replace(schemaViaSQL, test.from, test.to, 1))
		diagnostics := Schema[schemaViaParent](catalog)
		if !strings.Contains(strings.Join(diagnosticCodes(diagnostics), ","), test.code) {
			t.Fatalf("%s: %#v", test.from, diagnostics)
		}
	}
}

type schemaViaUniqueParent struct {
	model.Meta `tidbgo:"table=via_parents"`
	ID         int64                 `tidbgo:",pk"`
	Edges      []schemaViaUniqueEdge `tidbgo:"has_many,join=ID:ParentID"`
	Targets    []schemaViaTarget     `tidbgo:"many_to_many,via=Edges.Target"`
}

type schemaViaUniqueEdge struct {
	model.Meta `tidbgo:"table=via_edges"`
	ID         int64 `tidbgo:",pk,auto_random"`
	ParentID   int64 `tidbgo:",unique=pair"`
	TargetID   int64 `tidbgo:",unique=pair"`
	Priority   int
	DeletedAt  *time.Time       `tidbgo:",soft_delete"`
	Target     *schemaViaTarget `tidbgo:"belongs_to"`
}

func TestSchemaViaChecksEdgeCardinalityClaimsFromParent(t *testing.T) {
	t.Parallel()
	diagnostics := Schema[schemaViaUniqueParent](parseSchemaCheckCatalog(t, schemaViaSQL))
	if len(diagnostics) != 1 || diagnostics[0].Code != codeCandidateKeyMismatch || diagnostics[0].Suppressible {
		t.Fatalf("missing edge unique key: %#v", diagnostics)
	}
	valid := strings.Replace(schemaViaSQL, "KEY parent_position", "UNIQUE KEY pair_key (parent_id, target_id), KEY parent_position", 1)
	if diagnostics := Schema[schemaViaUniqueParent](parseSchemaCheckCatalog(t, valid)); len(diagnostics) != 0 {
		t.Fatalf("constrained edge: %#v", diagnostics)
	}
	invalidPK := strings.Replace(valid, "id BIGINT PRIMARY KEY AUTO_RANDOM", "id BIGINT NOT NULL AUTO_RANDOM, PRIMARY KEY (parent_id, target_id)", 1)
	if codes := strings.Join(diagnosticCodes(Schema[schemaViaUniqueEdge](parseSchemaCheckCatalog(t, invalidPK))), ","); !strings.Contains(codes, codePrimaryKeyMismatch) {
		t.Fatalf("missing primary-key mismatch: %s", codes)
	}
}
