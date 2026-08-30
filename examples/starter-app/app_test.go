package starterapp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

func TestApplicationModelsCanBeDescribedOffline(t *testing.T) {
	t.Parallel()

	user := requireDescription[User](t, "users", []string{"id"})
	if got, want := columns(user), []string{"id", "email", "order_count"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("User columns = %#v, want %#v", got, want)
	}
	orderCount, exists := user.FieldByGoName("OrderCount")
	if !exists || !orderCount.IsComputed() {
		t.Fatalf("User.OrderCount metadata = %#v, exists = %t", orderCount, exists)
	}
	if _, exists := user.FieldByColumn("created_at"); exists {
		t.Fatal("database-managed created_at column must not be required by User")
	}
	if got, want := relations(user), []string{"Orders", "Roles"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("User relations = %#v, want %#v", got, want)
	}
	orders, exists := user.RelationByName("Orders")
	if !exists || orders.Kind() != model.RelationHasMany || !reflect.DeepEqual(columnsFromFields(orders.TargetKey()), []string{"user_id"}) {
		t.Fatalf("User.Orders metadata = %#v, exists = %t", orders, exists)
	}
	roles, exists := user.RelationByName("Roles")
	junction, hasJunction := roles.Junction()
	if !exists || roles.Kind() != model.RelationManyToMany || !hasJunction || junction.TableName() != "user_roles" {
		t.Fatalf("User.Roles metadata = %#v, junction = %#v", roles, junction)
	}

	order := requireDescription[Order](t, "orders", []string{"id"})
	total, exists := order.FieldByColumn("total")
	if !exists || total.Kind() != model.KindCustom || !total.UsesScanner() || !total.UsesValuer() {
		t.Fatalf("Order.Total metadata = %#v, exists = %t", total, exists)
	}
	owner, exists := order.RelationByName("User")
	if !exists || owner.Kind() != model.RelationBelongsTo || !reflect.DeepEqual(columnsFromFields(owner.SourceKey()), []string{"user_id"}) {
		t.Fatalf("Order.User metadata = %#v, exists = %t", owner, exists)
	}

	role := requireDescription[Role](t, "roles", []string{"id"})
	users, exists := role.RelationByName("Users")
	if !exists || users.Kind() != model.RelationManyToMany {
		t.Fatalf("Role.Users metadata = %#v, exists = %t", users, exists)
	}
	requireDescription[UserRole](t, "user_roles", []string{"user_id", "role_id"})
	clip := requireDescription[Clip](t, "clips", []string{"id"})
	clipGenres, exists := clip.RelationByName("ClipGenres")
	if !exists || clipGenres.Kind() != model.RelationHasMany || !reflect.DeepEqual(columnsFromFields(clipGenres.TargetKey()), []string{"clip_id"}) {
		t.Fatalf("Clip.ClipGenres metadata = %#v, exists = %t", clipGenres, exists)
	}
	requireDescription[ClipGenre](t, "clip_genres", []string{"clip_id", "genre_id"})
	video := requireDescription[Video](t, "videos", []string{"id"})
	deletedAt, exists := video.SoftDeleteField()
	if !exists || deletedAt.GoName() != "DeletedAt" || !deletedAt.IsSoftDelete() {
		t.Fatalf("Video soft-delete metadata = %#v, exists = %t", deletedAt, exists)
	}
	watchLater := requireDescription[WatchLater](t, "user_watch_later_videos", []string{"user_id", "video_id"})
	videoRelation, exists := watchLater.RelationByName("Video")
	if !exists || videoRelation.Kind() != model.RelationBelongsTo {
		t.Fatalf("WatchLater.Video metadata = %#v, exists = %t", videoRelation, exists)
	}
}

func TestApplicationModelsPassOfflineChecks(t *testing.T) {
	t.Parallel()

	if diagnostics := CheckModels(); len(diagnostics) != 0 {
		t.Fatalf("CheckModels() = %#v, want no diagnostics", diagnostics)
	}
}

