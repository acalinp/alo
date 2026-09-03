# PocketBeagle 2 fastboot bring-up

This example supplies only a goal, hardware capabilities, fixture preparation,
and an observable success check. It contains no boot sequence or artifact names.

Every attempt resets the board and captures USB/serial facts. Alo then runs the
agent-created `automation/.alo/run` in a fresh container with USB and serial
access. The host verifier accepts only a visible fastboot device. A persistent
agent workshop receives all failed prepare, replay, verifier, and serial logs.

Run `alo auth set openrouter`, adjust `parameters.serial` and `AIL_GPIOCTL` when
needed, then run:

```bash
alo run examples/pocketbeagle2-fastboot/alo.yaml
```

The candidate intentionally starts without `.alo/run`; discovering and
persisting the complete reproducible solution is the agent's job.
