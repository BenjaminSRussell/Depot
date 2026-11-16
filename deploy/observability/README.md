# Observability (wave 5)

Helm chart `charts/observability` ships Grafana SLO dashboards, Tempo/Loki/Prometheus config and PagerDuty-compatible Alertmanager routes. It does not vendor kube-prometheus-stack; wave 1 owns cluster add-ons.

In dev, the Argo CD app `deploy/envs/dev/apps/kube-prometheus-stack.yaml` installs the Prometheus Operator and its CRDs, Prometheus, Alertmanager, Grafana and kube-state-metrics (OCI chart `ghcr.io/prometheus-community/charts/kube-prometheus-stack`, pinned 92.16.2, sync wave -2, before every service). Prometheus selects every ServiceMonitor, PodMonitor and PrometheusRule in the cluster, so a chart's `serviceMonitor.enabled` and `alerts.enabled` take effect without a release label; Grafana's sidecar loads the `passage-slo` dashboard from this chart's ConfigMap (label `grafana_dashboard: "1"`). Grafana's admin login is the Secret `grafana-admin` in `observability` (keys `admin-user`, `admin-password`), created outside Git. Open: Alertmanager still runs the chart's default (null) route, not this chart's PagerDuty routes, and Tempo and Loki are not deployed. Stage and prod have no monitoring app yet.

A UI click emits W3C `traceparent` from `apps/web/src/shell/telemetry.ts`. The documented hop is web-shell → gateway → service → postgres and kafka consumer, which the `passage-slo` Tempo panel queries.

```
helm lint deploy/observability/charts/observability
```
