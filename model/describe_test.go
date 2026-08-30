package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type UserID uint64

type AuditFields struct {
	CreatedAt time.Time `tidbgo:"created_at"`
	UpdatedAt *time.Time
	hidden    string
}

type User struct {
	Meta        `tidbgo:"table=users"`
	ID          UserID `tidbgo:",pk"`
	DisplayName string `tidbgo:"display_name"`
	URLValue    string
	Metadata    json.RawMessage
	Password    string `tidbgo:"-"`
	AuditFields
}

func (User) TableName() string {
	panic("Describe must not execute user methods")
}

func TestDescribeMapsScalarFieldsWithoutExecutingUserCode(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[User]()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	if descriptor.Type() != reflect.TypeFor[User]() || descriptor.Name() != "User" {
		t.Fatalf("descriptor identity = %v %q", descriptor.Type(), descriptor.Name())
	}
	if descriptor.TableName() != "users" {
		t.Fatalf("TableName() = %q, want %q", descriptor.TableName(), "users")
	}

	fields := descriptor.Fields()
	if got, want := columnNames(fields), []string{"id", "display_name", "url_value", "metadata", "created_at", "updated_at"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("column names = %#v, want %#v", got, want)
	}
	if fields[0].Kind() != KindUint || fields[0].GoType() != reflect.TypeFor[UserID]() {
		t.Fatalf("ID metadata = %#v", fields[0])
	}
	if primaryKey := descriptor.PrimaryKeyFields(); len(primaryKey) != 1 || primaryKey[0].GoName() != "ID" {
		t.Fatalf("primary key = %#v, want ID", primaryKey)
	}
	if fields[3].Kind() != KindBytes {
		t.Fatalf("Metadata kind = %v, want KindBytes", fields[3].Kind())
	}
	if fields[4].Kind() != KindTime || fields[5].PointerDepth() != 1 {
		t.Fatalf("audit metadata = %#v %#v", fields[4], fields[5])
	}
	if got, want := fields[4].Index(), []int{6, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CreatedAt index = %#v, want %#v", got, want)
	}
	if _, ok := descriptor.FieldByColumn("password"); ok {
		t.Fatal("ignored password field was mapped")
	}
}

type APIKey struct {
	ID string `tidbgo:"id,pk"`
}

type LogEntry struct {
	Message string
}

type PositionalColumn struct {
	ID uint64 `tidbgo:"pkk"`
}

type Membership struct {
	Meta           `tidbgo:"table=memberships"`
	OrganizationID uint64 `tidbgo:"organization_id,pk"`
	UserID         uint64 `tidbgo:",pk"`
	Role           string
}

func TestDescribeMapsDefaultTableAndOrderedPrimaryKeys(t *testing.T) {
	t.Parallel()

	defaultTable, err := Describe[APIKey]()
	if err != nil {
		t.Fatalf("Describe[APIKey]() error = %v", err)
	}
	if defaultTable.TableName() != "api_key" {
		t.Fatalf("APIKey table = %q, want %q", defaultTable.TableName(), "api_key")
	}
	if got, want := columnNames(defaultTable.PrimaryKeyFields()), []string{"id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("APIKey primary key = %#v, want %#v", got, want)
	}
	withoutPrimaryKey, err := Describe[LogEntry]()
	if err != nil {
		t.Fatalf("Describe[LogEntry]() error = %v", err)
	}
	if primaryKey := withoutPrimaryKey.PrimaryKeyFields(); len(primaryKey) != 0 {
		t.Fatalf("LogEntry primary key = %#v, want none", primaryKey)
	}

	descriptor, err := Describe[Membership]()
	if err != nil {
		t.Fatalf("Describe[Membership]() error = %v", err)
	}
	primaryKey := descriptor.PrimaryKeyFields()
	if got, want := columnNames(primaryKey), []string{"organization_id", "user_id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary key = %#v, want %#v", got, want)
	}
	for _, field := range primaryKey {
		if !field.IsPrimaryKey() {
			t.Fatalf("primary-key field metadata = %#v", field)
		}
	}
	role, exists := descriptor.FieldByColumn("role")
	if !exists || role.IsPrimaryKey() {
		t.Fatalf("role metadata = %#v, exists = %t", role, exists)
	}
}

type MutationMetadata struct {
	ID         int64 `tidbgo:",pk,auto_random"`
	Name       string
	VideoCount int `tidbgo:"video_count,computed"`
}

