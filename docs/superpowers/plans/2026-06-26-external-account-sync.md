# 外部账号同步 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现一个可配置的外部账号同步服务，从 `token-export` URL 每 10 秒同步账号，并让同步账号跳过本地默认 token refresh。

**Architecture:** 后端新增 `ExternalAccountSyncService`，通过 `SettingService` 读取 `external_account_sync_url`，通过 `AccountRepository` 按 `platform + type + credentials.email` upsert 账号。账号通过 `extra.external_token_export_managed` 标记为外部托管；`TokenRefreshService` 的候选 SQL 跳过该标记，`RateLimitService` 在 OAuth 401 时 best-effort 触发一次同步。

**Tech Stack:** Go, Ent, PostgreSQL JSONB / Ent `sqljson`, Gin admin settings API, Vue 3, TypeScript, existing i18n.

---

## 文件结构

- Modify `docs/superpowers/specs/2026-06-26-external-account-sync-design.md`
  - 已补充账号筛选实现细节。
- Modify `backend/internal/service/setting_service.go`
  - 增加 `SettingKeyExternalAccountSyncURL`、`SystemSettings.ExternalAccountSyncURL`、解析和持久化逻辑、读取方法。
- Modify `backend/internal/handler/admin/setting_handler.go`
  - 在 `UpdateSettingsRequest`、GET/PUT settings payload 映射中加入 `external_account_sync_url`。
- Modify `backend/internal/service/account_service.go`
  - 在 `AccountRepository` interface 增加 `ListByPlatformTypeCredentialEmail`。
- Modify `backend/internal/repository/account_repo.go`
  - 用 `sqljson.ValueEQ(dbaccount.FieldCredentials, email, sqljson.Path("email"))` 实现账号筛选。
  - 在 `ListOAuthRefreshCandidates` SQL 中排除 `extra.external_token_export_managed == true`。
- Create `backend/internal/service/external_account_sync_service.go`
  - 独立同步服务、payload 类型、HTTP 拉取、upsert、URL 脱敏、立即触发合并。
- Create `backend/internal/service/external_account_sync_service_test.go`
  - 覆盖空 email 跳过、创建、更新、重复命中跳过、触发合并。
- Modify `backend/internal/service/token_refresh_service_candidates_test.go`
  - 增加托管账号不进入候选的 repository 测试或服务层测试。
- Modify `backend/internal/service/ratelimit_service.go`
  - 增加可选 `ExternalAccountSyncTrigger` 依赖和 setter，在 OAuth 401 分支触发。
- Modify `backend/internal/service/ratelimit_service_401_test.go`
  - 覆盖外部托管账号 401 会触发同步。
- Modify `backend/internal/service/wire.go`
  - provider 中创建并启动 `ExternalAccountSyncService`，并注入到 `RateLimitService`。
- Modify `frontend/src/api/admin/settings.ts`
  - `SystemSettings` 和 update request 类型增加 `external_account_sync_url`。
- Modify `frontend/src/views/admin/SettingsView.vue`
  - 通用设置页面增加 URL 输入框，form 默认值、加载、保存 payload 全链路加入该字段。
- Modify `frontend/src/i18n/locales/zh.ts`
  - 增加中文文案。
- Modify `frontend/src/i18n/locales/en.ts`
  - 增加英文文案。

## Task 1: 提交设计文档补充

**Files:**
- Modify: `docs/superpowers/specs/2026-06-26-external-account-sync-design.md`

- [ ] **Step 1: Verify the spec contains the repository filtering section**

Run:

```bash
rg -n "账号筛选实现|ListByPlatformTypeCredentialEmail|sqljson.ValueEQ" docs/superpowers/specs/2026-06-26-external-account-sync-design.md
```

Expected: all three terms are present.

- [ ] **Step 2: Check markdown whitespace**

Run:

```bash
git diff --check docs/superpowers/specs/2026-06-26-external-account-sync-design.md
```

Expected: no output and exit code 0.

- [ ] **Step 3: Commit the spec update**

Run:

```bash
git add -f docs/superpowers/specs/2026-06-26-external-account-sync-design.md
git commit -m "docs: clarify external account sync matching"
```

