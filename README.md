# Alo

Alo is a predictable agent iteration loop:

```text
trusted prepare -> fresh candidate replay -> trusted verify
                                              |
                                    evidence on failure
                                              v
                                persistent agent workshop
                                              |
                                      repaired candidate
```

The candidate owns an executable `.alo/run`. Alo resets or prepares the fixture,
runs that entrypoint in a fresh rootless Podman container without model secrets,
then lets a trusted verifier judge the observable result. Only a successful
replay followed by a successful verifier can pass.

On failure, an autonomous agent repairs the candidate inside a separate,
run-scoped workshop. Its root filesystem, candidate, cache, and model session
persist across turns, so it can install tools, build artifacts, and experiment.
The next prepare step resets fixture state; workshop activity cannot cause
success unless the candidate's `.alo/run` can repeat the relevant result.

An optional trusted `capture` command can record fixture output across both
prepare and candidate replay. It must signal readiness through
`$ALO_CAPTURE_READY_FD`, stay in the foreground, and keep running until Alo
stops it before verification. Alo stores its output as `capture.log`.

```yaml
capture:
  command: [./capture]
```

For example, a serial capture opens its source, signals readiness, then remains
in the foreground:

```bash
serial=${ALO_PARAMETER_SERIAL:?}
ready_fd=${ALO_CAPTURE_READY_FD:?}
exec 4<"$serial"
printf . >&"$ready_fd"
exec {ready_fd}>&-
cat <&4
```

Capture receives configured `ALO_PARAMETER_*` values and `ALO_EVIDENCE_DIR`,
but not the candidate, references, result path, or credential passthrough.

The first repair can have a larger exploration budget while later repairs stay
short and focused:

```yaml
agent:
  bootstrap_timeout: 30m
  timeout: 5m
```

If `bootstrap_timeout` is omitted, it uses `timeout`. If both are omitted, agent
turns have no deadline. A resume during the first repair keeps the bootstrap
budget until that repair completes.

## Commands

```bash
go build -o alo ./cmd/alo

./alo auth set openrouter
./alo init
./alo validate [FILE]
./alo try prepare [FILE]
./alo try verify [FILE]
./alo run [FILE]
./alo resume RUN_ID
./alo logs RUN_ID [FILE]
```

`alo auth set openrouter` reads the API key without displaying it and stores it
in the operating system keyring. Use `alo auth status openrouter` to check it or
`alo auth delete openrouter` to remove it. The key enters only the agent
workshop; it is not written to `alo.yaml`, retained run configuration, or clean
candidate replay. `agent.pass_env` remains available for custom providers and
explicit environment-based credentials.
On Linux, this uses the desktop Secret Service provided by tools such as GNOME
Keyring or KWallet. On a headless machine where that collection cannot unlock,
use `alo auth set openrouter --file`; Alo writes a mode-`0600` credential under
the user configuration directory and uses it only because you created it
explicitly.

`alo logs RUN_ID` lists retained attempt files. Pass one listed path, such as
`alo logs RUN_ID 0001/prepare.log`, to print its contents.

`alo init` creates a starter `alo.yaml`, executable `prepare` and `verify`
scripts, and a candidate directory. Use `alo try prepare` or `alo try verify`
to run one trusted phase and inspect the evidence files it produced before
starting the full loop.

Alo owns and lazily builds its Pi-based agent toolbox; loop authors do not
provide an image or agent command. `agent` selects the provider, model, thinking
level, privacy policy, and credential environment names. For OpenRouter,
`zdr: true` restricts Pi to zero-data-retention endpoints. Only explicitly
named variables enter the workshop, and the resolved selection is retained
with the run. Candidate artifacts may be published through `.alo/outputs.json`;
Alo confines them to the candidate and records their hashes.

## Contributor rules

- Keep Alo an iteration coordinator, not a workflow engine.
- Let `prepare` establish fixture state and collect facts, never prescribe a solution.
- Keep `verify` a trusted observation of the goal; do not execute candidate code there.
- Run candidate code only through the fresh, credential-free replay container.
- Keep agent exploration in the persistent workshop and require `.alo/run` to reproduce it.
- Never let an agent response, file count, or model judgment declare success.
- Add no action DAGs, watcher plugins, or user-selectable task images to the core.
- Prefer a few state-machine and sandbox-boundary tests over field-by-field tests.

The primary example asks Alo to discover how to bring up fastboot on a
PocketBeagle 2 without embedding a boot recipe:
[`examples/pocketbeagle2-fastboot`](examples/pocketbeagle2-fastboot).

The RUBIK Pi example asks Alo to make a recoverable, one-time GRUB boot into an
Alpine user space:
[`examples/rubikpi3-alpine-grub`](examples/rubikpi3-alpine-grub).
