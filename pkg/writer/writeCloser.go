package writer

import "io"

type writeCloser struct {
	closed bool
	io.WriteCloser
	action func() error
}

func (c *writeCloser) Close() error {
	if c.closed {
		return nil
	}

	if err := c.WriteCloser.Close(); err != nil {
		return err
	}
	c.closed = true

	if err := c.action(); err != nil {
		return err
	}

	return nil
}

func WriteCloser(wc io.WriteCloser, action func() error) io.WriteCloser {
	return &writeCloser{
		WriteCloser: wc,
		action:      action,
	}
}
