//go:build windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	toast "gopkg.in/toast.v1"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

func startSecurityWarning(ctx context.Context) func() {
	warning := newSecurityWarning(flags.Endpoint, warningCurrentUser())
	go warnWindowsSessions(ctx, warning)
	return func() {}
}

func warnWindowsSessions(ctx context.Context, warning securityWarning) {
	// Keep service notifications in logged-in users' sessions, never in Session 0.
	// 启用权限
	if err := enablePrivileges([]string{"SeAssignPrimaryTokenPrivilege", "SeIncreaseQuotaPrivilege"}); err != nil {
		log.Printf("[warn] enabling privileges failed: %v", err)
	}

	seen := map[uint32]struct{}{}

	// 轮询新登录
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		current, err := enumerateActiveSessions()
		if err != nil {
			log.Printf("[warn] enumerateActiveSessions error: %v", err)
		}

		// 将 current 列表转换为集合，便于清理旧会话
		currentSet := make(map[uint32]struct{}, len(current))
		for _, sid := range current {
			currentSet[sid] = struct{}{}
		}

		// 找到新出现的会话 -> 在该会话启动进程
		for _, sid := range current {
			_, known := seen[sid]
			if !known {
				if err := launchSelfInSession(sid, warningHelperArgs(warning)); err != nil {
					log.Printf("[warn] launch in session %d failed: %v", sid, err)
				} else {
					seen[sid] = struct{}{}
					log.Printf("[info] launched toast helper in session %d", sid)
				}
			}
		}

		// 清理不再存在的会话，避免 map 膨胀
		if err == nil {
			for sid := range seen {
				if _, ok := currentSet[sid]; !ok {
					delete(seen, sid)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ShowToast 在用户态中执行
func ShowToast() {
	warning := newSecurityWarning(flags.Endpoint, warningCurrentUser())
	if warningPanelHost != "" {
		warning.PanelHost = warningSingleLine(warningPanelHost)
	}
	if warningRunAsUser != "" {
		warning.RunAsUser = warningSingleLine(warningRunAsUser)
	}
	showSecurityToast(warning)
}

func showSecurityToast(warning securityWarning) {
	const aumid = "Komari.Monitor.Agent"
	const linkName = "Komari Warning (Auto Delete Later)"

	if err := ensureStartMenuShortcut(aumid, linkName); err != nil {
		log.Printf("[warn] ensureStartMenuShortcut failed: %v", err)
	}

	n := securityToast(warning)
	if err := n.Push(); err != nil {
		log.Printf("[warn] toast push failed: %v", err)
	}

	// 等待 15 秒后删除快捷方式
	shortcutPath := getStartMenuShortcutPath(linkName)
	time.Sleep(15 * time.Second)
	if err := os.Remove(shortcutPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[warn] remove shortcut failed: %v", err)
		}
	}
}

func securityToast(warning securityWarning) toast.Notification {
	return toast.Notification{
		AppID:               "Komari.Monitor.Agent",
		Title:               escapeToastText(warningTitle),
		Message:             escapeToastText(warning.message()),
		Duration:            toast.Long,
		ActivationArguments: warningUninstallURL,
		Actions: []toast.Action{
			{Type: "protocol", Label: "Uninstall Komari Agent", Arguments: warningUninstallURL},
		},
	}
}

// toast.v1 embeds text in CDATA inside an expandable PowerShell here-string.
func escapeToastText(text string) string {
	return strings.NewReplacer("`", "``", "$", "`$", "]]>", "]]]]><![CDATA[>").Replace(text)
}

func warningHelperArgs(warning securityWarning) []string {
	return []string{"--show-warning", "--warning-panel-host", warning.PanelHost, "--warning-run-as-user", warning.RunAsUser}
}

// ensureStartMenuShortcut 使用 WScript.Shell 创建 .lnk 并设置 AppUserModelID
func ensureStartMenuShortcut(aumid, linkName string) error {
	programs := getStartMenuProgramsDir()
	if err := os.MkdirAll(programs, 0o755); err != nil {
		return err
	}
	shortcutPath := filepath.Join(programs, sanitizeFileName(linkName)+".lnk")
	if _, err := os.Stat(shortcutPath); err == nil {
		return nil
	}

	if hr := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); hr != nil {
		// S_OK (0) 或 S_FALSE (1) 都视为成功；go-ole 将非零 HRESULT 作为 error 返回
		// 当返回错误时，我们再进行 Uninitialize 保护即可
		// 这里直接继续执行，由后续操作决定是否可用
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("CreateObject WScript.Shell: %w", err)
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("QueryInterface IDispatch: %w", err)
	}
	defer shell.Release()

	cs, err := oleutil.CallMethod(shell, "CreateShortcut", shortcutPath)
	if err != nil {
		return fmt.Errorf("CreateShortcut: %w", err)
	}
	shortcut := cs.ToIDispatch()
	defer shortcut.Release()

	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)

	if _, err = oleutil.PutProperty(shortcut, "TargetPath", exePath); err != nil {
		return fmt.Errorf("set TargetPath: %w", err)
	}
	if _, err = oleutil.PutProperty(shortcut, "WorkingDirectory", exeDir); err != nil {
		return fmt.Errorf("set WorkingDirectory: %w", err)
	}
	_, _ = oleutil.PutProperty(shortcut, "Description", "Komari Agent")
	// 设置 AUMID
	if _, err = oleutil.PutProperty(shortcut, "AppUserModelID", aumid); err != nil {
		// 某些系统该属性不存在时，依然尝试保存；Toast 可能仍然显示
		log.Printf("[warn] set AppUserModelID failed: %v", err)
	}

	if _, err = oleutil.CallMethod(shortcut, "Save"); err != nil {
		return fmt.Errorf("shortcut.Save: %w", err)
	}
	return nil
}

// 返回当前用户开始菜单 Programs 目录
func getStartMenuProgramsDir() string {
	return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")
}

// 获取快捷方式完整路径
func getStartMenuShortcutPath(linkName string) string {
	return filepath.Join(getStartMenuProgramsDir(), sanitizeFileName(linkName)+".lnk")
}

func sanitizeFileName(name string) string {
	replacer := strings.NewReplacer("\\", "_", "/", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return replacer.Replace(name)
}

// enumerateActiveSessions 列出当前处于交互活动状态的会话 ID
func enumerateActiveSessions() ([]uint32, error) {
	type wtsSessionInfo struct {
		SessionID  uint32
		WinStation *uint16
		State      uint32
	}

	wtsapi := windows.NewLazySystemDLL("wtsapi32.dll")
	procEnum := wtsapi.NewProc("WTSEnumerateSessionsW")
	procFree := wtsapi.NewProc("WTSFreeMemory")

	var (
		server  windows.Handle // WTS_CURRENT_SERVER_HANDLE == 0
		pinfo   *wtsSessionInfo
		count   uint32
		version uint32 = 1
	)
	r1, _, err := procEnum.Call(
		uintptr(server),
		0,
		uintptr(version),
		uintptr(unsafe.Pointer(&pinfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if r1 == 0 {
		return nil, fmt.Errorf("WTSEnumerateSessionsW: %w", err)
	}
	defer procFree.Call(uintptr(unsafe.Pointer(pinfo)))

	// WTS_CONNECTSTATE_CLASS
	const WTSActive = 0

	// 遍历结构数组
	res := make([]uint32, 0, count)
	infos := unsafe.Slice(pinfo, int(count))
	for i := 0; i < len(infos); i++ {
		info := &infos[i]
		if info.State == WTSActive {
			if hasUserName(info.SessionID) {
				res = append(res, info.SessionID)
			}
		}
	}
	return res, nil
}

// hasUserName 检查会话是否有用户名（避免将空会话当作登录）
func hasUserName(sessionID uint32) bool {
	const WTSUserName = 5
	wtsapi := windows.NewLazySystemDLL("wtsapi32.dll")
	procQuery := wtsapi.NewProc("WTSQuerySessionInformationW")
	procFree := wtsapi.NewProc("WTSFreeMemory")

	var buf *uint16
	var blen uint32
	r1, _, _ := procQuery.Call(0, uintptr(sessionID), uintptr(WTSUserName), uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&blen)))
	if r1 == 0 || buf == nil {
		return false
	}
	defer procFree.Call(uintptr(unsafe.Pointer(buf)))
	name := windows.UTF16PtrToString(buf)
	return strings.TrimSpace(name) != ""
}

// launchSelfInSession 在指定会话中以该用户身份启动当前进程并追加 args
func launchSelfInSession(sessionID uint32, extraArgs []string) error {
	// 获取用户令牌
	userToken, err := queryUserToken(sessionID)
	if err != nil {
		return fmt.Errorf("queryUserToken: %w", err)
	}
	defer userToken.Close()

	primary, err := duplicateTokenPrimary(userToken)
	if err != nil {
		return fmt.Errorf("duplicateToken: %w", err)
	}
	defer primary.Close()

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	// Never forward tokens, config paths or service-only arguments to a user session.
	cmdlineStr := windows.ComposeCommandLine(append([]string{exePath}, extraArgs...))
	cmdline, err := windows.UTF16PtrFromString(cmdlineStr)
	if err != nil {
		return fmt.Errorf("UTF16PtrFromString: %w", err)
	}

	env, err := createEnvironmentBlock(primary)
	if err != nil {
		return fmt.Errorf("createEnvironmentBlock: %w", err)
	}
	defer destroyEnvironmentBlock(env)

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = 0
	si.ShowWindow = 0
	// 指定桌面，确保窗口可见
	desktop, _ := windows.UTF16PtrFromString("winsta0\\default")
	si.Desktop = desktop

	var pi windows.ProcessInformation
	// CREATE_UNICODE_ENVIRONMENT | DETACHED_PROCESS
	const CREATE_UNICODE_ENVIRONMENT = 0x00000400
	const DETACHED_PROCESS = 0x00000008

	err = windows.CreateProcessAsUser(primary, nil, cmdline, nil, nil, false, CREATE_UNICODE_ENVIRONMENT|DETACHED_PROCESS, env, nil, &si, &pi)
	if err != nil {
		return fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return nil
}

// enablePrivileges 尝试启用一组权限
func enablePrivileges(names []string) error {
	var errs []string
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	for _, name := range names {
		if err := setPrivilege(token, name, true); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func setPrivilege(token windows.Token, privName string, enable bool) error {
	var luid windows.LUID
	nameUTF16, _ := windows.UTF16PtrFromString(privName)
	if err := windows.LookupPrivilegeValue(nil, nameUTF16, &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: 0},
		},
	}
	if enable {
		tp.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
	}
	return windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil)
}

// queryUserToken 调用 WTSQueryUserToken 获取指定会话的用户令牌（模拟令牌）
func queryUserToken(sessionID uint32) (windows.Token, error) {
	wtsapi := windows.NewLazySystemDLL("wtsapi32.dll")
	proc := wtsapi.NewProc("WTSQueryUserToken")
	var h windows.Handle
	r1, _, err := proc.Call(uintptr(sessionID), uintptr(unsafe.Pointer(&h)))
	if r1 == 0 {
		return 0, fmt.Errorf("WTSQueryUserToken: %w", err)
	}
	return windows.Token(h), nil
}

// duplicateTokenPrimary 将模拟令牌复制为主令牌，以供 CreateProcessAsUser 使用
func duplicateTokenPrimary(token windows.Token) (windows.Token, error) {
	var primary windows.Token
	err := windows.DuplicateTokenEx(token, windows.TOKEN_ALL_ACCESS, nil, windows.SecurityIdentification, windows.TokenPrimary, &primary)
	return primary, err
}

// createEnvironmentBlock 为用户令牌创建环境块
func createEnvironmentBlock(token windows.Token) (*uint16, error) {
	userenv := windows.NewLazySystemDLL("userenv.dll")
	proc := userenv.NewProc("CreateEnvironmentBlock")
	var env *uint16
	r1, _, err := proc.Call(uintptr(unsafe.Pointer(&env)), uintptr(token), 0)
	if r1 == 0 {
		return nil, fmt.Errorf("CreateEnvironmentBlock: %w", err)
	}
	return env, nil
}

func destroyEnvironmentBlock(env *uint16) {
	if env == nil {
		return
	}
	userenv := windows.NewLazySystemDLL("userenv.dll")
	proc := userenv.NewProc("DestroyEnvironmentBlock")
	_, _, _ = proc.Call(uintptr(unsafe.Pointer(env)))
}
