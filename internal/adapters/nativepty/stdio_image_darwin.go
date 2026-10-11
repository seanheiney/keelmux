//go:build darwin

package nativepty

import ps "github.com/seanheiney/keelmux/internal/primarystdio"

func stdioPlatformImageAvailable() bool        { return false }
func verifyStdioRunningImage(ps.Channel) error { return ps.ErrUnavailable }