func TestDescribeMapsAutoRandomAndComputedFields(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[MutationMetadata]()
	if err != nil {
		t.Fatalf("Describe[MutationMetadata]() error = %v", err)
	}
	id, exists := descriptor.FieldByGoName("ID")
	if !exists || !id.IsPrimaryKey() || !id.IsAutoRandom() || id.IsComputed() {
		t.Fatalf("ID metadata = %#v, exists = %t", id, exists)
	}
	videoCount, exists := descriptor.FieldByGoName("VideoCount")
	if !exists || videoCount.IsPrimaryKey() || videoCount.IsAutoRandom() || !videoCount.IsComputed() {
		t.Fatalf("VideoCount metadata = %#v, exists = %t", videoCount, exists)
	}
}

type SoftDeleteValueModel struct {
	ID        int64     `tidbgo:",pk"`
	DeletedAt time.Time `tidbgo:",soft_delete"`
}

type SoftDeletePointerModel struct {
	ID        int64      `tidbgo:",pk"`
	DeletedAt *time.Time `tidbgo:"removed_at,soft_delete"`
}

func TestDescribeMapsOneTimeSoftDeleteField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		descriptor *Descriptor
		column     string
		pointer    int
	}{
		{name: "value", descriptor: mustDescribe[SoftDeleteValueModel](t), column: "deleted_at"},
		{name: "pointer", descriptor: mustDescribe[SoftDeletePointerModel](t), column: "removed_at", pointer: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, exists := tt.descriptor.SoftDeleteField()
			if !exists || !field.IsSoftDelete() || field.Kind() != KindTime || field.ColumnName() != tt.column || field.PointerDepth() != tt.pointer {
				t.Fatalf("SoftDeleteField() = %#v, %t", field, exists)
			}
			mapped, mappedExists := tt.descriptor.FieldByGoName("DeletedAt")
			if !mappedExists || !mapped.IsSoftDelete() {
				t.Fatalf("DeletedAt metadata = %#v, %t", mapped, mappedExists)
			}
		})
	}

	withoutSoftDelete, err := Describe[LogEntry]()
	if err != nil {
		t.Fatalf("Describe[LogEntry]() error = %v", err)
	}
	if field, exists := withoutSoftDelete.SoftDeleteField(); exists {
		t.Fatalf("LogEntry SoftDeleteField() = %#v, want none", field)
	}
}

func TestDescribeTreatsFirstScalarTagValueAsColumnName(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[PositionalColumn]()
	if err != nil {
		t.Fatalf("Describe[PositionalColumn]() error = %v", err)
	}
	if got, want := columnNames(descriptor.Fields()), []string{"pkk"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %#v, want %#v", got, want)
	}
	if primaryKey := descriptor.PrimaryKeyFields(); len(primaryKey) != 0 {
		t.Fatalf("primary key = %#v, want none", primaryKey)
	}
}

type CustomDecimal struct{}

func (*CustomDecimal) Scan(any) error { return nil }

func (CustomDecimal) Value() (driver.Value, error) { return "0", nil }

type CustomValues struct {
	Amount   CustomDecimal
	Optional *CustomDecimal
}

func TestDescribeRecognizesScannerAndValuerWithoutLibraryDependency(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[CustomValues]()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	fields := descriptor.Fields()
	if len(fields) != 2 {
		t.Fatalf("fields = %#v", fields)
	}
	for _, field := range fields {
		if field.Kind() != KindCustom || !field.UsesScanner() || !field.UsesValuer() || !field.CanScan() || !field.CanValue() {
			t.Fatalf("custom field metadata = %#v", field)
		}
	}
	if fields[1].PointerDepth() != 1 || fields[1].BaseType() != reflect.TypeFor[CustomDecimal]() {
		t.Fatalf("optional custom field = %#v", fields[1])
	}
}

type CachedModel struct {
	ID uint64
}

func TestDescribeCachesByNonPointerStructType(t *testing.T) {
	t.Parallel()

	first, err := Describe[CachedModel]()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	second, err := Describe[*CachedModel]()
	if err != nil {
		t.Fatalf("Describe() pointer error = %v", err)
	}
	if first != second {
		t.Fatal("Describe() did not return the cached descriptor")
	}

	const workers = 32
	results := make(chan *Descriptor, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			descriptor, describeErr := Describe[CachedModel]()
			if describeErr != nil {
				t.Errorf("Describe() error = %v", describeErr)
				return
			}
			results <- descriptor
		}()
	}
	group.Wait()
	close(results)
	for descriptor := range results {
		if descriptor != first {
			t.Fatal("concurrent Describe() returned a different descriptor")
		}
	}
}

