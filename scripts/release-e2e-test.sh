#!/bin/sh
set -eu

script_dir=$(CDPATH=; cd -- "$(dirname -- "$0")" && pwd)
"$script_dir/release-e2e.sh" --test-file-descriptions
