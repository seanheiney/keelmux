//go:build darwin

package ircd

import "golang.org/x/sys/unix"

func renameDirectoryExclusive(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
