# Custom Verification

Docker `running` does not guarantee that an application works. Vibewatch Custom Verification checks the service after an update or rollback.

## Check types

- HTTP
- HTTPS
- TCP

HTTP/HTTPS checks can validate an expected HTTP status and optional response content. TCP checks validate that a connection can be established to the configured host/port.

Checks are executed by the Vibewatch controller, so the configured address must be reachable from the controller.

## Scope

### Container scope

The profile belongs to one container. Its verification state is shown as `Verify: ...`.

### Compose stack scope

One application-level profile is shared across the stack. A successful stack check produces a shared `Stack: Verified` state for members that do not have a container-specific override.

A container-specific profile takes precedence over an inherited stack profile.

## Run verification now

The Verification dialog can execute the current profile without performing an update. The result is written to verification history and updates the visible status.

## Update behavior

After Docker health/running-state validation, Vibewatch runs the effective Custom Verification profile. A failed verification makes the update fail and can enter automatic rollback when a valid restore point exists.

After rollback, verification is run again. A rollback is not considered functionally successful merely because a container process started.
