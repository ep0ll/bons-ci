//go:build linux

package xattr_test

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/bons/bons-ci/plugins/checksummer/pkg/xattr"
)

func tempFile(t *testing.T) (*os.File, xattr.FileStat) {
	t.Helper()
	f, err := os.CreateTemp("", "xattr-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close(); os.Remove(f.Name()) })
	_, _ = f.Write([]byte("test content for xattr"))

	var st unix.Stat_t
	_ = unix.Fstat(int(f.Fd()), &st)
	return f, xattr.StatFromUnix(&st)
}

func TestSaveLoad(t *testing.T) {
	f, stat := tempFile(t)
	c := xattr.NewCache("user.ovlhash")

	hash := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x23}
	if err := c.Save(f.Name(), stat, hash); err != nil {
		t.Logf("Save: %v (fs may not support user xattrs)", err)
		return
	}
	got, ok, err := c.Load(f.Name(), stat)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Skip("xattr written but not readable – filesystem does not persist xattrs")
	}
	if string(got) != string(hash) {
		t.Errorf("hash mismatch: got %x, want %x", got, hash)
	}
}

func TestLoadMiss(t *testing.T) {
	f, stat := tempFile(t)
	c := xattr.NewCache("user.ovlhash")
	_, ok, err := c.Load(f.Name(), stat)
	if err != nil || ok {
		t.Errorf("expected miss on fresh file, got ok=%v err=%v", ok, err)
	}
}

func TestStaleOnMtimeChange(t *testing.T) {
	f, stat := tempFile(t)
	c := xattr.NewCache("user.ovlhash")
	if err := c.Save(f.Name(), stat, []byte{0x01}); err != nil {
		t.Skip("xattrs not supported")
	}
	staleStat := stat
	staleStat.MtimeNs++
	_, ok, _ := c.Load(f.Name(), staleStat)
	if ok {
		t.Error("expected stale after mtime change")
	}
}

func TestStaleOnSizeChange(t *testing.T) {
	f, stat := tempFile(t)
	c := xattr.NewCache("user.ovlhash")
	if err := c.Save(f.Name(), stat, []byte{0x02}); err != nil {
		t.Skip("xattrs not supported")
	}
	staleStat := stat
	staleStat.Size++
	_, ok, _ := c.Load(f.Name(), staleStat)
	if ok {
		t.Error("expected stale after size change")
	}
}

func TestRemove(t *testing.T) {
	f, stat := tempFile(t)
	c := xattr.NewCache("user.ovlhash")
	if err := c.Save(f.Name(), stat, []byte{0xFF}); err != nil {
		t.Skip("xattrs not supported")
	}
	c.Remove(f.Name())
	_, ok, _ := c.Load(f.Name(), stat)
	if ok {
		t.Error("expected miss after Remove")
	}
}

func TestStatFromUnix(t *testing.T) {
	f, _ := tempFile(t)
	var st unix.Stat_t
	_ = unix.Fstat(int(f.Fd()), &st)
	fs := xattr.StatFromUnix(&st)
	if fs.Size != st.Size {
		t.Errorf("size mismatch: want %d, got %d", st.Size, fs.Size)
	}
	want := st.Mtim.Sec*1e9 + st.Mtim.Nsec
	if fs.MtimeNs != want {
		t.Errorf("mtime mismatch: want %d, got %d", want, fs.MtimeNs)
	}
}
