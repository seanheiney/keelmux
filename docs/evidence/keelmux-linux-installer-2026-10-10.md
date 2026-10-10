# Non-root Linux installer acceptance

The actual installer and Keelmux v0.1.0 Linux arm64 archive from clean source `0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44` passed all 16 independent installer/runtime groups. The run used non-root UID/GID 65534 in the same owned, unprivileged, mount-free Debian container documented in [packaged Linux runtime acceptance](keelmux-linux-arm64-2026-10-10.md). Docker networking was disconnected; package downloads came only from the harness's owned loopback HTTP server with explicit test opt-in.

The [machine receipt](keelmux-linux-installer-2026-10-10.json) records actual archive identity and binary metadata, installer SHA-256, and executed harness SHA-256. Harness source is `bdbae07298a9a7cb61916b70c7dbad613150bc97`; exact executed bytes hash to `bf6b8b3bdabfdc5fc7eaff8b9c37bf736e64a2cc832d4b9ec8765d0c0ec39c14`. Because the helper was copied into the container, its revision is explicitly caller-supplied and its checkout implementation diff is null. Actual release provenance is verified from the consumed archive, manifest, checksums, and executable versions.

The checks passed archive identity, HTTP test opt-in, install/reinstall, paths with spaces, corrupt/truncated/missing assets, unowned collisions, finite archive extraction, unsupported platform refusal, concurrent installation, owned uninstall with retained state, read-only product information, automatic sibling daemon discovery across state-selection combinations, and refusal to start a second daemon over existing authority. Eleven empty real packaged daemon cases were started and stopped using exact process and socket receipts. Post-run process inspection showed no live `keelmuxd`, native provider, or fixture process. Adopted zombie processes are removed with the owned container; they are not live runtimes.

```sh
docker exec --user 65534:65534 keelmux-validation-20261010 \
  python3 /probe/release_acceptance.py \
  --installer /probe/install.sh --assets /probe/assets --version v0.1.0 \
  --artifact-source-sha 0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44 \
  --harness-checkout-sha bdbae07298a9a7cb61916b70c7dbad613150bc97 \
  --runtime --report /probe/nonroot/installer-final.json
```

Python 3.11, tmux 3.3a, curl 7.88.1, GNU tar/checksum tools, and procps were installed only inside this owned container. Git was also installed during preparation of a genuine shallow checkout fallback, but the passing standalone harness did not use it for provenance.

The first copied-harness attempt could not write its report because report generation assumed a Git checkout and invoked unavailable `git`. This was a harness portability failure, not a product result; its private diagnostics were retained. The committed portable helper explicitly labels caller-supplied harness revision and keeps absent checkout diff metadata null. No report metadata or result was fabricated.

This is actual installer behavior with owned fixture downloads and sentinel credentials, not a public GitHub download or native Linux Codex/Claude model workflow. Public release download, authenticated model turns, independent remote hosts, and upgrade/downgrade schema compatibility remain separate gates. Later product changes, including daemon lifecycle commands, require new exact-source artifacts and reruns.