Expected: commit succeeds.

## Task 2: 打通 Settings 配置字段

**Files:**
- Modify: `backend/internal/service/setting_service.go`
- Modify: `backend/internal/handler/admin/setting_handler.go`
- Test: `backend/internal/service/setting_service_update_test.go`
- Test: `backend/internal/handler/admin/setting_handler_auth_source_defaults_test.go`

- [ ] **Step 1: Write failing service test for persisting `external_account_sync_url`**

Add this test to `backend/internal/service/setting_service_update_test.go`:

```go
func TestSettingService_UpdateSettings_PersistsExternalAccountSyncURL(t *testing.T) {
	repo := newInMemorySettingRepo()
	svc := NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})

	settings, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	settings.ExternalAccountSyncURL = "https://source.example.com/api/v1/accounts/token-export?password=secret"

	require.NoError(t, svc.UpdateSettings(context.Background(), settings))

	got, err := repo.GetValue(context.Background(), SettingKeyExternalAccountSyncURL)
	require.NoError(t, err)
	require.Equal(t, "https://source.example.com/api/v1/accounts/token-export?password=secret", got)

	reloaded, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "https://source.example.com/api/v1/accounts/token-export?password=secret", reloaded.ExternalAccountSyncURL)
}
```

If `newInMemorySettingRepo` is not available in that file, reuse the existing fake setting repo pattern already present in nearby setting tests.

- [ ] **Step 2: Run the failing service test**

Run:

```bash
cd backend && go test ./internal/service -run TestSettingService_UpdateSettings_PersistsExternalAccountSyncURL -count=1
```

Expected: FAIL because `ExternalAccountSyncURL` and `SettingKeyExternalAccountSyncURL` do not exist yet.

- [ ] **Step 3: Add service setting key and model field**

In `backend/internal/service/setting_service.go`, add:

```go
const SettingKeyExternalAccountSyncURL = "external_account_sync_url"
```

Add this field to `SystemSettings`:

```go
ExternalAccountSyncURL string `json:"external_account_sync_url"`
```

Update `getDefaultSettingsMap`, `parseSettings`, and `buildSystemSettingsUpdates` so:

```go
updates[SettingKeyExternalAccountSyncURL] = strings.TrimSpace(settings.ExternalAccountSyncURL)
result.ExternalAccountSyncURL = strings.TrimSpace(settings[SettingKeyExternalAccountSyncURL])
SettingKeyExternalAccountSyncURL: "",
```

Add one method:

```go
// GetExternalAccountSyncURL 获取外部账号同步 URL。
func (s *SettingService) GetExternalAccountSyncURL(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return ""
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyExternalAccountSyncURL)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
```

- [ ] **Step 4: Add handler request and response mappings**

In `backend/internal/handler/admin/setting_handler.go`, add to `UpdateSettingsRequest`:

```go
ExternalAccountSyncURL string `json:"external_account_sync_url"`
```

Add to both `dto.SystemSettings{...}` payloads:

```go
ExternalAccountSyncURL: settings.ExternalAccountSyncURL,
```

and:

```go
ExternalAccountSyncURL: updatedSettings.ExternalAccountSyncURL,
```

Add to the `service.SystemSettings{...}` built from `req`:

```go
ExternalAccountSyncURL: strings.TrimSpace(req.ExternalAccountSyncURL),
```

If `dto.SystemSettings` lacks this field, add:

```go
ExternalAccountSyncURL string `json:"external_account_sync_url"`
```

to the actual dto struct package imported as `dto`.

- [ ] **Step 5: Run setting tests**

Run:

