//go:build unix

package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func listFilesystemRoots() ([]byte, error) {
	path := string(filepath.Separator)
	info, err := describeFile(path, false)
	if err != nil {
		return nil, err
	}
	info.Name = "/"
	info.Path = "/"
	roots := []fileInfo{info}
	return json.Marshal(roots)
}

func resolveOSPath(path string) string {
	return path
}

func listVirtualRootEntries() (json.RawMessage, error) {
	return listFiles("/")
}

func virtualRootSearchPaths() []string {
	return []string{"/"}
}

func changeOwnership(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}

func fileOwnership(info os.FileInfo) (uid, gid int, owner, group string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1, "", ""
	}
	uid = int(stat.Uid)
	gid = int(stat.Gid)
	owner, _ = accountField(strconv.Itoa(uid), "/etc/passwd", 2, 0)
	group, _ = accountField(strconv.Itoa(gid), "/etc/group", 2, 0)
	return uid, gid, owner, group
}

func resolveUnixAccount(value string, isUser bool) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return -1, nil
	}
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed, nil
	}
	file := "/etc/passwd"
	if !isUser {
		file = "/etc/group"
	}
	if id, found := accountField(value, file, 0, 2); found {
		parsed, err := strconv.Atoi(id)
		if err != nil {
			return -1, fmt.Errorf("invalid %s id for %s", accountKind(isUser), value)
		}
		return parsed, nil
	}
	return -1, fmt.Errorf("unknown %s: %s", accountKind(isUser), value)
}

func accountKind(isUser bool) string {
	if isUser {
		return "user"
	}
	return "group"
}

func accountField(key string, file string, keyField, valueField int) (string, bool) {
	handle, err := os.Open(file)
	if err != nil {
		return "", false
	}
	defer handle.Close()

	scanner := bufio.NewScanner(handle)
	key = strings.TrimSpace(key)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) <= keyField || len(fields) <= valueField {
			continue
		}
		if fields[keyField] == key {
			return fields[valueField], true
		}
	}
	return "", false
}
