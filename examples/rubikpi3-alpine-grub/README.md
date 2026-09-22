# RUBIK Pi 3 Alpine one-time GRUB boot

This example runs Alo on a Raspberry Pi 4. The Pi 4 connects to the RUBIK Pi 3
through its USB data port and through a USB serial adapter. The example asks Alo
to boot an Alpine user space without replacing the installed operating system
or its default GRUB entry. Each attempt resets the board to the normal UFS boot
path. The candidate uses root ADB to stage boot files. It uses the debug serial
port to control GRUB for that boot only.

The verifier opens the serial port after candidate replay. It sends a new shell
command with a random value. It accepts the attempt only when the live target
returns that value, an Alpine release, the `aarch64` architecture, and the
`alo_alpine=1` kernel argument. Thus, a candidate log cannot satisfy the check.

## Fixture requirements

- Flash a RUBIK Pi Debian image that has GRUB and root ADB access.
- Connect the RUBIK Pi USB data port to the Raspberry Pi 4.
- Connect the debug UART to a Raspberry Pi 4 USB serial adapter.
- Use a 64-bit Raspberry Pi operating system.
- Install `adb` and `usbutils` on the Raspberry Pi 4.
- Confirm that the Raspberry Pi user can use ADB and the serial device.
- Put the executable `tapo-power` tool in the example `tools` directory.
- Keep a recoverable copy of the installed UFS image.

The example defaults to `/dev/ttyACM0`. If your serial port is different,
change both `parameters.serial` and the matching `sandbox.devices` entry in
`alo.yaml`. If more than one ADB target is present, put its serial number in
`parameters.adb_serial`.

The prepare step uses `tools/tapo-power` to power off the RUBIK Pi. It waits
three seconds and powers on the board. The tool uses its default host. This full
power cycle also recovers the fixture after a kernel hang. Prepare then waits up
to 90 seconds for the ADB device. Candidate replay starts only after ADB is
available.

The candidate directory is initially empty. The agent must create an executable
`.alo/run` and retain all files that a fresh replay needs. The replay can read
and write the serial port. It should include useful serial output in its own log
when an attempt fails.

Run the example as follows:

```bash
alo auth set openrouter
alo run examples/rubikpi3-alpine-grub/alo.yaml
```

A failed experimental kernel does not change the default GRUB entry. After a
reset, the installed operating system starts again. EDL remains the last-resort
recovery path if the UFS boot files are damaged.
