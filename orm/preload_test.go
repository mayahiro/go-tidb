package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
)

type preloadUser struct {
	model.Meta `tidbgo:"table=preload_users"`
	ID         uint64 `tidbgo:",pk"`
	Email      string
	Orders     []preloadOrder  `tidbgo:"has_many,join=ID:UserID"`
	Profile    *preloadProfile `tidbgo:"has_one,join=ID:UserID"`
	Roles      []*preloadRole  `tidbgo:"many_to_many,through=preload_user_roles,source=ID:user_id,target=role_id:ID"`
}

type preloadOrder struct {
	model.Meta `tidbgo:"table=preload_orders"`
	ID         uint64 `tidbgo:",pk"`
	UserID     uint64
	Total      string
	User       *preloadUser `tidbgo:"belongs_to,join=UserID:ID"`
}

type preloadProfile struct {
	model.Meta `tidbgo:"table=preload_profiles"`
	ID         uint64 `tidbgo:",pk"`
	UserID     uint64
	Bio        string
}

type preloadRole struct {
	model.Meta `tidbgo:"table=preload_roles"`
	ID         uint64 `tidbgo:",pk"`
	Name       string
}

type preloadGraph struct {
	model.Meta `tidbgo:"table=preload_graphs"`
	ID         uint64               `tidbgo:",pk"`
	NodeAID    uint64               `tidbgo:"node_a_id"`
	NodeBID    uint64               `tidbgo:"node_b_id"`
	NodeCID    uint64               `tidbgo:"node_c_id"`
	NodeA      *preloadGraphNode    `tidbgo:"belongs_to,join=NodeAID:ID"`
	NodeB      *preloadGraphNode    `tidbgo:"belongs_to,join=NodeBID:ID"`
	NodeC      *preloadGraphNode    `tidbgo:"belongs_to,join=NodeCID:ID"`
	DetailA    *preloadGraphDetailA `tidbgo:"has_one,join=ID:GraphID"`
	DetailB    *preloadGraphDetailB `tidbgo:"has_one,join=ID:GraphID"`
	Tags       []preloadGraphTag    `tidbgo:"many_to_many,through=preload_graph_tags,source=ID:graph_id,target=tag_id:ID"`
	Children   []preloadGraphChild  `tidbgo:"has_many,join=ID:GraphID"`
}

type preloadGraphNode struct {
	model.Meta `tidbgo:"table=preload_graph_nodes"`
	ID         uint64 `tidbgo:",pk"`
	Value      string
}

type preloadGraphDetailA struct {
	model.Meta `tidbgo:"table=preload_graph_details_a"`
	ID         uint64 `tidbgo:",pk"`
	GraphID    uint64 `tidbgo:"graph_id"`
	Value      string
}

type preloadGraphDetailB struct {
	model.Meta `tidbgo:"table=preload_graph_details_b"`
	ID         uint64 `tidbgo:",pk"`
	GraphID    uint64 `tidbgo:"graph_id"`
	Value      string
}

type preloadGraphTag struct {
	model.Meta `tidbgo:"table=preload_graph_tag_targets"`
	ID         uint64 `tidbgo:",pk"`
	NodeID     uint64 `tidbgo:"node_id"`
	Value      string
	Node       *preloadGraphNode `tidbgo:"belongs_to,join=NodeID:ID"`
}

type preloadGraphChild struct {
	model.Meta `tidbgo:"table=preload_graph_children"`
	ID         uint64            `tidbgo:",pk"`
	GraphID    uint64            `tidbgo:"graph_id"`
	NodeID     uint64            `tidbgo:"node_id"`
	Node       *preloadGraphNode `tidbgo:"belongs_to,join=NodeID:ID"`
}

type preloadMember struct {
	model.Meta `tidbgo:"table=preload_members"`
	TenantID   uint64               `tidbgo:",pk"`
	ID         uint64               `tidbgo:",pk"`
	Groups     []preloadMemberGroup `tidbgo:"many_to_many,through=preload_member_groups,source=TenantID:tenant_id,source=ID:member_id,target=group_tenant_id:TenantID,target=group_id:ID"`
}

type preloadMemberGroup struct {
	model.Meta `tidbgo:"table=preload_groups"`
	TenantID   uint64 `tidbgo:",pk"`
	ID         uint64 `tidbgo:",pk"`
	Name       string
}

type preloadTenant struct {
	model.Meta `tidbgo:"table=preload_tenants"`
	TenantID   uint64          `tidbgo:",pk"`
	ID         uint64          `tidbgo:",pk"`
	Records    []preloadRecord `tidbgo:"has_many,join=TenantID:TenantID,join=ID:ParentID"`
}

type preloadRecord struct {
	model.Meta `tidbgo:"table=preload_records"`
	TenantID   uint64
	ParentID   uint64
	Value      string
}

type preloadNullableParent struct {
	ID       *uint64
	Children []preloadNullableChild `tidbgo:"has_many,join=ID:ParentID"`
}

type preloadNullableChild struct {
	ID       uint64
	ParentID *uint64
}

type preloadCustomKey struct {
	text string
}

var preloadCustomKeyValueCalls int

func (key *preloadCustomKey) Scan(source any) error {
	text, ok := source.(string)
	if !ok {
		return fmt.Errorf("preloadCustomKey requires string, got %T", source)
	}
	key.text = text
	return nil
}

func (key preloadCustomKey) Value() (driver.Value, error) {
	preloadCustomKeyValueCalls++
	return key.text, nil
}

type preloadCustomParent struct {
	ID       preloadCustomKey     `tidbgo:",pk"`
	Children []preloadCustomChild `tidbgo:"has_many,join=ID:ParentID"`
}

type preloadCustomChild struct {
	ID       uint64
	ParentID preloadCustomKey
}

type preloadCustomManyParent struct {
	ID   preloadCustomKey          `tidbgo:",pk"`
	Tags []preloadCustomManyTarget `tidbgo:"many_to_many,through=preload_custom_parent_tags,source=ID:parent_id,target=tag_id:ID"`
}

