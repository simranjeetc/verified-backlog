# Prepare the independent check writer prompt

Use the CLI to combine the current role packet with the versioned instructions:

```sh
"$BL" packet A1 --role checks --render --workspace "$WORKSPACE" --output-dir "$OUTPUT_DIR" > /dev/null
```

Both directories must exist. Use the producer worktree for producer work and the
repository being verified for checks/verifier reading. Keep the output directory
outside `.backlog/`, preferably outside the repository. The command writes the
complete prompt to stdout and to `A1-aN-checks-prompt.md` in that directory.
Dispatch that file; do not retype the instructions or paste additional role packets.
Stdout is suppressed above to keep the generated worker prompt out of supervisor context.

Canonical instructions: [checks template](../../scripts/cmd/bl/prompts/v1/checks.md.tmpl),
embedded in the binary. Plain `bl packet A1 --role checks` remains available.

Prepare this prompt before reading producer claims. Record the generated check list
and test file before recording claims; keep the producer blind to those checks.
