#!/bin/bash
# Runs for N seconds then crashes with a non-zero exit code.
# Usage: h2 run ./scripts/crash.sh 3

seconds="${1:-2}"

echo "=== Crash test ==="
echo "Will crash in $seconds second(s)..."
echo ""

for i in $(seq 1 "$seconds"); do
    echo "  alive... ($i/$seconds)"
    sleep 1
done

echo ""
echo "Crashing now!"
exit 1
