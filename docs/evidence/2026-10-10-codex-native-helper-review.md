# Independent final native-helper audit

Read-only review on 2026-10-10. Reviewed helper source commit `6abe52d04e44849db2537e2468e072715de405eb` and evidence-only follow-up `6360a6cb007462ee7fbd47bc84dcca1f259523c1` in the transport worktree. That checkout was clean when inspected. I did not execute native programs, submit model work, change authentication or modify source during this audit.

Source digests independently matched the committed clean-run receipt:

| File | SHA-256 |
| --- | --- |
| tests/codex_broker_native.py | 606ad7aa9753c2eaa86896ff94b568b07b9e7ca7fce59c62293f9db83c435b2b |
| tests/codexbrokerprobe/main.go | 0ed6485778bcd61e04bbfe544783ac01f4df1569886a19c9b9df87a2263bc25c |
| tests/codexbrokerprobe/main_test.go | ce3d6c566fe1bff05073348db091c640cf90db49df822e909761cdbdc4c4095e |

No remaining blocker was found in the reviewed helper's finite zero-turn acceptance criteria, public-evidence redaction or cleanup ownership checks.

The success predicate requires each distinct disposable native view to have its own exactly correlated source resume acknowledgement and an exact selected-thread response reconstructed from bytes actually written on its accepted Unix WebSocket connection. Policy prevalidation alone cannot satisfy the downstream write predicate. A composer glyph plus a 750 ms observation interval supplements those wire predicates; the glyph does not establish account/model identity or provider acceptance of a turn. Both retained upstream descriptors and the live primary are checked after each UI disconnect, then real scoped idle observations and empty native history are checked.

Final success now runs after retained Channel.Close calls have joined their source readers. It rejects final policy/projection/RPC/source errors, decode/schema issues, model-turn writes and an acknowledgement count other than two. Unexpected source-close errors participate in that final verdict; only controller context cancellation is accepted as expected shutdown. This closes the reviewed late-counter and otherwise uncounted late-source-loss paths.

Public failure values are finite tags. Raw main/relay/source-close errors and native screens/config captures remain in private state files, requested as mode 0600. Public shape output uses reviewed finite schema keys and redacts unknown names and values. The Python wrapper publishes source/helper/binary fingerprints and finite counters rather than subprocess stderr or raw RPC content.

Native cleanup uses spawned child handles with boot/start checks. Socket removal requires prior matching Unix peer PID, successful strict private wire Dial, current live child/start/boot/inode proof, and the same inode at cleanup. Failed ownership proof does not grant unlink authority. Parent timeout fallback checks a still-live unreaped owned helper plus child parent/start receipts; it does not kill from a saved PID alone. The existing three socket cases check unproved ownership, proved original inode and replacement preservation. These checks support owned fixture cleanup; they are not a same-user process sandbox.

The committed receipt reports a clean run at the exact helper source SHA with two per-view actual-written resume responses/composers, 15 forwarded bootstrap methods, two suppressed global remote-control notifications and zero policy/projection/upstream/native/source error counters. This audit verified the receipt's source attribution and predicates, not an independent repetition of that run.

Retained missing evidence: production bpd app-server launcher; authenticated model work; useful nonempty and paginated history/items delivery; native approval callbacks; restart recovery; writer handover; remote hosts; and public release/package/installer behavior. The helper is explicitly a controlled backend with pregrant zero-turn legacy fixtures, isolated HOME/CODEX_HOME and no-auth loopback provider. Empty fixture success must not certify those product features.
