package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsUnifiedDrivePaths(t *testing.T) {
	systemDrive := filepath.VolumeName(os.Getenv("SystemDrive"))
	if systemDrive == "" {
		systemDrive = "C:"
	}
	systemLetter := strings.ToUpper(strings.TrimSuffix(systemDrive, ":"))

	cases := []struct {
		input string
		want  string
	}{
		{input: "/", want: systemDrive + "\\"},
		{input: "/C", want: "C:\\"},
		{input: "/c", want: "C:\\"},
		{input: "/C/Users", want: "C:\\Users"},
		{input: "/d/Program Files", want: "D:\\Program Files"},
	}
	for _, item := range cases {
		if got := resolveFilePath(item.input); got != item.want {
			t.Fatalf("resolveFilePath(%q) = %q, want %q", item.input, got, item.want)
		}
	}

	virtualRoot := "/" + systemLetter
	if got := virtualizeFilePath(filepath.Join("D:\\", "Program Files")); got != "/D/Program Files" {
		t.Fatalf("virtualizeFilePath = %q", got)
	}

	raw, err := listVirtualRootEntries()
	if err != nil {
		t.Fatalf("list virtual root: %v", err)
	}
	var items []fileInfo
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode virtual root: %v", err)
	}
	foundSystem := false
	for _, item := range items {
		if item.Path == virtualRoot {
			foundSystem = true
		}
	}
	if len(items) == 0 || !foundSystem {
		t.Fatalf("expected drive entries, got %+v", items)
	}
	for _, item := range items {
		if !item.IsDir || len(item.Path) != 2 || item.Path[0] != '/' {
			t.Fatalf("unexpected virtual drive entry: %+v", item)
		}
	}
}
