#!/bin/sh
# Keelmux user installer: verified release code only, never native auth or state.
set -eu
export LC_ALL=C
umask 077

usage() {
    cat <<'HELP'
Keelmux installer
  sh install.sh [--version vX.Y.Z] [--prefix /absolute/path] [--bin-dir /absolute/path]
  sh install.sh --uninstall [--prefix /absolute/path] [--bin-dir /absolute/path]
Default code: $HOME/.local/share/keelmux; commands: $HOME/.local/bin
No sudo, dependency installation, shell-profile edits or native auth changes.
HELP
}
die() { printf 'Keelmux: %s\n' "$*" >&2; exit 1; }
validate_manifest() {
    # Standalone finite JSON parser: no jq/Python dependency, duplicate keys or
    # ambiguous scalar coercion. The release schema contains only ASCII public
    # provenance, five or six file digests and three exact relative aliases.
    awk -v expected_version="$version" -v expected_os="$os" -v expected_arch="$arch" -v development_allowed="$test_http" '
    function bad() { failed=1; exit 1 }
    function ws() { while (substr(src,pos,1) ~ /^[ \t\r\n]$/) pos++ }
    function string(    out,c,e,h,i,n,d) {
        if (substr(src,pos++,1) != "\"") bad()
        out=""
        while (pos <= length(src)) {
            c=substr(src,pos++,1)
            if (c == "\"") return out
            if (c == "\\") {
                e=substr(src,pos++,1)
                if (e == "\"" || e == "\\" || e == "/") c=e
                else if (e == "u") {
                    h=substr(src,pos,4); pos+=4
                    if (length(h) != 4 || h ~ /[^0-9A-Fa-f]/) bad()
                    n=0
                    for (i=1;i<=4;i++) {
                        d=index("0123456789abcdef",tolower(substr(h,i,1)))-1
                        n=n*16+d
                    }
                    if (n < 32 || n > 126) bad()
                    c=sprintf("%c",n)
                } else bad()
            }
            if (index(printable,c) == 0 || c == "") bad()
            out=out c
        }
        bad()
    }
    function value(path,depth,    c,key,child,num,start) {
        if (depth > 3 || ++nodes > 64) bad()
        ws(); c=substr(src,pos,1)
        if (c == "{") {
            kind[path]="object"; pos++; ws()
            if (substr(src,pos,1) == "}") {pos++; return}
            while (1) {
                ws(); key=string()
                if (key == "" || index(key,"/")) bad()
                child=path "/" key
                if (child in kind) bad()
                count[path]++
                ws(); if (substr(src,pos++,1) != ":") bad()
                value(child,depth+1)
                ws(); c=substr(src,pos++,1)
                if (c == "}") return
                if (c != ",") bad()
            }
        } else if (c == "\"") {
            kind[path]="string"; val[path]=string()
        } else if (substr(src,pos,4) == "true") {
            kind[path]="boolean"; val[path]="true"; pos+=4
        } else if (substr(src,pos,5) == "false") {
            kind[path]="boolean"; val[path]="false"; pos+=5
        } else {
            start=pos
            while (substr(src,pos,1) ~ /^[0-9]$/) pos++
            num=substr(src,start,pos-start)
            if (num !~ /^(0|[1-9][0-9]*)$/) bad()
            kind[path]="number"; val[path]=num
        }
    }
    function need(path,type) { if (kind[path] != type) bad() }
    function exact(path,type,want) { need(path,type); if (val[path] != want) bad() }
    function hash(path) { need(path,"string"); if (length(val[path]) != 64 || val[path] ~ /[^a-f0-9]/) bad() }
    BEGIN {
        for (i=32;i<=126;i++) printable=printable sprintf("%c",i)
        pos=1
    }
    {src=src $0 "\n"}
    END {
        if (failed) exit 1
        value("",0); ws()
        if (pos <= length(src)) bad()
        need("","object")
        for (path in kind) {
            if (path == "" || path == "/schema" || path == "/product" || path == "/version" ||
                path == "/commit" || path == "/built_at" || path == "/os" || path == "/arch" ||
                path == "/go_version" || path == "/files" || path == "/aliases" ||
                path == "/source_clean" || path == "/development_test" ||
                path == "/files/keelmux" || path == "/files/keelmuxd" ||
                path == "/files/keelmux-entry" || path == "/files/keelmux-supervisor" || path == "/files/LICENSE" ||
                path == "/files/README.md" || path == "/aliases/bp" ||
                path == "/aliases/bpd" || path == "/aliases/bpg") continue
            bad()
        }
        need("/schema","number"); if (val["/schema"] != "1" && val["/schema"] != "2") bad()
        exact("/product","string","Keelmux")
        exact("/version","string",expected_version)
        exact("/os","string",expected_os); exact("/arch","string",expected_arch)
        need("/commit","string")
        if (length(val["/commit"]) != 40 || val["/commit"] ~ /[^a-f0-9]/) bad()
        need("/built_at","string")
        if (val["/built_at"] !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z$/) bad()
        need("/go_version","string")
        if (val["/go_version"] !~ /^go[0-9]+\.[0-9]+(\.[0-9]+)?$/) bad()
        need("/files","object"); if (count["/files"] != (val["/schema"] == "2" ? 6 : 5)) bad()
        if (val["/schema"] == "2") hash("/files/keelmux-supervisor")
        else if ("/files/keelmux-supervisor" in kind) bad()
        hash("/files/keelmux"); hash("/files/keelmuxd"); hash("/files/keelmux-entry")
        hash("/files/LICENSE"); hash("/files/README.md")
        need("/aliases","object"); if (count["/aliases"] != 3) bad()
        exact("/aliases/bp","string","keelmux")
        exact("/aliases/bpd","string","keelmuxd")
        exact("/aliases/bpg","string","keelmux-entry")
        need("/source_clean","boolean"); need("/development_test","boolean")
        if (development_allowed != 1 && (val["/source_clean"] != "true" || val["/development_test"] != "false")) bad()
        for (path in val) print path "=" val[path]
    }' "$1" > "$2"
}
uid=$(id -u)
[ "$uid" != 0 ] || die 'Run as your normal user, without sudo.'
version=${KEELMUX_VERSION:-}
prefix=${KEELMUX_INSTALL_DIR:-${HOME:?HOME is required}/.local/share/keelmux}
bin_dir=${KEELMUX_BIN_DIR:-$HOME/.local/bin}
uninstall=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --help|-h) usage; exit 0;;
        --version|--prefix|--bin-dir)
            [ "$#" -ge 2 ] || die "Missing value for $1."
            case "$1" in --version) version=$2;; --prefix) prefix=$2;; --bin-dir) bin_dir=$2;; esac
            shift 2;;
        --uninstall) uninstall=1; shift;;
        *) die "Unknown option: $1";;
    esac
