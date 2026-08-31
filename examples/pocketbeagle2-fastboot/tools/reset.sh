#!/usr/bin/env bash
set -euo pipefail

gpioctl=${AIL_GPIOCTL:-gpioctl}
command -v "$gpioctl" >/dev/null || {
	echo "gpio controller is unavailable: $gpioctl" >&2
	exit 1
}
"$gpioctl" pulse
