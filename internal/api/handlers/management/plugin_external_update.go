package management

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/htmlsanitize"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	log "github.com/sirupsen/logrus"
)

const externalPluginDownloadLimit = 128 << 20

type externalPluginUpdateInfo struct {
	InstalledVersion string `json:"installed_version"`
	LatestVersion    string `json:"latest_version"`
	UpdateAvailable  bool   `json:"update_available"`
	Supported        bool   `json:"supported"`
	Message          string `json:"message,omitempty"`
}

type externalPluginTarget struct {
	id           string
	version      string
	pluginsDir   string
	repository   string
	client       pluginstore.Client
	pluginLoaded func() bool
}

func (h *Handler) externalPluginTarget(c *gin.Context) (externalPluginTarget, bool) {
	id, okID := pluginIDFromRequest(c)
	if !okID {
		return externalPluginTarget{}, false
	}
	_, pluginsDir, proxyURL, _, storeAuth, configs, host := h.pluginStoreSnapshot()
	if _, _, managed := pluginStoreConfiguredSource(configs[id]); managed {
		c.JSON(http.StatusConflict, gin.H{"error": "plugin_store_managed", "message": "use the plugin store to update this plugin"})
		return externalPluginTarget{}, false
	}
	resolvedDir, errDir := config.ResolvePluginsDir(pluginsDir)
	if errDir != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_directory_invalid", "message": errDir.Error()})
		return externalPluginTarget{}, false
	}
	statuses, errStatus := pluginLocalStatuses(false, resolvedDir, configs, host)
	if errStatus != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "plugin_discovery_failed", "message": errStatus.Error()})
		return externalPluginTarget{}, false
	}
	status := statuses[id]
	if !status.Installed || status.Path == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_installed", "message": "plugin library not found"})
		return externalPluginTarget{}, false
	}
	if _, errStat := os.Stat(status.Path); errStat != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_not_installed", "message": "plugin library not found"})
		return externalPluginTarget{}, false
	}
	if host == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "plugin_metadata_unavailable", "message": "plugin must be loaded to check its repository"})
		return externalPluginTarget{}, false
	}
	for _, info := range host.RegisteredPlugins() {
		if info.ID != id {
			continue
		}
		repository := strings.TrimSpace(info.Metadata.GitHubRepository)
		if _, _, errRepo := pluginstore.GitHubRepositoryParts(repository); errRepo != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "plugin_repository_invalid", "message": "plugin does not declare a valid GitHub repository"})
			return externalPluginTarget{}, false
		}
		version := strings.TrimSpace(info.Metadata.Version)
		if version == "" {
			version = status.InstalledVersion
		}
		return externalPluginTarget{
			id: id, version: version, pluginsDir: resolvedDir, repository: repository,
			client:       h.newPluginStoreClient(proxyURL, "", storeAuth),
			pluginLoaded: func() bool { return pluginBusy(host, id) },
		}, true
	}
	c.JSON(http.StatusConflict, gin.H{"error": "plugin_metadata_unavailable", "message": "plugin must be loaded to check its repository"})
	return externalPluginTarget{}, false
}

func externalPluginAsset(release pluginstore.Release, id, version, goos, goarch string) (pluginstore.ReleaseAsset, pluginstore.ReleaseAsset, bool, error) {
	archiveName := pluginstore.ArchiveName(id, version, goos, goarch)
	extension := ".so"
	switch goos {
	case "windows":
		extension = ".dll"
	case "darwin":
		extension = ".dylib"
	}
	libraryName := fmt.Sprintf("%s-%s-%s%s", id, goos, goarch, extension)
	var archive, library, checksums, libraryChecksum pluginstore.ReleaseAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case archiveName:
			archive = asset
		case libraryName:
			library = asset
		case "checksums.txt":
			checksums = asset
		case libraryName + ".sha256":
			libraryChecksum = asset
		}
	}
	if archive.Name != "" && checksums.Name != "" {
		return archive, checksums, false, nil
	}
	if library.Name != "" {
		if libraryChecksum.Name != "" {
			return library, libraryChecksum, true, nil
		}
		if checksums.Name != "" {
			return library, checksums, true, nil
		}
	}
	return pluginstore.ReleaseAsset{}, pluginstore.ReleaseAsset{}, false, fmt.Errorf("release has no verified %s/%s plugin asset", goos, goarch)
}

