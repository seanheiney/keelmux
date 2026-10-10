# Independent Keelmux macOS package acceptance

The first independent run passed all 16 groups on macOS 26.7.1 arm64. The actual `v0.1.0` archive was built from clean source `0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44`; its SHA-256 is `53aaf724e8c245bc89454633e50dfb62644e2727028d3785823017ad71c050cc`. The committed acceptance harness was `4a9f9cfc48f76cddc475973b18fef7fdcd4d2ff2`, with an empty implementation diff. The [machine receipt](keelmux-release-macos-first.json) records installer, harness and installed executable digests.

The command was:

```sh
python3 tests/release_acceptance.py \
  --installer /Volumes/DataSSD1/agent-backplane-worktrees/keelmux-installer/install.sh \
  --assets /private/tmp/keelmux-release-0be153b \
  --version v0.1.0 \
  --artifact-source-sha 0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44 \
  --runtime \
  --report docs/evidence/keelmux-release-macos-first.json
```

Fresh installation and same-version repetition used real released binary bytes and loopback downloads. The manifest, aliases, file hashes, version metadata and target matched. Corrupt, truncated and missing assets refused activation while preserving the accepted tuple. Four malformed archive fixtures had matching download checksums but unsafe member/link shapes; all were refused. Six foreign command files, foreign and prospective links, and an unrelated PATH command were preserved. Concurrent initial installers produced one complete install and one clean refusal. Owned uninstall preserved native/shell/state/retained-skill sentinels and a replaced foreign command; reinstall succeeded.

Eighteen actual packaged metadata/help cases created no state. Ten cold starts through `keelmux` and `bp` covered fresh and validated legacy defaults, canonical/legacy environment precedence and explicit paths with spaces. The same host identity survived alias reuse. Three unsafe legacy authorities were refused without creating a fresh second authority. All eleven empty owned daemons, including the startup control below, were stopped after private Unix peer PID, current process lifetime/command and exact socket inode checks. No vendor CLI or model turn was launched.

Five actual packaged CLI requests reached private Unix HTTP fixtures that returned protocol mismatch, protocol status rejection, authorization rejection, malformed JSON or unexpected EOF. Each refused a second daemon with zero calls to a counted sibling executable. A healthy existing-authority control succeeded without a call. An absent-socket control called the same wrapper once and started the actual packaged empty daemon. Only that controlled sibling wrapper was substituted; the CLI bytes were checked against the archive, so this is an executable refusal test rather than a claim about a pristine daemon fixture or arbitrary system process census.

Native settings, authentication and shell preservation were measured using owned sentinel files in temporary homes, not real credential contents. The successful run removed its private temporary test files. This receipt does not establish packaged two-provider native attach/recovery, upgrade or downgrade schema compatibility, interrupted/unwritable install behavior, public GitHub download, a remote machine or authenticated model turns. Those remain separate release gates where advertised. The earlier native/broker source receipts retain their own exact source and scope.
