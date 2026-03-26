package executor

import (
	"context"
	"io"
	"net"
	"syscall"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/user"
)

type Meta struct {
	Args           []string
	Env            []string
	User           string
	Cwd            string
	Hostname       string
	Tty            bool
	ReadonlyRootFS bool
	ExtraHosts     []HostIP
	Ulimit         []*pb.Ulimit
	CDIDevices     []*pb.CDIDevice
	CgroupParent   string
	NetMode        pb.NetMode
	SecurityMode   pb.SecurityMode
	ValidExitCodes []int

	RemoveMountStubsRecursive bool
}

type HostIP struct {
	Host string
	IP   net.IP
}

type ProcessInfo struct {
	Meta           Meta
	Stdin          io.ReadCloser
	Stdout, Stderr io.WriteCloser
	Resize         <-chan WinSize
	Signal         <-chan syscall.Signal
}

type WinSize struct {
	Rows uint32
	Cols uint32
}

type Mount struct {
	Src      Mountable
	Selector string
	Dest     string
	Readonly bool
}

type MountableRef interface {
	Mount() ([]mount.Mount, func() error, error)
	IdentityMapping() *user.IdentityMapping
}

type Mountable interface {
	Mount(ctx context.Context, readonly bool) (MountableRef, error)
}