func (h *Handler) CheckExternalPluginUpdate(c *gin.Context) {
	target, ok := h.externalPluginTarget(c)
	if !ok {
		return
	}
	release, errRelease := target.client.FetchLatestRelease(c.Request.Context(), pluginstore.Plugin{Repository: target.repository})
	if errRelease != nil {
		if !writePluginStoreRateLimit(c, errRelease) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_release_failed", "message": errRelease.Error()})
		}
		return
	}
	version, errVersion := pluginstore.ReleaseVersion(release)
	if errVersion != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_release_invalid", "message": errVersion.Error()})
		return
	}
	_, _, _, errAsset := externalPluginAsset(release, target.id, version, runtime.GOOS, runtime.GOARCH)
	info := externalPluginUpdateInfo{
		InstalledVersion: htmlsanitize.String(target.version), LatestVersion: htmlsanitize.String(version),
		Supported: errAsset == nil, UpdateAvailable: errAsset == nil && pluginstore.UpdateAvailable(target.version, version),
	}
	if errAsset != nil {
		info.Message = errAsset.Error()
	}
	c.JSON(http.StatusOK, info)
}

func (h *Handler) UpdateExternalPlugin(c *gin.Context) {
	target, ok := h.externalPluginTarget(c)
	if !ok {
		return
	}
	release, errRelease := target.client.FetchLatestRelease(c.Request.Context(), pluginstore.Plugin{Repository: target.repository})
	if errRelease != nil {
		if !writePluginStoreRateLimit(c, errRelease) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_release_failed", "message": errRelease.Error()})
		}
		return
	}
	version, errVersion := pluginstore.ReleaseVersion(release)
	if errVersion != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_release_invalid", "message": errVersion.Error()})
		return
	}
	if !pluginstore.UpdateAvailable(target.version, version) {
		c.JSON(http.StatusConflict, gin.H{"error": "plugin_up_to_date", "message": "plugin is already up to date"})
		return
	}
	asset, checksumAsset, standalone, errAsset := externalPluginAsset(release, target.id, version, runtime.GOOS, runtime.GOARCH)
	if errAsset != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_release_unsupported", "message": errAsset.Error()})
		return
	}
	data, errDownload := target.client.DownloadAssetWithLimit(c.Request.Context(), asset, externalPluginDownloadLimit)
	if errDownload != nil {
		if !writePluginStoreRateLimit(c, errDownload) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_download_failed", "message": errDownload.Error()})
		}
		return
	}
	checksumData, errChecksum := target.client.DownloadAssetWithLimit(c.Request.Context(), checksumAsset, 1<<20)
	if errChecksum != nil {
		if !writePluginStoreRateLimit(c, errChecksum) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_checksum_failed", "message": errChecksum.Error()})
		}
		return
	}
	checksums, errParse := pluginstore.ParseChecksums(checksumData)
	if errParse == nil {
		errParse = pluginstore.VerifyChecksum(asset.Name, data, checksums)
	}
	if errParse != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_checksum_failed", "message": errParse.Error()})
		return
	}
	options := pluginstore.InstallOptions{PluginsDir: target.pluginsDir, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, PluginLoaded: target.pluginLoaded}
	var result pluginstore.InstallResult
	var errInstall error
	if standalone {
		result, errInstall = pluginstore.InstallLibrary(data, target.id, version, options)
	} else {
		result, errInstall = pluginstore.InstallArchive(data, pluginstore.Plugin{ID: target.id, Version: version}, options)
	}
	if errInstall != nil {
		if errors.Is(errInstall, pluginstore.ErrLoadedPluginLocked) {
			c.JSON(http.StatusConflict, gin.H{"error": "plugin_update_requires_restart", "message": errInstall.Error(), "restart_required": true})
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": "plugin_install_failed", "message": errInstall.Error()})
		}
		return
	}
	log.WithFields(log.Fields{"plugin_id": target.id, "version": version, "path": result.Path}).Info("external plugin update staged; restart required")
	c.JSON(http.StatusOK, gin.H{"status": "installed", "version": htmlsanitize.String(version), "restart_required": true})
}
