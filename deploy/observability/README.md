# Observability (wave 5)

Helm chart `charts/observability` ships Grafana SLO dashboards, Tempo/Loki/Prometheus config and PagerDuty-compatible Alertmanager routes. It does not vendor kube-prometheus-stack; wave 1 owns cluster add-ons.

In dev, `deploy/envs/dev/apps/kube-prometheus-stack.yaml` holds two Argo CD apps. `prometheus-operator-crds` (OCI chart `ghcr.io/prometheus-community/charts/prometheus-operator-crds`, pinned 32.0.2, operator v0.94.2) installs the `monitoring.coreos.com` CRDs at sync wave -2, before every service. `kube-prometheus-stack` (pinned 92.16.2, its own CRDs off) runs the Prometheus Operator, Prometheus, Alertmanager, Grafana and kube-state-metrics at wave 3, after the services, so a slow or failing stack cannot hold up root-dev, which waits for each wave to be Healthy. Prometheus selects every ServiceMonitor, PodMonitor and PrometheusRule in the cluster, so a chart's `serviceMonitor.enabled` and `alerts.enabled` take effect without a release label; Grafana's sidecar loads the `passage-slo` dashboard from this chart's ConfigMap (label `grafana_dashboard: "1"`). Grafana's admin login is the Secret `grafana-admin` in `observability`, which `grafana-admin/grafana-admin.yaml` has the External Secrets Operator generate in the cluster (Password generator, created once), so nothing is created by hand. Open: Alertmanager still runs the chart's default (null) route, not this chart's PagerDuty routes, and Tempo and Loki are not deployed. Stage and prod have no monitoring app yet.

A UI click emits W3C `traceparent` from `apps/web/src/shell/telemetry.ts`. The documented hop is web-shell → gateway → service → postgres and kafka consumer, which the `passage-slo` Tempo panel queries.

```
helm lint deploy/observability/charts/observability
```
