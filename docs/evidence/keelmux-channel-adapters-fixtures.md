# Authored channel adapter fixture evidence

2026-10-10, macOS arm64, Go 1.26.6. Adapter contribution is based on public
release source `60b8bc705b2636b087bf2fd872e976aa466ad3e9`, the private-file
credential helper and frozen `internal/channels` contract. The contribution
commit identifies the exact adapter/helper/test bytes. No production account,
credential, external message or model turn was used.

Commands: `go test -race ./internal/chatadapter ./internal/channels` and
`go vet ./internal/chatadapter ./internal/channels`. Both passed.

Mattermost and Slack run against owned HTTPS fixtures. Slack's injected
RoundTripper routes its fixed API endpoint to that fixture; production endpoint
validation and redirect refusal remain enabled. Tests establish same-token
authentication and send, immutable self/root/realm refusal before effectful
POST, strict acknowledgement scope, lost-ACK uncertainty, read-only authored
marker reconciliation with one actual POST, bounded exact-root pagination,
Slack cursor expiry overlap recovery, JSON duplicate-key/nesting refusal and
Retry-After cooldown without retries. They do not establish real provider
permission, API-version compatibility or account capability.

IRC runs against owned `net.Pipe` protocol fixtures using the injected dialer.
Production uses verified TLS only. Tests establish CAP/SASL 400-byte framing,
services-account numeric 900 plus SASL success and channel membership, persistent
live intake, account-tag/source-ID authority without nick or client-tag
authority, qualified echo acknowledgements, completed labeled batches, refusal
before PRIVMSG on changed self, Unicode byte bounds/injection refusal, lost or
late echo reconciliation without replay, explicit buffer/disconnection gaps and
connection cleanup. IRC delivery acknowledges the server's qualified echo;
it does not prove any reader received the message. External IRC history and
reliable replay after a gap are unsupported. No real IRC network was contacted.

The shared endpoint helper permits benign hostname case and trailing slash
normalization before immutable bind comparisons, retains exact Mattermost base
paths/ports, fixes Slack to `https://slack.com/api`, and requires an explicit
`ircs://host:port` authority. It refuses insecure schemes, embedded credentials,
queries, fragments, path traversal and unsupported provider scopes.

Wire references: [Mattermost posts API](https://raw.githubusercontent.com/mattermost/mattermost/master/api/v4/source/posts.yaml),
[Mattermost thread pagination implementation](https://raw.githubusercontent.com/mattermost/mattermost/master/server/channels/store/sqlstore/post_store.go),
[Slack auth.test](https://docs.slack.dev/reference/methods/auth.test/),
[Slack chat.postMessage](https://docs.slack.dev/reference/methods/chat.postMessage/),
[Slack conversations.replies](https://docs.slack.dev/reference/methods/conversations.replies/),
[Slack rate limits](https://docs.slack.dev/apis/web-api/rate-limits/),
[IRCv3 SASL](https://ircv3.net/specs/extensions/sasl-3.1.html),
[account-tag](https://ircv3.net/specs/extensions/account-tag.html),
[labeled-response](https://ircv3.net/specs/extensions/labeled-response.html),
[echo-message](https://ircv3.net/specs/extensions/echo-message.html), and
[message IDs](https://ircv3.net/specs/extensions/message-ids.html).

Provider marker properties are correlation evidence when authenticated self,
root/channel and payload hash match. They are not durable remote deduplication
guarantees. The shared durable controller must claim once and retain uncertainty;
adapter reconciliation never sends another message.

Follow-up review controls: disabled bindings refuse Send while retaining
read-only historical Reconcile. All provider receipts require the exact frozen
text as well as hash/scope/self evidence; edited/copied MM properties or Slack
metadata do not prove the original payload. Mattermost receipts additionally
require a normal, undeleted reply with positive creation time; Slack receipts
require a normal message with a valid non-root timestamp. HTTP parsing rejects
invalid UTF-8 and case-aliased JSON fields that Go would otherwise decode into
the same identity member. Closing adapters stops subsequent HTTP requests and
closes owned idle resources. These controls passed the same focused race/vet
commands.
