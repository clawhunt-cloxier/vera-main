#!/usr/bin/env bash
set -euo pipefail

# Match embeddedpostgres.V18 in dbtest (Linux amd64 CI runner).
cache_dir="${1:?pass the test PostgreSQL directory}/cache"
artifact=embedded-postgres-binaries-linux-amd64
version=18.3.0
digest=e09b13df10e9df257275d498b19b8c214a3e86d7a5d8cd263b0860e308e8b0b6
jar_path="io/zonky/test/postgres/$artifact/$version/$artifact-$version.jar"
mkdir -p "$cache_dir"
stage=$(mktemp -d "$cache_dir/download.XXXXXXXX")
trap 'rm -f "$stage/postgres.jar" "$stage/postgres.txz"; rmdir "$stage"' EXIT

for repo in https://repo.maven.apache.org/maven2 https://maven-central.storage-download.googleapis.com/maven2; do
  if curl --fail --silent --show-error --location --retry 2 --connect-timeout 10 --max-time 90 \
       "$repo/$jar_path" -o "$stage/postgres.jar" &&
     unzip -p "$stage/postgres.jar" '*.txz' > "$stage/postgres.txz" &&
     (cd "$stage" && sha256sum --check <<< "$digest  postgres.txz"); then
    mv "$stage/postgres.txz" "$cache_dir/$artifact-$version.txz"
    exit 0
  fi
done
echo "Failed to fetch and verify PostgreSQL $version from Maven Central mirrors" >&2
exit 1
