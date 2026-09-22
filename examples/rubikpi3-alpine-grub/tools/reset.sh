#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
power_tool="$script_dir/tapo-power"

[[ -x "$power_tool" ]] || {
	echo "power controller is unavailable or is not executable: $power_tool" >&2
	exit 1
}

"$power_tool" off
sleep 3
"$power_tool" on
