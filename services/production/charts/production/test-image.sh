#!/usr/bin/env bash
# The image reference must never render with an empty tag (`repo:`): the tag defaults to the chart appVersion, a digest wins
# over a tag, and an explicit tag is used as given. Covers the API, consumer and shopfloor-consumer Deployments.
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
app_version="$(sed -n 's/^appVersion: *"\{0,1\}\([^"]*\)"\{0,1\} *$/\1/p' "$chart_dir/Chart.yaml")"
[ -n "$app_version" ] || { echo "Chart.yaml has no appVersion" >&2; exit 1; }

images() { grep -E '^\s+image:' | sed -E 's/^\s+image: *"?([^"]*)"?$/\1/' | sort -u; }
fail() { echo "FAIL: $*" >&2; exit 1; }

repo="ghcr.io/benjaminsrussell/production"
set_consumers=(--set consumer.enabled=true --set consumer.kafka.secret=s --set consumer.kafka.brokers=k:9092
  --set shopfloorConsumer.enabled=true --set shopfloorConsumer.kafka.secret=s --set shopfloorConsumer.kafka.brokers=k:9092)

default="$(helm template production "$chart_dir" "${set_consumers[@]}" | images)"
[ -n "$default" ] || fail "no image rendered"
while read -r image; do
  case "$image" in
    *: | "") fail "empty image tag: '$image'" ;;
    "$repo:$app_version") ;;
    *) fail "default image '$image', want $repo:$app_version" ;;
  esac
done <<<"$default"

tagged="$(helm template production "$chart_dir" "${set_consumers[@]}" --set image.tag=9.9.9 | images)"
[ "$tagged" = "$repo:9.9.9" ] || fail "explicit tag gave '$tagged'"

digest="sha256:$(printf 'a%.0s' $(seq 64))"
pinned="$(helm template production "$chart_dir" "${set_consumers[@]}" --set image.tag=9.9.9 --set "image.digest=$digest" | images)"
[ "$pinned" = "$repo@$digest" ] || fail "digest gave '$pinned'"
echo "production chart image tests passed"