func TestDescriptorAccessorsDoNotExposeCachedSlices(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[User]()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	fields := descriptor.Fields()
	index := fields[0].Index()
	index[0] = 99
	fields[0] = Field{}

	again := descriptor.Fields()
	if again[0].GoName() != "ID" || again[0].Index()[0] != 1 {
		t.Fatalf("cached fields were mutated = %#v", again[0])
	}

	primaryKey := descriptor.PrimaryKeyFields()
	primaryKey[0].index[0] = 99
	if againPrimaryKey := descriptor.PrimaryKeyFields(); len(againPrimaryKey) != 1 || againPrimaryKey[0].Index()[0] != 1 {
		t.Fatalf("cached primary key was mutated = %#v", againPrimaryKey)
	}
}

type DuplicateColumns struct {
	First  string `tidbgo:"same"`
	Second string `tidbgo:"same"`
}

type UnsupportedField struct {
	Values map[string]string
}

type InvalidTag struct {
	Value string `tidbgo:"not-valid"`
}

type UnsupportedOption struct {
	Value string `tidbgo:"value,primary"`
}

type EmptyModel struct{}

type RecursiveModel struct {
	*RecursiveModel
}

type InvalidTable struct {
	Meta `tidbgo:"table=not-valid"`
	ID   uint64
}

type RepeatedMeta struct {
	Meta
	Other Meta
	ID    uint64
}

type InvalidModelOption struct {
	ID uint64 `tidbgo:",primary"`
}

type IgnoredPrimaryKey struct {
	ID   uint64 `tidbgo:"-,pk"`
	Name string
}

type ModelWithDBTags struct {
	ID     uint64 `db:"legacy_id"`
	Secret string `db:"-"`
	Name   string `db:"legacy_name" tidbgo:"display_name"`
}

type IgnoredUnsupportedField struct {
	Values map[string]string `tidbgo:"-"`
	Name   string
}

type AutoRandomWithoutPrimaryKey struct {
	ID int64 `tidbgo:",auto_random"`
}

type AutoRandomString struct {
	ID string `tidbgo:",pk,auto_random"`
}

type AutoRandomPointer struct {
	ID *int64 `tidbgo:",pk,auto_random"`
}

type MultipleAutoRandomFields struct {
	First  int64 `tidbgo:",pk,auto_random"`
	Second int64 `tidbgo:",pk,auto_random"`
}

type ComputedPrimaryKey struct {
	ID int64 `tidbgo:",pk,computed"`
}

type RepeatedAutoRandomOption struct {
	ID int64 `tidbgo:",pk,auto_random,auto_random"`
}

type RepeatedComputedOption struct {
	Value int64 `tidbgo:",computed,computed"`
}

type SoftDeleteString struct {
	DeletedAt string `tidbgo:",soft_delete"`
}

type SoftDeletePointerDepth struct {
	DeletedAt **time.Time `tidbgo:",soft_delete"`
}

type SoftDeletePrimaryKey struct {
	DeletedAt time.Time `tidbgo:",pk,soft_delete"`
}

type SoftDeleteComputed struct {
	DeletedAt time.Time `tidbgo:",computed,soft_delete"`
}

type MultipleSoftDeleteFields struct {
	DeletedAt time.Time  `tidbgo:",soft_delete"`
	RemovedAt *time.Time `tidbgo:",soft_delete"`
}

type RepeatedSoftDeleteOption struct {
	DeletedAt time.Time `tidbgo:",soft_delete,soft_delete"`
}

func TestDescribeSkipsTidbgoIgnoredFieldsBeforeTypeValidation(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[IgnoredUnsupportedField]()
	if err != nil {
		t.Fatalf("Describe[IgnoredUnsupportedField]() error = %v", err)
	}
	if got, want := columnNames(descriptor.Fields()), []string{"name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %#v, want %#v", got, want)
	}
}

func TestDescribeIgnoresDBTags(t *testing.T) {
	t.Parallel()

	descriptor, err := Describe[ModelWithDBTags]()
	if err != nil {
		t.Fatalf("Describe[ModelWithDBTags]() error = %v", err)
	}
	if got, want := columnNames(descriptor.Fields()), []string{"id", "secret", "display_name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %#v, want %#v", got, want)
	}
	if _, exists := descriptor.FieldByColumn("legacy_id"); exists {
		t.Fatal("db tag column was mapped")
	}
}

