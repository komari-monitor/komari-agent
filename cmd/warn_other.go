//go:build !windows && !linux

package cmd

import "context"

func startSecurityWarning(context.Context) func() {
	return func() {}
}
