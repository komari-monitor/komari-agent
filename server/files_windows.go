//go:build windows

package server

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

func listFilesystemRoots() ([]byte, error) {
	return json.Marshal([]fileInfo{{
		Name:      "/",
		Path:      "/",
		IsDir:     true,
		Mode:      "drwxrwxrwx",
		ModeOctal: "0755",
		UID:       -1,
		GID:       -1,
	}})
}

func resolveOSPath(path string) string {
	native := filepath.ToSlash(path)
	if native == "/" || native == "" {
		systemDrive := filepath.VolumeName(os.Getenv("SystemDrive"))
		if systemDrive == "" {
			systemDrive = "C:"
		}
		return filepath.Clean(systemDrive + string(filepath.Separator))
	}

	if len(native) >= 3 && native[0] == '/' && native[2] == '/' {
		if drive := native[1]; drive >= 'a' && drive <= 'z' || drive >= 'A' && drive <= 'Z' {
			return filepath.Clean(strings.ToUpper(string(drive)) + ":" + native[2:])
		}
	} else if len(native) == 2 && native[0] == '/' {
		if drive := native[1]; drive >= 'a' && drive <= 'z' || drive >= 'A' && drive <= 'Z' {
			return strings.ToUpper(string(drive)) + ":\\"
		}
	}

	return filepath.Clean(path)
}

func listVirtualRootEntries() (json.RawMessage, error) {
	const allDrives = (1 << 26) - 1
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		mask = allDrives
	}
	size := bits.Len(uint(mask))
	roots := make([]fileInfo, 0, size)
	for bit := 0; bit < size; bit++ {
		if mask&(1<<bit) == 0 {
			continue
		}
		path := string(rune('A'+bit)) + ":" + string(filepath.Separator)
		info, describeErr := describeFile(path, false)
		if describeErr != nil {
			continue
		}
		driveLetter := path[:1]
		info.Name = strings.ToUpper(driveLetter)
		info.Path = "/" + strings.ToUpper(driveLetter)
		roots = append(roots, info)
	}
	sort.SliceStable(roots, func(left, right int) bool {
		return roots[left].Path < roots[right].Path
	})
	if len(roots) == 0 {
		return nil, fmt.Errorf("no drives found")
	}
	return json.Marshal(roots)
}

func virtualRootSearchPaths() []string {
	const allDrives = (1 << 26) - 1
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		mask = allDrives
	}
	size := bits.Len(uint(mask))
	paths := make([]string, 0, size)
	for bit := 0; bit < size; bit++ {
		if mask&(1<<bit) == 0 {
			continue
		}
		path := string(rune('A'+bit)) + ":\\"
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		}
	}
	return paths
}

func fileOwnership(info os.FileInfo) (int, int, string, string) {
	return -1, -1, "", ""
}

func resolveUnixAccount(value string, isUser bool) (int, error) {
	return -1, fmt.Errorf("user and group ownership are only supported on Unix systems")
}

func changeOwnership(path string, uid, gid int) error {
	return fmt.Errorf("user and group ownership are only supported on Unix systems")
}

func replaceFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}
