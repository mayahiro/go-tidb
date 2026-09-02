// Package starterapp demonstrates struct-first models, queries, and mutations.
package starterapp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mayahiro/go-tidb/model"
	"github.com/mayahiro/go-tidb/orm"
)

var (
	_ sql.Scanner   = (*Decimal)(nil)
	_ driver.Valuer = Decimal{}
)

// Decimal is an application-selected decimal representation for the example.
// A real application can use any type compatible with database/sql instead.
type Decimal struct {
	text string
}

// Scan implements sql.Scanner without coupling tidbgo to a decimal library.
func (value *Decimal) Scan(source any) error {
	if value == nil {
		return errors.New("starterapp: scan Decimal into nil receiver")
	}
	switch current := source.(type) {
	case string:
		value.text = current
	case []byte:
		value.text = string(current)
	default:
		return fmt.Errorf("starterapp: cannot scan Decimal from %T", source)
	}
	return nil
}

// Value implements driver.Valuer.
func (value Decimal) Value() (driver.Value, error) {
	return value.text, nil
}

// User is the application-owned representation used by this example.
// The database-managed created_at column is intentionally omitted.
type User struct {
	model.Meta `tidbgo:"table=users"`
	ID         int64   `tidbgo:",pk,auto_random"`
	Email      string  `tidbgo:"email"`
	OrderCount int64   `tidbgo:"order_count,computed"`
	Orders     []Order `tidbgo:"has_many"`
	Roles      []Role  `tidbgo:"many_to_many,through=user_roles,source=ID:user_id,target=role_id:ID"`
}

// Order is the application-owned order representation.
type Order struct {
	model.Meta `tidbgo:"table=orders"`
	ID         int64 `tidbgo:",pk,auto_random"`
	UserID     int64
	Total      Decimal
	User       *User `tidbgo:"belongs_to"`
}

// Role is the application-owned role representation.
type Role struct {
	model.Meta `tidbgo:"table=roles"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Name       string
	Users      []User `tidbgo:"many_to_many,through=user_roles,source=ID:role_id,target=user_id:ID"`
}

// UserRole is the application-owned pure-junction representation.
type UserRole struct {
	model.Meta `tidbgo:"table=user_roles"`
	UserID     int64 `tidbgo:",pk"`
	RoleID     int64 `tidbgo:",pk"`
}

// Clip is an application-owned root model used for relation-filtered TopN.
type Clip struct {
	model.Meta `tidbgo:"table=clips"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Title      string
	ClipGenres []ClipGenre `tidbgo:"has_many,join=ID:ClipID"`
}

// ClipGenre is an application-owned association model with one row per pair.
type ClipGenre struct {
	model.Meta `tidbgo:"table=clip_genres"`
	ClipID     int64 `tidbgo:",pk"`
	GenreID    int64 `tidbgo:",pk"`
}

// JobLease is an application-owned conditional-update model.
type JobLease struct {
	model.Meta `tidbgo:"table=job_leases"`
	JobID      int64 `tidbgo:",pk"`
	LockOwner  *string
	LockUntil  *time.Time
	RetryCount int64
	LastError  *string
}

// Video demonstrates a value-form nullable soft-delete timestamp.
type Video struct {
	model.Meta `tidbgo:"table=videos"`
	ID         int64 `tidbgo:",pk,auto_random"`
	Title      string
	DeletedAt  time.Time `tidbgo:",soft_delete"`
}

// WatchLater demonstrates relation-specific inclusion of deleted videos.
type WatchLater struct {
	model.Meta `tidbgo:"table=user_watch_later_videos"`
	UserID     int64  `tidbgo:",pk"`
	VideoID    int64  `tidbgo:",pk"`
	Video      *Video `tidbgo:"belongs_to"`
}

// BuildRecentOrdersQuery compiles a keyset-paginated query without a database
// connection or generated code.
func BuildRecentOrdersQuery(userID, afterID int64) (string, []any, error) {
	return recentOrdersQuery(userID, afterID).Build()
}

func recentOrdersQuery(userID, afterID int64) *orm.SelectQuery[Order] {
	return orm.Query[Order]().
		Select("ID", "UserID", "Total").
		Where(orm.Equal("UserID", userID)).
		OrderBy(orm.Desc("ID")).
		SeekAfter(afterID).
		Limit(100)
}

