# Contributing to pocketdev

Thanks for helping. pocketdev is a Go CLI; the bar is clean, readable code that
matches the surrounding style.

## Build and test

```
go build -o pocketdev .
go test ./...
go vet ./...
gofmt -l .   # prints nothing when formatting is clean
```

You need Go 1.25+.

## Pull requests

- Keep changes focused: one concern per PR.
- Add or update tests for behavior you change. The provisioning paths are hard to
  exercise end to end, so favor pure, table-driven tests; see the existing
  `*_test.go` files for the pattern.
- Run `gofmt`, `go vet`, and `go test ./...` before pushing. CI runs the same.
- Commit messages explain the why, not only the what.
- Prose (README, help text, docs) reads like a person wrote it: active voice, no
  filler, no em dashes.

## Security

Don't open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).

## License

By contributing, you agree your work is licensed under [AGPL-3.0](LICENSE).
