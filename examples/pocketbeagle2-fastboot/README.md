# PocketBeagle 2 fastboot bring-up

This is Alo's primary example. The agent receives a rootless container, USB and
serial access, an empty persistent candidate, and evidence from earlier turns.
It must discover how to reach fastboot and create whatever automation and
artifacts make that result repeatable. The fixed host verifier knows no boot
recipe: it checks for fastboot and, on failure, captures evidence and resets the
board for the next turn.

Set `OPENROUTER_API_KEY`, adjust `parameters.serial` and `AIL_GPIOCTL` when
needed, then run. Alo builds its own agent toolbox on first use:

```bash
alo run examples/pocketbeagle2-fastboot/alo.yaml
```

The example intentionally starts without a recipe. Large downloads, source
trees, and build intermediates can remain in Alo's persistent run cache.
