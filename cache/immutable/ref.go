package immutable

type Ref interface {
	Size() (int64, error)
}