done
for tool in stat readlink mkdir mv rm rmdir awk grep sed sort wc tr cut cat ls chmod ln uname; do command -v "$tool" >/dev/null 2>&1 || die "Required tool is missing: $tool."; done
host=$(uname -s)
case "$host" in Darwin) stat_mode=bsd;; Linux) stat_mode=gnu;; *) die 'Supported hosts are macOS and Linux.';; esac
owner() { if [ "$stat_mode" = bsd ]; then stat -f '%u' "$1"; else stat -c '%u' "$1"; fi; }
mode() { if [ "$stat_mode" = bsd ]; then stat -f '%Lp' "$1"; else stat -c '%a' "$1"; fi; }
sha() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
    else die 'Required SHA-256 tool is missing (sha256sum or shasum).'; fi
}
clean_path() {
    case "$1" in /*) ;; *) die 'Installation paths must be absolute.';; esac
    case "$1" in /|*/|*//*|*/../*|*/./*|*/..|*/.) die 'Installation paths must be clean, non-root absolute paths.';; esac
    [ "$(printf '%s' "$1" | tr -d '\011\012\015')" = "$1" ] || die 'Installation paths cannot contain control characters.'
}
clean_path "$prefix"
clean_path "$bin_dir"
[ "$prefix" != "$bin_dir" ] || die 'Code and command directories must differ.'
case "$bin_dir/" in "$prefix/"*) die 'Command directory cannot be inside the code directory.';; esac
case "$prefix/" in "$bin_dir/"*) die 'Code directory cannot be inside the command directory.';; esac

