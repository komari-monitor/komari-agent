package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	goupdate "github.com/inconshreveable/go-update"
	"github.com/komari-monitor/komari-agent/dnsresolver"
)

var ErrRestartRequired = errors.New("update installed; restart required")

var (
	CurrentVersion string = "0.0.1"
	Repo           string = "komari-monitor/komari-agent"
)

const (
	snapshotVersionPrefix = "Snapshot-"
	containerMarkerPath   = "/.komari-agent-container"
	githubAPIBaseURL      = "https://api.github.com"
	maxReleaseAssetSize   = int64(128 << 20)
)

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	PublishedAt time.Time            `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int    `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseCandidate struct {
	Version     semver.Version
	TagName     string
	PublishedAt time.Time
	Asset       githubReleaseAsset
}

type targetKind uint8

const (
	targetStableLatest targetKind = iota
	targetMajor
	targetMinor
	targetExact
	targetSnapshotLatest
	targetSnapshotExact
	targetCurrentLine
)

type updateTarget struct {
	kind    targetKind
	version semver.Version
	tag     string
	input   string
}

type releaseLister func(owner, repo string) ([]githubRelease, error)
type containerCheck func() bool
type binaryInstaller func(candidate releaseCandidate) error

// updateDeps 聚合一次升级检查所需的外部依赖，便于测试注入。
type updateDeps struct {
	list        releaseLister
	isContainer containerCheck
	install     binaryInstaller
}

func defaultDeps() updateDeps {
	return updateDeps{
		list:        listGitHubReleases,
		isContainer: isContainerAgent,
		install:     installRelease,
	}
}

var (
	// versionWarned 保证版本号无法解析时只警告一次，避免定时任务反复刷错误。
	versionWarned sync.Once
	updateCheckMu sync.Mutex

	// updateClient 是升级模块专用的 HTTP 客户端，惰性创建一次。
	// 依赖启动阶段加载的 flags（如 ignore_unsafe_cert），因此不能在包初始化时构建，
	// 也不再像旧实现那样反复改写 http.DefaultClient（数据竞争）。
	updateClient   *http.Client
	httpClientOnce sync.Once
)

func getHTTPClient() *http.Client {
	httpClientOnce.Do(func() {
		updateClient = dnsresolver.GetHTTPClient(60 * time.Second)
	})
	return updateClient
}

// parseVersion 解析可能带有 v/V 前缀，以及预发布或构建元数据的版本字符串
func parseVersion(ver string) (semver.Version, error) {
	ver = strings.TrimPrefix(ver, "v")
	ver = strings.TrimPrefix(ver, "V")
	return semver.ParseTolerant(ver)
}

// needUpdate 判断是否需要更新
func needUpdate(current, latest semver.Version) bool {
	return latest.Compare(current) > 0
}

func parseNumericVersionPrefix(value string) (semver.Version, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return semver.Version{}, fmt.Errorf("invalid version prefix %q", value)
	}
	major, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return semver.Version{}, fmt.Errorf("invalid major version in %q: %w", value, err)
	}
	version := semver.Version{Major: major}
	if len(parts) == 2 {
		version.Minor, err = strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return semver.Version{}, fmt.Errorf("invalid minor version in %q: %w", value, err)
		}
	}
	return version, nil
}

func parseUpdateTarget(input string) (updateTarget, error) {
	input = strings.TrimSpace(input)
	target := updateTarget{input: input}
	switch strings.ToLower(input) {
	case "latest", "*":
		target.kind = targetStableLatest
		return target, nil
	case "snapshot":
		target.kind = targetSnapshotLatest
		return target, nil
	}

	if strings.HasPrefix(input, snapshotVersionPrefix) {
		timestamp := strings.TrimPrefix(input, snapshotVersionPrefix)
		if len(timestamp) != 10 {
			return updateTarget{}, fmt.Errorf("invalid snapshot target %q", input)
		}
		if _, err := strconv.ParseUint(timestamp, 10, 64); err != nil {
			return updateTarget{}, fmt.Errorf("invalid snapshot target %q: %w", input, err)
		}
		target.kind = targetSnapshotExact
		target.tag = input
		return target, nil
	}

	versionInput := strings.TrimPrefix(strings.TrimPrefix(input, "v"), "V")
	wildcard := strings.HasSuffix(versionInput, ".*")
	prefix := versionInput
	if wildcard {
		prefix = strings.TrimSuffix(prefix, ".*")
	}
	if wildcard || strings.Count(prefix, ".") == 1 {
		version, err := parseNumericVersionPrefix(prefix)
		if err != nil {
			return updateTarget{}, err
		}
		target.version = version
		target.kind = targetMajor
		if strings.Contains(prefix, ".") {
			target.kind = targetMinor
		}
		return target, nil
	}
	if !strings.Contains(versionInput, ".") {
		return updateTarget{}, fmt.Errorf("invalid update target %q", input)
	}

	version, err := parseVersion(versionInput)
	if err != nil {
		return updateTarget{}, fmt.Errorf("invalid update target %q: %w", input, err)
	}
	target.kind = targetExact
	target.version = version
	return target, nil
}