type preloadCustomManyTarget struct {
	ID   uint64 `tidbgo:",pk"`
	Name string
}

type preloadNullableManyParent struct {
	ID    *uint64
	Roles []preloadRole `tidbgo:"many_to_many,through=preload_nullable_parent_roles,source=ID:parent_id,target=role_id:ID"`
}

type PreloadEmbeddedRelations struct {
	Children []*preloadNullableChild `tidbgo:"has_many,join=ID:ParentID"`
}

type preloadEmbeddedParent struct {
	ID uint64
	*PreloadEmbeddedRelations
}

type preloadKeyKinds struct {
	Bool   bool
	Int    int64
	Uint   uint64
	Float  float64
	String string
	Bytes  []byte
	Time   time.Time
	Ptr    *int64
}

type preloadPointerValuer struct {
	value driver.Value
	err   error
}

func (valuer *preloadPointerValuer) Value() (driver.Value, error) {
	if valuer == nil {
		return nil, errors.New("nil preloadPointerValuer")
	}
	return valuer.value, valuer.err
}

type preloadUnsupportedValuer struct{}

func (preloadUnsupportedValuer) Value() (driver.Value, error) {
	return uint64(1), nil
}

type preloadTestResponse struct {
	columns    []string
	values     [][]driver.Value
	queryErr   error
	nextErr    error
	closeErr   error
	closeCalls int
}

type preloadTestCall struct {
	query     string
	arguments []driver.NamedValue
}

type preloadTestState struct {
	mu            sync.Mutex
	responses     []*preloadTestResponse
	responseIndex int
	repeat        bool
	record        bool
	calls         []preloadTestCall
}

type preloadTestConnector struct {
	state *preloadTestState
}

func (connector *preloadTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &preloadTestConn{state: connector.state}, nil
}

func (*preloadTestConnector) Driver() driver.Driver {
	return preloadTestDriver{}
}

type preloadTestDriver struct{}

func (preloadTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("preload test driver requires OpenDB")
}

type preloadTestConn struct {
	state *preloadTestState
}

func (*preloadTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*preloadTestConn) Close() error {
	return nil
}

func (*preloadTestConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func (connection *preloadTestConn) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	state := connection.state
	state.mu.Lock()
	if len(state.responses) == 0 || !state.repeat && state.responseIndex >= len(state.responses) {
		state.mu.Unlock()
		return nil, fmt.Errorf("unexpected preload test query %q", query)
	}
	responseIndex := state.responseIndex
	if state.repeat {
		responseIndex %= len(state.responses)
	}
	response := state.responses[responseIndex]
	state.responseIndex++
	if state.record {
		state.calls = append(state.calls, preloadTestCall{
			query:     query,
			arguments: append([]driver.NamedValue(nil), arguments...),
		})
	}
	state.mu.Unlock()
	if response.queryErr != nil {
		return nil, response.queryErr
	}
	return &preloadTestRows{state: state, response: response}, nil
}

type preloadTestRows struct {
	state       *preloadTestState
	response    *preloadTestResponse
	index       int
	closed      bool
	nextErrSent bool
}

func (rows *preloadTestRows) Columns() []string {
	return rows.response.columns
}

func (rows *preloadTestRows) Close() error {
	if rows.closed {
		return nil
	}
	rows.closed = true
	rows.state.mu.Lock()
	rows.response.closeCalls++
	rows.state.mu.Unlock()
	return rows.response.closeErr
}

func (rows *preloadTestRows) Next(destination []driver.Value) error {
	if rows.closed {
		return io.EOF
	}
	if rows.index < len(rows.response.values) {
		copy(destination, rows.response.values[rows.index])
		rows.index++
		return nil
	}
	if rows.response.nextErr != nil && !rows.nextErrSent {
		rows.nextErrSent = true
		return rows.response.nextErr
	}
	return io.EOF
}