```bash
cd backend && go test ./internal/service ./internal/handler/admin -run 'TestSettingService_UpdateSettings_PersistsExternalAccountSyncURL|TestSettingHandler_UpdateSettings' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit settings changes**

Run:

```bash
git add backend/internal/service/setting_service.go backend/internal/handler/admin/setting_handler.go backend/internal/service/setting_service_update_test.go backend/internal/handler/admin/setting_handler_auth_source_defaults_test.go
git commit -m "feat: add external account sync setting"
```

Expected: commit succeeds.

## Task 3: 增加账号 Email 查询能力

**Files:**
- Modify: `backend/internal/service/account_service.go`
- Modify: `backend/internal/repository/account_repo.go`
- Test: `backend/internal/repository/account_repository_refresh_candidates_unit_test.go` or a new focused repository test if existing integration helpers are easier.

- [ ] **Step 1: Write failing repository contract test**

Add a test that creates three accounts:

```go
openAIEmail := "user@example.com"
mustCreateAccount(t, client, &service.Account{
	Name: "openai target",
	Platform: service.PlatformOpenAI,
	Type: service.AccountTypeOAuth,
	Status: service.StatusActive,
	Schedulable: true,
	Credentials: map[string]any{"email": openAIEmail, "access_token": "a"},
})
mustCreateAccount(t, client, &service.Account{
	Name: "openai other type",
	Platform: service.PlatformOpenAI,
	Type: service.AccountTypeAPIKey,
	Status: service.StatusActive,
	Schedulable: true,
	Credentials: map[string]any{"email": openAIEmail, "api_key": "k"},
})
mustCreateAccount(t, client, &service.Account{
	Name: "gemini same email",
	Platform: service.PlatformGemini,
	Type: service.AccountTypeOAuth,
	Status: service.StatusActive,
	Schedulable: true,
	Credentials: map[string]any{"email": openAIEmail, "access_token": "g"},
})

