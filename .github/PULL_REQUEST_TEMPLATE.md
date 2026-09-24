## What changed and why

<!-- Describe the problem first, then the change. The "why" matters more
     than the "what" — the diff already shows the what. -->

## How it was verified

<!-- Commands you ran, and what you saw. CI runs these on Linux and macOS:

       gofmt -l .          # prints nothing
       go vet ./...
       go build ./...
       go test -race -count=1 ./...
       staticcheck ./...

     For a behaviour change, say which test fails without your change. -->

## Checklist

- [ ] `gofmt -l .`, `go vet ./...`, `go build ./...` are all clean
- [ ] `go test -race -count=1 ./...` passes
- [ ] `staticcheck ./...` passes
- [ ] Tests added or updated for the changed behaviour
- [ ] Documentation updated if behaviour, a flag, a config field or an error message changed
- [ ] No unrelated changes mixed into this PR

## Notes for the reviewer

<!-- Anything you considered and rejected, anything you would do
     differently with more time, anything you deliberately left out. -->
