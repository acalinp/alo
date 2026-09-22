# Alo Repository Notes

## Verify Changes

- This module requires Go 1.26 (`go.mod`). Run `go test ./...` and `go vet ./...`; build the CLI with `go build -o alo ./cmd/alo`.
- Run one test with `go test ./... -run '^TestName$'`.
- `TestRootlessPodmanReplayAndPersistentWorkshop` is skipped unless `ALO_PODMAN_TEST_IMAGE` names a local image containing `sh`. Run it explicitly with `ALO_PODMAN_TEST_IMAGE=<image> go test ./... -run '^TestRootlessPodmanReplayAndPersistentWorkshop$'`; it requires rootless Podman 4.9+.

## Execution Boundaries

- Preserve the loop in `internal/run/run.go`: trusted capture starts, trusted prepare, fresh candidate replay, capture stops, trusted verify, then repair only after rejection. Success requires both replay exit 0 and verifier exit 0.
- `.alo/run` is the candidate's executable replay contract. It runs in a fresh credential-free container; only the candidate is writable, references are read-only under `/refs/<name>`, and configured parameters/references arrive as `ALO_PARAMETER_*`/`ALO_REFERENCE_*`.
- The repair workshop persists across turns (`candidate`, run-scoped `cache`, `session`, and container root), while replay does not. Do not let workshop-only state satisfy verification.
- `capture`, `prepare`, and `verify` are trusted host executables and must remain outside the candidate. Verifier exit 1 means candidate rejection; any other nonzero exit or timeout is infrastructure failure.
- Keep `capture` a single diagnostics-only foreground process with Alo-owned readiness and lifetime; do not add named captures, phase selection, restart policies, or dependencies.
- Keep Alo an iteration coordinator, not a workflow engine: do not add action DAGs, watcher plugins, user-selected task images, or model/file-count declarations of success.

## Change Hotspots

- `internal/config/config.go` uses strict YAML decoding and resolves relative candidate, reference, prepare, and verify paths against the config file directory. Preserve overlap, symlink, executable, device, and reserved `ALO_` environment checks.
- `internal/podman/podman.go` hashes assets exposed by `internal/agent/assets.go`. Changing `internal/agent/{Containerfile,run-agent,instructions.md}` changes the managed image tag and causes a lazy rebuild.
- `internal/agent/run-agent` emits a private line-delimited JSON protocol consumed by `internal/podman/agent_stream.go`. Status records drive TTY-only progress; only human output/diagnostics belong in retained logs, with passed credentials redacted.
- Run state defaults to `$ALO_STATE_DIR`, then `$XDG_STATE_HOME/alo`, then `~/.local/state/alo`. Resumes use the stored config and pinned image ID; preserve phase persistence and run locking.
- Candidate `.alo/outputs.json` uses candidate-relative regular-file paths. Verifier output written to `$ALO_RESULT` uses absolute paths; all outputs must resolve inside the candidate and are hashed after successful verification.
- OpenRouter credentials default to the OS keyring (`alo auth`), enter only the workshop, and must stay out of stored config and replay. Keep `agent.pass_env` for explicit custom-provider credentials.