func TestSelectQueryPreloadBuildsParentSQLAndValidatesOffline(t *testing.T) {
	preloadCustomKeyValueCalls = 0

	query := Query[preloadUser]().Select("Email").Preload("Orders")
	sqlText, arguments, err := query.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `email`, `id` FROM `preload_users`"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if arguments != nil {
		t.Fatalf("arguments = %#v, want nil", arguments)
	}

	secondSQL, _, err := query.Build()
	if err != nil || secondSQL != sqlText {
		t.Fatalf("second Build() = %q, %v, want %q, nil", secondSQL, err, sqlText)
	}
	if got := query.selection.projection; !reflect.DeepEqual(got, []string{"Email"}) {
		t.Fatalf("builder projection = %#v, want unchanged", got)
	}

	if _, _, err := Query[preloadCustomParent]().Select("ID").Preload("Children").Build(); err != nil {
		t.Fatalf("custom-key Build() error = %v", err)
	}
	if preloadCustomKeyValueCalls != 0 {
		t.Fatalf("custom key Value() calls = %d, want 0", preloadCustomKeyValueCalls)
	}
	if _, _, err := Query[preloadCustomManyParent]().Select("ID").Preload("Tags").Build(); err != nil {
		t.Fatalf("custom many-to-many key Build() error = %v", err)
	}
	if preloadCustomKeyValueCalls != 0 {
		t.Fatalf("custom many-to-many key Value() calls = %d, want 0", preloadCustomKeyValueCalls)
	}

	manyToManySQL, manyToManyArguments, err := Query[preloadUser]().Select("Email").Preload("Roles").Build()
	if err != nil {
		t.Fatalf("many-to-many Build() error = %v", err)
	}
	if got, want := manyToManySQL, "SELECT `email`, `id` FROM `preload_users`"; got != want {
		t.Fatalf("many-to-many SQL = %q, want %q", got, want)
	}
	if manyToManyArguments != nil {
		t.Fatalf("many-to-many arguments = %#v, want nil", manyToManyArguments)
	}

	tests := []struct {
		name  string
		query *SelectQuery[preloadUser]
		want  string
	}{
		{name: "unknown", query: Query[preloadUser]().Preload("Missing"), want: "not a mapped relation field"},
		{name: "scalar", query: Query[preloadUser]().Preload("Email"), want: "not a mapped relation field"},
		{name: "duplicate", query: Query[preloadUser]().Preload("Orders").Preload("Orders"), want: "repeats relation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tt.query.Build()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSelectQueryPreloadHydratesManyToManyInOneSecondaryQuery(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"id", "email"},
				values:  [][]driver.Value{{int64(1), "ada@example.com"}, {int64(1), "duplicate@example.com"}, {int64(2), "grace@example.com"}},
			},
			{
				columns: []string{"user_id", "id", "name"},
				values: [][]driver.Value{
					{int64(1), int64(10), "admin"},
					{int64(2), int64(20), "reader"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Select("ID", "Email").Preload("Roles").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 3 || len(users[0].Roles) != 1 || len(users[1].Roles) != 1 || len(users[2].Roles) != 1 {
		t.Fatalf("users = %#v, want hydrated roles for three parents", users)
	}
	if users[0].Roles[0].Name != "admin" || users[1].Roles[0].ID != 10 || users[2].Roles[0].Name != "reader" {
		t.Fatalf("users = %#v, want roles grouped by junction source key", users)
	}
	if users[0].Roles[0] == users[1].Roles[0] {
		t.Fatal("duplicate parent keys shared a mutable many-to-many target pointer")
	}

	calls := preloadCalls(state)
	if len(calls) != 2 {
		t.Fatalf("query count = %d, want 2", len(calls))
	}
	wantSQL := "SELECT `j`.`user_id`, `t`.`id`, `t`.`name` FROM `preload_user_roles` AS `j` JOIN `preload_roles` AS `t` ON (`t`.`id` = `j`.`role_id`)"
	if got := calls[1].query; got != wantSQL {
		t.Fatalf("preload query = %q, want %q", got, wantSQL)
	}
	if got := namedValues(calls[1].arguments); len(got) != 0 {
		t.Fatalf("preload arguments = %#v, want none", got)
	}
}

func TestSelectQueryPreloadHydratesDirectAndManyToManyInRequestOrder(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{columns: []string{"id", "user_id", "total"}, values: [][]driver.Value{{int64(11), int64(1), "10.00"}}},
			{columns: []string{"user_id", "id", "name"}, values: [][]driver.Value{{int64(1), int64(20), "admin"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Preload("Orders").Preload("Roles").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 1 || len(users[0].Orders) != 1 || users[0].Orders[0].ID != 11 || len(users[0].Roles) != 1 || users[0].Roles[0].ID != 20 {
		t.Fatalf("users = %#v, want direct and many-to-many relations", users)
	}
	calls := preloadCalls(state)
	if len(calls) != 3 {
		t.Fatalf("query count = %d, want 3", len(calls))
	}
	if !strings.Contains(calls[1].query, "FROM `preload_orders`") || !strings.Contains(calls[2].query, "FROM `preload_user_roles` AS `j`") {
		t.Fatalf("secondary queries = %q, %q, want request order", calls[1].query, calls[2].query)
	}
}

func TestSelectQueryPreloadRelationGraphUsesThreeStatements(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{
					"id", "node_a_id", "node_b_id", "node_c_id",
					"node_a_id", "node_a_value",
					"node_b_id", "node_b_value",
					"node_c_id", "node_c_value",
					"detail_a_id", "detail_a_value", "detail_a_graph_id",
					"detail_b_id", "detail_b_value", "detail_b_graph_id",
				},
				values: [][]driver.Value{{
					int64(1), int64(10), int64(20), int64(30),
					int64(10), "a",
					int64(20), "b",
					int64(30), "c",
					int64(40), "detail-a", int64(1),
					int64(50), "detail-b", int64(1),
				}},
			},
			{
				columns: []string{"graph_id", "id", "value", "joined_node_id", "node_value"},
				values:  [][]driver.Value{{int64(1), int64(60), "tag", int64(90), "tag-node"}},
			},
			{
				columns: []string{"id", "node_id", "graph_id", "joined_node_id", "node_value"},
				values:  [][]driver.Value{{int64(70), int64(80), int64(1), int64(80), "child-node"}},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	graph, err := relationGraphPreloadQuery().Only(context.Background(), database)
	if err != nil {
		t.Fatalf("Only() error = %v", err)
	}
	if graph.ID != 1 || graph.NodeA == nil || graph.NodeA.Value != "a" || graph.NodeB == nil || graph.NodeB.Value != "b" || graph.NodeC == nil || graph.NodeC.Value != "c" {
		t.Fatalf("graph belongs-to relations = %#v", graph)
	}
	if graph.DetailA == nil || graph.DetailA.Value != "detail-a" || graph.DetailB == nil || graph.DetailB.Value != "detail-b" {
		t.Fatalf("graph has-one relations = %#v", graph)
	}
	if len(graph.Tags) != 1 || graph.Tags[0].Value != "tag" || graph.Tags[0].Node == nil || graph.Tags[0].Node.Value != "tag-node" {
		t.Fatalf("graph Tags = %#v", graph.Tags)
	}
	if len(graph.Children) != 1 || graph.Children[0].Node == nil || graph.Children[0].Node.Value != "child-node" {
		t.Fatalf("graph Children = %#v", graph.Children)
	}

	calls := preloadCalls(state)
	if len(calls) != 3 {
		t.Fatalf("statement count = %d, want 3", len(calls))
	}
	if got := strings.Count(calls[0].query, " LEFT JOIN "); got != 5 {
		t.Fatalf("parent LEFT JOIN count = %d, want 5 in %q", got, calls[0].query)
	}
	if !strings.Contains(calls[1].query, "FROM `preload_graph_tags` AS `j`") {
		t.Fatalf("tag query = %q", calls[1].query)
	}
	if got := strings.Count(calls[1].query, " LEFT JOIN "); got != 1 {
		t.Fatalf("Tags LEFT JOIN count = %d, want 1 in %q", got, calls[1].query)
	}
	if got := strings.Count(calls[2].query, " LEFT JOIN "); got != 1 {
		t.Fatalf("Children LEFT JOIN count = %d, want 1 in %q", got, calls[2].query)
	}
}

func relationGraphPreloadQuery() *SelectQuery[preloadGraph] {
	return Query[preloadGraph]().
		Select("ID", "NodeAID", "NodeBID", "NodeCID").
		Preload("NodeA", PreloadFields("ID", "Value")).
		Preload("NodeB", PreloadFields("ID", "Value")).
		Preload("NodeC", PreloadFields("ID", "Value")).
		Preload("DetailA", PreloadFields("ID", "Value")).
		Preload("DetailB", PreloadFields("ID", "Value")).
		Preload("Tags", PreloadFields("ID", "Value")).
		Preload("Tags.Node", PreloadFields("ID", "Value")).
		Preload("Children", PreloadFields("ID", "NodeID")).
		Preload("Children.Node", PreloadFields("ID", "Value")).
		Where(Equal("ID", uint64(1)))
}

func TestSelectQueryInlinePreloadQualifiesRootClauses(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := Query[preloadOrder]().
		Select("ID").
		Preload("User", PreloadFields("ID")).
		Where(Equal("ID", uint64(1)), Has("User")).
		OrderBy(Asc("ID")).
		SeekAfter(uint64(2)).
		Limit(10).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `tidbgo_t0`.`id`, `tidbgo_t1`.`id` FROM `preload_orders` AS `tidbgo_t0` LEFT JOIN `preload_users` AS `tidbgo_t1` ON (`tidbgo_t0`.`user_id` = `tidbgo_t1`.`id`) WHERE `tidbgo_t0`.`id` = ? AND EXISTS (SELECT 1 FROM `preload_users` AS `tidbgo_r1` WHERE (`tidbgo_r1`.`id` = `tidbgo_t0`.`user_id`)) AND (`tidbgo_t0`.`id` > ?) ORDER BY `tidbgo_t0`.`id` ASC LIMIT ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{uint64(1), uint64(2), int64(10)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryPreloadSupportsNestedPathsProjectionAndOrdering(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{
				columns: []string{"id", "total", "user_id", "joined_user_id", "joined_user_email"},
				values: [][]driver.Value{
					{int64(12), "20.00", int64(1), int64(1), "user@example.com"},
					{int64(11), "10.00", int64(1), int64(1), "user@example.com"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().
		Preload("Orders", PreloadFields("ID", "Total"), PreloadOrderBy(Desc("ID"))).
		Preload("Orders.User").
		All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 1 || len(users[0].Orders) != 2 {
		t.Fatalf("users = %#v", users)
	}
	if users[0].Orders[0].ID != 12 || users[0].Orders[1].ID != 11 {
		t.Fatalf("ordered Orders = %#v", users[0].Orders)
	}
	for _, order := range users[0].Orders {
		if order.User == nil || order.User.ID != 1 || order.User.Email != "user@example.com" {
			t.Fatalf("nested User for order %d = %#v", order.ID, order.User)
		}
	}

	calls := preloadCalls(state)
	if len(calls) != 2 {
		t.Fatalf("query count = %d, want 2", len(calls))
	}
	wantOrdersSQL := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`total`, `tidbgo_t0`.`user_id`, `tidbgo_t1`.`id`, `tidbgo_t1`.`email` FROM `preload_orders` AS `tidbgo_t0` LEFT JOIN `preload_users` AS `tidbgo_t1` ON (`tidbgo_t0`.`user_id` = `tidbgo_t1`.`id`) ORDER BY `tidbgo_t0`.`id` DESC"
	if calls[1].query != wantOrdersSQL {
		t.Fatalf("Orders query = %q, want %q", calls[1].query, wantOrdersSQL)
	}
}

func TestSelectQueryPreloadManyToManyProjectionAndOrdering(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{
				columns: []string{"user_id", "name", "id"},
				values: [][]driver.Value{
					{int64(1), "admin", int64(10)},
					{int64(1), "reader", int64(20)},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().
		Preload("Roles", PreloadFields("Name"), PreloadOrderBy(Asc("Name"))).
		All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 1 || len(users[0].Roles) != 2 || users[0].Roles[0].Name != "admin" || users[0].Roles[1].Name != "reader" {
		t.Fatalf("users = %#v", users)
	}
	calls := preloadCalls(state)
	wantSQL := "SELECT `j`.`user_id`, `t`.`name`, `t`.`id` FROM `preload_user_roles` AS `j` JOIN `preload_roles` AS `t` ON (`t`.`id` = `j`.`role_id`) ORDER BY `t`.`name` ASC"
	if len(calls) != 2 || calls[1].query != wantSQL {
		t.Fatalf("calls = %#v, want relation SQL %q", calls, wantSQL)
	}
}

func TestSelectQueryPreloadRejectsInvalidPathsAndOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query *SelectQuery[preloadUser]
		want  string
	}{
		{name: "empty path", query: Query[preloadUser]().Preload(""), want: "requires a relation path"},
		{name: "empty nested segment", query: Query[preloadUser]().Preload("Orders..User"), want: "invalid relation path"},
		{name: "unknown nested relation", query: Query[preloadUser]().Preload("Orders.Missing"), want: "not a mapped relation field"},
		{name: "duplicate nested path", query: Query[preloadUser]().Preload("Orders.User").Preload("Orders.User"), want: "repeats relation path"},
		{name: "zero option", query: Query[preloadUser]().Preload("Orders", PreloadOption{}), want: "invalid option"},
		{name: "empty fields", query: Query[preloadUser]().Preload("Orders", PreloadFields()), want: "requires at least one field"},
		{name: "empty order", query: Query[preloadUser]().Preload("Orders", PreloadOrderBy()), want: "requires at least one term"},
		{name: "duplicate fields option", query: Query[preloadUser]().Preload("Orders", PreloadFields("ID"), PreloadFields("Total")), want: "repeats an option"},
		{name: "unknown projected field", query: Query[preloadUser]().Preload("Orders", PreloadFields("Missing")), want: "not a mapped scalar field"},
		{name: "duplicate order field", query: Query[preloadUser]().Preload("Orders", PreloadOrderBy(Asc("ID"), Desc("ID"))), want: "repeats field"},
		{name: "to-one order", query: Query[preloadUser]().Preload("Profile", PreloadOrderBy(Asc("ID"))), want: "requires a collection relation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := test.query.Build(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSelectQueryPreloadSupportsCompositeManyToManyKeys(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"tenant_id", "id"},
				values:  [][]driver.Value{{int64(1), int64(10)}, {int64(2), int64(20)}},
			},
			{
				columns: []string{"tenant_id", "member_id", "tenant_id", "id", "name"},
				values: [][]driver.Value{
					{int64(2), int64(20), int64(8), int64(80), "second"},
					{int64(1), int64(10), int64(7), int64(70), "first"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	members, err := Query[preloadMember]().Preload("Groups").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(members) != 2 || len(members[0].Groups) != 1 || members[0].Groups[0].Name != "first" || len(members[1].Groups) != 1 || members[1].Groups[0].Name != "second" {
		t.Fatalf("members = %#v, want composite-key groups", members)
	}

	calls := preloadCalls(state)
	wantSQL := "SELECT `j`.`tenant_id`, `j`.`member_id`, `t`.`tenant_id`, `t`.`id`, `t`.`name` FROM `preload_member_groups` AS `j` JOIN `preload_groups` AS `t` ON (`t`.`tenant_id` = `j`.`group_tenant_id` AND `t`.`id` = `j`.`group_id`)"
	if got := calls[1].query; got != wantSQL {
		t.Fatalf("preload query = %q, want %q", got, wantSQL)
	}
	if got := namedValues(calls[1].arguments); len(got) != 0 {
		t.Fatalf("preload arguments = %#v, want none", got)
	}
}

func TestSelectQueryPreloadSplitsManyToManyBatchesAtParameterBudget(t *testing.T) {
	parentRows := make([][]driver.Value, preloadParameterBudget+1)
	for index := range parentRows {
		parentRows[index] = []driver.Value{int64(index + 1), "user@example.com"}
	}
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: parentRows},
			{columns: []string{"user_id", "id", "name"}},
			{columns: []string{"user_id", "id", "name"}},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Preload("Roles").Limit(int64(len(parentRows))).All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != len(parentRows) {
		t.Fatalf("user count = %d, want %d", len(users), len(parentRows))
	}
	calls := preloadCalls(state)
	if len(calls) != 3 {
		t.Fatalf("query count = %d, want 3", len(calls))
	}
	if got, want := len(calls[1].arguments), preloadParameterBudget; got != want {
		t.Fatalf("first batch argument count = %d, want %d", got, want)
	}
	if got, want := len(calls[2].arguments), 1; got != want {
		t.Fatalf("second batch argument count = %d, want %d", got, want)
	}
}

func TestSelectQueryPreloadRejectsUnrequestedManyToManySourceKey(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{columns: []string{"user_id", "id", "name"}, values: [][]driver.Value{{int64(2), int64(10), "admin"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Preload("Roles").Limit(1).All(context.Background(), database)
	if err == nil || !strings.Contains(err.Error(), "unrequested junction source key") {
		t.Fatalf("All() users = %#v, error = %v, want unrequested junction source key error", users, err)
	}
	if users != nil {
		t.Fatalf("All() users = %#v, want nil on preload failure", users)
	}
	if state.responses[1].closeCalls != 1 {
		t.Fatalf("secondary close calls = %d, want 1", state.responses[1].closeCalls)
	}
}

func TestSelectQueryPreloadRejectsNullManyToManySourceKey(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
			{columns: []string{"parent_id", "id", "name"}, values: [][]driver.Value{{nil, int64(10), "admin"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadNullableManyParent]().Preload("Roles").Limit(1).All(context.Background(), database)
	if err == nil || !strings.Contains(err.Error(), "NULL source key") {
		t.Fatalf("All() parents = %#v, error = %v, want NULL source key error", parents, err)
	}
	if parents != nil {
		t.Fatalf("All() parents = %#v, want nil on preload failure", parents)
	}
}

func TestSelectQueryPreloadAllSkipsUnrelatedManyToManySourceKeys(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
			{
				columns: []string{"parent_id", "id", "name"},
				values: [][]driver.Value{
					{nil, int64(10), "null"},
					{int64(2), int64(11), "orphan"},
					{int64(1), int64(12), "matched"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadNullableManyParent]().Preload("Roles").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(parents) != 1 || len(parents[0].Roles) != 1 || parents[0].Roles[0].ID != 12 {
		t.Fatalf("parents = %#v, want only the matching role", parents)
	}
	calls := preloadCalls(state)
	if len(calls) != 2 || len(calls[1].arguments) != 0 {
		t.Fatalf("calls = %#v, want one argument-free relation query", calls)
	}
}

func TestSelectQueryPreloadReportsManyToManyScanFailure(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{columns: []string{"user_id", "id", "name"}, values: [][]driver.Value{{"invalid", int64(10), "admin"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Preload("Roles").All(context.Background(), database)
	if err == nil || !strings.Contains(err.Error(), "many-to-many row") {
		t.Fatalf("All() users = %#v, error = %v, want scan error", users, err)
	}
	if users != nil {
		t.Fatalf("All() users = %#v, want nil on preload failure", users)
	}
	if state.responses[1].closeCalls != 1 {
		t.Fatalf("secondary close calls = %d, want 1", state.responses[1].closeCalls)
	}
}

func TestSelectQueryPreloadUsesApplicationCustomManyToManySourceKey(t *testing.T) {
	preloadCustomKeyValueCalls = 0
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{"account-a"}}},
			{columns: []string{"parent_id", "id", "name"}, values: [][]driver.Value{{"account-a", int64(10), "tag"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadCustomManyParent]().Preload("Tags").Limit(1).All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(parents) != 1 || len(parents[0].Tags) != 1 || parents[0].Tags[0].Name != "tag" {
		t.Fatalf("parents = %#v, want hydrated custom-key tags", parents)
	}
	if preloadCustomKeyValueCalls != 2 {
		t.Fatalf("custom key Value() calls = %d, want 2", preloadCustomKeyValueCalls)
	}
	calls := preloadCalls(state)
	if got, want := namedValues(calls[1].arguments), []any{"account-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preload arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryPreloadHydratesHasManyInOneSecondaryQuery(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"email", "id"},
				values:  [][]driver.Value{{"ada@example.com", int64(1)}, {"grace@example.com", int64(2)}},
			},
			{
				columns: []string{"id", "user_id", "total"},
				values: [][]driver.Value{
					{int64(11), int64(1), "10.00"},
					{int64(12), int64(1), "20.00"},
					{int64(21), int64(2), "30.00"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().
		Select("Email").
		Preload("Orders").
		OrderBy(Asc("ID")).
		All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if got, want := users, []preloadUser{
		{ID: 1, Email: "ada@example.com", Orders: []preloadOrder{{ID: 11, UserID: 1, Total: "10.00"}, {ID: 12, UserID: 1, Total: "20.00"}}},
		{ID: 2, Email: "grace@example.com", Orders: []preloadOrder{{ID: 21, UserID: 2, Total: "30.00"}}},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("users = %#v, want %#v", got, want)
	}

	calls := preloadCalls(state)
	if got, want := calls[0].query, "SELECT `email`, `id` FROM `preload_users` ORDER BY `id` ASC"; got != want {
		t.Fatalf("parent query = %q, want %q", got, want)
	}
	if got, want := calls[1].query, "SELECT `id`, `user_id`, `total` FROM `preload_orders`"; got != want {
		t.Fatalf("preload query = %q, want %q", got, want)
	}
	if got := namedValues(calls[1].arguments); len(got) != 0 {
		t.Fatalf("preload arguments = %#v, want none", got)
	}
	if state.responses[0].closeCalls != 1 || state.responses[1].closeCalls != 1 {
		t.Fatalf("close calls = %d, %d, want 1, 1", state.responses[0].closeCalls, state.responses[1].closeCalls)
	}
}

func TestSelectQueryPreloadHydratesBelongsToAndDeduplicatesKeys(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"id", "user_id", "total", "joined_user_id", "joined_user_email"},
				values: [][]driver.Value{
					{int64(1), int64(7), "10.00", int64(7), "user@example.com"},
					{int64(2), int64(7), "20.00", int64(7), "user@example.com"},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	orders, err := Query[preloadOrder]().Preload("User").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(orders) != 2 || orders[0].User == nil || orders[1].User == nil {
		t.Fatalf("orders = %#v, want two hydrated users", orders)
	}
	if orders[0].User.ID != 7 || orders[1].User.Email != "user@example.com" {
		t.Fatalf("orders = %#v, want matching users", orders)
	}
	if orders[0].User == orders[1].User {
		t.Fatal("duplicate parent keys shared a mutable relation pointer")
	}
	calls := preloadCalls(state)
	wantSQL := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`user_id`, `tidbgo_t0`.`total`, `tidbgo_t1`.`id`, `tidbgo_t1`.`email` FROM `preload_orders` AS `tidbgo_t0` LEFT JOIN `preload_users` AS `tidbgo_t1` ON (`tidbgo_t0`.`user_id` = `tidbgo_t1`.`id`)"
	if len(calls) != 1 || calls[0].query != wantSQL {
		t.Fatalf("calls = %#v, want one inline query %q", calls, wantSQL)
	}
}

func TestSelectQueryPreloadSupportsCompositeKeys(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"tenant_id", "id"},
				values:  [][]driver.Value{{int64(1), int64(10)}, {int64(2), int64(20)}},
			},
			{
				columns: []string{"tenant_id", "parent_id", "value"},
				values:  [][]driver.Value{{int64(2), int64(20), "second"}, {int64(1), int64(10), "first"}},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	tenants, err := Query[preloadTenant]().Preload("Records").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if got, want := tenants[0].Records[0].Value, "first"; got != want {
		t.Fatalf("first tenant record = %q, want %q", got, want)
	}
	if got, want := tenants[1].Records[0].Value, "second"; got != want {
		t.Fatalf("second tenant record = %q, want %q", got, want)
	}
	calls := preloadCalls(state)
	wantSQL := "SELECT `tenant_id`, `parent_id`, `value` FROM `preload_records`"
	if got := calls[1].query; got != wantSQL {
		t.Fatalf("preload query = %q, want %q", got, wantSQL)
	}
	if got := namedValues(calls[1].arguments); len(got) != 0 {
		t.Fatalf("preload arguments = %#v, want none", got)
	}
}

func TestSelectQueryPreloadSplitsCompositeKeysAtParameterBudget(t *testing.T) {
	parentRows := make([][]driver.Value, preloadParameterBudget/2+1)
	for index := range parentRows {
		parentRows[index] = []driver.Value{int64(index + 1), int64(index + 1001)}
	}
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"tenant_id", "id"}, values: parentRows},
			{columns: []string{"tenant_id", "parent_id", "value"}},
			{columns: []string{"tenant_id", "parent_id", "value"}},
		},
	}
	database := openPreloadTestDB(t, state)

	tenants, err := Query[preloadTenant]().Preload("Records").Limit(int64(len(parentRows))).All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(tenants) != len(parentRows) {
		t.Fatalf("tenant count = %d, want %d", len(tenants), len(parentRows))
	}
	calls := preloadCalls(state)
	if len(calls) != 3 {
		t.Fatalf("query count = %d, want 3", len(calls))
	}
	if got, want := len(calls[1].arguments), preloadParameterBudget; got != want {
		t.Fatalf("first batch argument count = %d, want %d", got, want)
	}
	if got, want := len(calls[2].arguments), 2; got != want {
		t.Fatalf("second batch argument count = %d, want %d", got, want)
	}
}

func TestSelectQueryPreloadSkipsNullSourceKeys(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{nil}}},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadNullableParent]().Preload("Children").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(parents) != 1 || parents[0].ID != nil || parents[0].Children != nil {
		t.Fatalf("parents = %#v, want one parent with nil key and relation", parents)
	}
	if got := len(preloadCalls(state)); got != 1 {
		t.Fatalf("query count = %d, want 1", got)
	}
}

func TestSelectQueryPreloadAllSkipsUnrelatedDirectTargetKeys(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
			{
				columns: []string{"id", "parent_id"},
				values: [][]driver.Value{
					{int64(10), nil},
					{int64(11), int64(2)},
					{int64(12), int64(1)},
				},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadNullableParent]().Preload("Children").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(parents) != 1 || len(parents[0].Children) != 1 || parents[0].Children[0].ID != 12 {
		t.Fatalf("parents = %#v, want only the matching child", parents)
	}
	calls := preloadCalls(state)
	if len(calls) != 2 || len(calls[1].arguments) != 0 {
		t.Fatalf("calls = %#v, want one argument-free relation query", calls)
	}
}

func TestSelectQueryPreloadUsesApplicationCustomKeyType(t *testing.T) {
	preloadCustomKeyValueCalls = 0
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{"account-a"}}},
			{columns: []string{"id", "parent_id"}, values: [][]driver.Value{{int64(1), "account-a"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadCustomParent]().Preload("Children").Limit(1).All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(parents) != 1 || parents[0].ID.text != "account-a" || len(parents[0].Children) != 1 || parents[0].Children[0].ParentID.text != "account-a" {
		t.Fatalf("parents = %#v, want hydrated custom relation key", parents)
	}
	if preloadCustomKeyValueCalls != 2 {
		t.Fatalf("custom key Value() calls = %d, want 2", preloadCustomKeyValueCalls)
	}
	calls := preloadCalls(state)
	if got, want := namedValues(calls[1].arguments), []any{"account-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preload arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryPreloadHydratesPointerSliceInNilEmbeddedContainer(t *testing.T) {
	state := &preloadTestState{
		responses: []*preloadTestResponse{
			{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}},
			{columns: []string{"id", "parent_id"}, values: [][]driver.Value{{int64(10), int64(1)}}},
		},
	}
	database := openPreloadTestDB(t, state)

	parents, err := Query[preloadEmbeddedParent]().Preload("Children").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(parents) != 1 || parents[0].PreloadEmbeddedRelations == nil || len(parents[0].Children) != 1 || parents[0].Children[0] == nil || parents[0].Children[0].ID != 10 {
		t.Fatalf("parents = %#v, want hydrated embedded pointer slice", parents)
	}
}

func TestPreloadKeyFieldSupportsNativeRepresentations(t *testing.T) {
	when := time.Date(2026, time.August, 29, 12, 34, 56, 789, time.FixedZone("test", 9*60*60))
	pointer := int64(9)
	value := preloadKeyKinds{
		Bool:   true,
		Int:    -7,
		Uint:   8,
		Float:  math.Copysign(0, -1),
		String: "key",
		Bytes:  []byte{0, 1, 2},
		Time:   when,
		Ptr:    &pointer,
	}
	descriptor, err := model.Describe[preloadKeyKinds]()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	root := reflect.ValueOf(&value).Elem()
	tests := []struct {
		field string
		want  preloadLookupKey
		arg   any
	}{
		{field: "Bool", want: preloadLookupKey{kind: 'b', first: 1}, arg: true},
		{field: "Int", want: preloadLookupKey{kind: 'i', first: ^uint64(6)}, arg: int64(-7)},
		{field: "Uint", want: preloadLookupKey{kind: 'u', first: 8}, arg: uint64(8)},
		{field: "Float", want: preloadLookupKey{kind: 'f'}, arg: float64(0)},
		{field: "String", want: preloadLookupKey{kind: 's', text: "key"}, arg: "key"},
		{field: "Bytes", want: preloadLookupKey{kind: 'x', text: string([]byte{0, 1, 2})}, arg: []byte{0, 1, 2}},
		{field: "Time", want: preloadLookupKey{kind: 't', first: uint64(when.Unix()), second: uint64(when.Nanosecond())}, arg: when},
		{field: "Ptr", want: preloadLookupKey{kind: 'i', first: 9}, arg: int64(9)},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			field, exists := descriptor.FieldByGoName(tt.field)
			if !exists {
				t.Fatalf("field %s does not exist", tt.field)
			}
			fieldValue := root.FieldByIndex(field.Index())
			lookup, argument, null, err := preloadKeyField(field, fieldValue, true)
			if err != nil {
				t.Fatalf("preloadKeyField() error = %v", err)
			}
			if null || lookup != tt.want || !reflect.DeepEqual(argument, tt.arg) {
				t.Fatalf("preloadKeyField() = %#v, %#v, %t, want %#v, %#v, false", lookup, argument, null, tt.want, tt.arg)
			}
			_, argument, _, err = preloadKeyField(field, fieldValue, false)
			if err != nil || argument != nil {
				t.Fatalf("preloadKeyField(no argument) argument = %#v, error = %v", argument, err)
			}
		})
	}

	value.Ptr = nil
	field, _ := descriptor.FieldByGoName("Ptr")
	_, argument, null, err := preloadKeyField(field, root.FieldByIndex(field.Index()), true)
	if err != nil || !null || argument != nil {
		t.Fatalf("nil pointer preloadKeyField() argument = %#v, null = %t, error = %v", argument, null, err)
	}
	value.Bytes = nil
	field, _ = descriptor.FieldByGoName("Bytes")
	_, argument, null, err = preloadKeyField(field, root.FieldByIndex(field.Index()), true)
	if err != nil || !null || argument != nil {
		t.Fatalf("nil bytes preloadKeyField() argument = %#v, null = %t, error = %v", argument, null, err)
	}
}

func TestPreloadDriverValueAndLookupValidation(t *testing.T) {
	when := time.Date(2026, time.August, 29, 1, 2, 3, 4, time.UTC)
	tests := []struct {
		name  string
		value driver.Value
		want  preloadLookupKey
	}{
		{name: "false", value: false, want: preloadLookupKey{kind: 'b'}},
		{name: "true", value: true, want: preloadLookupKey{kind: 'b', first: 1}},
		{name: "int", value: int64(-2), want: preloadLookupKey{kind: 'i', first: ^uint64(1)}},
		{name: "float", value: math.Copysign(0, -1), want: preloadLookupKey{kind: 'f'}},
		{name: "string", value: "key", want: preloadLookupKey{kind: 's', text: "key"}},
		{name: "bytes", value: []byte("key"), want: preloadLookupKey{kind: 'x', text: "key"}},
		{name: "time", value: when, want: preloadLookupKey{kind: 't', first: uint64(when.Unix()), second: uint64(when.Nanosecond())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup, err := preloadDriverLookupKey(tt.value)
			if err != nil || lookup != tt.want {
				t.Fatalf("preloadDriverLookupKey() = %#v, %v, want %#v, nil", lookup, err, tt.want)
			}
		})
	}
	if _, err := preloadDriverLookupKey(uint64(1)); err == nil {
		t.Fatal("preloadDriverLookupKey(uint64) error = nil")
	}

	pointer := preloadPointerValuer{value: "pointer"}
	got, err := preloadDriverValue(reflect.ValueOf(&pointer).Elem())
	if err != nil || got != "pointer" {
		t.Fatalf("preloadDriverValue(pointer receiver) = %#v, %v", got, err)
	}
	pointer.err = errors.New("valuer failure")
	if _, err := preloadDriverValue(reflect.ValueOf(&pointer).Elem()); !errors.Is(err, pointer.err) {
		t.Fatalf("preloadDriverValue(error) = %v, want valuer failure", err)
	}
	if _, err := preloadDriverValue(reflect.ValueOf(preloadUnsupportedValuer{})); err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("preloadDriverValue(unsupported) error = %v", err)
	}
	if _, err := preloadDriverValue(reflect.ValueOf(struct{}{})); err == nil || !strings.Contains(err.Error(), "does not expose") {
		t.Fatalf("preloadDriverValue(no valuer) error = %v", err)
	}
}

func TestAppendCompositePreloadKeyEncodesAllRepresentations(t *testing.T) {
	components := []preloadLookupKey{
		{kind: 'b', first: 1},
		{kind: 'i', first: 2},
		{kind: 'u', first: 3},
		{kind: 'f', first: math.Float64bits(4)},
		{kind: 't', first: 5, second: 6},
		{kind: 's', text: "seven"},
		{kind: 'x', text: "eight"},
	}
	var encoded []byte
	var err error
	for _, component := range components {
		encoded, err = appendCompositePreloadKey(encoded, component)
		if err != nil {
			t.Fatalf("appendCompositePreloadKey(%q) error = %v", component.kind, err)
		}
	}
	if len(encoded) == 0 {
		t.Fatal("encoded composite key is empty")
	}
	if _, err := appendCompositePreloadKey(nil, preloadLookupKey{kind: '?'}); err == nil {
		t.Fatal("appendCompositePreloadKey(unknown) error = nil")
	}
}

func TestSelectQueryPreloadFirstHydratesRelation(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{columns: []string{"id", "user_id", "total"}, values: [][]driver.Value{{int64(10), int64(1), "5.00"}}},
		},
	}
	database := openPreloadTestDB(t, state)

	user, err := Query[preloadUser]().Preload("Orders").First(context.Background(), database)
	if err != nil {
		t.Fatalf("First() error = %v", err)
	}
	if user.ID != 1 || len(user.Orders) != 1 || user.Orders[0].ID != 10 {
		t.Fatalf("user = %#v, want hydrated order", user)
	}
	calls := preloadCalls(state)
	if len(calls) != 2 || !strings.Contains(calls[1].query, " WHERE `user_id` IN (?)") {
		t.Fatalf("calls = %#v, want a keyed relation query after First", calls)
	}
	if got, want := namedValues(calls[1].arguments), []any{int64(1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preload arguments = %#v, want %#v", got, want)
	}
}

func TestSelectQueryPreloadLeavesMissingToOneNil(t *testing.T) {
	state := &preloadTestState{
		record: true,
		responses: []*preloadTestResponse{
			{
				columns: []string{"id", "email", "joined_profile_id", "joined_profile_user_id", "joined_profile_bio"},
				values:  [][]driver.Value{{int64(1), "user@example.com", nil, nil, nil}},
			},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Preload("Profile").All(context.Background(), database)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(users) != 1 || users[0].ID != 1 || users[0].Profile != nil {
		t.Fatalf("users = %#v, want one user with a nil Profile", users)
	}
	if got := len(preloadCalls(state)); got != 1 {
		t.Fatalf("query count = %d, want 1", got)
	}
}

func TestSelectQueryPreloadReportsSecondaryQueryFailure(t *testing.T) {
	queryFailure := errors.New("secondary query failure")
	state := &preloadTestState{
		responses: []*preloadTestResponse{
			{columns: []string{"id", "email"}, values: [][]driver.Value{{int64(1), "user@example.com"}}},
			{queryErr: queryFailure},
		},
	}
	database := openPreloadTestDB(t, state)

	users, err := Query[preloadUser]().Preload("Orders").All(context.Background(), database)
	if !errors.Is(err, queryFailure) || !strings.Contains(err.Error(), "preload preloadUser.Orders") {
		t.Fatalf("All() users = %#v, error = %v, want wrapped secondary query failure", users, err)
	}
}

func openPreloadTestDB(t testing.TB, state *preloadTestState) *sql.DB {
	t.Helper()
	database := sql.OpenDB(&preloadTestConnector{state: state})
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("DB.Close() error = %v", err)
		}
	})
	return database
}

func preloadCalls(state *preloadTestState) []preloadTestCall {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]preloadTestCall(nil), state.calls...)
}
