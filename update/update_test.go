package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blang/semver"
)

// TestParseVersion 验证 parseVersion 能够解析各种版本号格式，包括带 v/V 前缀、预发布和构建元数据
func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v1.2.3", "1.2.3"},
		{"V1.2.3", "1.2.3"},
		{"1.2.3-beta.1", "1.2.3-beta.1"},
		{"v1.2.3+meta", "1.2.3+meta"},
		{"1.2.3-pre.1+build.123", "1.2.3-pre.1+build.123"},
		{"  v2.0.0  ", "2.0.0"},
		{"invalid", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseVersion(strings.TrimSpace(tt.input))
			if tt.want == "" {
				if err == nil {
					t.Errorf("parseVersion(%q) expected error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseVersion(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got.String() != tt.want {
				t.Errorf("parseVersion(%q) = %q, want %q", tt.input, got.String(), tt.want)
			}
		})
	}
}

func TestParseUpdateTarget(t *testing.T) {
	tests := []struct {
		input   string
		kind    targetKind
		version string
		tag     string
		valid   bool
	}{
		{"latest", targetStableLatest, "", "", true},
		{"*", targetStableLatest, "", "", true},
		{"1.*", targetMajor, "1.0.0", "", true},
		{"1.2", targetMinor, "1.2.0", "", true},
		{"1.2.*", targetMinor, "1.2.0", "", true},
		{"v1.2.*", targetMinor, "1.2.0", "", true},
		{"  V1.2.*  ", targetMinor, "1.2.0", "", true},
		{"1.2.3", targetExact, "1.2.3", "", true},
		{"1.2.3-beta.1", targetExact, "1.2.3-beta.1", "", true},
		{"1.2.3+build.1", targetExact, "1.2.3+build.1", "", true},
		{"snapshot", targetSnapshotLatest, "", "", true},
		{"Snapshot-2609231430", targetSnapshotExact, "", "Snapshot-2609231430", true},
		{"", 0, "", "", false},
		{"one.two.*", 0, "", "", false},
		{"Snapshot-", 0, "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseUpdateTarget(tt.input)
			if !tt.valid {
				if err == nil {
					t.Fatalf("parseUpdateTarget(%q) expected an error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUpdateTarget(%q) error = %v", tt.input, err)
			}
			if got.kind != tt.kind || got.tag != tt.tag {
				t.Errorf("parseUpdateTarget(%q) = %+v, want kind=%v tag=%q", tt.input, got, tt.kind, tt.tag)
			}
			if tt.version != "" && got.version.String() != tt.version {
				t.Errorf("parseUpdateTarget(%q) version = %q, want %q", tt.input, got.version.String(), tt.version)
			}
		})
	}
}

