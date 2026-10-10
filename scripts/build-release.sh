#!/bin/sh
# Build reviewed release assets; never publishes or installs them.
set -eu
export LC_ALL=C
usage() { printf '%s\n' 'Usage: scripts/build-release.sh vX.Y.Z OUTDIR [darwin/arm64 darwin/amd64 linux/amd64 linux/arm64]'; }
[ "$#" -ge 2 ] || { usage >&2; exit 2; }
version=$1
out=$2
shift 2
printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || { usage >&2; exit 2; }
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$root"
for tool in git go python3; do command -v "$tool" >/dev/null 2>&1 || { printf 'Required build tool: %s\n' "$tool" >&2; exit 1; }; done
[ "$(go list -m)" = github.com/seanheiney/keelmux ] || { printf '%s\n' 'Release requires the Keelmux module and buildinfo integration.' >&2; exit 1; }
go list github.com/seanheiney/keelmux/internal/buildinfo >/dev/null 2>&1 || { printf '%s\n' 'Release buildinfo integration is missing.' >&2; exit 1; }
dirty=false
[ -z "$(git status --porcelain)" ] || dirty=true
dev=${KEELMUX_DEV_TEST:-0}
case "$dev" in 0|1) ;; *) printf '%s\n' 'KEELMUX_DEV_TEST must be 0 or 1.' >&2; exit 2;; esac
[ "$dirty" = false ] || [ "$dev" = 1 ] || { printf '%s\n' 'Release source must be committed and clean. KEELMUX_DEV_TEST=1 marks a development test build.' >&2; exit 1; }
commit=$(git rev-parse HEAD)
epoch=${SOURCE_DATE_EPOCH:-$(date +%s)}
printf '%s\n' "$epoch" | grep -Eq '^[0-9]+$' || { printf '%s\n' 'SOURCE_DATE_EPOCH must be a nonnegative integer.' >&2; exit 2; }
built_at=$(python3 -c 'import datetime,sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))' "$epoch")
[ "$#" -gt 0 ] || set -- darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
seen=' '
for platform do
    case "$platform" in darwin/arm64|darwin/amd64|linux/amd64|linux/arm64) ;; *) printf 'Unsupported target: %s\n' "$platform" >&2; exit 2;; esac
    case "$seen" in *" $platform "*) printf 'Duplicate target: %s\n' "$platform" >&2; exit 2;; esac
    seen="$seen$platform "
done
mkdir -p "$out"
out=$(CDPATH= cd -- "$out" && pwd -P)
[ ! -e "$out/SHA256SUMS" ] || { printf '%s\n' 'Output already contains SHA256SUMS; use a fresh directory.' >&2; exit 1; }
stage=$(mktemp -d "$out/.keelmux-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
go_version=$(go env GOVERSION)
flags="-s -w -buildid= -X github.com/seanheiney/keelmux/internal/buildinfo.Version=$version -X github.com/seanheiney/keelmux/internal/buildinfo.Commit=$commit -X github.com/seanheiney/keelmux/internal/buildinfo.BuiltAt=$built_at"
for platform do
    os=${platform%/*}
    arch=${platform#*/}
    dir="$stage/${os}_${arch}"
    mkdir -p "$dir"
    for pair in keelmux:bp keelmuxd:bpd keelmux-entry:bpg; do
        name=${pair%:*}
        pkg=${pair#*:}
        GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$flags" -o "$dir/$name" "./cmd/$pkg"
    done
    cp LICENSE "$dir/LICENSE"
    cp README.md "$dir/README.md"
    python3 scripts/pack-release.py "$dir" "$stage" "$version" "$commit" "$built_at" "$os" "$arch" "$go_version" "$epoch" "$dirty" "$dev"
done
# Publish the completed local asset set only after every target succeeds.
python3 scripts/pack-release.py --checksums "$stage"
for platform do
    os=${platform%/*}
    arch=${platform#*/}
    asset="keelmux_${version}_${os}_${arch}.tar.gz"
    [ ! -e "$out/$asset" ] || { printf 'Output already contains %s.\n' "$asset" >&2; exit 1; }
done
for platform do
    os=${platform%/*}
    arch=${platform#*/}
    asset="keelmux_${version}_${os}_${arch}.tar.gz"
    mv "$stage/$asset" "$out/$asset"
done
mv "$stage/SHA256SUMS" "$out/SHA256SUMS"
printf 'Built %s from %s into %s\n' "$version" "$commit" "$out"
