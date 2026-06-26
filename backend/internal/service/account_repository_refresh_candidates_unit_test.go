//go:build unit

package service

import "context"

func (s *accountRepoStub) ListOAuthRefreshCandidates(context.Context) ([]Account, error) {
	panic("unexpected ListOAuthRefreshCandidates call")
}

func (s *accountRepoStub) ListByPlatformTypeCredentialEmail(context.Context, string, string, string) ([]Account, error) {
	panic("unexpected ListByPlatformTypeCredentialEmail call")
}

func (r *openAIAccountTestRepo) ListOAuthRefreshCandidates(context.Context) ([]Account, error) {
	panic("unexpected ListOAuthRefreshCandidates call")
}

func (r *openAIAccountTestRepo) ListByPlatformTypeCredentialEmail(context.Context, string, string, string) ([]Account, error) {
	panic("unexpected ListByPlatformTypeCredentialEmail call")
}

func (m *groupAwareMockAccountRepo) ListOAuthRefreshCandidates(context.Context) ([]Account, error) {
	panic("unexpected ListOAuthRefreshCandidates call")
}

func (m *groupAwareMockAccountRepo) ListByPlatformTypeCredentialEmail(context.Context, string, string, string) ([]Account, error) {
	panic("unexpected ListByPlatformTypeCredentialEmail call")
}

func (m *mockAccountRepoForPlatform) ListOAuthRefreshCandidates(context.Context) ([]Account, error) {
	panic("unexpected ListOAuthRefreshCandidates call")
}

func (m *mockAccountRepoForPlatform) ListByPlatformTypeCredentialEmail(context.Context, string, string, string) ([]Account, error) {
	panic("unexpected ListByPlatformTypeCredentialEmail call")
}

func (m *mockAccountRepoForGemini) ListOAuthRefreshCandidates(context.Context) ([]Account, error) {
	return m.ListActive(context.Background())
}

func (m *mockAccountRepoForGemini) ListByPlatformTypeCredentialEmail(context.Context, string, string, string) ([]Account, error) {
	panic("unexpected ListByPlatformTypeCredentialEmail call")
}
