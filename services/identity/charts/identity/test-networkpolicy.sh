#!/usr/bin/env bash
# identity's ingress: the generated default-deny policy (<release>-ingress) admits port 8080 only from the depot-api Gateway's
# Envoy pods and the generated peers, 9090 only from Prometheus; the bundle policy admits 8443 only to labelled OPA pods.
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ingress_rules NAME: the ingress rules of the rendered NetworkPolicy NAME (empty when it is not rendered).
ingress_rules() {
  ruby -ryaml -rjson -e '
    docs = YAML.load_stream(STDIN.read).compact
    policy = docs.find { |d| d["kind"] == "NetworkPolicy" && d["metadata"]["name"] == ARGV[0] }
    puts JSON.generate(policy ? (policy.dig("spec", "ingress") || []) : nil)
  ' "$1"
}

assert_rules() {
  local label="$1" rendered="$2" port="$3" mode="$4"
  ruby -rjson -e '
    label, port, mode = ARGV
    all = JSON.parse(STDIN.read)
    abort "#{label}: policy not rendered" if all.nil?
    rules = all.select { |r| (r["ports"] || []).any? { |p| p["port"].to_s == port } }
    from = rules.flat_map { |r| r["from"] || [] }
    abort "#{label}: port #{port} is admitted from every source" if rules.any? { |r| (r["from"] || []).empty? }
    abort "#{label}: port #{port} admits every pod in the namespace" if from.any? { |f| f["podSelector"] == {} || f["podSelector"].nil? }
    selector = ->(f) { (f.dig("podSelector", "matchLabels") || {}).merge((f.dig("podSelector", "matchExpressions") || []).to_h { |e| [e["key"], e["values"]] }) }
    case mode
    when "gateway-and-peers"
      gateway = from.select { |f| f.dig("namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") == "envoy-gateway-system" }
      abort "#{label}: no Envoy Gateway peer" if gateway.size != 1
      abort "#{label}: gateway peer is not depot-api" unless selector.(gateway[0])["gateway.envoyproxy.io/owning-gateway-name"] == ["depot-api"]
      from.each { |f| abort "#{label}: peer #{f} lacks pod labels" if selector.(f).empty? }
    when "prometheus"
      abort "#{label}: expected only Prometheus, got #{from}" unless from.size == 1 &&
        from[0].dig("namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") == "observability" &&
        from[0].dig("podSelector", "matchLabels", "app.kubernetes.io/name") == "prometheus"
    when "labelled"
      abort "#{label}: no rule admits port #{port}" if from.empty?
      from.each { |f| abort "#{label}: peer #{f} lacks pod labels" if selector.(f).empty? }
    when "closed"
      abort "#{label}: port #{port} should be closed, got #{from}" unless from.empty?
    end
  ' "$label" "$port" "$mode" <<<"$rendered"
}

render() {
  helm template identity "$chart_dir" --namespace hello-ledger "$@"
}

defaults="$(render)"
assert_rules defaults "$(ingress_rules identity-ingress <<<"$defaults")" 8080 gateway-and-peers
assert_rules metrics "$(ingress_rules identity-ingress <<<"$defaults")" 9090 prometheus
if ruby -ryaml -e 'exit(YAML.load_stream(STDIN.read).compact.any? { |d| d["kind"] == "NetworkPolicy" && d["metadata"]["name"] == "identity-bundle" } ? 0 : 1)' <<<"$defaults"; then
  echo "the bundle policy must render only when bundle.enabled" >&2
  exit 1
fi
assert_rules no-peers "$(render --set-json 'ingressPolicy.gateway.names=[]' --set-json 'ingressPolicy.peers=[]' | ingress_rules identity-ingress)" 8080 closed
assert_rules bundle "$(render --set bundle.enabled=true | ingress_rules identity-bundle)" 8443 labelled

if helm template identity "$chart_dir" --set bundle.enabled=true --set networkPolicy.opaPodLabels=null >/dev/null 2>&1; then
  echo "bundle.enabled with empty opaPodLabels must fail to render" >&2
  exit 1
fi

# The serving Deployment carries MIGRATE_DATABASE_URL only for the dev on-start exception (migrateDatabaseSecret); the
# PreSync migration Job (templates/migrate-job.yaml, B-09a) always sets it from the owner Secret, so check the Deployment alone.
helm template identity "$chart_dir" --set migrateDatabaseSecret=identity-db-owner -s templates/deployment.yaml | grep -Fq 'MIGRATE_DATABASE_URL'
if helm template identity "$chart_dir" -s templates/deployment.yaml | grep -Fq 'MIGRATE_DATABASE_URL'; then
  echo "the serving Deployment must carry MIGRATE_DATABASE_URL only when migrateDatabaseSecret is set" >&2
  exit 1
fi
echo "identity chart NetworkPolicy checks passed"