// BuildRecentClipsInGenreQuery compiles a relation-filtered TopN query that
// can apply LIMIT to clip_genres before loading Clip rows.
func BuildRecentClipsInGenreQuery(genreID int64) (string, []any, error) {
	return recentClipsInGenreQuery(genreID).Build()
}

func recentClipsInGenreQuery(genreID int64) *orm.SelectQuery[Clip] {
	return orm.Query[Clip]().
		Select("ID", "Title").
		Where(orm.Has("ClipGenres", orm.Equal("GenreID", genreID))).
		OrderBy(orm.Desc("ID")).
		Limit(20)
}

// ListRecentClipsInGenre returns the newest clips having one matching
// ClipGenre row through an explicitly supplied database/sql executor.
func ListRecentClipsInGenre(ctx context.Context, executor orm.QueryExecutor, genreID int64) ([]Clip, error) {
	return recentClipsInGenreQuery(genreID).All(ctx, executor)
}

// FirstRecentOrder returns the newest order for a user through an explicitly
// supplied database/sql executor.
func FirstRecentOrder(ctx context.Context, executor orm.QueryExecutor, userID int64) (Order, error) {
	return orm.Query[Order]().
		Select("ID", "UserID", "Total").
		Where(orm.Equal("UserID", userID)).
		OrderBy(orm.Desc("ID")).
		First(ctx, executor)
}

// FindUserByEmail returns the only user matching an email address through an
// explicitly supplied database/sql executor.
func FindUserByEmail(ctx context.Context, executor orm.QueryExecutor, email string) (User, error) {
	return orm.Query[User]().
		Select("ID", "Email").
		Where(orm.Equal("Email", email)).
		Only(ctx, executor)
}

// ExplainUserByEmail asks TiDB for the execution plan of the typed user lookup.
func ExplainUserByEmail(ctx context.Context, executor orm.QueryExecutor, email string) ([]orm.ExplainRow, error) {
	return orm.Query[User]().
		Select("ID", "Email").
		Where(orm.Equal("Email", email)).
		Explain(ctx, executor)
}

// ExplainAnalyzeUserByEmail executes the typed lookup and returns TiDB's
// runtime execution plan.
func ExplainAnalyzeUserByEmail(ctx context.Context, executor orm.QueryExecutor, email string) (orm.ExplainAnalyzePlan, error) {
	return orm.Query[User]().
		Select("ID", "Email").
		Where(orm.Equal("Email", email)).
		ExplainAnalyze(ctx, executor)
}

// FindUserByEmailWithServerRU runs one query on a pinned connection and reads
// the ServerRU reported by TiDB for that completed DML statement.
func FindUserByEmailWithServerRU(ctx context.Context, connection *sql.Conn, email string) (User, float64, error) {
	user, err := FindUserByEmail(ctx, connection, email)
	if err != nil {
		return User{}, 0, err
	}
	serverRU, err := orm.LastServerRU(ctx, connection)
	return user, serverRU, err
}

// HasUserWithEmail reports whether an email address is already present through
// an explicitly supplied database/sql executor.
func HasUserWithEmail(ctx context.Context, executor orm.QueryExecutor, email string) (bool, error) {
	return orm.Query[User]().
		Where(orm.Equal("Email", email)).
		Exists(ctx, executor)
}

// CountOrdersForUser returns the number of orders owned by a user through an
// explicitly supplied database/sql executor.
func CountOrdersForUser(ctx context.Context, executor orm.QueryExecutor, userID int64) (int64, error) {
	return orm.Query[Order]().
		Where(orm.Equal("UserID", userID)).
		Count(ctx, executor)
}

// ListVideos returns active videos through the default soft-delete scope.
func ListVideos(ctx context.Context, executor orm.QueryExecutor) ([]Video, error) {
	return orm.Query[Video]().OrderBy(orm.Asc("ID")).All(ctx, executor)
}

// ListVideosWithDeleted returns active and logically deleted videos.
func ListVideosWithDeleted(ctx context.Context, executor orm.QueryExecutor) ([]Video, error) {
	return orm.Query[Video]().WithDeleted().OrderBy(orm.Asc("ID")).All(ctx, executor)
}

