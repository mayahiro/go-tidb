package orm

type preloadOptionKind uint8

const (
	preloadOptionFields preloadOptionKind = iota + 1
	preloadOptionOrderBy
	preloadOptionWithDeleted
)

// PreloadOption configures one relation requested through Preload.
// Values are created by PreloadFields, PreloadOrderBy, or PreloadWithDeleted.
type PreloadOption struct {
	kind    preloadOptionKind
	fields  []string
	orderBy []orderTerm
}

// PreloadFields limits a relation projection to the named target-model Go fields.
// Required relation keys are added automatically.
func PreloadFields(fields ...string) PreloadOption {
	return PreloadOption{kind: preloadOptionFields, fields: append([]string(nil), fields...)}
}

// PreloadOrderBy orders a relation collection by target-model Go fields.
func PreloadOrderBy(terms ...OrderTerm) PreloadOption {
	values := make([]orderTerm, len(terms))
	for index := range terms {
		values[index] = terms[index].value
	}
	return PreloadOption{kind: preloadOptionOrderBy, orderBy: values}
}

// PreloadWithDeleted includes logically deleted rows for one requested
// relation path. Other relation paths remain independently filtered.
func PreloadWithDeleted() PreloadOption {
	return PreloadOption{kind: preloadOptionWithDeleted}
}

type preloadRequest struct {
	path    string
	options []PreloadOption
}
