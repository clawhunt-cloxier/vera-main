#!/bin/bash
set -e
required="26"
if ! command -v node &> /dev/null; then
    echo "Node.js not found. Install Node.js >= ${required}"
    exit 1
fi
version=$(node --version | sed 's/v//')
major=$(echo "$version" | cut -d. -f1)
if [ "$major" -lt "$required" ]; then
    echo "Node.js $version found, but >= $required required"
    exit 1
fi
echo "Node.js $version OK"
