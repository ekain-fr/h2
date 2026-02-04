#!/bin/bash
# Outputs text in bursts with sleeps in between.
# Usage: h2 run ./scripts/trickle.sh

echo "=== Trickle output test ==="
echo "Starting up..."
sleep 1

echo ""
echo "Phase 1: counting"
for i in 1 2 3 4 5; do
    echo "  tick $i"
    sleep 0.5
done

sleep 1
echo ""
echo "Phase 2: a block of text"
echo "  Lorem ipsum dolor sit amet, consectetur adipiscing elit."
echo "  Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua."
echo "  Ut enim ad minim veniam, quis nostrud exercitation ullamco."

sleep 2
echo ""
echo "Phase 3: fast burst"
for i in $(seq 1 20); do
    echo "  line $i"
done

sleep 1
echo ""
echo "Done. Exiting normally."
