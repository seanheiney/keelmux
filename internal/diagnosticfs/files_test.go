package diagnosticfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistingPrivateStateOnly(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent")
	if _, e := OpenDir(missing); !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("created missing state")
	}
	d := filepath.Join(root, "state")
	os.Mkdir(d, 0700)
	dir, e := OpenDir(d)
	if e != nil {
		t.Fatal(e)
	}
	defer dir.Close()
	os.WriteFile(filepath.Join(d, "real"), []byte("private"), 0600)
	for _, kind := range []string{"symlink", "hardlink", "public", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(d, kind)
			switch kind {
			case "symlink":
				os.Symlink("real", p)
			case "hardlink":
				os.Link(filepath.Join(d, "real"), p)
			case "public":
				os.WriteFile(p, []byte("private"), 0644)
			case "fifo":
				makeFIFO(t, p)
			}
			if _, e := dir.Read(kind, 0600, 32); e == nil {
				t.Fatal("accepted unsafe file")
			}
		})
	}
	alias := filepath.Join(root, "alias")
	os.Symlink(d, alias)
	if _, e := OpenDir(alias); e == nil {
		t.Fatal("followed state leaf")
	}
	os.Mkdir(filepath.Join(d, "child"), 0755)
	if _, e := dir.Child("child"); e == nil {
		t.Fatal("accepted public child")
	}
	if _, e := dir.Read("../real", 0600, 32); e == nil {
		t.Fatal("path escaped descriptor")
	}
}