stage=
prefix_lock=0
bin_lock=0
created_prefix=0
created_links=
created_receipt=0
links_owned=0
links_receipt=
new_version=
committed=0
marker_text=
target_for() {
    case "$1" in bp) printf '%s' keelmux;; bpd) printf '%s' keelmuxd;; bpg) printf '%s' keelmux-entry;; *) printf '%s' "$1";; esac
}
owned_link() {
    [ -L "$1" ] && [ "$(owner "$1")" = "$uid" ] && [ "$(readlink "$1")" = "$2" ]
}
cleanup() {
    rc=$?
    trap - 0 HUP INT TERM
    if [ "$committed" = 0 ]; then
        for name in $created_links; do
            target="$prefix/current/$(target_for "$name")"
            if owned_link "$bin_dir/$name" "$target"; then rm "$bin_dir/$name"; fi
        done
        if [ -n "$new_version" ] && [ -d "$new_version" ] && [ ! -L "$new_version" ]; then
            remove_version "$new_version" || :
        fi
        if [ "$created_receipt" = 1 ] && [ -f "$prefix/.keelmux-links" ] && [ ! -L "$prefix/.keelmux-links" ] && [ "$(cat "$prefix/.keelmux-links")" = "$links_receipt" ]; then
            rm "$prefix/.keelmux-links"
        fi
    fi
    if [ -n "$stage" ]; then rm -rf "$stage"; fi
    if [ "$bin_lock" = 1 ]; then rmdir "$bin_dir/.keelmux-install.lock" || :; fi
    if [ "$prefix_lock" = 1 ]; then rmdir "$prefix/.install.lock" || :; fi
    # Keep the private ownership scaffold. Removing it after releasing locks
    # could race a new installer that has already acquired the same prefix.
    exit "$rc"
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

if [ "$uninstall" = 1 ] && [ ! -e "$prefix" ] && [ ! -L "$prefix" ]; then
    printf '%s\n' 'Keelmux is not installed at this prefix.'
    committed=1
    exit 0
fi
if [ -L "$prefix" ]; then die 'Code prefix cannot be a symlink.'; fi
if [ ! -e "$prefix" ]; then mkdir -p "$prefix"; created_prefix=1; fi
[ -d "$prefix" ] && [ "$(owner "$prefix")" = "$uid" ] && [ "$(mode "$prefix")" = 700 ] || die 'Code prefix must be an owned private (0700) directory.'
prefix=$(CDPATH= cd -P "$prefix" && pwd -P)
if [ -L "$bin_dir" ]; then die 'Command directory cannot be a symlink.'; fi
if [ ! -e "$bin_dir" ]; then mkdir -p "$bin_dir"; fi
[ -d "$bin_dir" ] && [ "$(owner "$bin_dir")" = "$uid" ] || die 'Command directory must belong to your user.'
case "$(mode "$bin_dir")" in *[2367][0-7]|*[0-7][2367]) die 'Command directory cannot be writable by other users.';; esac
bin_dir=$(CDPATH= cd -P "$bin_dir" && pwd -P)
[ "$prefix" != "$bin_dir" ] || die 'Canonical code and command directories must differ.'
case "$bin_dir/" in "$prefix/"*) die 'Canonical command directory cannot be inside the code directory.';; esac
case "$prefix/" in "$bin_dir/"*) die 'Canonical code directory cannot be inside the command directory.';; esac
marker_text=$(printf 'keelmux-installer-v1\nuid=%s\nprefix=%s\nbin=%s\n' "$uid" "$prefix" "$bin_dir")
if [ -e "$prefix/.keelmux-owned" ] || [ -L "$prefix/.keelmux-owned" ]; then
    [ ! -L "$prefix/.keelmux-owned" ] && [ -f "$prefix/.keelmux-owned" ] && [ "$(owner "$prefix/.keelmux-owned")" = "$uid" ] || die 'Invalid ownership marker.'
    [ "$(cat "$prefix/.keelmux-owned")" = "$marker_text" ] || die 'This prefix belongs to another install or binary directory.'
