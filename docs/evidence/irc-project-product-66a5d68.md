# Owned project/thread IRC product proof

Passed against clean source `66a5d687e0d4317383c522a1039eacd634d23777` on `darwin/arm64`. This checkout starts at captain wiring commit `30e26d2` and adds the expanded product harness plus the missing-authority recovery correction. The [finite receipt](irc-project-product-66a5d68.json) records binary digests, pinned runtime identity, operation IDs, stable project/thread identities, and process lifetime evidence. It contains no private credential bytes, credential references, or private state paths.

The explicit opt-in test built the actual CLI, daemon, and native supervisor from that clean source, installed the verified offline Ergo archive, and ran the real local CLI/API against a private loopback TLS realm and private CA. The control PATH contained only those three owned executables; tmux was absent. Three `/bin/sh` byte-input sentinels were launched as native sessions, with two threads explicitly bound to one core project and the third bound to another. No provider, model, native input API, or terminal Enter was used.

All **152 checks passed**. Each thread had its own full-identity registered services account and exact stable nickname. Both project channels were authenticated as registered to the management account. Actual allowlisted human messages reached only the exact initial `nick: text` or `@nick text` recipient; the durable inbox retained the full original text. Broadcasts, addresses in the wrong project, and body mentions produced no fanout. Three explicit bot replies reached the real human peer from their exact per-thread accounts, with each server message ID matching its Delivered echo receipt. Reusing the original message operations produced the same receipt and no duplicate reply.

Actual daemon replacement preserved all three native session/thread identities and the original Ergo child/helper lifetimes. Explicit Ergo replacement preserved project membership, channel registration timestamp/operation, private account references, and stable thread accounts/nicks; all three addressed inboxes worked again after each restart. A stale stop left the replacement alive. All three native input files remained empty and all three native supervisors reported zero writable clients. Exact authenticated cleanup confirmed exit of five owned child/helper pairs and both control processes, released the owned sockets, and removed successful private test state.

The recovery correction separately passed the source race suite, vet, and the opt-in real Ergo thread-account journal test. Missing private directories, credentials or journals, replacement prepared journals, credential drift, and registered-incarnation drift fail closed. Only the fresh claim turn can consume prepared account authority; retained claims reconcile already attempted/confirmed account state without creating a replacement password or registration. A separate no-network full-call-path reproducer retained the thread claim, removed its private account directory, and simulated loss of the remote account. Both confirmed and lost-ACK cases returned repair and kept the registration call count at one. The claim pins its credential digest before account API calls and its registered incarnation on confirmation. Historical format2 records retain read compatibility and acquire pins only from intact, already attempted authority.

Invocation:

```sh
KEELMUX_MANAGED_IRCD_ACCEPTANCE=1 \
KEELMUX_ERGO_ARCHIVE=/absolute/owned/ergo-2.19.1-macos-arm64.tar.gz \
KEELMUX_MANAGED_IRCD_REPORT=/absolute/new/irc-project-product-receipt.json \
go test -race ./cmd/bpd -run '^TestManagedIRCDProductAcceptance$' -v -count=1
```

Pinned runtime: Ergo `2.19.1`, upstream source `63c743a70644f0f19109508ade8580bf37f7d23d`, archive SHA256 `1bd97a0917036061e2dcdfc29149c2e252e3bc856282e0956afa2aaaa54dd787`. The product test completed in 21.14 seconds. Default tests skip this provisioning gate unless explicitly enabled.

This is exact-source, owned local product evidence. IRC intake has bounded live coverage and reports a disconnect history gap. Delivered proves the server echo, not a reader receipt or model acceptance. Permission consumed before failed account IO/authentication can require manual repair. The test does not qualify distributable packages, other host targets, production/customer deployments, or public release. A captain integration with a different source SHA needs its own final gate.
