package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type externalAccountSyncSettingRepoStub struct {
	values map[string]string
	err    error
}

func (s *externalAccountSyncSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *externalAccountSyncSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (s *externalAccountSyncSettingRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func (s *externalAccountSyncSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *externalAccountSyncSettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	if s.values == nil {
		s.values = make(map[string]string)
	}
	for key, value := range settings {
		s.values[key] = value
	}
	return nil
}

func (s *externalAccountSyncSettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	result := make(map[string]string, len(s.values))
	for key, value := range s.values {
		result[key] = value
	}
	return result, nil
}

func (s *externalAccountSyncSettingRepoStub) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func TestSettingService_UpdateSettings_PersistsExternalAccountSyncURL(t *testing.T) {
	repo := &externalAccountSyncSettingRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{
		ExternalAccountSyncURL: " https://sync.example.com/token-export ",
	}))

	settings, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "https://sync.example.com/token-export", settings.ExternalAccountSyncURL)
	require.Equal(t, "https://sync.example.com/token-export", repo.values[SettingKeyExternalAccountSyncURL])
	require.Equal(t, "https://sync.example.com/token-export", svc.GetExternalAccountSyncURL(context.Background()))
}

func TestSettingService_GetExternalAccountSyncURL_ReturnsEmptyWhenUnavailable(t *testing.T) {
	require.Empty(t, (*SettingService)(nil).GetExternalAccountSyncURL(context.Background()))

	svcWithoutRepo := NewSettingService(nil, &config.Config{})
	require.Empty(t, svcWithoutRepo.GetExternalAccountSyncURL(context.Background()))

	svcWithMissingValue := NewSettingService(&externalAccountSyncSettingRepoStub{}, &config.Config{})
	require.Empty(t, svcWithMissingValue.GetExternalAccountSyncURL(context.Background()))

	svcWithReadError := NewSettingService(&externalAccountSyncSettingRepoStub{err: errors.New("boom")}, &config.Config{})
	require.Empty(t, svcWithReadError.GetExternalAccountSyncURL(context.Background()))
}
