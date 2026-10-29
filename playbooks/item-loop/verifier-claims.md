# Prepare the claims verifier prompt

Use the CLI to combine the current role packet with the versioned instructions:

```sh
"$BL" packet A1 --role verifier --render --workspace "$WORKSPACE" --output-dir "$OUTPUT_DIR" > /dev/null
```

Both directories must exist. Use the producer worktree for producer work and the
repository being verified for checks/verifier reading. Keep the output directory
outside `.backlog/`, preferably outside the repository. The command writes the
complete prompt to stdout and to `A1-aN-verifier-prompt.md` in that directory.
Dispatch that file; do not retype the instructions or paste additional role packets.
Stdout is suppressed above to keep the generated worker prompt out of supervisor context.

Canonical instructions: [verifier template](../../scripts/cmd/bl/prompts/v1/verifier.md.tmpl),
embedded in the binary. Plain `bl packet A1 --role verifier` remains available.

Generate only after the checks, claims and run are recorded. This records the existing
verifier window, so prepare other prompts first and keep the backlog unchanged until
the verdict is recorded. Optional `--required-cases-file "$CASES"` supplies explicit
supervisor-requested cases; it is accepted only for this role. It does not replace the
independent verifier’s checks.
