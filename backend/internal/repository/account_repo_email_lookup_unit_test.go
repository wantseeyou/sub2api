package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newAccountEntRepo(t *testing.T) (*accountRepository, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return newAccountRepositoryWithSQL(client, db, nil), client
}

func TestAccountRepository_ListByPlatformTypeCredentialEmail(t *testing.T) {
	repo, client := newAccountEntRepo(t)
	ctx := context.Background()
	email := "owner@example.com"

	matching := createAccountForCredentialEmailLookup(t, client, service.Account{
		Name:        "matching-openai-oauth",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"email": email},
	})
	createAccountForCredentialEmailLookup(t, client, service.Account{
		Name:        "different-platform",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"email": email},
	})
	createAccountForCredentialEmailLookup(t, client, service.Account{
		Name:        "different-type",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"email": email},
	})
	createAccountForCredentialEmailLookup(t, client, service.Account{
		Name:        "different-email",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"email": "other@example.com"},
	})

	accounts, err := repo.ListByPlatformTypeCredentialEmail(ctx, service.PlatformOpenAI, service.AccountTypeOAuth, "  "+email+"  ")
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, matching.ID, accounts[0].ID)

	accounts, err = repo.ListByPlatformTypeCredentialEmail(ctx, service.PlatformOpenAI, service.AccountTypeOAuth, "   ")
	require.NoError(t, err)
	require.Empty(t, accounts)
}

func createAccountForCredentialEmailLookup(t *testing.T, client *dbent.Client, account service.Account) *dbent.Account {
	t.Helper()

	if account.Status == "" {
		account.Status = service.StatusActive
	}
	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}

	created, err := client.Account.Create().
		SetName(account.Name).
		SetPlatform(account.Platform).
		SetType(account.Type).
		SetStatus(account.Status).
		SetCredentials(account.Credentials).
		SetExtra(account.Extra).
		SetConcurrency(account.Concurrency).
		SetPriority(account.Priority).
		SetSchedulable(account.Schedulable).
		Save(context.Background())
	require.NoError(t, err)
	return created
}
