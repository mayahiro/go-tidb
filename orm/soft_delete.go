package orm

import (
	"fmt"
	"strings"

	"github.com/mayahiro/go-tidb/model"
)

const softDeleteCurrentTimestamp = "CURRENT_TIMESTAMP(6)"

func validateWithDeleted(descriptor *model.Descriptor, withDeleted bool, operation string) error {
	if !withDeleted {
		return nil
	}
	if _, exists := descriptor.SoftDeleteField(); !exists {
		return fmt.Errorf("orm: %s WithDeleted requires a soft-delete field on %s", operation, descriptor.Name())
	}
	return nil
}

func activeSoftDeleteField(descriptor *model.Descriptor, withDeleted bool) (model.Field, bool) {
	if withDeleted {
		return model.Field{}, false
	}
	return descriptor.SoftDeleteField()
}

func writeActiveSoftDeletePredicate(query *strings.Builder, qualifier string, field model.Field) {
	writeMaybeQualifiedIdentifier(query, qualifier, field.ColumnName())
	query.WriteString(" IS NULL")
}