else
    [ "$uninstall" = 0 ] || die 'Refusing to uninstall an unowned directory.'
    [ "$(ls -A "$prefix" | wc -c | tr -d ' ')" = 0 ] || die 'Refusing to claim a nonempty unowned directory.'
    (set -C; printf '%s\n' "$marker_text" > "$prefix/.keelmux-owned") || die 'Another installation claimed this prefix.'
fi
mkdir "$prefix/.install.lock" 2>/dev/null || die 'Another installation is active; inspect the owned install lock if it was interrupted.'
prefix_lock=1
mkdir "$bin_dir/.keelmux-install.lock" 2>/dev/null || die 'Another installation is using this command directory.'
bin_lock=1
links_receipt=$(printf '%s\n' "$marker_text"; for name in keelmux keelmuxd keelmux-entry bp bpd bpg; do printf '%s=%s/current/%s\n' "$name" "$prefix" "$(target_for "$name")"; done)
if [ -e "$prefix/.keelmux-links" ] || [ -L "$prefix/.keelmux-links" ]; then
    [ -f "$prefix/.keelmux-links" ] && [ ! -L "$prefix/.keelmux-links" ] && [ "$(owner "$prefix/.keelmux-links")" = "$uid" ] && [ "$(cat "$prefix/.keelmux-links")" = "$links_receipt" ] || die 'Invalid installed command ownership receipt.'
    links_owned=1
fi
if [ -L "$prefix/versions" ]; then die 'Versions directory cannot be a symlink.'; fi
if [ ! -e "$prefix/versions" ]; then mkdir "$prefix/versions"; fi
[ -d "$prefix/versions" ] && [ "$(owner "$prefix/versions")" = "$uid" ] && [ "$(mode "$prefix/versions")" = 700 ] || die 'Invalid owned versions directory.'

