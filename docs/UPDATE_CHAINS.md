# Update Chains

Update Chains execute related containers in an explicit order. They are useful when a Compose stack should be updated as one controlled application transaction rather than as unrelated containers.

## Stack Chains

A stack-scoped Chain detects members of one Compose stack and keeps the configured order explicit. The Chain can own Manual / Auto Update / Excluded policy for the stack.

## Step behavior

If a member has no image update, the step can be configured to:

- Skip
- Restart
- Recreate

Vibewatch does not infer arbitrary application dependencies. These configured lifecycle decisions remain authoritative.

## Preflight

For automatic Chain runs, Vibewatch checks the complete actionable plan before the first mutation. If a step is blocked, the Chain is held before earlier services are changed.

Each actual update step still repeats the normal Preflight immediately before its mutation.

Manual `Run Now` displays the Chain Preflight review before execution. Advisory warnings can be accepted for that one run; hard blockers cannot be overridden.

In v1.0.2 this review uses the staged Web UI v2 transaction inspector. Each member progresses independently through the review, and warnings/blockers are shown directly under the member that produced them. Once approved, the same transaction language continues through the live Chain run so the current member and failure point remain visible.

## Verification

After each actual update/recreate, Docker health/running-state and the effective Custom/Stack Verification are evaluated before the Chain proceeds.

## Data Protection

A shared stack Data Protection scope is captured once per Chain run. Later members reuse that persistent-data baseline while keeping their own writable-layer/config restore points. This prevents repeated large data captures within one Chain transaction.

If a destructive failure means protected data must return to the pre-Chain baseline, already completed software may also be rolled back so old data is not combined with a partially newer application generation.

## Controller restart recovery

v1.0.0 treats a controller restart as a recovery event, not a reason to blindly continue a multi-service Chain.

- Child update transactions reconcile first.
- A started restart/recreate is verified or restored where possible.
- Completed/rolled-back started work is recorded.
- Remaining unstarted steps are marked **Interrupted** and are not automatically resumed.
- A successfully reconciled interrupted Chain is shown as **Recovered**.
- An unresolved state is shown as **Recovery required** and blocks another run until recovery is resolved.

## Target-image verification

Every actual image update inside a Chain uses the same container update transaction as a standalone update. Since v1.0.4 a Chain member is only successful after the live container image matches the target image detected by Preflight. `updated=0 / skipped=1` with the old image still active therefore fails at that member and cannot be reported as a successful Chain step.
