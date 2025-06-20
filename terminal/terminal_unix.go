//go:build !windows

package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

func newTerminalImpl() (*terminalImpl, error) {
	shell := ""
	// 从 /etc/passwd 获取
	user, err := os.UserHomeDir() // 当前用户
	if err == nil {
		passwd, err := os.ReadFile("/etc/passwd")
		if err == nil {
			for _, line := range strings.Split(string(passwd), "\n") {
				if strings.Contains(line, user) {
					parts := strings.Split(line, ":")
					if len(parts) >= 7 && parts[6] != "" {
						shell = parts[6]
						break
					}
				}
			}
		}
	}

	// 验证 shell 是否可用
	if shell != "" {
		if _, err := exec.LookPath(shell); err != nil {
			shell = "" // 默认 shell 不可用，清空以进入回退逻辑
		}
	}

	// 回退到默认 shell 列表
	defaultShells := []string{"zsh", "bash", "sh"}
	if shell == "" {
		for _, s := range defaultShells {
			if _, err := exec.LookPath(s); err == nil {
				shell = s
				break
			}
		}
	}

	if shell == "" {
		return nil, fmt.Errorf("no supported shell found")
	}
	// 创建进程: 优先使用交互模式，如不支持则回退
	cmd := exec.Command(shell, "-i")
	cmd.Env = append(os.Environ(), // 继承系统环境变量
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	)
	tty, err := pty.Start(cmd)
	if err != nil {
		// 交互模式不被支持，回退到无 -i 的启动方式
		cmd = exec.Command(shell)
		cmd.Env = append(os.Environ(),
			"TERM=xterm-256color",
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
		)
		tty, err = pty.Start(cmd)
		if err != nil {
			return nil, fmt.Errorf("failed to start pty with or without -i: %v", err)
		}
	}

	// 设置初始终端大小
	pty.Setsize(tty, &pty.Winsize{Rows: 24, Cols: 80})

	return &terminalImpl{
		shell: shell,
		term: &unixTerminal{
			tty: tty,
			cmd: cmd,
		},
	}, nil
}

type unixTerminal struct {
	tty *os.File
	cmd *exec.Cmd
}

func (t *unixTerminal) Close() error {
	pgid, err := syscall.Getpgid(t.cmd.Process.Pid)
	if err != nil {
		pgid = t.cmd.Process.Pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() {
		done <- t.cmd.Wait()
	}()
	select {
	case err := <-done:
		if err == nil {
		} else {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.Exited() {
				return nil
			}
			return fmt.Errorf("process group did not exit gracefully: %v", err)
		}
		return err
	case <-time.After(5 * time.Second):
	}
	// 超时未退出，强制 SIGKILL
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return <-done
}

func (t *unixTerminal) Read(p []byte) (int, error) {
	return t.tty.Read(p)
}

func (t *unixTerminal) Write(p []byte) (int, error) {
	return t.tty.Write(p)
}

func (t *unixTerminal) Resize(cols, rows int) error {
	return pty.Setsize(t.tty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (t *unixTerminal) Wait() error {
	return t.cmd.Wait()
}
