#!/bin/bash
set -e
required="1.27"
if ! command -v go &> /dev/null; then
    echo "Go not found. Install Go >= ${required}"
    exit 1
fi
version=$(go version | awk '{print $3}' | sed 's/go//')
major=$(echo "$version" | cut -d. -f1)
minor=$(echo "$version" | cut -d. -f2)
req_major=$(echo "$required" | cut -d. -f1)
req_minor=$(echo "$required" | cut -d. -f2)
if [ "$major" -lt "$req_major" ] || ([ "$major" -eq "$req_major" ] && [ "$minor" -lt "$req_minor" ]); then
    echo "Go $version found, but >= $required required"
    exit 1
fi
echo "Go $version OK"