got, err := repo.ListByPlatformTypeCredentialEmail(context.Background(), service.PlatformOpenAI, service.AccountTypeOAuth, openAIEmail)
require.NoError(t, err)
require.Len(t, got, 1)
require.Equal(t, "openai target", got[0].Name)
```

Use the repository test harness already used by nearby account repository tests. If no shared helper fits, create a minimal test file under `backend/internal/repository/` using the existing ent test setup in that package.

- [ ] **Step 2: Run the failing repository test**

Run:

```bash
cd backend && go test ./internal/repository -run TestAccountRepository_ListByPlatformTypeCredentialEmail -count=1
```

Expected: FAIL because repository method does not exist.

- [ ] **Step 3: Add repository interface method**

In `backend/internal/service/account_service.go`, add:

```go
// ListByPlatformTypeCredentialEmail 按 platform、type 和 credentials.email 查找账号。
ListByPlatformTypeCredentialEmail(ctx context.Context, platform, accountType, email string) ([]Account, error)
```

- [ ] **Step 4: Implement repository method**

In `backend/internal/repository/account_repo.go`, add:

```go
func (r *accountRepository) ListByPlatformTypeCredentialEmail(ctx context.Context, platform, accountType, email string) ([]service.Account, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return []service.Account{}, nil
	}
	accounts, err := r.client.Account.Query().
		Where(
			dbaccount.PlatformEQ(platform),
			dbaccount.TypeEQ(accountType),
			dbpredicate.Account(func(s *entsql.Selector) {
				s.Where(sqljson.ValueEQ(dbaccount.FieldCredentials, email, sqljson.Path("email")))
			}),
		).
		Order(dbent.Asc(dbaccount.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return r.accountsToService(ctx, accounts)
}
```

- [ ] **Step 5: Run repository test**

Run:

```bash
cd backend && go test ./internal/repository -run TestAccountRepository_ListByPlatformTypeCredentialEmail -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit repository lookup**

Run:

```bash
git add backend/internal/service/account_service.go backend/internal/repository/account_repo.go backend/internal/repository
git commit -m "feat: add account lookup by credential email"
```

Expected: commit succeeds.

## Task 4: 实现外部账号同步服务

**Files:**
- Create: `backend/internal/service/external_account_sync_service.go`
- Create: `backend/internal/service/external_account_sync_service_test.go`

- [ ] **Step 1: Write failing sync service tests**

Create `backend/internal/service/external_account_sync_service_test.go` with tests:

```go
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
		matches: []Account{{ID: 10, Name: "old", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"email":"u@example.com","access_token":"old"}, Extra: map[string]any{"keep":"yes"}}},
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
```

Add stub types in the same file implementing only the methods used by the service.

- [ ] **Step 2: Run failing sync tests**

Run:

```bash
cd backend && go test ./internal/service -run TestExternalAccountSyncService -count=1
```

Expected: FAIL because service does not exist.

- [ ] **Step 3: Implement service types and constructor**

Create `backend/internal/service/external_account_sync_service.go` with:

```go
type ExternalAccountSyncSettingReader interface {
	GetExternalAccountSyncURL(ctx context.Context) string
}

type ExternalAccountSyncAccountRepository interface {
	Create(ctx context.Context, account *Account) error
	Update(ctx context.Context, account *Account) error
	ListByPlatformTypeCredentialEmail(ctx context.Context, platform, accountType, email string) ([]Account, error)
}

type ExternalAccountSyncOptions struct {
	Interval           time.Duration
	RequestTimeout     time.Duration
	DefaultConcurrency int
}

type ExternalAccountSyncService struct {
	settings ExternalAccountSyncSettingReader
	accountRepo ExternalAccountSyncAccountRepository
	client *http.Client
	interval time.Duration
	requestTimeout time.Duration
	defaultConcurrency int
	triggerCh chan string
	stopCh chan struct{}
	stopOnce sync.Once
	wg sync.WaitGroup
	running int32
}
```

Add:

```go
// NewExternalAccountSyncService 创建外部账号同步服务。
func NewExternalAccountSyncService(settings ExternalAccountSyncSettingReader, accountRepo ExternalAccountSyncAccountRepository, opts ExternalAccountSyncOptions) *ExternalAccountSyncService
```

- [ ] **Step 4: Implement payload parsing and upsert**

Use these payload structs:

```go
type externalAccountSyncResponse struct {
	Items []externalAccountSyncItem `json:"items"`
	Total int `json:"total"`
}

type externalAccountSyncItem struct {
	ID int64 `json:"id"`
	Name string `json:"name"`
	Platform string `json:"platform"`
	Type string `json:"type"`
	Status string `json:"status"`
	Credentials map[string]any `json:"credentials"`
	Extra map[string]any `json:"extra"`
	UpdatedAt time.Time `json:"updated_at"`
}
```

Implement:

```go
// SyncOnce 执行一次外部账号同步。
func (s *ExternalAccountSyncService) SyncOnce(ctx context.Context, reason string) error
```

Rules:

- empty setting URL returns nil.
- non-2xx returns error.
- empty `credentials.email` skips item.
- 0 matches creates account.
- 1 match updates account.
- more than 1 match logs warning and skips item.
- merge `Extra` instead of replacing existing local extra on update.
- set marker keys exactly:
  - `external_token_export_managed`
  - `external_token_export_email`
  - `external_token_export_source_id`
  - `external_token_export_updated_at`

- [ ] **Step 5: Implement lifecycle and trigger coalescing**

Implement:

```go
// Start 启动外部账号同步服务。
func (s *ExternalAccountSyncService) Start()

// Stop 停止外部账号同步服务。
func (s *ExternalAccountSyncService) Stop()

// TriggerNow 触发一次立即同步。
func (s *ExternalAccountSyncService) TriggerNow(reason string)
```

Use a buffered channel of size 1 and `atomic.CompareAndSwapInt32` in `SyncOnce` so concurrent scheduled and immediate sync calls coalesce.

- [ ] **Step 6: Run sync tests**

Run:

```bash
cd backend && go test ./internal/service -run TestExternalAccountSyncService -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit sync service**

Run:

```bash
git add backend/internal/service/external_account_sync_service.go backend/internal/service/external_account_sync_service_test.go
git commit -m "feat: add external account sync service"
```

Expected: commit succeeds.

## Task 5: 默认 Token 刷新排除托管账号

**Files:**
- Modify: `backend/internal/repository/account_repo.go`
- Test: `backend/internal/repository/account_repository_refresh_candidates_unit_test.go`

- [ ] **Step 1: Write failing candidate test**

Add a test creating two OAuth accounts with refresh tokens:

```go
normal := mustCreateAccount(t, client, &service.Account{
	Name: "normal",
	Platform: service.PlatformOpenAI,
	Type: service.AccountTypeOAuth,
	Status: service.StatusActive,
	Schedulable: true,
	Credentials: map[string]any{"refresh_token":"r1"},
})
managed := mustCreateAccount(t, client, &service.Account{
	Name: "managed",
	Platform: service.PlatformOpenAI,
	Type: service.AccountTypeOAuth,
	Status: service.StatusActive,
	Schedulable: true,
	Credentials: map[string]any{"refresh_token":"r2"},
	Extra: map[string]any{"external_token_export_managed": true},
})

got, err := repo.ListOAuthRefreshCandidates(context.Background())
require.NoError(t, err)
require.Contains(t, accountIDs(got), normal.ID)
require.NotContains(t, accountIDs(got), managed.ID)
```

- [ ] **Step 2: Run failing candidate test**

Run:

```bash
cd backend && go test ./internal/repository -run TestAccountRepository_ListOAuthRefreshCandidates_SkipsExternalManaged -count=1
```

Expected: FAIL because managed account is still returned.

- [ ] **Step 3: Update candidate SQL**

In `backend/internal/repository/account_repo.go`, update `ListOAuthRefreshCandidates` SQL:

```sql
AND COALESCE((extra->>'external_token_export_managed')::boolean, false) = false
```

If tests use SQLite for this repository path, use a dialect-compatible predicate in Ent instead of the raw SQL cast. Keep behavior identical: missing marker and false marker are refreshable; true marker is skipped.

- [ ] **Step 4: Run candidate test**

Run:

```bash
cd backend && go test ./internal/repository -run TestAccountRepository_ListOAuthRefreshCandidates_SkipsExternalManaged -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit refresh exclusion**

Run:

```bash
git add backend/internal/repository/account_repo.go backend/internal/repository/account_repository_refresh_candidates_unit_test.go
git commit -m "fix: skip external managed accounts in token refresh"
```

Expected: commit succeeds.

## Task 6: OAuth 401 触发立即同步

**Files:**
- Modify: `backend/internal/service/ratelimit_service.go`
- Modify: `backend/internal/service/wire.go`
- Test: `backend/internal/service/ratelimit_service_401_test.go`

- [ ] **Step 1: Write failing rate-limit trigger test**

In `backend/internal/service/ratelimit_service_401_test.go`, add:

```go
func TestRateLimitService_HandleUpstreamError_ExternalManagedOAuth401TriggersSync(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	trigger := &externalAccountSyncTriggerStub{}
	cfg := &config.Config{}
	cfg.RateLimit.OAuth401CooldownMinutes = 10
	svc := NewRateLimitService(repo, nil, cfg, nil, nil)
	svc.SetExternalAccountSyncTrigger(trigger)

	account := &Account{
		ID: 1,
		Platform: PlatformOpenAI,
		Type: AccountTypeOAuth,
		Status: StatusActive,
		Credentials: map[string]any{"refresh_token":"r"},
		Extra: map[string]any{"external_token_export_managed": true},
	}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, trigger.calls)
	require.Equal(t, "token_invalid", trigger.reasons[0])
}
```

Add stub:

```go
type externalAccountSyncTriggerStub struct {
	calls int
	reasons []string
}

func (s *externalAccountSyncTriggerStub) TriggerNow(reason string) {
	s.calls++
	s.reasons = append(s.reasons, reason)
}
```

- [ ] **Step 2: Run failing trigger test**

Run:

```bash
cd backend && go test ./internal/service -run TestRateLimitService_HandleUpstreamError_ExternalManagedOAuth401TriggersSync -count=1
```

Expected: FAIL because setter/interface does not exist.

- [ ] **Step 3: Add trigger interface and setter**

In `backend/internal/service/ratelimit_service.go`, add:

```go
type ExternalAccountSyncTrigger interface {
	TriggerNow(reason string)
}
```

Add field to `RateLimitService`:

```go
externalAccountSyncTrigger ExternalAccountSyncTrigger
```

Add method:

```go
func (s *RateLimitService) SetExternalAccountSyncTrigger(trigger ExternalAccountSyncTrigger) {
	s.externalAccountSyncTrigger = trigger
}
```

- [ ] **Step 4: Trigger on managed OAuth 401**

Inside the OAuth 401 branch, after token cache invalidation and before setting temp unschedulable, add:

```go
if account.Extra != nil && account.Extra["external_token_export_managed"] == true && s.externalAccountSyncTrigger != nil {
	s.externalAccountSyncTrigger.TriggerNow("token_invalid")
}
```

Keep existing temp-unschedulable behavior so the failed token is not immediately reused while sync catches up.

- [ ] **Step 5: Wire trigger into rate limit service**

In `backend/internal/service/wire.go`, update `ProvideRateLimitService` signature to accept:

```go
externalAccountSyncService *ExternalAccountSyncService,
```

Then call:

```go
svc.SetExternalAccountSyncTrigger(externalAccountSyncService)
```

- [ ] **Step 6: Run trigger tests**

Run:

```bash
cd backend && go test ./internal/service -run 'TestRateLimitService_HandleUpstreamError_ExternalManagedOAuth401TriggersSync|TestRateLimitService_HandleUpstreamError_OAuth401' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit 401 trigger**

Run:

```bash
git add backend/internal/service/ratelimit_service.go backend/internal/service/ratelimit_service_401_test.go backend/internal/service/wire.go
git commit -m "feat: trigger external sync on managed oauth 401"
```

Expected: commit succeeds.

## Task 7: 接入服务生命周期

**Files:**
- Modify: `backend/internal/service/wire.go`

- [ ] **Step 1: Add provider**

In `backend/internal/service/wire.go`, add:

```go
// ProvideExternalAccountSyncService creates and starts ExternalAccountSyncService.
func ProvideExternalAccountSyncService(settingService *SettingService, accountRepo AccountRepository, cfg *config.Config) *ExternalAccountSyncService {
	defaultConcurrency := 1
	if cfg != nil && cfg.Default.UserConcurrency > 0 {
		defaultConcurrency = cfg.Default.UserConcurrency
	}
	svc := NewExternalAccountSyncService(settingService, accountRepo, ExternalAccountSyncOptions{
		Interval:           10 * time.Second,
		RequestTimeout:     10 * time.Second,
		DefaultConcurrency: defaultConcurrency,
	})
	svc.Start()
	return svc
}
```

- [ ] **Step 2: Add provider to WireSet**

In the provider set near other services, add:

```go
ProvideExternalAccountSyncService,
```

- [ ] **Step 3: Regenerate wire if this project requires generated injection**

Run:

```bash
cd backend && go generate ./internal/service
```

Expected: generated wire files update only if this repo uses Wire generation for this provider set. If the command reports no generator, record that in the implementation notes and continue.

- [ ] **Step 4: Build backend package**

Run:

```bash
cd backend && go test ./internal/service -run TestExternalAccountSyncService -count=1
```

Expected: PASS and compile succeeds with wiring changes.

- [ ] **Step 5: Commit lifecycle wiring**

Run:

```bash
git add backend/internal/service/wire.go backend/internal/service/wire_gen.go
git commit -m "feat: wire external account sync service"
```

Expected: commit succeeds. If `wire_gen.go` does not exist or is unchanged, omit it from `git add`.

## Task 8: 增加后台配置 UI

**Files:**
- Modify: `frontend/src/api/admin/settings.ts`
- Modify: `frontend/src/views/admin/SettingsView.vue`
- Modify: `frontend/src/i18n/locales/zh.ts`
- Modify: `frontend/src/i18n/locales/en.ts`

- [ ] **Step 1: Add TypeScript field**

In `frontend/src/api/admin/settings.ts`, add to `SystemSettings` and update request type:

```ts
external_account_sync_url: string;
```

- [ ] **Step 2: Add form default**

In `frontend/src/views/admin/SettingsView.vue`, add to `form`:

```ts
external_account_sync_url: "",
```

Add it to `SettingsForm` by ensuring it is not omitted from `SystemSettings`.

- [ ] **Step 3: Load setting into form**

Where settings are assigned to `form`, add:

```ts
form.external_account_sync_url = settings.external_account_sync_url || "";
```

- [ ] **Step 4: Save setting in payload**

Where `payload` is built for `adminAPI.settings.updateSettings(payload)`, add:

```ts
external_account_sync_url: form.external_account_sync_url.trim(),
```

- [ ] **Step 5: Add general tab input**

In the general tab after API Base URL, add:

```vue
<div>
  <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
    {{ t("admin.settings.site.externalAccountSyncUrl") }}
  </label>
  <input
    v-model="form.external_account_sync_url"
    type="url"
    class="input font-mono text-sm"
    :placeholder="t('admin.settings.site.externalAccountSyncUrlPlaceholder')"
  />
  <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
    {{ t("admin.settings.site.externalAccountSyncUrlHint") }}
  </p>
</div>
```

- [ ] **Step 6: Add i18n**

In `frontend/src/i18n/locales/zh.ts`, under `admin.settings.site`, add:

```ts
externalAccountSyncUrl: '外部账号同步 URL',
externalAccountSyncUrlPlaceholder: 'https://source.example.com/api/v1/accounts/token-export?password=123456',
externalAccountSyncUrlHint: '填写完整 token-export URL 后，后端每 10 秒同步一次账号；留空表示禁用。',
```

In `frontend/src/i18n/locales/en.ts`, under `admin.settings.site`, add:

```ts
externalAccountSyncUrl: 'External Account Sync URL',
externalAccountSyncUrlPlaceholder: 'https://source.example.com/api/v1/accounts/token-export?password=123456',
externalAccountSyncUrlHint: 'When set, the backend syncs accounts from this token-export URL every 10 seconds. Leave empty to disable.',
```

- [ ] **Step 7: Run frontend checks**

Run:

```bash
cd frontend && pnpm type-check
```

Expected: PASS.

- [ ] **Step 8: Commit UI setting**

Run:

```bash
git add frontend/src/api/admin/settings.ts frontend/src/views/admin/SettingsView.vue frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts
git commit -m "feat: add external account sync setting UI"
```

Expected: commit succeeds.

## Task 9: 完整验证和 GitNexus 变更检测

**Files:**
- All changed files.

- [ ] **Step 1: Format Go code**

Run:

```bash
cd backend && gofmt -w internal/service/external_account_sync_service.go internal/service/external_account_sync_service_test.go internal/service/setting_service.go internal/handler/admin/setting_handler.go internal/service/ratelimit_service.go internal/service/ratelimit_service_401_test.go internal/repository/account_repo.go internal/service/account_service.go
```

Expected: command succeeds.

- [ ] **Step 2: Run focused backend tests**

Run:

```bash
cd backend && go test ./internal/service ./internal/repository ./internal/handler/admin -run 'ExternalAccountSync|ListByPlatformTypeCredentialEmail|ListOAuthRefreshCandidates|ExternalManagedOAuth401|SettingService_UpdateSettings_PersistsExternalAccountSyncURL' -count=1
```

Expected: PASS.

- [ ] **Step 3: Run broader backend tests if time allows**

Run:

```bash
cd backend && go test ./internal/service ./internal/repository ./internal/handler/admin -count=1
```

Expected: PASS. If it fails from unrelated environment dependencies, record the exact failure.

- [ ] **Step 4: Run frontend type check**

Run:

```bash
cd frontend && pnpm type-check
```

Expected: PASS.

- [ ] **Step 5: Run GitNexus detect changes**

Run the GitNexus MCP equivalent:

```text
detect_changes({scope: "all", repo: "sub2api"})
```

Expected: changed symbols match settings, account sync service, account repository, token refresh exclusion, rate-limit 401 trigger, and admin settings UI.

- [ ] **Step 6: Final commit**

If any verification-only fixes were required, commit them:

```bash
git add backend frontend docs/superpowers
git commit -m "chore: verify external account sync"
```

Expected: commit only if there are actual changes.

## 自检记录

- Spec coverage: settings, polling service, account matching by `credentials.email`, empty email skip, duplicate skip, managed marker, token refresh exclusion, token invalid trigger, and UI configuration all map to tasks.
- Placeholder scan: no unresolved placeholders or undefined later-only function names are required by the plan.
- Type consistency: `ExternalAccountSyncService`, `ExternalAccountSyncOptions`, `ExternalAccountSyncTrigger`, `ListByPlatformTypeCredentialEmail`, and `external_account_sync_url` are used consistently across tasks.