func expectedAssetName(goos, goarch string) string {
	name := fmt.Sprintf("komari-agent-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func findReleaseAsset(release githubRelease, assetName string) (githubReleaseAsset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func candidateFromRelease(release githubRelease, asset githubReleaseAsset, version semver.Version) releaseCandidate {
	return releaseCandidate{
		Version:     version,
		TagName:     release.TagName,
		PublishedAt: release.PublishedAt,
		Asset:       asset,
	}
}

func selectLatestStableRelease(releases []githubRelease, assetName string, target updateTarget) (releaseCandidate, bool) {
	var latest releaseCandidate
	found := false

	for _, release := range releases {
		if release.Draft {
			continue
		}

		version, err := parseVersion(release.TagName)
		if err != nil || !target.matches(version) {
			continue
		}
		if release.Prerelease || len(version.Pre) > 0 {
			if target.kind != targetExact || len(target.version.Pre) == 0 {
				continue
			}
		}

		asset, ok := findReleaseAsset(release, assetName)
		if !ok {
			continue
		}

		candidate := candidateFromRelease(release, asset, version)
		if !found || candidate.Version.Compare(latest.Version) > 0 {
			latest = candidate
			found = true
		}
	}

	return latest, found
}

func (target updateTarget) matches(version semver.Version) bool {
	switch target.kind {
	case targetStableLatest:
		return true
	case targetCurrentLine:
		return target.version.Major == version.Major && target.version.Minor == version.Minor
	case targetMajor:
		return target.version.Major == version.Major
	case targetMinor:
		return target.version.Major == version.Major && target.version.Minor == version.Minor
	case targetExact:
		return target.version.String() == version.String()
	default:
		return false
	}
}

func selectLatestSnapshotRelease(releases []githubRelease, assetName, exactTag string) (releaseCandidate, bool) {
	var latest releaseCandidate
	found := false

	for _, release := range releases {
		if release.Draft || !release.Prerelease || !strings.HasPrefix(release.TagName, snapshotVersionPrefix) {
			continue
		}
		if exactTag != "" && release.TagName != exactTag {
			continue
		}

		asset, ok := findReleaseAsset(release, assetName)
		if !ok {
			continue
		}

		candidate := candidateFromRelease(release, asset, semver.Version{})
		if !found ||
			candidate.PublishedAt.After(latest.PublishedAt) ||
			(candidate.PublishedAt.Equal(latest.PublishedAt) && candidate.TagName > latest.TagName) {
			latest = candidate
			found = true
		}
	}

	return latest, found
}

func snapshotNeedsUpdate(currentVersion string, latest releaseCandidate) bool {
	return currentVersion != latest.TagName
}

func isContainerAgent() bool {
	_, err := os.Stat(containerMarkerPath)
	return err == nil
}

func splitRepoSlug(slug string) (string, string, error) {
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo slug %q, expected owner/name", slug)
	}
	return parts[0], parts[1], nil
}

func getRepoReleases(list releaseLister) ([]githubRelease, error) {
	owner, repo, err := splitRepoSlug(Repo)
	if err != nil {
		return nil, err
	}
	return list(owner, repo)
}

func listGitHubReleases(owner, repo string) ([]githubRelease, error) {
	var releases []githubRelease

	for page := 1; ; page++ {
		endpoint := fmt.Sprintf(
			"%s/repos/%s/%s/releases?per_page=100&page=%d",
			githubAPIBaseURL,
			url.PathEscape(owner),
			url.PathEscape(repo),
			page,
		)
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create GitHub releases request: %w", err)
		}

		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "komari-agent")
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := getHTTPClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to list GitHub releases: %w", err)
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("GitHub releases API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var pageReleases []githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&pageReleases); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("failed to decode GitHub releases response: %w", err)
		}
		_ = resp.Body.Close()

		releases = append(releases, pageReleases...)
		if len(pageReleases) < 100 {
			return releases, nil
		}
	}
}

func currentExecutablePath() (string, error) {
	cmdPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(cmdPath, ".exe") {
		cmdPath += ".exe"
	}

	stat, err := os.Lstat(cmdPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat %q: %w", cmdPath, err)
	}
	if stat.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(cmdPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve symlink %q for executable: %w", cmdPath, err)
		}
		cmdPath = resolved
	}

	return cmdPath, nil
}

