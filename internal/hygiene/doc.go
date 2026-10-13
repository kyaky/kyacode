// Package hygiene gates the artifacts whose correctness no Go compiler
// sees: the CI workflow YAML (runner matrix, action pins, cache layers),
// the README's executable install snippets, and the CONTEXT.md glossary's
// falsifiable carrier claims. Its tests parse the checked-in files the
// way their real consumers (GitHub Actions, a pasting shell, a fresh
// carrier author) do, so shape drift fails `go test` before it fails CI.
package hygiene
