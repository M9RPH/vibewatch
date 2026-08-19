# Vibewatch v1.0.19

v1.0.19 fixes Developer Update/repository packaging integrity after GitHub CI exposed that release-skeleton dotfiles were missing from the generated source package. Runtime update, chain, rollback and recovery semantics are unchanged from v1.0.18.

## Repository and Developer Update integrity

- Restores the complete repository skeleton to Developer Update packages, including `.gitignore`, `.github/workflows/*` and the six `data/**/.gitkeep` markers required by CI.
- Developer Update staging now rejects an archive that is missing those repository/release-skeleton files instead of accepting an incomplete project tree.
- Keeps `data/` runtime contents protected during source apply, but safely recreates missing empty `.gitkeep` markers after the source overlay so a mounted development workspace remains GitHub-release compatible.
- Adds regression tests proving a package missing a release-skeleton marker is rejected and that source apply restores all six markers without copying staged runtime data.
- Includes the v1.0.18 legacy-restore ancestry fixture correction; production container-update behavior is otherwise unchanged.

## Validation

- `go test -count=1 ./...`
- `go vet ./...`
- version consistency check
- GitHub CI release-data-skeleton checks
- Developer Update ZIP staging through the integrated `StageArchive()` implementation