type NestedMetaFields struct {
	Meta `tidbgo:"table=nested"`
}

type NestedMetaModel struct {
	NestedMetaFields
	ID uint64
}

type PointerMeta struct {
	*Meta `tidbgo:"table=pointer_meta"`
	ID    uint64
}

func TestDescribeRejectsInvalidMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		describe func() error
		contains string
	}{
		{name: "duplicate column", describe: describeError[DuplicateColumns], contains: "already mapped"},
		{name: "unsupported field", describe: describeError[UnsupportedField], contains: "not a supported scalar"},
		{name: "invalid tag", describe: describeError[InvalidTag], contains: "simple SQL identifier"},
		{name: "unsupported option", describe: describeError[UnsupportedOption], contains: "tag option"},
		{name: "empty model", describe: describeError[EmptyModel], contains: "at least one mapped field"},
		{name: "recursive embedding", describe: describeError[RecursiveModel], contains: "embedded struct cycle"},
		{name: "invalid table", describe: describeError[InvalidTable], contains: "table name"},
		{name: "repeated metadata", describe: describeError[RepeatedMeta], contains: "only once"},
		{name: "invalid model option", describe: describeError[InvalidModelOption], contains: "not supported after the column position"},
		{name: "ignore with primary key", describe: describeError[IgnoredPrimaryKey], contains: "must not be combined"},
		{name: "nested metadata", describe: describeError[NestedMetaModel], contains: "directly in the model type"},
		{name: "pointer metadata", describe: describeError[PointerMeta], contains: "directly as a value"},
		{name: "AUTO_RANDOM without primary key", describe: describeError[AutoRandomWithoutPrimaryKey], contains: "must also be primary-key"},
		{name: "AUTO_RANDOM string", describe: describeError[AutoRandomString], contains: "non-pointer signed or unsigned integer"},
		{name: "AUTO_RANDOM pointer", describe: describeError[AutoRandomPointer], contains: "non-pointer signed or unsigned integer"},
		{name: "multiple AUTO_RANDOM fields", describe: describeError[MultipleAutoRandomFields], contains: "already declared"},
		{name: "computed primary key", describe: describeError[ComputedPrimaryKey], contains: "cannot be primary-key"},
		{name: "repeated AUTO_RANDOM option", describe: describeError[RepeatedAutoRandomOption], contains: "must not be repeated"},
		{name: "repeated computed option", describe: describeError[RepeatedComputedOption], contains: "must not be repeated"},
		{name: "soft delete string", describe: describeError[SoftDeleteString], contains: "time.Time or *time.Time"},
		{name: "soft delete pointer depth", describe: describeError[SoftDeletePointerDepth], contains: "time.Time or *time.Time"},
		{name: "soft delete primary key", describe: describeError[SoftDeletePrimaryKey], contains: "cannot be primary-key"},
		{name: "soft delete computed", describe: describeError[SoftDeleteComputed], contains: "cannot be computed"},
		{name: "multiple soft delete fields", describe: describeError[MultipleSoftDeleteFields], contains: "already declared"},
		{name: "repeated soft delete option", describe: describeError[RepeatedSoftDeleteOption], contains: "must not be repeated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.describe()
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("Describe() error = %v, want substring %q", err, tt.contains)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || len(validationErr.Issues) == 0 {
				t.Fatalf("Describe() error type = %T, want *ValidationError", err)
			}
		})
	}
}

func TestDescribeRejectsNonStructTypes(t *testing.T) {
	t.Parallel()

	for _, modelType := range []reflect.Type{nil, reflect.TypeFor[string](), reflect.TypeOf(struct{ ID int }{})} {
		_, err := DescribeType(modelType)
		if !errors.Is(err, ErrModelType) {
			t.Fatalf("DescribeType(%v) error = %v, want ErrModelType", modelType, err)
		}
	}
}

func describeError[T any]() error {
	_, err := Describe[T]()
	return err
}

func mustDescribe[T any](t testing.TB) *Descriptor {
	t.Helper()
	descriptor, err := Describe[T]()
	if err != nil {
		t.Fatalf("Describe[%T]() error = %v", *new(T), err)
	}
	return descriptor
}

func columnNames(fields []Field) []string {
	names := make([]string, len(fields))
	for index, field := range fields {
		names[index] = field.ColumnName()
	}
	return names
}
