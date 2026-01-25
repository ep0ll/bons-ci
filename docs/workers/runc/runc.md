# RUNC

## SnapshotFactory
a containerd snapshotter instance
```go
type SnapshotterFactory struct {
	Name string
	New  func(root string) (ctdsnapshot.Snapshotter, error)
}
```

## NewWorkerOpt
1. creates a new root folder based on snapshotfactory name and requested root dir.
2. sets up appropriate `network provider`.
3. sets up new resource monitor with "github.com/prometheus/procfs"
4. creates a new runc executor("github.com/moby/buildkit/executor/runcexecutor")
5. create a new snapshot factory under the root + snapshots
6. also a containerd local content store under "content" dir of root
7. a boltdb "containermeta.db" with 0664 permission
8. NewDB creates a new metadata database using the provided bolt database, content store, and snapshotters. also initializes it.
9. new containerd content store under "buildkit" namespace
10. creates a workerid else if not exists.
11. retrives user kernel's hostname.
12. sets few xlabels.
13. leases a new containerd metadata store( from metadata database ) under "buildkit" namespace.
14. migrate any cache metadata to v2 and create a metadata store named "metadata_v2.db" from "metadata.db"
15. returns a base WorkerOpt