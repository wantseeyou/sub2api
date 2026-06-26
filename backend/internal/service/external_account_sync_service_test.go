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

func TestExternalAccountSyncService_SyncOnce_EmptyURLIsNoop(t *testing.T) {
	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 0, repo.createCalls)
	require.Equal(t, 0, repo.updateCalls)
}

func TestExternalAccountSyncService_SyncOnce_InvalidURLIsNoop(t *testing.T) {
	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{url: "://bad-url"}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 0, repo.createCalls)
	require.Equal(t, 0, repo.updateCalls)
}

func TestExternalAccountSyncService_SyncOnce_ReturnsErrorForNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusBadGateway)
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.Error(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 0, repo.createCalls)
	require.Equal(t, 0, repo.updateCalls)
}

func TestExternalAccountSyncService_SyncOnce_ReturnsErrorForInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.Error(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, 0, repo.createCalls)
	require.Equal(t, 0, repo.updateCalls)
}

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
	require.Equal(t, "u@example.com", repo.created[0].Extra["external_token_export_email"])
	require.Equal(t, "7", repo.created[0].Extra["external_token_export_source_id"])
	require.Equal(t, "2026-06-26T10:00:00Z", repo.created[0].Extra["external_token_export_updated_at"])
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
	require.True(t, repo.updated[0].Schedulable)
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

func TestExternalAccountSyncService_SyncOnce_TrimsEmailForLookupAndPersist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":11,"name":"trim","platform":"openai","type":"oauth","status":"active","credentials":{"email":"  u@example.com  ","access_token":"new"},"extra":{}}],"total":1}`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{
		matches: []Account{{ID: 10, Name: "old", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"email": "u@example.com"}}},
	}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	require.NoError(t, svc.SyncOnce(context.Background(), "test"))
	require.Equal(t, "u@example.com", repo.lastLookupEmail)
	require.Equal(t, "u@example.com", repo.updated[0].Credentials["email"])
	require.Equal(t, "u@example.com", repo.updated[0].Extra["external_token_export_email"])
}

func TestExternalAccountSyncService_TriggerNow_CoalescesTriggers(t *testing.T) {
	settings := &externalSyncSettingReaderStub{}
	svc := NewExternalAccountSyncService(settings, &externalSyncAccountRepoStub{}, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	svc.TriggerNow("first")
	svc.TriggerNow("second")

	require.Len(t, svc.triggerCh, 1)
	require.Equal(t, "first", <-svc.triggerCh)
}

func TestExternalAccountSyncService_StartStop_HandlesStartupSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer server.Close()

	repo := &externalSyncAccountRepoStub{}
	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, repo, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	svc.Start()
	require.Eventually(t, func() bool {
		return settings.calls() > 0
	}, time.Second, 10*time.Millisecond)
	svc.Stop()
}

func TestExternalAccountSyncService_Start_IsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer server.Close()

	settings := &externalSyncSettingReaderStub{url: server.URL}
	svc := NewExternalAccountSyncService(settings, &externalSyncAccountRepoStub{}, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	svc.Start()
	svc.Start()
	require.Eventually(t, func() bool { return settings.calls() > 0 }, time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return settings.calls() > 1 }, 50*time.Millisecond, 5*time.Millisecond)
	svc.Stop()
}

func TestExternalAccountSyncService_Stop_IsIdempotent(t *testing.T) {
	svc := NewExternalAccountSyncService(&externalSyncSettingReaderStub{}, &externalSyncAccountRepoStub{}, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	svc.Stop()
	svc.Stop()
}

func TestExternalAccountSyncService_RedactsSensitiveURLParts(t *testing.T) {
	rawURL := "https://user:pass@example.com/api/v1/accounts/token-export?password=p&access_token=a&refresh-token=r&client_secret=s&apikey=k&safe=ok"

	got := redactExternalAccountSyncURL(rawURL)

	require.Contains(t, got, "user:redacted@example.com")
	require.Contains(t, got, "password=redacted")
	require.Contains(t, got, "access_token=redacted")
	require.Contains(t, got, "refresh-token=redacted")
	require.Contains(t, got, "client_secret=redacted")
	require.Contains(t, got, "apikey=redacted")
	require.Contains(t, got, "safe=ok")
	require.NotContains(t, got, "user:pass")
	require.NotContains(t, got, "password=p")
	require.NotContains(t, got, "access_token=a")
}

func TestExternalAccountSyncService_SanitizesHTTPErrorURL(t *testing.T) {
	rawURL := "https://example.com/api/v1/accounts/token-export?password=secret"

	got := sanitizeExternalAccountSyncHTTPError(assertAnError(`Get "`+rawURL+`": timeout`), rawURL)

	require.NotContains(t, got.Error(), "secret")
	require.Contains(t, got.Error(), "password=redacted")
}

type externalSyncSettingReaderStub struct {
	mu        sync.Mutex
	url       string
	callCount int
}

func (s *externalSyncSettingReaderStub) GetExternalAccountSyncURL(context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callCount++
	return s.url
}

func (s *externalSyncSettingReaderStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

type externalSyncAccountRepoStub struct {
	mu              sync.Mutex
	matches         []Account
	created         []Account
	updated         []Account
	lastLookupEmail string
	lookupCount     int
	createCalls     int
	updateCalls     int
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

func (r *externalSyncAccountRepoStub) ListByPlatformTypeCredentialEmail(_ context.Context, _, _, email string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookupCount++
	r.lastLookupEmail = email
	out := make([]Account, len(r.matches))
	copy(out, r.matches)
	return out, nil
}

func (r *externalSyncAccountRepoStub) lookupCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupCount
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

func TestExternalAccountSyncService_Stop_DoesNotBlockWithoutStart(t *testing.T) {
	svc := NewExternalAccountSyncService(&externalSyncSettingReaderStub{}, &externalSyncAccountRepoStub{}, ExternalAccountSyncOptions{Interval: time.Hour, RequestTimeout: time.Second})

	done := make(chan struct{})
	go func() {
		svc.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked without Start")
	}
}

type assertAnError string

func (e assertAnError) Error() string {
	return string(e)
}