# Validate local ownership and all recorded bytes before reuse or removal.
verify_version() {
    dir=$1
    [ -d "$dir" ] && [ ! -L "$dir" ] && [ "$(owner "$dir")" = "$uid" ] || return 1
    for meta in .keelmux-version .keelmux-files.sha256 .keelmux-archive.sha256; do
        [ -f "$dir/$meta" ] && [ ! -L "$dir/$meta" ] && [ "$(owner "$dir/$meta")" = "$uid" ] || return 1
    done
    verified_files="keelmux keelmuxd keelmux-entry LICENSE README.md RELEASE.json"
    case "$(cat "$dir/.keelmux-version")" in
        keelmux-installed-v1) verified_count=6;;
        keelmux-installed-v2) verified_count=7; verified_files="$verified_files keelmux-supervisor";;
        *) return 1;;
    esac
    [ "$(wc -l < "$dir/.keelmux-files.sha256" | tr -d ' ')" = "$verified_count" ] || return 1
    for name in $verified_files; do
        [ -f "$dir/$name" ] && [ ! -L "$dir/$name" ] && [ "$(owner "$dir/$name")" = "$uid" ] || return 1
        actual=$(sha "$dir/$name") || return 1
        [ "$(awk -v expected="$actual  $name" '$0 == expected {n++} END {print n+0}' "$dir/.keelmux-files.sha256")" = 1 ] || return 1
    done
    for name in bp bpd bpg; do owned_link "$dir/$name" "$(target_for "$name")" || return 1; done
}
remove_version() {
    dir=$1
    verify_version "$dir" || return 1
    for name in $verified_files bp bpd bpg .keelmux-files.sha256 .keelmux-archive.sha256 .keelmux-version; do rm "$dir/$name"; done
    # Extra state/auth/skills entries are never recursively removed.
    rmdir "$dir" 2>/dev/null || :
}
if [ "$uninstall" = 1 ]; then
    for name in keelmux keelmuxd keelmux-entry bp bpd bpg; do
        if [ "$links_owned" = 1 ] && owned_link "$bin_dir/$name" "$prefix/current/$(target_for "$name")"; then rm "$bin_dir/$name"; fi
    done
    if [ -L "$prefix/current" ] && [ "$(owner "$prefix/current")" = "$uid" ]; then
        current=$(readlink "$prefix/current")
        case "$current" in versions/*)
            case "${current#versions/}" in */*|..|.) ;; *) if verify_version "$prefix/$current"; then rm "$prefix/current"; fi;; esac;;
        esac
    fi
    for dir in "$prefix"/versions/*; do
        if [ -d "$dir" ] && [ ! -L "$dir" ]; then remove_version "$dir" || :; fi
    done
    if [ "$links_owned" = 1 ]; then rm "$prefix/.keelmux-links"; fi
    committed=1
    printf '%s\n' 'Keelmux owned commands and unmodified installed code removed. State, sessions, native auth and skills are preserved.'
    exit 0
fi

for tool in curl tar mktemp; do command -v "$tool" >/dev/null 2>&1 || die "Required tool is missing: $tool."; done
for name in keelmux keelmuxd keelmux-entry bp bpd bpg; do
    target="$prefix/current/$(target_for "$name")"
    if [ -e "$bin_dir/$name" ] || [ -L "$bin_dir/$name" ]; then
        [ "$links_owned" = 1 ] && owned_link "$bin_dir/$name" "$target" || die "Existing command is not owned by this installation: $name."
    fi
    found=$(command -v "$name" 2>/dev/null || :)
    if [ -n "$found" ]; then
        [ "$links_owned" = 1 ] && owned_link "$found" "$target" || die "Existing PATH command is not owned by this installation: $name."
    fi
done
if [ -e "$prefix/current" ] || [ -L "$prefix/current" ]; then
    [ -L "$prefix/current" ] && [ "$(owner "$prefix/current")" = "$uid" ] || die 'Current release pointer is not an owned symlink.'
    current=$(readlink "$prefix/current")
    case "$current" in versions/*) ;; *) die 'Current release pointer is outside owned versions.';; esac
    case "${current#versions/}" in */*|..|.) die 'Invalid current release pointer.';; esac
    verify_version "$prefix/$current" || die 'Current installed code was modified; preserve it and repair explicitly.'
