# Native Linux candidate archive acceptance

The clean local `v0.2.0` candidate at `db947051f79dadb45d538604b29a174ffce82056` passed installed archive acceptance on Linux arm64. This is a pre-final candidate receipt; later integrated source and final release archives require a fresh run.

| Gate | Actual result | Scope |
| --- | --- | --- |
| Packaged native terminal fixture | 8 groups passed | Real packaged daemon, CLI, entry helper and native supervisor; authored Python terminal fixture |
| Daemon lifecycle controls | 11 restarts passed | Exact work/runtime/writer identity survives control daemon loss; status/stop/restart controls |
| Installer acceptance | 16 groups passed | 14 install/security groups plus 2 installed empty-daemon runtime groups |
| Loaded binary install smoke | Passed | Install/reinstall/version/uninstall verifies all 4 loaded canonical binary digests; supervisor stays an internal sibling |
| Schema 1 macOS arm64 compatibility | 16 groups passed | Current installer/harness with original clean `v0.1.0` source `60b8bc7` archive; empty daemons only |
| Schema 1 Linux arm64 compatibility | 16 groups passed on corrected fixture | Same legacy archive, image and harness; expanded temporary space after a recorded capacity failure |

The Linux candidate archive SHA256 is `68553a9f47c697e2bd346b4b79ff069a8820850f9f342e98f112e990313db1c9`. Its release manifest declares schema 2, clean source, Go `1.26.6`, Linux arm64 and the exact candidate commit. All manifest file and loaded executable digests were checked. The native fixture and install-smoke scripts are exact candidate Git-object bytes. Installer acceptance ran the separately committed `503b024855c2b59e629944cfd13e4f4616d55982` harness, SHA256 `809c50e416379061a47cd43346436199cf5cefcc9e60fca34f002516e6c1dfd2`. No product binaries were rebuilt for this gate.

The native checks cover dropped launch/attach/stop acknowledgements, one cooperating writer, Unicode, Escape, Ctrl-C, resize, readonly attachment, detach/reconnect, actual TUI recovery, durable private registry/journal, and stale generation refusal. Renaming the exact owned runtime socket and authority receipt produced an explicit unavailable result, retained the same child/helper, and refused replay or replacement across daemon loss. Restoring each original inode reconciled the same live execution. The native fixture image contains no tmux executable; the counted denial shim observed zero invocations.

Linux ran as UID/GID `65534:65534` on kernel `6.8.0-100-generic`, Debian `13.7`, Python `3.11.16`. The fixture derives from pinned official Python image `sha256:e41613d42d4891e4930f79523f93f81bbc7632584ec65e36ab055f41a800b41e`. Its actual immutable image digest is `sha256:096fcabef171b957632013ea1e7d87a7d0e507d16ff7ba85bbd4c589bd363d96`. Public Debian curl/procps packages were added only to this owned fixture. Tests used `--init`, no network, a readonly root filesystem, all capabilities dropped, no-new-privileges, private executable tmpfs, no restart policy and no host mounts. No native authentication or customer credentials were copied.

Installer cleanup now invokes the installed canonical CLI with the original verified daemon instance, after rechecking the owned private socket inode, kernel peer/start identity and package digests. Candidate and both schema 1 runs each stopped 11 empty daemons through that exact-instance API, refused a stale-instance stop, left no pending cleanup receipt, and used zero saved-PID signals. Native child/supervisor cleanup used authenticated exact runtime authority. Process scans found only container init/tail and the inspection command after tests; all four owned containers, including the failed legacy fixture attempt, were removed and verified absent. Successful harness private state was removed.

The first Linux schema 1 run had four late install failures because its `/tmp` tmpfs was limited to 256 MiB. The preserved extraction diagnostic reports `No space left on device`; no daemon was launched and no authority receipt remained pending. A separate repeat with 1024 MiB temporary space passed all 16 groups with unchanged image, archive and script bytes. The receipt also preserves an earlier fixture image build resolution failure and its correction to the original pinned official base. Neither diagnostic is hidden or counted as a product pass.

The compatibility checks establish fresh schema 1 installation and empty-daemon lifecycle, not a live state migration or downgrade. This gate does not establish Linux Codex/Claude program behavior, authenticated model turns, Linux amd64 runtime, host reboot/native resume, an independent remote host, final public download, or an independent user usability/performance pilot. Original per-harness limitations remain in the machine receipt; the separate installer run fills the native fixture's installer-only gap.

[Machine receipt, exact commands, binary digests, image/package provenance, original diagnostics and cleanup](native-release-linux-db94705-candidate.json).