// openAssetStream 打开 release asset 下载流。
// 设置了 GITHUB_TOKEN 时走 Assets API（兼容私有仓库），
// 否则直接使用浏览器下载地址（公开仓库）。
func openAssetStream(client *http.Client, owner, repo string, asset githubReleaseAsset) (io.ReadCloser, error) {
	token := os.Getenv("GITHUB_TOKEN")

	var endpoint string
	if token != "" {
		endpoint = fmt.Sprintf("%s/repos/%s/%s/releases/assets/%d",
			githubAPIBaseURL, url.PathEscape(owner), url.PathEscape(repo), asset.ID)
	} else {
		endpoint = asset.BrowserDownloadURL
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create asset download request: %w", err)
	}
	req.Header.Set("User-Agent", "komari-agent")
	if token != "" {
		// 重定向到对象存储时 Go 会自动剥离 Authorization 头，令牌不会外泄。
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download release asset: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("failed to download release asset: status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func downloadAsset(client *http.Client, owner, repo string, asset githubReleaseAsset) ([]byte, error) {
	size := int64(asset.Size)
	if size <= 0 || size > maxReleaseAssetSize {
		return nil, fmt.Errorf("invalid release asset size %d (allowed: 1..%d bytes)", size, maxReleaseAssetSize)
	}

	src, err := openAssetStream(client, owner, repo, asset)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	data, err := io.ReadAll(io.LimitReader(src, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("release asset size mismatch: got %d bytes, want %d", len(data), size)
	}
	return data, nil
}

// verifyAssetDigest 用 GitHub Releases API 返回的 digest 校验下载内容。
// 老资产可能没有 digest，此时只告警并放行；digest 存在但不匹配则拒绝安装。
func verifyAssetDigest(asset githubReleaseAsset, data []byte) error {
	if asset.Digest == "" {
		log.Printf("WARNING: release asset %s has no digest; skipping integrity check", asset.Name)
		return nil
	}
	algorithm, want, found := strings.Cut(asset.Digest, ":")
	if !found || !strings.EqualFold(algorithm, "sha256") {
		return fmt.Errorf("unsupported release asset digest %q", asset.Digest)
	}
	expected, err := hex.DecodeString(want)
	if err != nil || len(expected) != sha256.Size {
		return fmt.Errorf("invalid SHA-256 digest %q for %s", asset.Digest, asset.Name)
	}
	sum := sha256.Sum256(data)
	if !bytes.Equal(sum[:], expected) {
		return fmt.Errorf("checksum mismatch for %s: got sha256:%s, want %s", asset.Name, hex.EncodeToString(sum[:]), want)
	}
	return nil
}

// installRelease 下载候选版本、校验摘要并替换当前可执行文件。
// 成功时返回 ErrRestartRequired，提示调用方重启进程。
func installRelease(candidate releaseCandidate) error {
	owner, repo, err := splitRepoSlug(Repo)
	if err != nil {
		return err
	}
	cmdPath, err := currentExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to resolve current executable path: %w", err)
	}

	log.Printf("Will update %s from %s to %s\n", cmdPath, CurrentVersion, candidate.TagName)

	data, err := downloadAsset(getHTTPClient(), owner, repo, candidate.Asset)
	if err != nil {
		return fmt.Errorf("failed to download version %s: %w", candidate.TagName, err)
	}
	if err := verifyAssetDigest(candidate.Asset, data); err != nil {
		return fmt.Errorf("refusing to update to version %s: %w", candidate.TagName, err)
	}
	if err := goupdate.Apply(bytes.NewReader(data), goupdate.Options{TargetPath: cmdPath}); err != nil {
		return fmt.Errorf("failed to update to version %s: %w", candidate.TagName, err)
	}

	log.Printf("Successfully updated to version %s\n", candidate.TagName)
	return ErrRestartRequired
}

// DoUpdateWorks 启动后立即检查一次升级，之后每 6 小时复查。
// 安装成功需要重启时回调 onRestartRequired 并停止循环。
func DoUpdateWorks(onRestartRequired func()) {
	if runScheduledCheck(CheckAndUpdate, onRestartRequired) {
		return
	}
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	runScheduledUpdates(ticker.C, CheckAndUpdate, onRestartRequired)
}

func runScheduledUpdates(ticks <-chan time.Time, check func() error, onRestartRequired func()) {
	for range ticks {
		if runScheduledCheck(check, onRestartRequired) {
			return
		}
	}
}

// runScheduledCheck 执行一次升级检查：需要重启时触发回调并返回 true；
// 其余错误只记录日志并继续下一轮。
func runScheduledCheck(check func() error, onRestartRequired func()) bool {
	err := check()
	if errors.Is(err, ErrRestartRequired) {
		if onRestartRequired != nil {
			onRestartRequired()
		}
		return true
	}
	if err != nil {
		log.Println("[ERROR]", err)
	}
	return false
}

func stableNeedsUpdate(current *semver.Version, latest semver.Version, target updateTarget) bool {
	if !target.matches(latest) {
		return false
	}
	if target.kind == targetExact {
		return current == nil || target.version.String() != current.String()
	}
	if current == nil {
		return true
	}
	if latest.Equals(*current) {
		return false
	}
	switch target.kind {
	case targetStableLatest, targetCurrentLine:
		return needUpdate(*current, latest)
	case targetMajor:
		return target.version.Major != current.Major || needUpdate(*current, latest)
	case targetMinor:
		sameLine := target.version.Major == current.Major && target.version.Minor == current.Minor
		return !sameLine || needUpdate(*current, latest)
	default:
		return false
	}
}

func checkAndUpdateStable(current *semver.Version, target updateTarget, deps updateDeps) error {
	releases, err := getRepoReleases(deps.list)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	latest, found := selectLatestStableRelease(releases, assetName, target)
	if !found {
		if target.kind == targetCurrentLine {
			log.Printf("No stable release asset was found for %s in %d.%d.*.", assetName, current.Major, current.Minor)
			return nil
		}
		return fmt.Errorf("no stable release matching %q was found for %s", target.input, assetName)
	}

	if !stableNeedsUpdate(current, latest.Version, target) {
		log.Println("Current stable version is up-to-date:", CurrentVersion)
		return nil
	}

	return deps.install(latest)
}

func checkAndUpdateSnapshot(target updateTarget, deps updateDeps) error {
	releases, err := getRepoReleases(deps.list)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	exactTag := ""
	if target.kind == targetSnapshotExact {
		exactTag = target.tag
	}
	latest, found := selectLatestSnapshotRelease(releases, assetName, exactTag)
	if !found {
		if target.kind != targetSnapshotLatest {
			return fmt.Errorf("no snapshot release matching %q was found for %s", target.input, assetName)
		}
		log.Printf("No suitable snapshot release asset was found for %s. Current snapshot is considered up-to-date.", assetName)
		return nil
	}

	if !snapshotNeedsUpdate(CurrentVersion, latest) {
		log.Println("Current snapshot version is the latest:", CurrentVersion)
		return nil
	}

	return deps.install(latest)
}

func checkAndUpdateTarget(target updateTarget, deps updateDeps) error {
	if deps.isContainer != nil && deps.isContainer() {
		return errors.New("agent is running in a container; binary self-update is disabled")
	}
	if target.kind == targetSnapshotLatest || target.kind == targetSnapshotExact {
		return checkAndUpdateSnapshot(target, deps)
	}

	var current *semver.Version
	if !strings.HasPrefix(CurrentVersion, snapshotVersionPrefix) {
		if version, err := parseVersion(CurrentVersion); err == nil {
			current = &version
		}
	}
	return checkAndUpdateStable(current, target, deps)
}

func runUpdateCheck(deps updateDeps) error {
	log.Println("Checking update...")

	// 容器内（无论 stable 还是 snapshot）一律跳过二进制自升级，避免容器重建后版本回退。
	if deps.isContainer() {
		log.Println("Agent is running in a container; skip binary self-update. Refresh the container image instead.")
		return nil
	}

	if strings.HasPrefix(CurrentVersion, snapshotVersionPrefix) {
		return checkAndUpdateSnapshot(updateTarget{kind: targetSnapshotLatest, input: "snapshot"}, deps)
	}

	current, err := parseVersion(CurrentVersion)
	if err != nil {
		// 无法解析的版本（例如本地 dev 构建）只警告一次，之后静默跳过。
		versionWarned.Do(func() {
			log.Printf("WARNING: cannot parse current version %q (%v); automatic updates are disabled", CurrentVersion, err)
		})
		return nil
	}

	return checkAndUpdateStable(&current, updateTarget{
		kind:    targetCurrentLine,
		version: current,
		input:   current.String(),
	}, deps)
}

// CheckAndUpdate checks for a newer patch release within the current major.minor line.
// Snapshot builds track the newest Snapshot-* release instead.
func CheckAndUpdate() error {
	updateCheckMu.Lock()
	defer updateCheckMu.Unlock()
	return runUpdateCheck(defaultDeps())
}

// CheckAndUpdateForVersionLine updates to "latest", a wildcard version line,
// an exact SemVer, "snapshot", or an exact Snapshot-* tag.
func CheckAndUpdateForVersionLine(target string) error {
	parsed, err := parseUpdateTarget(target)
	if err != nil {
		return err
	}
	updateCheckMu.Lock()
	defer updateCheckMu.Unlock()
	return checkAndUpdateTarget(parsed, defaultDeps())
}
