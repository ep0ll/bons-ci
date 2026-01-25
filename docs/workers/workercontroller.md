# Worker Controller

## Controller
Worker Controller holds a list of Workers(oci or containerd). with the methods to add or close worker.
The controller currently only support local workers.

## infoController
infocontroller is an implementation of `worker.Info` with a default cache manager, and worker infos such as id, label, platforms, buildkit version, gc policy and cdi devices