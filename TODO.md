# TODO

- [x] Make the agent provider, model, thinking level, and credential environment
  configurable under `agent`, while Alo continues to own and version the toolbox
  and runner. Remove the implicit OpenRouter/API-key coupling and retain the
  resolved choices with each run so resumes remain predictable.

- [x] Show a TTY-only, live-updating agent progress line with elapsed and silent
  time. Clear it around real agent output, keep it out of retained logs, and
  disable it automatically for redirected or non-interactive output.
