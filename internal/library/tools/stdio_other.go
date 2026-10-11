//go:build !darwin && !linux

package tools

import "context"

func checkStdio(_ context.Context, _ []string, _ string, _ map[string]string, _ string, r Receipt) Receipt {
	r.Health, r.Code = "unsupported", "platform_unqualified"
	return r
}
