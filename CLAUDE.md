Think before coding. State your assumptions out loud. If the request is ambiguous, ask. If a simpler approach exists, push back. Stop when you are confused, name what is unclear, do not just pick one interpretation and run.
Simplicity first. Write the minimum code that solves the problem. No speculative abstractions. No flexibility nobody asked for. The test: would a senior engineer call this overcomplicated.
Surgical changes. Touch only what the task requires. Do not improve neighboring code. Do not refactor what is not broken. Every changed line should trace back to the request.
Goal-driven execution. Turn vague instructions into verifiable targets before writing a line. “Add validation” becomes “write tests for invalid inputs, then make them pass.”

# Building

Run `scripts/fetch-themes.sh` before `go build` to refresh `static/themes/`
and `static/assets/fonts/` from github.com/DrEVILish/ftl-themes (requires
`gh` authenticated). Skippable on a routine rebuild once those directories
already exist - it's a "pick up new/updated themes" step, not a dependency
`go build` itself needs. Never edit files under `static/themes/` directly:
they're that repo's compiled output, and any change to a theme goes through
a pull request there, approved by its owner.

```
scripts/fetch-themes.sh
go build -o bin/playlist-lab-server ./cmd/server
```
