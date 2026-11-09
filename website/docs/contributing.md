---
title: Contributing
---

# Contributing

## Development

You need Go (see `go.mod`) and [golangci-lint](https://golangci-lint.run) v2.

```bash
make fmt lint test build     # bin/minato
make snapshot                # every platform in dist/, needs goreleaser
make docs-serve              # this site on http://localhost:3000/minato/
```

## Commit messages

Pull requests are squash merged, and their title becomes the commit message on
`main`. Titles follow [Conventional Commits](https://www.conventionalcommits.org):

```
feat(secrets): add rotate --value
fix(webhook): ignore ping events
docs: explain swarm configs
```

| Type | Release |
| --- | --- |
| `feat` | minor version |
| `fix`, `perf` | patch version |
| `feat!` or a `BREAKING CHANGE:` footer | major version (minor before 1.0) |
| `docs`, `refactor`, `test`, `build`, `ci`, `chore` | no release on their own |

A check on every pull request rejects titles that do not follow the format.

## Releases

Releases are automatic:

1. [release-please](https://github.com/googleapis/release-please) keeps a
   `chore: release x.y.z` pull request up to date with the next version and
   `CHANGELOG.md`.
2. Merging it tags `vx.y.z` and creates the GitHub release.
3. [GoReleaser](https://goreleaser.com) builds the Linux and macOS binaries and
   attaches them with `checksums.txt`.

## License

Minato is licensed under the GNU AGPL v3. By contributing, you agree that your
contributions are licensed under the same terms.
