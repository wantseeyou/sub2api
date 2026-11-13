package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// configuredGitHubReleaseClient 在每次更新请求时应用后台保存的代理选择。
type configuredGitHubReleaseClient struct {
	settings *service.SettingService // 提供最新更新线路配置。
}

// client 根据当前设置创建客户端，底层连接池按代理配置复用。
func (c *configuredGitHubReleaseClient) client(ctx context.Context) (service.GitHubReleaseClient, error) {
	proxyURL, allowDirectOnError, err := c.settings.GetUpdateProxyURL(ctx)
	if err != nil {
		return nil, err
	}
	return NewGitHubReleaseClient(proxyURL, allowDirectOnError), nil
}

// FetchLatestRelease 使用所选线路检查最新版本。
func (c *configuredGitHubReleaseClient) FetchLatestRelease(ctx context.Context, repo string) (*service.GitHubRelease, error) {
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.FetchLatestRelease(ctx, repo)
}

// FetchRecentReleases 使用所选线路获取可回滚版本。
func (c *configuredGitHubReleaseClient) FetchRecentReleases(ctx context.Context, repo string, perPage int) ([]*service.GitHubRelease, error) {
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.FetchRecentReleases(ctx, repo, perPage)
}

// DownloadFile 使用所选线路下载更新包并保留下载大小限制。
func (c *configuredGitHubReleaseClient) DownloadFile(ctx context.Context, url, dest string, maxSize int64) error {
	client, err := c.client(ctx)
	if err != nil {
		return err
	}
	return client.DownloadFile(ctx, url, dest, maxSize)
}

// FetchChecksumFile 使用所选线路获取更新包校验文件。
func (c *configuredGitHubReleaseClient) FetchChecksumFile(ctx context.Context, url string) ([]byte, error) {
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	return client.FetchChecksumFile(ctx, url)
}
