//go:build linux

// Package filekey defines the canonical identity key for a file.
//
// Deduplication across overlayfs mergedviews depends on identifying the SAME
// underlying file regardless of which merged view exposed it.
//
// Strategy:
//  1. unix.NameToHandleAt(fd,"",AT_EMPTY_PATH) bypasses overlayfs and returns
//     the UNDERLYING filesystem's (mount_id, file_handle). Two mergedviews
//     sharing the same lowerdir return identical keys for the same file.
//  2. unix.Fstat(dev,ino) fallback for FUSE / proc / some RAFS configs.
package filekey

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"

	"golang.org/x/sys/unix"
)

// Source indicates which resolution strategy produced the Key.
type Source uint8

const (
	SourceHandle Source = iota // NameToHandleAt succeeded (preferred)
	SourceStat                 // Fstat fallback
)

// Key is a unique, comparable file identity.
// Value type – zero allocations, safe as map key.
type Key struct {
	Source     Source
	MountID    int
	HandleType int32
	Handle     [64]byte
	HandleLen  int
	Dev        uint64
	Ino        uint64
}

func (k Key) Equal(other Key) bool {
	if k.Source != other.Source {
		return false
	}
	switch k.Source {
	case SourceHandle:
		return k.MountID == other.MountID &&
			k.HandleType == other.HandleType &&
			k.HandleLen == other.HandleLen &&
			k.Handle == other.Handle
	case SourceStat:
		return k.Dev == other.Dev && k.Ino == other.Ino
	}
	return false
}

func (k Key) IsZero() bool {
	return k.Source == 0 && k.MountID == 0 && k.Dev == 0 && k.Ino == 0
}

func (k Key) String() string {
	switch k.Source {
	case SourceHandle:
		return fmt.Sprintf("handle:%d:%d:%s",
			k.MountID, k.HandleType,
			hex.EncodeToString(k.Handle[:k.HandleLen]))
	case SourceStat:
		return fmt.Sprintf("stat:%d:%d", k.Dev, k.Ino)
	}
	return "zero"
}

// SFKey returns a compact binary string for use as a singleflight/cache key.
// Stack-allocated: zero heap allocations.
func (k Key) SFKey() string {
	var buf [30 + 64]byte
	buf[0] = byte(k.Source)
	binary.LittleEndian.PutUint64(buf[1:9], uint64(k.MountID))
	binary.LittleEndian.PutUint32(buf[9:13], uint32(k.HandleType))
	binary.LittleEndian.PutUint64(buf[13:21], k.Dev)
	binary.LittleEndian.PutUint64(buf[21:29], k.Ino)
	buf[29] = byte(k.HandleLen)
	copy(buf[30:], k.Handle[:k.HandleLen])
	return string(buf[:30+k.HandleLen])
}

func (k Key) Hash() uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(k.SFKey()))
	return h.Sum64()
}

func fromFileHandle(mountID int, fh unix.FileHandle) Key {
	k := Key{Source: SourceHandle, MountID: mountID, HandleType: fh.Type()}
	k.HandleLen = copy(k.Handle[:], fh.Bytes())
	return k
}

func fromStat(stat *unix.Stat_t) Key {
	return Key{Source: SourceStat, Dev: stat.Dev, Ino: stat.Ino}
}

// Resolver resolves a Key from an open fd or path. Stateless; concurrent-safe.
type Resolver struct {
	DisableHandles bool // force fstat fallback
}

var DefaultResolver = &Resolver{}

func (r *Resolver) FromFD(fd int) (Key, error) {
	if !r.DisableHandles {
		if k, err := r.tryHandle(fd); err == nil {
			return k, nil
		}
	}
	return r.statFallback(fd)
}

func (r *Resolver) tryHandle(fd int) (Key, error) {
	fh, mountID, err := unix.NameToHandleAt(fd, "", unix.AT_EMPTY_PATH)
	if err != nil {
		return Key{}, err
	}
	return fromFileHandle(mountID, fh), nil
}

func (r *Resolver) statFallback(fd int) (Key, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Key{}, fmt.Errorf("fstat fd=%d: %w", fd, err)
	}
	return fromStat(&st), nil
}

func (r *Resolver) FromPath(path string) (Key, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		fd, err = unix.Open(path, unix.O_PATH|unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return Key{}, fmt.Errorf("open %q: %w", path, err)
		}
	}
	defer unix.Close(fd)
	return r.FromFD(fd)
}

func (r *Resolver) Same(fd1, fd2 int) (bool, error) {
	k1, err := r.FromFD(fd1)
	if err != nil {
		return false, err
	}
	k2, err := r.FromFD(fd2)
	if err != nil {
		return false, err
	}
	return k1.Equal(k2), nil
}
