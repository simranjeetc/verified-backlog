# Playbook: irreversible op

For a change that rewrites real state: a migration, a re-key, a backfill, a deletion, a deploy. The failure mode is not "the code was wrong", it is "the code was wrong on the only copy".

## Step 1: Find out who else writes this state

Learn what else writes the same files and when, before touching anything.

- A scheduler, cron job, CI job, or deploy hook that writes, stages or publishes this state.
- A publish step that stages a directory. **This is the trap:** a backup written inside a staged directory becomes part of the next automatic commit. Name the staged path, put the backup outside it, and check that by reading the two paths back.
- Another agent or session with the files open.

Completion: you can name every writer, and the backup path is provably outside what any of them stages.

## Step 2: Prove it on copies, including a full rehearsal

Work only on copies. Run the operation end to end against a copy of the real state, then run the real pipeline over that copy with every external side effect disabled — network sends, publishes, notifications, emails — because the change is not proven until the thing that consumes the state has read it, and the pipeline is where the parts you did not touch get exercised.

State the invariants the operation must hold, and check each as a command:

- **No record lost.** Every input key maps to an output, reconstructed from the original file. A cover-set check, not a count.
- **No field corrupted.** Timestamps sane, terminal states not regressed, no field emptied.
- **Idempotent.** A second run changes nothing, byte for byte.
- **Backup first.** Written before any mutation; touches nothing else. If that write fails, the whole operation aborts with the state untried. Diff the backup against the original: byte-identical.
- **The backup restores.** Restore from it onto a copy and diff against the original. An untested backup is a file, not a backup. For a deploy, name the rollback command here instead.
- **The reads agree with the writes.** If code later reads this state, check that it interprets what you just wrote. A rewrite can produce a value the reader cannot see, and that gap surfaces weeks later as a silent merge or a lost row.

Where a copy or a dry run is not available — a deploy, a third-party API — say so plainly, use staging or a skipped-publish flag, and carry the untested remainder to Step 4 as a named risk rather than hoping it does not matter.

Completion: every invariant above is a command with output run against a copy, and every step of the pipeline replay is accounted for, including the ones you disabled.

## Step 3: Two verifiers, not one

A rewrite of real state gets two independent verifiers, chosen for different angles. Give the first the semantics: does it do what the spec says. Give the second the data: can it lose or corrupt anything. Tell the second that another verifier exists, so it does not assume coverage.

Both work on copies and never touch the real state. Ask each for the strongest counterexample it found, and whether that counterexample belongs to this item or to another.

Completion: two verdicts, and every counterexample classified as this item's or filed.

## Step 4: Hold the trigger

The producer and the verifiers stop at the copy. Applying to live is one explicit action by the human, and it is the last thing you do. Present the change, the invariants, the backup path and how to restore from it, what the next automatic run will do with the new state, and any risk you could not test. Then ask.

A verification that ran on copies is not permission to run on the original. Keep those separate in the report, or the distinction quietly disappears.

Completion: the human has said go, or the item is left verified-but-unapplied — `bl defer <id> --reason "verified, awaiting human go"` records that state instead of leaving the pointer ambiguous.
