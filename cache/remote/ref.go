package remote

import "time"

type Fetcher interface {
	Metadata() Descriptor
	Pull(Reference) ()
}

type Descriptor struct {
	Size int64
	CreatedAt, UpdatedAt, LastAccesedOn time.Time
}

type Reference string