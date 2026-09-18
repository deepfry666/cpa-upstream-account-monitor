#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  printf 'usage: %s amd64|arm64 path/to/upstream-monitor.so\n' "$0" >&2
  exit 2
fi

arch=$1
shared_library=$2
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

resolve_executable() {
  local candidate=$1
  if [[ "$candidate" == */* ]]; then
    if [[ -x "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
    return 1
  fi
  command -v "$candidate"
}

if [[ ! -f "$shared_library" ]]; then
  printf 'shared library not found: %s\n' "$shared_library" >&2
  exit 2
fi

case "$arch" in
  amd64)
    cc -O2 -Wall -Wextra -o "$work_dir/plugin_load_check" \
      "$script_dir/plugin_load_check.c" -ldl
    timeout 60s "$work_dir/plugin_load_check" "$shared_library"
    ;;
  arm64)
    cc_arm64=$(resolve_executable "${CC_ARM64:-aarch64-linux-gnu-gcc}") || {
      printf 'ARM64 C compiler not found; set CC_ARM64\n' >&2
      exit 2
    }
    qemu_arm64=$(resolve_executable "${QEMU_ARM64:-qemu-aarch64-static}" || true)
    if [[ -z "$qemu_arm64" ]]; then
      qemu_arm64=$(resolve_executable qemu-aarch64 || true)
    fi
    if [[ -z "$qemu_arm64" && -x /usr/bin/qemu-aarch64-static ]]; then
      qemu_arm64=/usr/bin/qemu-aarch64-static
    fi
    if [[ -z "$qemu_arm64" ]] && command -v apt-get >/dev/null 2>&1 && command -v dpkg-deb >/dev/null 2>&1; then
      mkdir -p "$work_dir/qemu-package" "$work_dir/qemu-root"
      (
        cd "$work_dir/qemu-package"
        apt-get download qemu-user-static >/dev/null
      )
      qemu_deb=$(find "$work_dir/qemu-package" -maxdepth 1 -name 'qemu-user-static_*.deb' -print -quit)
      if [[ -n "$qemu_deb" ]]; then
        dpkg-deb -x "$qemu_deb" "$work_dir/qemu-root"
        qemu_arm64="$work_dir/qemu-root/usr/bin/qemu-aarch64-static"
      fi
    fi
    if [[ -z "$qemu_arm64" || ! -x "$qemu_arm64" ]]; then
      printf 'ARM64 qemu loader not found; set QEMU_ARM64\n' >&2
      exit 2
    fi
    if [[ ! -d /usr/aarch64-linux-gnu ]]; then
      printf 'ARM64 sysroot /usr/aarch64-linux-gnu not found\n' >&2
      exit 2
    fi
    "$cc_arm64" -O2 -Wall -Wextra -o "$work_dir/plugin_load_check" \
      "$script_dir/plugin_load_check.c" -ldl
    timeout 60s "$qemu_arm64" -L /usr/aarch64-linux-gnu \
      "$work_dir/plugin_load_check" "$shared_library"
    ;;
  *)
    printf 'unsupported architecture: %s\n' "$arch" >&2
    exit 2
    ;;
esac
