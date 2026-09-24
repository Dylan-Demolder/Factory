# Contributing to factory

Thanks for considering a contribution. factory is a single Go binary with
no database and no build step, so the loop is short: edit, run the checks,
open a pull request.

## Ground rules

- **Be kind.** See the [Code of Conduct](CODE_OF_CONDUCT.md).
- **Security issues go private.** See [SECURITY.md](SECURITY.md); please do
  not open a public issue for a vulnerability.
- **Small, focused pull requests.** One concern per PR is far easier to
  review and much more likely to be merged.

## Getting set up

You need **Go 1.24.7 or newer** and `git` on your `PATH`.

```sh
git clone https://github.com/Dylan-Demolder/Factory.git
cd Factory
go build ./cmd/factory
```

factory ships no agents and no models, so you do **not** need any API keys or
CLIs to build, test or contribute. They are only needed if you want to run
factory end to end yourself.

## Making a change

1. Fork and create a branch off `main`:

   ```sh
   git checkout -b fix/something-that-was-broken
   ```

2. Make the change. Keep to the existing structure — the package layout is
   described under [Architecture and development](README.md#architecture-and-development).

3. **Add or update tests.** A change is not done because it compiles: it is
   done when a test would have failed before the change and passes after it.
   Where a package has no tests at all, say so in the pull request rather
   than silently skipping it.

4. Update documentation if you changed behaviour, a flag, a config field or
   an error message. `README.md` carries the reference sections;
   [`docs/agents.md`](docs/agents.md) carries the agent and model cookbook.

## Running the checks

CI runs these on every push and pull request, on both Linux and macOS. Run
them locally first so you are not waiting on a round trip:

```sh
gofmt -l .          # must print nothing
go vet ./...
go build ./...
go test -race -count=1 ./...
staticcheck ./...    # https://staticcheck.dev  (optional locally, required in CI)
```

`go test -race` matters: factory runs an interview, a web server and a
background build across goroutines, so the race detector is part of how the
suite earns its keep.

To run staticcheck locally:

```sh
go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
staticcheck ./...
```

## Tests in this repo

Roughly 140 tests drive the suite, and the interesting ones are not unit
tests of pure functions. They include scripted fake agents running whole
pipeline runs — an agent failure and its retry, unparseable output, failing
tests that get repaired, reviewer rejections, a blocked task and its
dependents, acceptance gaps turning into fix tasks, and resuming after a
crash — plus HTTP tests covering auth, CSRF, CORS, base paths, security
headers and the artifact allowlist.

If you touch one of those paths, extend its test rather than adding a new
isolated one.

## What a good pull request looks like

- a title that says what changed, in the imperative
  (*"Fix dropped answers between Ask and its select"*, not *"Fixed bug"*)
- a body that explains **why**, especially when the reason is not obvious
  from the diff
- tests that fail without the change
- no unrelated refactoring in the same PR

## Review

Pull requests are reviewed before merging. Reviewers may push changes or
ask for revisions; that is a normal part of the process rather than a
verdict on the work.

By contributing, you agree that your contribution is licensed under the
[MIT License](LICENSE) that covers this project.