// ListWatchLaterVideos keeps a watch-later edge even when its Video target is
// logically deleted.
func ListWatchLaterVideos(ctx context.Context, executor orm.QueryExecutor, userID int64) ([]WatchLater, error) {
	return orm.Query[WatchLater]().
		Where(orm.Equal("UserID", userID)).
		Preload("Video", orm.PreloadWithDeleted()).
		OrderBy(orm.Asc("VideoID")).
		All(ctx, executor)
}

// ListUsersWithOrders returns users with projected, ordered Orders loaded in a
// secondary query and each order's nested User joined into that statement.
func ListUsersWithOrders(ctx context.Context, executor orm.QueryExecutor) ([]User, error) {
	return usersWithOrdersQuery().All(ctx, executor)
}

// LoadUserWithOrderCount uses explicit SQL for an aggregate while retaining
// model-aware result scanning.
func LoadUserWithOrderCount(ctx context.Context, executor orm.QueryExecutor, userID int64) (User, error) {
	return orm.Raw[User](`
SELECT u.id, u.email, COUNT(o.id) AS order_count
FROM users AS u
LEFT JOIN orders AS o ON o.user_id = u.id
WHERE u.id = ?
GROUP BY u.id, u.email`, userID).Only(ctx, executor)
}

// WithQueryLog enables context-scoped statement logging for this example.
// Interactive terminal writers receive colored operation names automatically.
func WithQueryLog(ctx context.Context, writer io.Writer) context.Context {
	return orm.WithStatementObserver(ctx, orm.NewStatementLogger(writer))
}

// InsertUser inserts one user and writes its AUTO_RANDOM ID back to value.
func InsertUser(ctx context.Context, executor orm.ExecExecutor, value *User) (int64, error) {
	return orm.Insert(value).Exec(ctx, executor)
}

// InsertOrders inserts orders with automatically bounded multi-row statements.
func InsertOrders(ctx context.Context, executor orm.ExecExecutor, values []*Order) (int64, error) {
	return orm.InsertMany(values).Exec(ctx, executor)
}

// UpsertUser inserts a user or updates its writable fields when a database
// unique key conflicts.
func UpsertUser(ctx context.Context, executor orm.ExecExecutor, value *User) (int64, error) {
	return orm.Upsert(value).Exec(ctx, executor)
}

// UpsertUsers inserts or updates users with automatically bounded bulk
// statements.
func UpsertUsers(ctx context.Context, executor orm.ExecExecutor, values []*User) (int64, error) {
	return orm.UpsertMany(values).Exec(ctx, executor)
}

// SaveUser updates every writable User field using the model primary key.
func SaveUser(ctx context.Context, executor orm.ExecExecutor, value *User) (int64, error) {
	return orm.Update(value).Exec(ctx, executor)
}

// SaveUserAndInsertOrders updates one user and inserts every order in one
// transaction, including any automatically split order batches.
func SaveUserAndInsertOrders(
	ctx context.Context,
	beginner orm.TransactionBeginner,
	value *User,
	orders []*Order,
) error {
	return orm.Transaction(ctx, beginner, func(transaction *sql.Tx) error {
		if _, err := orm.Update(value).Exec(ctx, transaction); err != nil {
			return err
		}
		_, err := orm.InsertMany(orders).Exec(ctx, transaction)
		return err
	})
}

// UpdateUserEmail updates only the Email field using the model primary key.
func UpdateUserEmail(ctx context.Context, executor orm.ExecExecutor, value *User) (int64, error) {
	return orm.Update(value, "Email").Exec(ctx, executor)
}

// ClaimJobLease claims one unlocked or expired job without a read-before-write
// race.
func ClaimJobLease(ctx context.Context, executor orm.ExecExecutor, jobID int64, owner string, now, lockUntil time.Time) (int64, error) {
	return orm.UpdateWhere[JobLease](
		orm.Set("LockOwner", owner),
		orm.Set("LockUntil", lockUntil),
	).Where(
		orm.Equal("JobID", jobID),
		orm.Or(orm.IsNull("LockUntil"), orm.LessThanOrEqual("LockUntil", now)),
	).Exec(ctx, executor)
}

