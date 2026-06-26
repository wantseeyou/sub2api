package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExternalAccountSyncService_SyncOnce_SkipsEmptyEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":1,"name":"no email","platform":"openai","type":"oauth","status":"active","credentials":{"access_token":"a"},"extra":{}}],"total":1}`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 0, repo.createCalls)
	require.Equal(t, 0, repo.updateCalls)
}

func TestExternalAccountSyncService_SyncOnce_CreatesManagedAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":7,"name":"src","platform":"openai","type":"oauth","status":"active","credentials":{"email":"u@example.com","access_token":"a","refresh_token":"r"},"extra":{"privacy_mode":"training_off"},"updated_at":"2026-06-26T10:00:00Z"}],"total":1}`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second, DefaultConcurrency: 5})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, "u@example.com", repo.created[0].Credentials["email"])
	require.Equal(t, true, repo.created[0].Extra["external_token_export_managed"])
	require.Equal(t, float64(7), repo.created[0].Extra["external_token_export_source_id"])
}

func TestExternalAccountSyncService_SyncOnce_UpdatesSingleMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":8,"name":"new name","platform":"openai","type":"oauth","status":"active","credentials":{"email":"u@example.com","access_token":"new"},"extra":{}}],"total":1}`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{
		matches: []Account{{ID: 10, Name: "old", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"email": "u@example.com", "access_token": "old"}, Extra: map[string]any{"keep": "yes"}}},
	}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, int64(10), repo.updated[0].ID)
	require.Equal(t, "new", repo.updated[0].Credentials["access_token"])
	require.Equal(t, "yes", repo.updated[0].Extra["keep"])
}

func TestExternalAccountSyncService_SyncOnce_SkipsDuplicateMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":9,"name":"dup","platform":"openai","type":"oauth","status":"active","credentials":{"email":"u@example.com","access_token":"new"},"extra":{}}],"total":1}`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{
		matches: []Account{{ID: 1}, {ID: 2}},
	}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 0, repo.createCalls)
	require.Equal(t, 0, repo.updateCalls)
}

type externalSyncSettingReaderStub struct {
	url string
}

func (s *externalSyncSettingReaderStub) GetExternalAccountSyncURL(_ context.Context) string {
	return s.url
}

type externalSyncAccountRepoStub struct {
	mu          sync.Mutex
	matches     []Account
	created     []Account
	updated     []Account
	createCalls int
	updateCalls int
}

func (r *externalSyncAccountRepoStub) Create(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	r.created = append(r.created, cloneExternalSyncAccount(account))
	return nil
}

func (r *externalSyncAccountRepoStub) Update(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	r.updated = append(r.updated, cloneExternalSyncAccount(account))
	return nil
}

func (r *externalSyncAccountRepoStub) ListByPlatformTypeCredentialEmail(_ context.Context, _, _, _ string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Account, len(r.matches))
	copy(out, r.matches)
	return out, nil
}

func cloneExternalSyncAccount(account *Account) Account {
	if account == nil {
		return Account{}
	}
	cloned := *account
	cloned.Credentials = cloneExternalSyncMap(account.Credentials)
	cloned.Extra = cloneExternalSyncMap(account.Extra)
	return cloned
}

func cloneExternalSyncMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
