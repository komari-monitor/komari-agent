package update

import (
	"strings"
	"testing"
	"time"
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
		got, err := parseVersion(strings.TrimSpace(tt.input))
		if tt.want == "" {
			if err == nil {
				t.Errorf("parseVersion(%q) expected error, got %v", tt.input, got)
			}
		} else {
			if err != nil {
				t.Errorf("parseVersion(%q) unexpected error: %v", tt.input, err)
				continue
			}
			if got.String() != tt.want {
				t.Errorf("parseVersion(%q) = %q, want %q", tt.input, got.String(), tt.want)
			}
		}
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

func TestDetectBuildTrack(t *testing.T) {
	tests := []struct {
		version string
		want    buildTrack
	}{
		{"v1.2.3", stableTrack},
		{"1.2.3", stableTrack},
		{"Snapshot-2607061200", snapshotTrack},
		{"invalid", stableTrack},
	}

	for _, tt := range tests {
		got := detectBuildTrack(tt.version)
		if got != tt.want {
			t.Errorf("detectBuildTrack(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}

	if _, err := parseVersion("invalid"); err == nil {
		t.Errorf("invalid non-snapshot version should still fail semver parsing")
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

	got, ok := selectLatestSnapshotRelease(releases, assetName)
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

	got, ok := selectLatestSnapshotRelease(releases, assetName)
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

	if got, ok := selectLatestSnapshotRelease(releases, "komari-agent-linux-amd64"); ok {
		t.Fatalf("selectLatestSnapshotRelease() = %+v, want no candidate", got)
	}
}

func TestSnapshotNeedsUpdate(t *testing.T) {
	latest := snapshotReleaseCandidate{TagName: "Snapshot-2607061200"}

	if snapshotNeedsUpdate("Snapshot-2607061200", latest) {
		t.Errorf("snapshotNeedsUpdate() should be false for the current snapshot tag")
	}
	if !snapshotNeedsUpdate("Snapshot-2607061100", latest) {
		t.Errorf("snapshotNeedsUpdate() should be true for an older snapshot tag")
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
		Name:        tag,
		Body:        "test release",
		Draft:       draft,
		Prerelease:  prerelease,
		HTMLURL:     "https://example.com/" + tag,
		PublishedAt: publishedAt,
		Assets:      assets,
	}
}
