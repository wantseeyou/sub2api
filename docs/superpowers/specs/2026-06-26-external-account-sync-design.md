# External Account Sync Design

## Background

The `pty` branch added a token export endpoint:

- `GET /api/v1/accounts/token-export`
- Password can be passed by `X-Token-Export-Password` or query parameter `password`.
- The response contains `items`, where each item includes `id`, `name`, `platform`, `type`, `status`, `access_token`, `refresh_token`, `expires_at`, `credentials`, `extra`, and `updated_at`.

The `my` branch needs to consume this endpoint from another Sub2API instance and keep local accounts synchronized. The source URL should be configured in the admin general settings as a full URL, including the password query string when desired.

## Goals

- Add an independent background service that polls the configured token export URL every 10 seconds.
- Upsert local accounts from exported items.
- Match accounts by `credentials.email`, not by source `id`.
- Skip exported accounts whose `credentials.email` is empty.
- Mark synchronized accounts as externally managed.
- Skip the default local OAuth token refresh flow for externally managed accounts.
- When a managed account token becomes invalid on the request path, trigger one immediate sync attempt.

## Non-Goals

- Do not change the source export endpoint in `pty`.
- Do not add database schema migrations unless implementation proves JSON matching is too slow or unreliable.
- Do not synchronize accounts without `credentials.email`.
- Do not use exported `id` as the local account identity. Source and destination are separate databases, so numeric IDs are not portable.
- Do not build a multi-source sync system in this change.

## Configuration

Add one admin general setting:

- `external_account_sync_url`: full URL for the token export endpoint.

Example:

```text
https://source.example.com/api/v1/accounts/token-export?password=123456
```

Empty value disables the sync service. The configured URL may contain sensitive credentials, so logs and UI display should avoid exposing query secrets in operational logs.

## Account Matching

Each exported item is processed as follows:

1. Read `email` from `item.credentials.email`.
2. Trim whitespace.
3. If `email` is empty, skip the item.
4. Find local accounts with matching:
   - `platform == item.platform`
   - `type == item.type`
   - `credentials.email == email`
5. If exactly one local account matches, update it.
6. If no local account matches, create a new account.
7. If multiple local accounts match, log a warning and skip that item to avoid corrupting the wrong account.

The exported `id` is stored only as source metadata.

## Managed Account Marker

Accounts created or updated by this sync are marked in `extra`:

```json
{
  "external_token_export_managed": true,
  "external_token_export_email": "user@example.com",
  "external_token_export_source_id": 123,
  "external_token_export_updated_at": "2026-06-26T10:00:00Z"
}
```

The marker is used by local background token refresh logic to skip these accounts.

## Upsert Behavior

For an existing account, update:

- `name`
- `platform`
- `type`
- `status`
- `credentials`
- `extra`, merged with the managed marker

For a new account, create:

- `name`
- `platform`
- `type`
- `status`
- `credentials`
- `extra`, including the managed marker

New account defaults should follow existing repository/service defaults where possible. If the existing create path requires explicit defaults, use conservative operational defaults:

- `schedulable = true` when exported status is active
- `concurrency` from existing account defaults if available, otherwise the same default used by manual account creation
- `priority` from existing account defaults if available, otherwise the same default used by manual account creation

## Polling Service

Add an independent `ExternalAccountSyncService` with:

- `Start()`
- `Stop()`
- `SyncOnce(ctx, reason string) error`
- `TriggerNow(reason string)`

Behavior:

- On start, perform one sync immediately.
- Then poll every 10 seconds.
- If the setting is empty, do nothing and keep waiting for later settings updates or the next interval.
- Use request timeout to prevent hung upstream calls.
- Use singleflight or a non-blocking lock so scheduled sync and immediate sync do not run concurrently.
- Log totals: fetched, skipped empty email, created, updated, skipped duplicate, failed.
- Redact the configured URL when logging errors.

## Token Invalid Trigger

When a request path detects a token invalid condition for an externally managed account, call:

```go
externalAccountSyncService.TriggerNow("token_invalid")
```

The trigger should be best effort:

- It must not block the gateway request for a long time.
- It must not spawn unbounded goroutines.
- It should coalesce duplicate triggers while a sync is already running.

## Default Token Refresh Exclusion

The existing `TokenRefreshService` should skip accounts where:

```go
account.Extra["external_token_export_managed"] == true
```

This avoids two instances refreshing the same upstream token independently. The source instance remains responsible for token refresh; the destination instance only imports current credentials.

## Error Handling

- Invalid configured URL: log warning and skip the cycle.
- Non-2xx response: log warning and skip the cycle.
- Invalid JSON: log warning and skip the cycle.
- Empty `items`: success with zero changes.
- Missing or empty `credentials.email`: skip the item.
- Duplicate local matches: skip the item and log account IDs.
- Account create/update failure: log the item email and continue processing other items.

## Impact Scope

Expected implementation areas:

- Backend settings model and handler for `external_account_sync_url`.
- Admin settings UI field in general settings.
- New backend service for external account sync.
- Account repository query support for matching by `platform`, `type`, and `credentials.email`.
- Token refresh skip condition for externally managed accounts.
- Gateway token-invalid handling path to trigger immediate sync.
- Service wiring and lifecycle start/stop.

Risk level: medium. The change touches auth credentials and scheduler-visible account state, but it can be isolated behind a disabled-by-default setting and JSON `extra` markers.

## Verification Strategy

- Unit test sync payload parsing and empty-email skip behavior.
- Unit test account matching and duplicate-match skip behavior.
- Unit test managed accounts are skipped by background token refresh.
- Unit test immediate trigger coalesces while a sync is running.
- Run focused backend tests for settings, account repository/service, and token refresh.
- Run `go test ./...` if environment dependencies allow it.

