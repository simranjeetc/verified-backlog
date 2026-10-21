# Playbook: design doc

For a decision other work will be built on: the shape of a key, a wire format, a state machine, a migration policy. Being wrong here is expensive, so the artifact is a document and the verifier re-implements it rather than reads it.

A design doc is one backlog item, and it closes through `item-loop.md` Step 5 like any other.

## Step 1: Write rules a stranger can implement

The document is the deliverable, and its test is that a third party can implement it from the text alone. So it needs:

- **Numbered rules**, each one decidable.
- **Worked examples in both directions**: the inputs that must produce the same outcome, and the inputs that must produce different ones. Use real values where real data exists, and redact or hash anything identifying before it goes into a committed file.
- **Numbers computed with the document's own rules.** This is the failure that hides best. A headline figure produced with one step disabled makes the doc contradict itself, and the contradiction survives review because the number looks authoritative.
- **Precedence, stated, wherever two rule sets can both match.** Say which wins and why, in the rule text rather than in the implementation.
- **Sensitivities labelled as sensitivities.** A figure a reader might mistake for the headline belongs under a heading that says it is not the headline.

Completion: you cannot find a rule whose outcome two readers could disagree on.

## Step 2: Dispatch a verifier that re-implements, not one that reads

```
You are an independent VERIFIER.

Re-implement this spec yourself, from its text alone, over the real data where
there is real data and over the worked examples plus cases you construct
otherwise. Do not trust its numbers or its examples.

<Intent, in one paragraph.>

VERIFY, adversarially:
  1. re-derive every headline number using the spec's rules as written
  2. each rule's must-hold examples hold, and its must-not-hold examples do not
  3. the stated precedence produces the outcome the worked examples claim
  4. for each rule, a counterexample: a real case the rule gets wrong
  5. the strongest counterexample you found, or "none found, tried X, Y, Z"
```

Do not hand the verifier your implementation, and do not let it fix the document. Its job is to find what the text fails to determine.

Completion: the verifier either reproduced the numbers from the text, or showed where the text is underdetermined.

## Step 3: Record every round

Designs take rounds. Record them as a table: round, verdict, the defect that round found. Three rounds is not a failure; it is a design that had three real defects, and the second one is usually the one that would have shipped.

When a round fails the same way twice, the premise is wrong, not the wording. Stop editing and re-derive the premise from the evidence: the real data where there is data, the worked examples and the rules' own text where there is not.

Write the stopping bar into the verifier's brief before the first round, and say what belongs under `Filed` rather than in a failing row: cosmetic findings, and anything only reachable by deliberately malformed input, do not extend the loop. A round whose only findings are below the bar then returns PASS with them filed, which is a record you can close. A FAIL over below-the-bar rows would leave you choosing between editing a verdict you are not allowed to edit and running the round the bar exists to prevent. Dispatch each round through a different model or harness where you can, so the rounds are not all one family's blind spots.

Completion: the table has a row per round, each naming a specific defect and its fix.

## Step 4: Keep the number honest at the end

Re-derive the design's headline figure, if it has one, with the final rules, and check the acceptance criterion against the corrected baseline. An acceptance criterion with no exceptions is usually a wish; when the correct outcome leaves cases behind, say so, list each with its class and reason, and make that the criterion. A number that cannot be reached is worse than one that admits a floor. A design with no number still owes a criterion a third party can apply.

Completion: the acceptance criterion is one a third party can reproduce, and every case the design leaves behind is listed with its class and reason.
