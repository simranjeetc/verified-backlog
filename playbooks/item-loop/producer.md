# Prepare the producer prompt

Use the CLI to combine the current role packet with the versioned instructions:

```sh
"$BL" packet A1 --role producer --render --workspace "$WORKSPACE" --output-dir "$OUTPUT_DIR" > /dev/null
```

Both directories must exist. Use the producer worktree for producer work and the
repository being verified for checks/verifier reading. Keep the output directory
outside `.backlog/`, preferably outside the repository. The command writes the
complete prompt to stdout and to `A1-aN-producer-prompt.md` in that directory.
Dispatch that file; do not retype the instructions or paste additional role packets.
Stdout is suppressed above to keep the generated worker prompt out of supervisor context.

Canonical instructions: [producer template](../../scripts/cmd/bl/prompts/v1/producer.md.tmpl),
embedded in the binary. Plain `bl packet A1 --role producer` remains available.

Prepare this prompt independently of the check writer. The producer receives intent
and scope, never frozen checks or producer claims.
