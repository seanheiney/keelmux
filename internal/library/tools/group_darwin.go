package tools

import "golang.org/x/sys/unix"

// Called only while our direct group leader is unreaped, so the group ID cannot
// be reassigned. A syscall error or any live member keeps cleanup unconfirmed.
func groupStopped(group int) bool {
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", group)
	if err != nil {
		return false
	}
	for _, member := range members {
		if member.Proc.P_stat != 5 {
			return false
		}
	}
	return true
}
