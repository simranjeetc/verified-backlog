# Running the verifier in another harness

A verifier on the same model as the producer shares its blind spots. A fresh context separates it from the producer's *reasoning*; another model, or another harness, separates it from the whole *family's* reasoning. The same brief against a second model is a second opinion, and two independent verdicts agreeing is the strongest signal this loop can produce.

Everything here is harness-specific and none of it is required. The invariant is what matters; the commands are one way to get it.

## The invariant

Whatever the command, the verifier must:

1. Start with a fresh context, with no transcript from the producer and no resumed session.
2. Run a different model family where one is reachable, not merely a different session of the same model.
3. Receive the brief verbatim. Write the brief to a file and hand over the file, so quoting cannot alter it.
4. Write nothing to the repo: tracked files untouched, no untracked leftovers. Compare `git status --porcelain` before and after, and point the harness at a copy of any real state.
5. Return the verdict verbatim, plus the model or harness that produced it. Write that label into the record: a single-family verdict is a weaker claim than a corroborated one, and the record should show which it is.

## Invocations

Non-interactive prompts, with the permission-bypass flags each harness documents. The model flag takes whatever ids that harness accepts.

| harness | non-interactive run | model | permissions bypassed |
|---|---|---|---|
| Claude Code | `claude -p "$(cat <brief>)"` | `--model <model-id>` | `--dangerously-skip-permissions`, or `--permission-mode bypassPermissions` |
| OpenCode | `opencode run "$(cat <brief>)"` | `--model <provider>/<model>#<variant>` | `--auto` |
| any other | `<harness> <its non-interactive flag> "$(cat <brief>)"` | read `<harness> --help` | read `<harness> --help` |

The first two are the commands verified on the machine this skill was written on. The others are deliberately blank rather than guessed: quoting a flag from memory is how a documented command ends up not existing.

## Caveats that cost something to learn

- **A bypass mode lets the verifier write.** Keep `Do NOT edit tracked files` in the brief and check `git status` afterwards. Prefer a sandboxed or allow-listed mode when the harness offers one, and never point a bypassed run at production data or a live deploy.
- **Budgets are minutes, not seconds.** A strong model re-deriving a result takes a while. Give the command a generous timeout; verification is the one step where waiting pays.
- **A bypassed run may not be resumable.** Where the harness cannot continue a session, run the two dispatches as two fresh verifiers rather than one continued one.
- **Cross-vendor beats cross-version.** Two models from different vendors disagree more usefully than two versions of one model. If the choice is one strong model from your own family or one moderate model from another, the other family is usually the better verifier.