fi
base=${KEELMUX_DOWNLOAD_BASE_URL:-https://github.com/seanheiney/keelmux/releases/download}
base=${base%/}
test_http=0
case "$base" in
    https://*) ;;
    http://localhost:*|http://127.0.0.1:*)
        [ "${KEELMUX_ALLOW_TEST_HTTP:-0}" = 1 ] || die 'HTTP is reserved for explicitly enabled loopback tests.'
        authority=${base#http://}
        authority=${authority%%/*}
        case "$authority" in localhost:[0-9]*|127.0.0.1:[0-9]*) ;; *) die 'Invalid loopback test URL.';; esac
        port=${authority#*:}
        printf '%s\n' "$port" | grep -Eq '^[0-9]+$' || die 'Invalid loopback port.'
        test_http=1;;
    *) die 'Downloads require HTTPS, or explicit loopback HTTP tests.';;
esac
case "$base" in *'@'*|*'?'*|*'#'*|*' '*|*'\'*) die 'Invalid download base URL.';; esac
download() {
    if [ "$test_http" = 1 ]; then
        curl -fsS --proto '=http' --proto-redir '=http' --max-redirs 0 --connect-timeout 10 --max-time 180 --max-filesize 268435456 "$1" -o "$2"
    else
        curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 180 --max-filesize 268435456 "$1" -o "$2"
    fi
}
stage=$(mktemp -d "$prefix/.stage.XXXXXX")
if [ -z "$version" ]; then
    [ "$test_http" = 0 ] || die 'Loopback tests must specify an exact version.'
    [ "$base" = https://github.com/seanheiney/keelmux/releases/download ] || die 'Custom download sources must specify an exact version.'
    download https://api.github.com/repos/seanheiney/keelmux/releases/latest "$stage/latest.json" || die 'Could not discover the latest release.'
    version=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$stage/latest.json")
fi
printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || die 'Version must be an exact vX.Y.Z release.'
case "$host" in Darwin) os=darwin;; Linux) os=linux;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) die 'Unsupported processor architecture.';; esac
if [ -n "${KEELMUX_TEST_OS:-}${KEELMUX_TEST_ARCH:-}" ]; then
    [ "$test_http" = 1 ] || die 'Platform overrides are reserved for loopback tests.'
    os=${KEELMUX_TEST_OS:-$os}
    arch=${KEELMUX_TEST_ARCH:-$arch}
fi
case "$os/$arch" in darwin/arm64|darwin/amd64|linux/arm64|linux/amd64) ;; *) die 'Unsupported release platform.';; esac
asset="keelmux_${version}_${os}_${arch}.tar.gz"
download "$base/$version/SHA256SUMS" "$stage/SHA256SUMS" || die 'Release checksum download failed.'
expected=$(awk -v name="$asset" '$2 == name && NF == 2 {print $1}' "$stage/SHA256SUMS")
[ "$(printf '%s' "$expected" | wc -c | tr -d ' ')" = 64 ] && printf '%s\n' "$expected" | grep -Eq '^[a-f0-9]{64}$' || die 'Release checksum is missing, duplicated or malformed.'
download "$base/$version/$asset" "$stage/archive.tar.gz" || die 'Release archive download failed.'
[ "$(sha "$stage/archive.tar.gz")" = "$expected" ] || die 'Release checksum does not match; current installation is preserved.'

tar -tzf "$stage/archive.tar.gz" > "$stage/names" || die 'Invalid release archive.'
tar -tvzf "$stage/archive.tar.gz" > "$stage/details" || die 'Invalid release archive headers.'
archive_count=$(wc -l < "$stage/names" | tr -d ' ')
case "$archive_count" in 9|10) ;; *) die 'Release archive must contain exactly nine legacy or ten native flat entries.';; esac
[ "$(sort -u "$stage/names" | wc -l | tr -d ' ')" = "$archive_count" ] || die 'Release archive contains duplicate entries.'
exec 3< "$stage/details"
while IFS= read -r name; do
    IFS= read -r detail <&3 || die 'Archive header count differs.'
    first=$(printf '%s' "$detail" | cut -c 1)
    last=$(printf '%s\n' "$detail" | awk '{print $NF}')
    case "$name" in
        keelmux|keelmuxd|keelmux-entry|keelmux-supervisor|LICENSE|README.md|RELEASE.json)
            [ "$first" = - ] && [ "$last" = "$name" ] || die 'Archive includes a nonregular code or metadata entry.';;
        bp|bpd|bpg)
            [ "$first" = l ] && [ "$last" = "$(target_for "$name")" ] || die 'Archive alias points outside its canonical binary.'
            [ "$(printf '%s\n' "$detail" | awk '{print $(NF-1)}')" = '->' ] && [ "$(printf '%s\n' "$detail" | awk '{print $(NF-2)}')" = "$name" ] || die 'Invalid archive alias header.';;
        *) die 'Archive contains an unreviewed path or entry.';;
    esac
done < "$stage/names"
exec 3<&-
mkdir "$stage/release"
tar --no-same-owner --no-same-permissions -xzf "$stage/archive.tar.gz" -C "$stage/release" || die 'Could not extract reviewed release.'
release_executables="keelmux keelmuxd keelmux-entry"
if [ "$archive_count" = 10 ]; then release_executables="$release_executables keelmux-supervisor"; fi
for name in $release_executables; do chmod 700 "$stage/release/$name"; done
for name in LICENSE README.md RELEASE.json; do chmod 600 "$stage/release/$name"; done
[ "$(wc -c < "$stage/release/RELEASE.json" | tr -d ' ')" -le 65536 ] || die 'Release manifest is too large.'
validate_manifest "$stage/release/RELEASE.json" "$stage/fields" || die 'Release manifest does not match the requested provenance.'
manifest_schema=$(awk -F= '$1 == "/schema" {print $2}' "$stage/fields")
case "$manifest_schema:$archive_count" in 1:9|2:10) ;; *) die 'Release archive entries differ from the declared manifest schema.';; esac
for name in $release_executables LICENSE README.md; do
    wanted=$(awk -F= -v field="/files/$name" '$1 == field {print $2}' "$stage/fields")
    [ "$(sha "$stage/release/$name")" = "$wanted" ] || die 'Release file digest differs from its manifest.'
done
manifest_commit=$(awk -F= '$1 == "/commit" {print $2}' "$stage/fields")
manifest_built=$(awk -F= '$1 == "/built_at" {print $2}' "$stage/fields")
for name in $release_executables; do
    loaded=$("$stage/release/$name" --version 2> "$stage/version.stderr") || die 'Verified release executable could not report its version.'
    [ "$loaded" = "Keelmux $version (commit $manifest_commit, protocol bp/1)" ] || die 'Loaded release executable differs from its requested version or commit.'
done
printf 'keelmux-installed-v%s\n' "$manifest_schema" > "$stage/release/.keelmux-version"
printf '%s\n' "$expected" > "$stage/release/.keelmux-archive.sha256"
for name in $release_executables LICENSE README.md RELEASE.json; do printf '%s  %s\n' "$(sha "$stage/release/$name")" "$name"; done > "$stage/release/.keelmux-files.sha256"
dir="$prefix/versions/${version}_${os}_${arch}"
if [ -e "$dir" ] || [ -L "$dir" ]; then
    verify_version "$dir" && [ "$(cat "$dir/.keelmux-archive.sha256")" = "$expected" ] || die 'An existing immutable version differs; refusing to overwrite it.'
else
    mv "$stage/release" "$dir"
    new_version=$dir
fi
for name in keelmux keelmuxd keelmux-entry bp bpd bpg; do
    target="$prefix/current/$(target_for "$name")"
    if [ ! -e "$bin_dir/$name" ] && [ ! -L "$bin_dir/$name" ]; then
        ln -s "$target" "$bin_dir/$name" || die 'Command changed during installation; rolling back.'
        created_links="$created_links $name"
    fi
done
if [ "$links_owned" = 0 ]; then
    (set -C; printf '%s\n' "$links_receipt" > "$prefix/.keelmux-links") || die 'Installed command ownership changed; rolling back.'
    created_receipt=1
fi
ln -s "versions/${version}_${os}_${arch}" "$stage/current"
if [ "$stat_mode" = bsd ]; then mv -fh "$stage/current" "$prefix/current"; else mv -fT "$stage/current" "$prefix/current"; fi
committed=1
printf 'Keelmux %s installed. Commands: %s\n' "$version" "$bin_dir"
case ":$PATH:" in *":$bin_dir:"*) ;; *) printf 'Add to PATH: export PATH="%s:$PATH"\n' "$bin_dir";; esac
if [ "$manifest_schema" = 1 ] && ! command -v tmux >/dev/null 2>&1; then printf '%s\n' 'This legacy release uses tmux; native supervision is included in newer releases.'; fi
