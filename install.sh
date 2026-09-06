#!/bin/sh
# Install an ASH GitHub release. Override ASH_VERSION or ASH_INSTALL_DIR as needed.
set -eu

main() {
    repository=https://github.com/0ctacity/ash
    for utility in curl tar mktemp; do
        command -v "$utility" >/dev/null 2>&1 || { echo "Required command missing: $utility" >&2; exit 1; }
    done
    case "$(uname -s)/$(uname -m)" in
        Linux/x86_64) target=linux-amd64 ;;
        Linux/aarch64|Linux/arm64) target=linux-arm64 ;;
        Darwin/arm64) target=macos-arm64 ;;
        *) echo 'Unsupported platform. See GitHub Releases for available binaries (including Windows).' >&2; exit 1 ;;
    esac
    if command -v sha256sum >/dev/null 2>&1; then
        checksum=sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        checksum=shasum
    else
        echo 'Required command missing: sha256sum or shasum' >&2
        exit 1
    fi
    version=${ASH_VERSION:-}
    if [ -z "$version" ]; then
        latest=$(curl --proto '=https' --tlsv1.2 -fsSL -o /dev/null -w '%{url_effective}' "$repository/releases/latest")
        version=${latest##*/}
    fi
    # Reject unsafe tag characters and require a version-shaped release tag.
    if ! printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'; then
        echo "Invalid release version: $version" >&2
        exit 1
    fi
    destination=${ASH_INSTALL_DIR:-"$HOME/.local/bin"}
    archive=ash-$version-$target.tar.gz
    temporary=$(mktemp -d)
    staged=''
    trap 'rm -rf "$temporary"; if [ -n "$staged" ]; then rm -f "$staged"; fi' EXIT
    trap 'exit 1' HUP INT TERM
    echo "Downloading ASH $version for $target..."
    base=$repository/releases/download/$version
    curl --proto '=https' --tlsv1.2 -fsSL -o "$temporary/$archive" "$base/$archive"
    curl --proto '=https' --tlsv1.2 -fsSL -o "$temporary/SHA256SUMS.txt" "$base/SHA256SUMS.txt"
    expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$temporary/SHA256SUMS.txt")
    if ! printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$' || [ "${#expected}" -ne 64 ]; then
        echo 'Missing or ambiguous archive checksum' >&2
        exit 1
    fi
    if [ "$checksum" = sha256sum ]; then
        actual=$(sha256sum "$temporary/$archive")
    else
        actual=$(shasum -a 256 "$temporary/$archive")
    fi
    actual=${actual%% *}
    [ "$actual" = "$expected" ] || { echo 'Archive checksum mismatch' >&2; exit 1; }
    tar -xzf "$temporary/$archive" -C "$temporary" ash
    [ -f "$temporary/ash" ] && [ ! -L "$temporary/ash" ] || { echo 'Archive does not contain a regular ash executable' >&2; exit 1; }
    mkdir -p "$destination"
    staged=$(mktemp "$destination/.ash-install.XXXXXX")
    cp "$temporary/ash" "$staged"
    chmod 755 "$staged"
    mv -f "$staged" "$destination/ash"
    staged=''
    echo "Installed ASH $version to $destination/ash"
    case ":$PATH:" in
        *":$destination:"*) ;;
        *) printf 'Add this directory to your PATH: %s\n' "$destination" ;;
    esac
}

# Keep execution at the end so a truncated download cannot start installation.
main "$@"
