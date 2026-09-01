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

## Commands

```bash
go build -o alo ./cmd/alo

./alo validate [FILE]
./alo run [FILE]
./alo resume RUN_ID
./alo logs RUN_ID
```

Alo owns and lazily builds its agent toolbox; loop authors do not provide an
image or agent command. The bundled agent currently uses Pi through OpenRouter
and reads `OPENROUTER_API_KEY`. Candidate artifacts may be published through
`.alo/outputs.json`; Alo confines them to the candidate and records their hashes.

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
