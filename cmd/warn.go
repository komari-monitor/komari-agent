//go:build !windows

package cmd

func ShowToast() {
	// No-op on non-Windows platforms
	return
}
