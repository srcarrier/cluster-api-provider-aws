#!/bin/bash

set -euo pipefail

unset GOFLAGS
tmp="$(mktemp -d)"

mkdir -p "$tmp/src/github.com/openshift/cluster-api-actuator-pkg"

git clone --single-branch --branch release-4.1 "https://github.com/openshift/cluster-api-actuator-pkg.git" "$tmp/src/github.com/openshift/cluster-api-actuator-pkg"
exec make -C "$tmp/src/github.com/openshift/cluster-api-actuator-pkg" test-e2e GOPATH="$tmp"
