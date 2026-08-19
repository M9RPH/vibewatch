# Automation and cleanup

Vibewatch keeps scheduling separate from update policy.

## Policy runs

A Policy run checks its target hosts and applies configured container/stack policies. Automatic updates remain subject to Preflight, restore-point creation and verification.

Automatic safety holds are visible as Held/Needs attention rather than being reported as successful updates.

## Cleanup runs

Automation can also schedule the same safe cleanup actions available from the Dashboard:

- unused images;
- build cache;
- unused networks;
- anonymous unused volumes.

Cleanup honors Vibewatch recovery protection. Retained restore images, protected volumes and Vibewatch restore storage are excluded from normal cleanup eligibility.

## Targets

Automations can target:

- all hosts;
- one host;
- a host group.

Update Chains may bind only to Policy automations, not Cleanup automations.

## Multi-host concurrency

Policy discovery is bounded-parallel across up to three target hosts. The update queue can also execute up to three pipelines concurrently when they belong to different hosts, while a single host never receives two active update pipelines at once. Ordinary checks, preflight/restore preparation and ordinary stop/recreate/verification windows use shared continuity access and may overlap across hosts. A DNS-capable target (runtime port 53) requests exclusive access; already-running shared work finishes, then no new registry/Watchtower check or ordinary mutation starts until DNS readiness is proven.
