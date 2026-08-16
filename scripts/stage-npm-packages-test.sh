#!/bin/sh
set -eu

script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
PYTHONDONTWRITEBYTECODE=1
export PYTHONDONTWRITEBYTECODE
exec python3 "$script_dir/stage-npm-packages-test.py"
