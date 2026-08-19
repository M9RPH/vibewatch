# Vibewatch v1.0.18

v1.0.18 is a CI/regression-fixture correction release. It does not change the production update, chain, rollback, recovery, or Developer Update runtime logic introduced through v1.0.17.

## Fixed

- Fixed the legacy restore ancestry regression fixture used by `TestDeterministicSourceDefaultsRecoversExpiredLegacyRestoreByUniqueLayerAncestry`.
- The fake Docker shell fixture now matches the multi-image `docker image inspect` request before the shorter single-image request. Shell `case` arms are first-match-wins; the previous ordering caused the batch request to return only the restore image and made the production code correctly fail closed with `no unique local image ancestor`.
- Added an explanatory fixture comment to prevent reintroducing the overlapping-pattern ordering bug.

## Runtime impact

None. Production deterministic restore ancestry logic is unchanged from v1.0.17.

## Validation

- Targeted legacy restore ancestry regression test.
- Full Go test suite.
- `go vet`.
- Version consistency check.
