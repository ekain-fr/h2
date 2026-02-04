#!/bin/bash
# Reads lines of input and echoes them back.
# Usage: h2 run ./scripts/echo-input.sh

echo "=== Echo input test ==="
echo "Type something and press Enter. Ctrl-D or 'quit' to exit."
echo ""

while true; do
    printf "> "
    if ! read -r line; then
        echo ""
        echo "Got EOF. Bye!"
        break
    fi
    if [ "$line" = "quit" ]; then
        echo "Bye!"
        break
    fi
    echo "You said: $line"
done
