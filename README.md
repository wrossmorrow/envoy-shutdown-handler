
This is a helper for supporting graceful draining of `envoy` server connections in `kubernetes`. Particularly, it is meant to run as a sidecar to an `envoy` container. `envoy` would have the `preStop` hook
```
httpGet:
  path: /shutdown # optionally [?delay=<int>&period=<int>&deadline=<int>]
  port: 9001 # or whatever port the shutdown container has
```
and the sidecar has the `preStop` hook
```
httpGet:
  path: /waitforshutdown # optionally [?delay=<int>&period=<int>&deadline=<int>]
  port: 9001 # or whatever port the shutdown container has
```
The sidecar itself only needs to be configured to contact the "primary" `envoy`'s admin interface, defaulting to `localhost:9901`. 

What this means is `kubelet` (on behalf of `envoy`) calls the sidecar to trigger shutdown on a pod shutdown signal (but not the `SIGTERM`), while also calling (on behalf of the sidecar) the sidecar's "wait" method intended to just pause until the shutdown is finished. When both calls return, the `preStop` hooks are complete and both `envoy` and the sidecar should receive a `SIGTERM` and terminate. 

This is a high level overview of the (reasonably well-known) process: 
1. Presume envoy is sitting behind a health-checking LB or readiness checker to begin with
2. Fail `envoy`'s health check so the LB removes it from _new_ inbound connections
3. Starts gracefully draining existing connections
4. Using `envoy`'s /stats endpoint, wait until there are 0 active connections
5. At 0 active connections or after a deadline, terminate the proxy

This is done here by letting `/shutdown` call `envoy`'s admin `/healthcheck/fail` endpoint to fail healthchecks, then iterating over `/stats?filter=http.envoy.downstream_cx_active` until there are no connections or the deadline has passed. The other endpoint `/waitforshutdown` just listens on a `chan` until `/shutdown` has both started and then completes (up to a deadline). 

There are CLI arguments to:

* set a delay between health check failing and graceful connection draining, to account for some period during which new connections might still be arriving
* set the period at which the connection countdown will be checked
* set the overall deadline for shutdown handling

Each also can be set with query parameters on an individual request. There are CLI arguments to support customizing the `envoy` administration endpoint's scheme, host, and port as well:

* `envoy-admin-scheme`, default `http`
* `envoy-admin-host`, default `localhost`
* `envoy-admin-port`, default `9901`

If running as a `kubernetes` sidecar, the admin interface need not actually be exposed outside of the `Pod`. 

With a distroless image the image is about 10MB and should require effectively no resources to run. 
