package store_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/migrations"
)

// The profile migration must upgrade a database of the previous release and keep its data.
func TestExternalAgentProfileMigrationKeepsExistingData(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN is required for PostgreSQL behavior tests")
	}
	ctx := t.Context()
	admin, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	schema := "migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, dropErr := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, dropErr)
		require.NoError(t, admin.Close())
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	const previousRelease = 9
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, previousRelease)
	require.NoError(t, err)
	user, chat := uuid.NewString(), uuid.NewString()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO users(id,issuer,subject) VALUES($1,'https://issuer.example','alice')", []any{user}},
		{"INSERT INTO memberships(owner_id,org_id) VALUES($1,'org')", []any{user}},
		{"INSERT INTO chats(id,owner_id,org_id,is_default,next_seq) VALUES($1,$2,'org',true,2)", []any{chat, user}},
		{
			"INSERT INTO chat_events(chat_id,seq,kind,text,doc,source_actor) VALUES($1,1,'message.user','hello','{}','alice')",
			[]any{chat},
		},
	} {
		_, err = db.ExecContext(ctx, statement.query, statement.args...)
		require.NoError(t, err)
	}

	require.NoError(t, store.Migrate(ctx, parsed.String()))
	_, err = db.ExecContext(
		ctx,
		"INSERT INTO external_agent_profiles(owner_id,org_id,enabled,history,memory,version,confirmed_by) VALUES($1,'org',true,true,false,1,'chat-client')",
		user,
	)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx, parsed.String()), "a repeated migration command must be safe")

	var text string
	var nextSeq, version int64
	require.NoError(
		t,
		db.QueryRowContext(ctx, "SELECT e.text,c.next_seq FROM chat_events e JOIN chats c ON c.id=e.chat_id WHERE c.owner_id=$1", user).
			Scan(&text, &nextSeq),
	)
	require.Equal(t, "hello", text)
	require.EqualValues(t, 2, nextSeq)
	require.NoError(
		t,
		db.QueryRowContext(ctx, "SELECT version FROM external_agent_profiles WHERE owner_id=$1 AND org_id='org'", user).
			Scan(&version),
	)
	require.EqualValues(t, 1, version)
}
