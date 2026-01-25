# Gateway Client

## NewGatewayFrontend(workers worker.Infos, allowedRepositories []string) (frontend.Frontend, error)
create a New Frontend with list of allowedRepos as their only possible frontends

## func filterPrefix(opts map[string]string, pfx string) map[string]string
filter map keys which start with prefix, then filter those key-value pair after cutting prefix into a new map

## func (gf *gatewayFrontend) checkSourceIsAllowed(source string) error
check if the frontend image is supported, i.e source, by checking against `NewGatewayFrontend` allowedRepos params

## func (gf *gatewayFrontend) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, exec executor.Executor, opts map[string]string, inputs map[string]*opspb.Definition, sid string, sm *session.Manager) (*frontend.Result, error) 
1. opts must have key "source"
2. creates a new `forwarder.LLBBridgeToGatewayClient` and tries to load "source" as llb.State
3. tries marshalling state, retrive frontendDef(llb.Definition) and Result from `Result.Ref`
4. from Result retrives Result.Sys() as workers.WorkerRef and creates a new RootFS
5. if frontend image has label `moby.buildkit.frontend.network.none`, then netmode is set to none for frontend
6. `moby.buildkit.frontend.caps` Label are the current capabilities of the frontend and `opts["frontend.caps"]` are the caps required capabilities, if doesn't met it errors
7. creates a llbBridgeForwarder (a grpc server) and creates a `buildkit-metadata` tmpfs mount with `frontendDef` data stored as file `frontend.bin`
8. the `frontend.bin` is accesssible in container at dir `/run/config/buildkit/metadata`  
9. mount RootFS as readonly with session group `sid`(func param)  
10. returns Result by calling `llbBridgeForwarder#Result()` created from point 7