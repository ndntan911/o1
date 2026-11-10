#!/usr/bin/env bash
# Decide whether a version may deploy to an environment.
#
#   validate-release.sh ENVIRONMENT VERSION WORKFLOW_REF
#
# Exit codes:
#   0  allowed
#   2  bad arguments or malformed version
#   3  production must not receive a pre-release version
#   4  production may only deploy from refs/heads/main
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: validate-release.sh ENVIRONMENT VERSION WORKFLOW_REF" >&2
  exit 2
fi

environment=$1
version=$2
workflow_ref=$3

if [[ ! $environment =~ ^(staging|production)$ ]]; then
  echo "validate-release: environment must be staging or production" >&2
  exit 2
fi

if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "validate-release: version must look like vMAJOR.MINOR.PATCH[-PRE]" >&2
  exit 2
fi

if [[ $environment == production ]]; then
  if [[ $version == *-* ]]; then
    echo "validate-release: production refuses pre-release $version" >&2
    exit 3
  fi
  if [[ $workflow_ref != refs/heads/main ]]; then
    echo "validate-release: production may only deploy from refs/heads/main" >&2
    exit 4
  fi
fi

exit 0
