#!/bin/sh
# Entrypoint of the GitHub Action: runs monorelease with the "args" input.
# $INPUT_ARGS is split into words on purpose, so "release --push" becomes two arguments.
set -e
# shellcheck disable=SC2086
exec monorelease ${INPUT_ARGS:-plan}