func TestApplicationSchemaCanBeCheckedOffline(t *testing.T) {
	t.Parallel()

	sqlText := `CREATE TABLE users (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  email VARCHAR(255) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (id)
);
CREATE TABLE orders (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  user_id BIGINT NOT NULL,
  total DECIMAL(20, 2) NOT NULL,
  PRIMARY KEY (id),
  KEY orders_user_id (user_id)
);
CREATE TABLE roles (
  id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (id)
);
CREATE TABLE user_roles (
  user_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  PRIMARY KEY (user_id, role_id)
);`
	diagnostics, err := CheckUserSchema(sqlText)
	if err != nil {
		t.Fatalf("CheckUserSchema() error = %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("CheckUserSchema() = %#v, want no diagnostics", diagnostics)
	}
}

func TestApplicationRelationsUseOrdinaryGoValues(t *testing.T) {
	t.Parallel()

	user := User{
		ID:     1,
		Orders: []Order{{ID: 2, UserID: 1}},
		Roles:  []Role{{ID: 7, Name: "admin"}},
	}
	if len(user.Orders) != 1 || user.Orders[0].UserID != user.ID {
		t.Fatalf("User.Orders = %#v", user.Orders)
	}
	if len(user.Roles) != 1 || user.Roles[0].Name != "admin" {
		t.Fatalf("User.Roles = %#v", user.Roles)
	}

	order := Order{ID: 2, UserID: user.ID, User: &user}
	if order.User != &user {
		t.Fatalf("Order.User = %#v", order.User)
	}
}

func TestApplicationDecimalUsesDatabaseSQLInterfaces(t *testing.T) {
	t.Parallel()

	var value Decimal
	if err := value.Scan([]byte("12.30")); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	driverValue, err := value.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	if driverValue != "12.30" {
		t.Fatalf("Value() = %#v, want %q", driverValue, "12.30")
	}
	if err := value.Scan(nil); err == nil {
		t.Fatal("Scan(nil) error = nil, want error")
	}
}

func TestApplicationBuildsScalarQueryOffline(t *testing.T) {
	t.Parallel()

	if diagnostics := CheckRecentOrdersQuery(7, 1000); len(diagnostics) != 0 {
		t.Fatalf("CheckRecentOrdersQuery() = %#v, want none", diagnostics)
	}
	sqlText, arguments, err := BuildRecentOrdersQuery(7, 1000)
	if err != nil {
		t.Fatalf("BuildRecentOrdersQuery() error = %v", err)
	}
	wantSQL := "SELECT `id`, `user_id`, `total` FROM `orders` WHERE `user_id` = ? AND (`id` < ?) ORDER BY `id` DESC LIMIT ?"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{int64(7), int64(1000), int64(100)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestApplicationBuildsRelationFirstTopNQueryOffline(t *testing.T) {
	t.Parallel()

	if diagnostics := CheckRecentClipsInGenreQuery(7); len(diagnostics) != 0 {
		t.Fatalf("CheckRecentClipsInGenreQuery() = %#v, want none", diagnostics)
	}
	sqlText, arguments, err := BuildRecentClipsInGenreQuery(7)
	if err != nil {
		t.Fatalf("BuildRecentClipsInGenreQuery() error = %v", err)
	}
	wantSQL := "SELECT `tidbgo_t0`.`id`, `tidbgo_t0`.`title` FROM (SELECT `tidbgo_a0`.`clip_id` FROM `clip_genres` AS `tidbgo_a0` WHERE `tidbgo_a0`.`genre_id` = ? ORDER BY `tidbgo_a0`.`clip_id` DESC LIMIT ?) AS `tidbgo_k0` JOIN `clips` AS `tidbgo_t0` ON (`tidbgo_k0`.`clip_id` = `tidbgo_t0`.`id`) ORDER BY `tidbgo_t0`.`id` DESC"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{int64(7), int64(20)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestApplicationBuildsMutationsOffline(t *testing.T) {
	t.Parallel()

	user := User{ID: 7, Email: "ada@example.test", OrderCount: 99}
	tests := []struct {
		name     string
		query    interface{ Build() (string, []any, error) }
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "insert",
			query:    orm.Insert(&user),
			wantSQL:  "INSERT INTO `users` (`email`) VALUES (?)",
			wantArgs: []any{"ada@example.test"},
		},
		{
			name:     "upsert",
			query:    orm.Upsert(&user),
			wantSQL:  "INSERT INTO `users` (`email`) VALUES (?) ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)",
			wantArgs: []any{"ada@example.test"},
		},
		{
			name:     "full update",
			query:    orm.Update(&user),
			wantSQL:  "UPDATE `users` SET `email` = ? WHERE `id` = ?",
			wantArgs: []any{"ada@example.test", int64(7)},
		},
		{
			name:     "partial update",
			query:    orm.Update(&user, "Email"),
			wantSQL:  "UPDATE `users` SET `email` = ? WHERE `id` = ?",
			wantArgs: []any{"ada@example.test", int64(7)},
		},
		{
			name:     "delete",
			query:    orm.Delete(&user),
			wantSQL:  "DELETE FROM `users` WHERE `id` = ?",
			wantArgs: []any{int64(7)},
		},
		{
			name:     "predicate delete",
			query:    orm.DeleteWhere[Order](orm.Equal("UserID", int64(7))),
			wantSQL:  "DELETE FROM `orders` WHERE `user_id` = ?",
			wantArgs: []any{int64(7)},
		},
		{
			name:     "relation add",
			query:    orm.AddRelation[User]("Roles", int64(7), int64(11), int64(12)),
			wantSQL:  "INSERT INTO `user_roles` (`user_id`, `role_id`) VALUES (?, ?), (?, ?)",
			wantArgs: []any{int64(7), int64(11), int64(7), int64(12)},
		},
		{
			name:     "relation remove",
			query:    orm.RemoveRelation[User]("Roles", int64(7), int64(11), int64(12)),
			wantSQL:  "DELETE FROM `user_roles` WHERE `user_id` = ? AND `role_id` IN (?, ?)",
			wantArgs: []any{int64(7), int64(11), int64(12)},
		},
		{
			name:     "relation clear",
			query:    orm.ClearRelation[User]("Roles", int64(7)),
			wantSQL:  "DELETE FROM `user_roles` WHERE `user_id` = ?",
			wantArgs: []any{int64(7)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sqlText, arguments, err := test.query.Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if sqlText != test.wantSQL || !reflect.DeepEqual(arguments, test.wantArgs) {
				t.Fatalf("Build() = %q, %#v, want %q, %#v", sqlText, arguments, test.wantSQL, test.wantArgs)
			}
		})
	}
}

func TestApplicationBuildsSoftDeleteFlowsOffline(t *testing.T) {
	t.Parallel()

	activeSQL, _, err := orm.Query[Video]().Build()
	if err != nil {
		t.Fatalf("active video Build() error = %v", err)
	}
	if want := "SELECT `id`, `title`, `deleted_at` FROM `videos` WHERE `deleted_at` IS NULL"; activeSQL != want {
		t.Fatalf("active video SQL = %q, want %q", activeSQL, want)
	}
	allSQL, _, err := orm.Query[Video]().WithDeleted().Build()
	if err != nil {
		t.Fatalf("all video Build() error = %v", err)
	}
	if want := "SELECT `id`, `title`, `deleted_at` FROM `videos`"; allSQL != want {
		t.Fatalf("all video SQL = %q, want %q", allSQL, want)
	}

	watchSQL, _, err := orm.Query[WatchLater]().Preload("Video", orm.PreloadWithDeleted()).Build()
	if err != nil {
		t.Fatalf("watch-later Build() error = %v", err)
	}
	if strings.Contains(watchSQL, "`tidbgo_t1`.`deleted_at` IS NULL") {
		t.Fatalf("watch-later SQL filtered deleted Video = %q", watchSQL)
	}

	video := Video{ID: 7, Title: "demo"}
	deleteSQL, arguments, err := orm.Delete(&video).Build()
	if err != nil {
		t.Fatalf("Delete().Build() error = %v", err)
	}
	wantDelete := "UPDATE `videos` SET `deleted_at` = CURRENT_TIMESTAMP(6) WHERE `id` = ? AND `deleted_at` IS NULL"
	if deleteSQL != wantDelete || !reflect.DeepEqual(arguments, []any{int64(7)}) {
		t.Fatalf("Delete().Build() = %q, %#v, want %q, [7]", deleteSQL, arguments, wantDelete)
	}

	restoreSQL, arguments, err := orm.UpdateWhere[Video](orm.Set("DeletedAt", time.Time{})).WithDeleted().Where(orm.Equal("ID", int64(7))).Build()
	if err != nil {
		t.Fatalf("restore Build() error = %v", err)
	}
	if want := "UPDATE `videos` SET `deleted_at` = ? WHERE `id` = ?"; restoreSQL != want || len(arguments) != 2 || arguments[0] != nil {
		t.Fatalf("restore Build() = %q, %#v, want %q", restoreSQL, arguments, want)
	}
}

func TestApplicationBuildsBulkInsertAndTypedRawQueryOffline(t *testing.T) {
	t.Parallel()

	orders := []*Order{
		{UserID: 7, Total: Decimal{text: "10.50"}},
		{UserID: 7, Total: Decimal{text: "20.25"}},
	}
	sqlText, arguments, err := orm.InsertMany(orders).Build()
	if err != nil {
		t.Fatalf("InsertMany().Build() error = %v", err)
	}
	wantSQL := "INSERT INTO `orders` (`user_id`, `total`) VALUES (?, ?), (?, ?)"
	if sqlText != wantSQL || len(arguments) != 4 {
		t.Fatalf("InsertMany().Build() = %q, %#v", sqlText, arguments)
	}

	users := []*User{
		{Email: "ada@example.test"},
		{Email: "grace@example.test"},
	}
	sqlText, arguments, err = orm.UpsertMany(users).Build()
	if err != nil {
		t.Fatalf("UpsertMany().Build() error = %v", err)
	}
	wantSQL = "INSERT INTO `users` (`email`) VALUES (?), (?) ON DUPLICATE KEY UPDATE `email` = VALUES(`email`)"
	if sqlText != wantSQL || !reflect.DeepEqual(arguments, []any{"ada@example.test", "grace@example.test"}) {
		t.Fatalf("UpsertMany().Build() = %q, %#v, want %q", sqlText, arguments, wantSQL)
	}

	rawSQL, rawArguments, err := orm.Raw[User](
		"SELECT id, email, COUNT(*) AS order_count FROM users WHERE id = ? GROUP BY id, email",
		int64(7),
	).Build()
	if err != nil {
		t.Fatalf("Raw().Build() error = %v", err)
	}
	if rawSQL == "" || !reflect.DeepEqual(rawArguments, []any{int64(7)}) {
		t.Fatalf("Raw().Build() = %q, %#v", rawSQL, rawArguments)
	}
}

func TestApplicationBuildsDirectPreloadParentQueryOffline(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := usersWithOrdersQuery().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `email`, `id` FROM `users` ORDER BY `id` ASC"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if arguments != nil {
		t.Fatalf("arguments = %#v, want nil", arguments)
	}
}

func TestApplicationBuildsManyToManyPreloadParentQueryOffline(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := usersWithRolesQuery().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := sqlText, "SELECT `email`, `id` FROM `users` ORDER BY `id` ASC"; got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
	if arguments != nil {
		t.Fatalf("arguments = %#v, want nil", arguments)
	}
}

func TestApplicationBuildsRelationPredicateOffline(t *testing.T) {
	t.Parallel()

	sqlText, arguments, err := usersInRoleQuery("admin").Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	wantSQL := "SELECT `id`, `email` FROM `users` AS `tidbgo_r0` WHERE EXISTS (SELECT /*+ SEMI_JOIN_REWRITE() */ 1 FROM `user_roles` AS `tidbgo_j1` JOIN `roles` AS `tidbgo_r1` ON (`tidbgo_r1`.`id` = `tidbgo_j1`.`role_id`) WHERE (`tidbgo_j1`.`user_id` = `tidbgo_r0`.`id`) AND `tidbgo_r1`.`name` = ?) ORDER BY `tidbgo_r0`.`id` ASC"
	if sqlText != wantSQL {
		t.Fatalf("SQL = %q, want %q", sqlText, wantSQL)
	}
	if got, want := arguments, []any{"admin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func columns(descriptor *model.Descriptor) []string {
	fields := descriptor.Fields()
	result := make([]string, len(fields))
	for index, field := range fields {
		result[index] = field.ColumnName()
	}
	return result
}

func requireDescription[T any](t *testing.T, table string, primaryKey []string) *model.Descriptor {
	t.Helper()
	descriptor, err := model.Describe[T]()
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	if descriptor.TableName() != table {
		t.Fatalf("TableName() = %q, want %q", descriptor.TableName(), table)
	}
	if got := columnsFromFields(descriptor.PrimaryKeyFields()); !reflect.DeepEqual(got, primaryKey) {
		t.Fatalf("primary key = %#v, want %#v", got, primaryKey)
	}
	return descriptor
}

func columnsFromFields(fields []model.Field) []string {
	result := make([]string, len(fields))
	for index, field := range fields {
		result[index] = field.ColumnName()
	}
	return result
}

func relations(descriptor *model.Descriptor) []string {
	metadata := descriptor.Relations()
	result := make([]string, len(metadata))
	for index, relation := range metadata {
		result[index] = relation.GoName()
	}
	return result
}
