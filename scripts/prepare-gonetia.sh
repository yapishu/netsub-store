#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
star="${1:?usage: scripts/prepare-gonetia.sh '~star'}"
mkdir -p tools data/gonetia
if [[ ! -d tools/gonetia/.git ]]; then
  git clone https://github.com/yapishu/gonetia tools/gonetia
fi
git -C tools/gonetia remote set-url origin https://github.com/yapishu/gonetia
git -C tools/gonetia fetch origin 73ed8b7bd29fde4b22f7fed35e6270b54d9471ea
git -C tools/gonetia checkout --detach 73ed8b7bd29fde4b22f7fed35e6270b54d9471ea
git -C tools/gonetia submodule update --init --recursive
(
  cd tools/gonetia
  go run . "$star"
)
# These files contain public planet names and must be readable by the container UID.
chmod 0755 data/gonetia
install -m 0644 tools/gonetia/output/"${star#\~}"/*_planets.txt data/gonetia/
printf 'Gonetia output is ready in data/gonetia. Refresh inventory in /admin.\n'
