# Cache Result

## cacheResultStorage
```go
type cacheResultStorage struct {
	wc *Controller
}
```
`Save()`: if solver.Result has has cachepolicyretain then set cachepolicyretain. else error.
`LoadRemotes()`: calls WorkerRef#GetRemotes with compression config set to defaults if omitted.