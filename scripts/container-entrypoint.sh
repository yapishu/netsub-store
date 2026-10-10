#!/bin/sh
set -eu

if [ "$#" -eq 0 ] && [ "${DEMO:-false}" != true ]; then
  : "${STAR:?Set STAR to the dispensing star}"
  # Gonetia embeds its wordlists; generation needs no runtime network access.
  work_dir=$(mktemp -d /tmp/gonetia.XXXXXX)
  (cd "$work_dir" && gonetia "$STAR" </dev/null)
  export GONETIA_DIR="$work_dir/output/${STAR#\~}"
fi

exec store "$@"