// TestNeedUpdate 验证 needUpdate 在不同版本组合下的判断
func TestNeedUpdate(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"1.0.0", "1.0.1", true},
		{"v1.0.0", "1.1.0", true},
		{"1.2.3", "1.2.3", false},
		{"1.2.4", "1.2.3", false},
		{"1.2.3-beta", "1.2.3", true},
		{"1.2.3", "1.2.3-beta", false},
		{"0.0.5", "0.0.6+build.1", true},
		{"0.0.6", "v0.0.6+build.1", false},
	}

	for _, tt := range tests {
		cur, err := parseVersion(strings.TrimSpace(tt.current))
		if err != nil {
			t.Fatalf("parseVersion(%q) error: %v", tt.current, err)
		}
		lat, err := parseVersion(strings.TrimSpace(tt.latest))
		if err != nil {
			t.Fatalf("parseVersion(%q) error: %v", tt.latest, err)
		}
		got := needUpdate(cur, lat)
		if got != tt.want {
			t.Errorf("needUpdate(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestSelectLatestStableRelease(t *testing.T) {
	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	publishedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	releases := []githubRelease{
		testRelease("v1.3.99", false, false, publishedAt.Add(time.Hour), assetName),
		testRelease("v1.2.14-rc.1", false, false, publishedAt.Add(5*time.Hour), assetName),
		testRelease("v1.2.10", false, false, publishedAt, assetName),
		testRelease("v1.2.11-beta.1", true, false, publishedAt.Add(2*time.Hour), assetName),
		testRelease("v1.2.12", false, true, publishedAt.Add(3*time.Hour), assetName),
		testRelease("v1.2.13", false, false, publishedAt.Add(4*time.Hour), "komari-agent-linux-arm64"),
		testRelease("v1.2.9", false, false, publishedAt.Add(-time.Hour), assetName),
	}

	for _, tt := range []struct {
		target string
		want   string
	}{
		{"1.2", "v1.2.10"},
		{"1.2.*", "v1.2.10"},
		{"1.*", "v1.3.99"},
		{"latest", "v1.3.99"},
		{"1.2.10", "v1.2.10"},
		{"1.2.11-beta.1", "v1.2.11-beta.1"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			target, err := parseUpdateTarget(tt.target)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := selectLatestStableRelease(releases, assetName, target)
			if !ok {
				t.Fatal("selectLatestStableRelease() found no candidate")
			}
			if got.TagName != tt.want {
				t.Errorf("selectLatestStableRelease() tag = %q, want %q", got.TagName, tt.want)
			}
			if got.Asset.Name != assetName {
				t.Errorf("selectLatestStableRelease() asset = %q, want %q", got.Asset.Name, assetName)
			}
		})
	}
}

func TestSelectStableTargetExcludesUnrequestedPrerelease(t *testing.T) {
	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	target, err := parseUpdateTarget("1.2.11")
	if err != nil {
		t.Fatal(err)
	}
	releases := []githubRelease{
		testRelease("v1.2.11", true, false, time.Now(), assetName),
	}

	if _, ok := selectLatestStableRelease(releases, assetName, target); ok {
		t.Fatal("stable exact target selected a GitHub prerelease")
	}
}

func TestStableNeedsUpdate(t *testing.T) {
	current := semver.MustParse("1.2.9")
	tests := []struct {
		name     string
		latest   string
		target   string
		autoLine bool
		want     bool
	}{
		{"automatic newer patch", "1.2.10", "", true, true},
		{"automatic never downgrades", "1.2.8", "", true, false},
		{"automatic stays on current line", "1.3.0", "", true, false},
		{"latest never downgrades", "1.2.8", "latest", false, false},
		{"explicit line switch", "1.1.9", "1.1.*", false, true},
		{"exact target can roll back", "1.2.8", "1.2.8", false, true},
		{"same exact target is a no-op", "1.2.9", "1.2.9", false, false},
		{"exact build metadata is significant", "1.2.9+build.1", "1.2.9+build.1", false, true},
		{"range in same line never downgrades", "1.2.8", "1.2.*", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var target updateTarget
			if tt.autoLine {
				target = updateTarget{kind: targetCurrentLine, version: current}
			} else {
				var err error
				target, err = parseUpdateTarget(tt.target)
				if err != nil {
					t.Fatal(err)
				}
			}
			latest := semver.MustParse(tt.latest)
			if got := stableNeedsUpdate(&current, latest, target); got != tt.want {
				t.Errorf("stableNeedsUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExpectedAssetName(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"linux", "amd64", "komari-agent-linux-amd64"},
		{"darwin", "arm64", "komari-agent-darwin-arm64"},
		{"windows", "amd64", "komari-agent-windows-amd64.exe"},
	}

	for _, tt := range tests {
		got := expectedAssetName(tt.goos, tt.goarch)
		if got != tt.want {
			t.Errorf("expectedAssetName(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestSelectLatestSnapshotRelease(t *testing.T) {
	assetName := "komari-agent-linux-amd64"
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	releases := []githubRelease{
		testRelease("v9.9.9", false, false, base.Add(5*time.Hour), assetName),
		testRelease("Snapshot-2607061400", true, true, base.Add(4*time.Hour), assetName),
		testRelease("beta-2607061500", true, false, base.Add(6*time.Hour), assetName),
		testRelease("Snapshot-2607061600", true, false, base.Add(7*time.Hour), "komari-agent-linux-arm64"),
		testRelease("Snapshot-2607061200", true, false, base, assetName),
		testRelease("Snapshot-2607061300", true, false, base.Add(time.Hour), assetName),
	}

	got, ok := selectLatestSnapshotRelease(releases, assetName, "")
	if !ok {
		t.Fatalf("selectLatestSnapshotRelease() found no candidate")
	}
	if got.TagName != "Snapshot-2607061300" {
		t.Errorf("selectLatestSnapshotRelease() tag = %q, want %q", got.TagName, "Snapshot-2607061300")
	}
	if got.Asset.Name != assetName {
		t.Errorf("selectLatestSnapshotRelease() asset = %q, want %q", got.Asset.Name, assetName)
	}
}

func TestSelectLatestSnapshotReleaseTieBreaksByTag(t *testing.T) {
	assetName := "komari-agent-linux-amd64"
	publishedAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	releases := []githubRelease{
		testRelease("Snapshot-2607061200", true, false, publishedAt, assetName),
		testRelease("Snapshot-2607061201", true, false, publishedAt, assetName),
	}

	got, ok := selectLatestSnapshotRelease(releases, assetName, "")
	if !ok {
		t.Fatalf("selectLatestSnapshotRelease() found no candidate")
	}
	if got.TagName != "Snapshot-2607061201" {
		t.Errorf("selectLatestSnapshotRelease() tag = %q, want %q", got.TagName, "Snapshot-2607061201")
	}
}

func TestSelectLatestSnapshotReleaseNoMatch(t *testing.T) {
	releases := []githubRelease{
		testRelease("v1.2.3", false, false, time.Now(), "komari-agent-linux-amd64"),
		testRelease("Snapshot-2607061200", true, true, time.Now(), "komari-agent-linux-amd64"),
		testRelease("Snapshot-2607061300", true, false, time.Now(), "komari-agent-linux-arm64"),
	}

	if got, ok := selectLatestSnapshotRelease(releases, "komari-agent-linux-amd64", ""); ok {
		t.Fatalf("selectLatestSnapshotRelease() = %+v, want no candidate", got)
	}
}

func TestSelectExactSnapshotRelease(t *testing.T) {
	assetName := "komari-agent-linux-amd64"
	releases := []githubRelease{
		testRelease("Snapshot-2609231200", true, false, time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC), assetName),
		testRelease("Snapshot-2609231300", true, false, time.Date(2026, 9, 23, 13, 0, 0, 0, time.UTC), assetName),
	}

	got, ok := selectLatestSnapshotRelease(releases, assetName, "Snapshot-2609231200")
	if !ok || got.TagName != "Snapshot-2609231200" {
		t.Fatalf("selectLatestSnapshotRelease() = %q, want exact requested snapshot", got.TagName)
	}
}

func TestSnapshotNeedsUpdate(t *testing.T) {
	latest := releaseCandidate{TagName: "Snapshot-2607061200"}

	if snapshotNeedsUpdate("Snapshot-2607061200", latest) {
		t.Errorf("snapshotNeedsUpdate() should be false for the current snapshot tag")
	}
	if !snapshotNeedsUpdate("Snapshot-2607061100", latest) {
		t.Errorf("snapshotNeedsUpdate() should be true for an older snapshot tag")
	}
}

func TestStableUpdateReturnsRestartRequired(t *testing.T) {
	current := semver.MustParse("1.2.3")
	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	release := testRelease("v1.2.4", false, false, time.Now(), assetName)

	lister := func(owner, repo string) ([]githubRelease, error) {
		if owner != "komari-monitor" || repo != "komari-agent" {
			t.Fatalf("list releases repo = %s/%s, want komari-monitor/komari-agent", owner, repo)
		}
		return []githubRelease{release}, nil
	}
	installed := ""
	deps := updateDeps{
		list: lister,
		install: func(candidate releaseCandidate) error {
			installed = candidate.TagName
			return ErrRestartRequired
		},
	}

	target := updateTarget{kind: targetCurrentLine, version: current}
	if err := checkAndUpdateStable(&current, target, deps); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("checkAndUpdateStable() error = %v, want ErrRestartRequired", err)
	}
	if installed != "v1.2.4" {
		t.Fatalf("installed tag = %q, want v1.2.4", installed)
	}
}

func TestStableUpdateSkipsWhenUpToDate(t *testing.T) {
	current := semver.MustParse("1.2.5")
	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	release := testRelease("v1.2.4", false, false, time.Now(), assetName)

	deps := updateDeps{
		list: func(owner, repo string) ([]githubRelease, error) {
			return []githubRelease{release}, nil
		},
		install: func(releaseCandidate) error {
			t.Fatal("install must not run when the current version is newer")
			return nil
		},
	}

	target := updateTarget{kind: targetCurrentLine, version: current}
	if err := checkAndUpdateStable(&current, target, deps); err != nil {
		t.Fatalf("checkAndUpdateStable() error = %v, want nil", err)
	}
}

func TestCheckAndUpdateTargetSwitchesVersionLine(t *testing.T) {
	oldVersion := CurrentVersion
	CurrentVersion = "1.2.9"
	t.Cleanup(func() { CurrentVersion = oldVersion })

	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	release := testRelease("v1.1.9", false, false, time.Now(), assetName)
	installed := ""
	target, err := parseUpdateTarget("1.1.*")
	if err != nil {
		t.Fatal(err)
	}
	deps := updateDeps{
		list: func(string, string) ([]githubRelease, error) {
			return []githubRelease{release}, nil
		},
		isContainer: func() bool { return false },
		install: func(candidate releaseCandidate) error {
			installed = candidate.TagName
			return ErrRestartRequired
		},
	}

	if err := checkAndUpdateTarget(target, deps); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("checkAndUpdateTarget() error = %v, want ErrRestartRequired", err)
	}
	if installed != "v1.1.9" {
		t.Fatalf("installed tag = %q, want v1.1.9", installed)
	}
}

func TestSnapshotUpdateReturnsRestartRequired(t *testing.T) {
	oldVersion := CurrentVersion
	CurrentVersion = "Snapshot-2607061100"
	t.Cleanup(func() { CurrentVersion = oldVersion })

	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	release := testRelease("Snapshot-2607061200", true, false, time.Now(), assetName)

	deps := updateDeps{
		list: func(owner, repo string) ([]githubRelease, error) {
			if owner != "komari-monitor" || repo != "komari-agent" {
				t.Fatalf("list releases repo = %s/%s, want komari-monitor/komari-agent", owner, repo)
			}
			return []githubRelease{release}, nil
		},
		install: func(candidate releaseCandidate) error {
			if candidate.TagName != "Snapshot-2607061200" {
				t.Fatalf("installed tag = %q, want Snapshot-2607061200", candidate.TagName)
			}
			return ErrRestartRequired
		},
	}

	target := updateTarget{kind: targetSnapshotLatest, input: "snapshot"}
	if err := checkAndUpdateSnapshot(target, deps); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("checkAndUpdateSnapshot() error = %v, want ErrRestartRequired", err)
	}
}

func TestRunUpdateCheckSkipsInContainer(t *testing.T) {
	deps := updateDeps{
		list: func(string, string) ([]githubRelease, error) {
			t.Fatal("release lister must not be called inside a container")
			return nil, nil
		},
		isContainer: func() bool { return true },
		install: func(releaseCandidate) error {
			t.Fatal("install must not run inside a container")
			return nil
		},
	}

	if err := runUpdateCheck(deps); err != nil {
		t.Fatalf("runUpdateCheck() error = %v, want nil", err)
	}
}

func TestRunUpdateCheckIgnoresUnparsableVersion(t *testing.T) {
	oldVersion := CurrentVersion
	CurrentVersion = "dev-build"
	t.Cleanup(func() { CurrentVersion = oldVersion })

	deps := updateDeps{
		list: func(string, string) ([]githubRelease, error) {
			t.Fatal("release lister must not be called for an unparsable version")
			return nil, nil
		},
		isContainer: func() bool { return false },
		install: func(releaseCandidate) error {
			t.Fatal("install must not run for an unparsable version")
			return nil
		},
	}

	// 多次检查都应静默跳过（版本解析失败只在第一次打印警告）。
	for i := 0; i < 2; i++ {
		if err := runUpdateCheck(deps); err != nil {
			t.Fatalf("runUpdateCheck() #%d error = %v, want nil", i+1, err)
		}
	}
}

func TestCurrentExecutablePath(t *testing.T) {
	path, err := currentExecutablePath()
	if err != nil {
		t.Fatalf("currentExecutablePath() error = %v", err)
	}
	if path == "" {
		t.Fatal("currentExecutablePath() returned an empty path")
	}
}

func TestVerifyAssetDigest(t *testing.T) {
	data := []byte("hello komari agent")
	sum := sha256.Sum256(data)
	matching := "sha256:" + hex.EncodeToString(sum[:])

	asset := githubReleaseAsset{Name: "komari-agent-linux-amd64", Digest: matching}
	if err := verifyAssetDigest(asset, data); err != nil {
		t.Fatalf("verifyAssetDigest() with matching digest error = %v", err)
	}

	asset.Digest = "sha256:" + strings.Repeat("0", 64)
	if err := verifyAssetDigest(asset, data); err == nil {
		t.Fatal("verifyAssetDigest() with mismatching digest should fail")
	}

	asset.Digest = ""
	if err := verifyAssetDigest(asset, data); err != nil {
		t.Fatalf("verifyAssetDigest() without digest should only warn, got %v", err)
	}

	asset.Digest = "sha512:" + strings.Repeat("0", 128)
	if err := verifyAssetDigest(asset, data); err == nil {
		t.Fatal("verifyAssetDigest() with unsupported digest should fail closed")
	}
	asset.Digest = "sha256:not-hex"
	if err := verifyAssetDigest(asset, data); err == nil {
		t.Fatal("verifyAssetDigest() with malformed digest should fail")
	}
}

func TestDownloadAssetEnforcesAdvertisedSize(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	data := []byte("payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	asset := githubReleaseAsset{Size: len(data), BrowserDownloadURL: server.URL}
	got, err := downloadAsset(server.Client(), "owner", "repo", asset)
	if err != nil {
		t.Fatalf("downloadAsset() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("downloadAsset() = %q, want %q", got, data)
	}

	asset.Size = 3
	if _, err := downloadAsset(server.Client(), "owner", "repo", asset); err == nil {
		t.Fatal("downloadAsset() should reject a body larger than the advertised size")
	}
}

func TestDownloadAssetRejectsInvalidSizes(t *testing.T) {
	for _, size := range []int{0, int(maxReleaseAssetSize + 1)} {
		asset := githubReleaseAsset{Size: size}
		if _, err := downloadAsset(http.DefaultClient, "", "", asset); err == nil {
			t.Errorf("downloadAsset() accepted invalid asset size %d", size)
		}
	}
}

func TestRunScheduledUpdatesStopsAfterRestartRequired(t *testing.T) {
	ticks := make(chan time.Time, 3)
	ticks <- time.Now()
	ticks <- time.Now()
	ticks <- time.Now()

	checks := 0
	restarts := 0
	runScheduledUpdates(
		ticks,
		func() error {
			checks++
			if checks == 1 {
				return errors.New("transient update check failure")
			}
			return ErrRestartRequired
		},
		func() { restarts++ },
	)

	if checks != 2 {
		t.Fatalf("scheduled checks = %d, want 2", checks)
	}
	if restarts != 1 {
		t.Fatalf("restart handoffs = %d, want 1", restarts)
	}
}

func TestRunScheduledCheckKeepsGoingOnError(t *testing.T) {
	restarts := 0
	if stop := runScheduledCheck(func() error { return errors.New("boom") }, func() { restarts++ }); stop {
		t.Fatal("runScheduledCheck() should keep going on a transient error")
	}
	if restarts != 0 {
		t.Fatalf("restart handoffs = %d, want 0", restarts)
	}
}

func testRelease(tag string, prerelease, draft bool, publishedAt time.Time, assetNames ...string) githubRelease {
	assets := make([]githubReleaseAsset, 0, len(assetNames))
	for i, name := range assetNames {
		assets = append(assets, githubReleaseAsset{
			ID:                 int64(i + 1),
			Name:               name,
			Size:               1024,
			BrowserDownloadURL: "https://example.com/" + name,
		})
	}

	return githubRelease{
		TagName:     tag,
		Draft:       draft,
		Prerelease:  prerelease,
		PublishedAt: publishedAt,
		Assets:      assets,
	}
}
