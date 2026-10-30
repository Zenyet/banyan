#!/bin/sh
# Print the app's version, from main.go.
grep -m1 '^var version = ' "$(dirname "$0")/../main.go" | cut -d '"' -f 2
