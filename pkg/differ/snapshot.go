package differ

import (
	"context"

	"github.com/containerd/containerd/v2/core/mount"
)

type diffOptions struct{}

type Opt func(*diffOptions) error

type Differ interface {
	Diff(ctx context.Context) (string, error)
}

func NewDiffer(lower, upper []mount.Mount, opt ...Opt) (differ Differ, err error) {
	err = mount.WithReadonlyTempMount(context.Background(), lower, func(lowerView string) error {
		return mount.WithReadonlyTempMount(context.Background(), upper, func(upperView string) error {
			differ, err = DifferFromPath(lowerView, upperView, opt...)
			return err
		})
	})

	return differ, err
}

func DifferFromPath(lower, upper string, opt ...Opt) (Differ, error) {
	var diffOpts = &diffOptions{}
	for _, op := range opt {
		if err := op(diffOpts); err != nil {
			return nil, err
		}
	}

	return &differ{
		lowerdir: lower,
		upperdir: upper,
		opt:      *diffOpts,
	}, nil
}

type differ struct {
	lowerdir, upperdir string
	opt                diffOptions
}

// Diff implements Differ.
func (d *differ) Diff(ctx context.Context) (string, error) {
	panic("unimplemented")
}