// FailJobLease atomically increments retry state and releases an owned lease.
func FailJobLease(ctx context.Context, executor orm.ExecExecutor, jobID int64, owner, message string) (int64, error) {
	return orm.UpdateWhere[JobLease](
		orm.Increment("RetryCount", int64(1)),
		orm.Set("LastError", message),
		orm.Set("LockOwner", nil),
		orm.Set("LockUntil", nil),
	).Where(
		orm.Equal("JobID", jobID),
		orm.Equal("LockOwner", owner),
	).Exec(ctx, executor)
}

// DeleteUser deletes one user using the model primary key.
func DeleteUser(ctx context.Context, executor orm.ExecExecutor, value *User) (int64, error) {
	return orm.Delete(value).Exec(ctx, executor)
}

// DeleteOrdersForUser deletes orders through an explicit predicate.
func DeleteOrdersForUser(ctx context.Context, executor orm.ExecExecutor, userID int64) (int64, error) {
	return orm.DeleteWhere[Order](orm.Equal("UserID", userID)).Exec(ctx, executor)
}

// DeleteVideo sets Video.DeletedAt through one server-timestamped UPDATE.
func DeleteVideo(ctx context.Context, executor orm.ExecExecutor, value *Video) (int64, error) {
	return orm.Delete(value).Exec(ctx, executor)
}

// RestoreVideo clears Video.DeletedAt for one primary key.
func RestoreVideo(ctx context.Context, executor orm.ExecExecutor, videoID int64) (int64, error) {
	return orm.UpdateWhere[Video](
		orm.Set("DeletedAt", time.Time{}),
	).WithDeleted().Where(
		orm.Equal("ID", videoID),
	).Exec(ctx, executor)
}

// AddUserRoles adds role IDs to one user with a single pure-junction INSERT.
func AddUserRoles(ctx context.Context, executor orm.ExecExecutor, userID int64, roleIDs ...int64) (int64, error) {
	return orm.AddRelation[User]("Roles", userID, roleIDs...).Exec(ctx, executor)
}

// AddUserRolesIfMissing adds only junction rows that do not already exist.
func AddUserRolesIfMissing(ctx context.Context, executor orm.ExecExecutor, userID int64, roleIDs ...int64) (int64, error) {
	return orm.AddRelation[User]("Roles", userID, roleIDs...).IgnoreExisting().Exec(ctx, executor)
}

// RemoveUserRoles removes selected role IDs from one user in one statement.
func RemoveUserRoles(ctx context.Context, executor orm.ExecExecutor, userID int64, roleIDs ...int64) (int64, error) {
	return orm.RemoveRelation[User]("Roles", userID, roleIDs...).Exec(ctx, executor)
}

// ClearUserRoles removes every role associated with one user.
func ClearUserRoles(ctx context.Context, executor orm.ExecExecutor, userID int64) (int64, error) {
	return orm.ClearRelation[User]("Roles", userID).Exec(ctx, executor)
}

// ListUsersWithRoles returns users and hydrates their Roles relation through a
// deterministic pure-junction secondary query.
func ListUsersWithRoles(ctx context.Context, executor orm.QueryExecutor) ([]User, error) {
	return usersWithRolesQuery().All(ctx, executor)
}

// ListUsersInRole returns users having at least one matching Role without
// hydrating the Roles relation.
func ListUsersInRole(ctx context.Context, executor orm.QueryExecutor, roleName string) ([]User, error) {
	return usersInRoleQuery(roleName).All(ctx, executor)
}

func usersWithOrdersQuery() *orm.SelectQuery[User] {
	return orm.Query[User]().
		Select("Email").
		Preload("Orders", orm.PreloadFields("ID", "Total"), orm.PreloadOrderBy(orm.Desc("ID"))).
		Preload("Orders.User").
		OrderBy(orm.Asc("ID"))
}

func usersWithRolesQuery() *orm.SelectQuery[User] {
	return orm.Query[User]().
		Select("Email").
		Preload("Roles").
		OrderBy(orm.Asc("ID"))
}

func usersInRoleQuery(roleName string) *orm.SelectQuery[User] {
	return orm.Query[User]().
		Select("ID", "Email").
		Where(orm.Has("Roles", orm.Equal("Name", roleName))).
		OrderBy(orm.Asc("ID"))
}
