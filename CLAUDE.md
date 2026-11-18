Think before coding. State your assumptions out loud. If the request is ambiguous, ask. If a simpler approach exists, push back. Stop when you are confused, name what is unclear, do not just pick one interpretation and run.
Simplicity first. Write the minimum code that solves the problem. No speculative abstractions. No flexibility nobody asked for. The test: would a senior engineer call this overcomplicated.
Surgical changes. Touch only what the task requires. Do not improve neighboring code. Do not refactor what is not broken. Every changed line should trace back to the request.
Goal-driven execution. Turn vague instructions into verifiable targets before writing a line. “Add validation” becomes “write tests for invalid inputs, then make them pass.”

# Building

Build with `go build -o bin/playlist-lab-server ./cmd/server`. The checked-in
theme bundles and fonts are embedded in the binary.

To update ftl-themes, set the upstream Git revision in
`scripts/fetch-themes.sh` and run it. The script validates the manifest and
referenced assets before replacing the embedded copies. Pass an ftl-themes
Git checkout to read the pinned revision locally. Make styling changes in the
upstream repository, then refresh the vendor copy here.
