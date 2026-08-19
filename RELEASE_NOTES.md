# Vibewatch v1.0.20

v1.0.20 hardens GitHub CI script invocation after a checkout exposed that executable mode bits may be recorded as non-executable in the Git index even when Developer Update packages preserve `0755`. Runtime update, chain, rollback, recovery and Developer Update behavior are unchanged from v1.0.19.

## CI script invocation portability

- GitHub CI and release-container workflows now invoke integration scripts explicitly through `bash` instead of relying on the Git executable bit.
- Covers both `scripts/test-integration.sh` and the privileged NetEm regression script.
- Developer Update packages continue to preserve executable file modes, so local `./scripts/...` use remains supported when the filesystem/Git checkout retains those modes.
- This prevents `Process completed with exit code 126` / `Permission denied` from blocking CI solely because a repository checkout recorded a script as `100644`.

## Validation

- `go test -count=1 ./...`
- `go vet ./...`
- version consistency check
- workflow syntax/source checks
- repository release-data-skeleton checks
- Developer Update ZIP staging through the integrated `StageArchive()` implementation
