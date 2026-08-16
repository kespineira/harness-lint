#!/bin/sh
set -eu

script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
PYTHONDONTWRITEBYTECODE=1 exec python3 "$script_dir/npm-package-e2e-test.py"
