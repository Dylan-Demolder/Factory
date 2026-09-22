#!/bin/sh
# Run the entire test suite for this repository.
# Usage: sh test.sh
set -eu
cd "$(dirname "$0")"
# -count=1 defeats the test cache so every run is a real run; -race matches
# the suite documented in README.md.
exec go test -race -count=1 ./...
