# Initial implementation contracts: bp/1

Captain owns domain, protocol, cmd, go.mod/go.sum, Makefile and integration. Contracts are deliberately small; future chat/federation adds scoped operations rather than making tmux a semantic adapter.

- `domain.Session` joins distinct host/work/thread/lane/session IDs and an execution generation. Runtime IDs are only locators. Account, actual model and native thread remain source-qualified unknown until verified.
- `runtime.Backend` implements Launch, Lookup by exact execution and operation, Inspect, Stop and AttachCommand. `domain.LaunchSpec` supplies argv, workspace, stable session, operation and optional environment. Launch installs BP_SESSION_ID, BP_EXECUTION_ID, BP_OPERATION_ID into its owned child. Native thread identity remains separate.
- `protocol` uses private Unix-socket HTTP JSON `bp/1`; no network listener or web interface. Paths: GET sessions; POST launch/link/attach/release/reconcile/stop under /v1. Clients carry generation, operation, client and lease identity.
- Durable-state worker implements `store.Open(path) (*Store,error)`, `Close() error`; daemon `New(store *store.Store, backend runtime.Backend, hostID domain.ID) (*Service,error)`, `Handler() http.Handler`, `Reconcile(context.Context) error`. Serialized mutations persist launch intent before runtime effects. Attempted/uncertain starts are looked up and never blindly relaunched. No lookup result is proof of provider acceptance.
- Runtime worker implements `tmux.New(socket string) (*Backend,error)`. Socket must be explicit and private. Exact markers survive daemon loss. Inspect validates locators and process-start/boot evidence. AttachCommand returns argument array; native keys use tmux with Ctrl-G d detach, Ctrl-G Ctrl-G literal prefix. Read-only attach is explicit. Detect writable tmux clients before admitting a managed writer.
- TUI worker implements `tui.Run(ctx context.Context, client *protocol.Client, statePath string) error`. It uses protocol inventory and attach/release. Native execution suspends/restarts the TUI and restores selection/group. Clientstate saves atomic private place/groups/drafts. It may use a pinned Bubble Tea v1 dependency; request manifest additions from captain.
- Provider worker will implement discovery and supported argv, and owned installation separately. Native logins/config stay untouched during proof. Link validates managed environment against backend identity; raw supplied IDs cannot establish arbitrary unmanaged native identity.

Workers own only assigned paths and report tests, exact commit, limitations and evidence gaps. No worker edits shared manifests or entry points. No arbitrary terminal adoption, semantic send-keys, inferred account/model, public release or billed-mode change.

## Lost acknowledgement and stale command amendments

Stop requires SessionID, ExecutionID, Generation and durable OperationID. Persist intent before stop; reject a stale generation and a conflicting reused operation ID. Retrying a confirmed stop returns its prior outcome and never targets a replacement execution.

Attach acquisition requires SessionID, ExecutionID, Generation, ClientID and OperationID. Persist the idempotent grant keyed by the full acquisition identity before replying. A lost response retries the same operation and returns the same token, or an explicit recovery outcome if the process/writer state changed. It cannot release/replace another client's writer. Release requires the exact stored token/client/execution/operation. Reconcile durable grants against actual tmux writable clients after daemon loss; absent database leases cannot fence a surviving native client.

Link also carries an inherited BP_LINK_TOKEN nonce kept only in the private launch receipt/child environment. Backend runtime.LinkVerifier validates it against exact identities and live markers. Daemon rejects linking when the verifier is absent; public inventory IDs are insufficient. Nonce is never included in session JSON or journal. It proves owned-launch correlation within the same-user boundary, not model-supplied native thread/account identity or OS isolation.
