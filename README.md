# Alo

Alo is a predictable agent iteration loop:

```text
trusted verifier -> failure evidence -> containerized agent -> new candidate -> verifier
```

The agent may edit source and build durable artifacts inside rootless Podman.
Only the verifier can declare success. Candidate directories persist between
turns; evidence and references are read-only; container scratch is disposable.
Alo lazily builds and versions its own general-purpose agent toolbox—the loop
author does not supply an image or agent command.

The first worked example asks an agent to bring a PocketBeagle 2 from its reset
state to fastboot. The fixed verifier checks only that endpoint; after failure
it captures serial/USB evidence and resets the board. The agent gets explicit
hardware access and must discover the procedure and build whatever it needs.

## Commands

```bash
go build -o alo ./cmd/alo

./alo validate [FILE]
./alo run [FILE]
./alo resume RUN_ID
./alo logs RUN_ID
```

Configuration declares a goal, named candidate and reference directories, one
verifier command, and an attempt limit. Alo uses rootless Podman 4.9 or newer;
its managed toolbox base is fetched only when that toolbox must be built.
The bundled agent currently uses Pi through OpenRouter and reads
`OPENROUTER_API_KEY` from the host environment.

## Design rules for contributors and agents

- Keep Alo an iteration coordinator, not a workflow engine.
- Make verifiers describe observable success and fixture reset, not a solution.
- Let the agent choose builds and actions within explicitly granted devices.
- Keep model providers, planning, and tools in Alo's managed agent toolbox.
- Add no sandbox abstraction until a second backend is actually required.
- Never let the agent edit the verifier or evidence.
- Never let an agent response declare success.
- Do not add model-based success judgments, action DAGs, watcher plugins, or
  user-selectable agent images to the core.
- Prefer one behavioral test over many tests of fields, formatting, or mocks.

See [`examples/pocketbeagle2-fastboot`](examples/pocketbeagle2-fastboot) for the
primary architecture example.
