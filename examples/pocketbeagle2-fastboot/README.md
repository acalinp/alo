# PocketBeagle 2 fastboot bring-up

This example supplies only a goal, hardware capabilities, fixture preparation,
and an observable success check. It contains no boot sequence or artifact names.

Every attempt starts the trusted `capture` command, resets the board, and runs
the agent-created `automation/.alo/run` in a fresh container. `capture` records
serial output across both reset and replay as `capture.log`; prepare also records
USB facts. The host verifier accepts only a visible fastboot device. A persistent
agent workshop receives all failed prepare, replay, verifier, and capture logs.
The serial device stays host-side so replay cannot compete with capture for
bytes.

Run `alo auth set openrouter`, adjust `parameters.serial` and `AIL_GPIOCTL` when
needed, then run:

```bash
alo run examples/pocketbeagle2-fastboot/alo.yaml
```

The candidate intentionally starts without `.alo/run`; discovering and
persisting the complete reproducible solution is the agent's job.

`parameters.serial` must identify the board's debug UART, not the GPIO reset
controller. Prefer a stable path from `/dev/serial/by-id/`; if `capture.log`
contains replies such as `OK GPIO ...`, the wrong serial device is selected and
capture may consume responses needed by the reset tool.
