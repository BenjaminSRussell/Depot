{{/* "true" when this region's Postgres is fenced: role FENCED, or a game-day fence. */}}
{{- define "depot-mr.fenced" -}}
{{- if or (eq .Values.region.role "FENCED") .Values.fence.enabled -}}true{{- end -}}
{{- end -}}

{{/* "true" when MirrorMaker 2 runs in this region: it runs next to its target cluster. */}}
{{- define "depot-mr.mm2Here" -}}
{{- if and .Values.mm2.enabled (eq .Values.kafka.localAlias .Values.mm2.targetAlias) -}}true{{- end -}}
{{- end -}}

{{/* synchronous_standby_names form of postgres.synchronous, e.g. "ANY 1". */}}
{{- define "depot-mr.syncQuorum" -}}
{{ upper .Values.postgres.synchronous.method }} {{ .Values.postgres.synchronous.number }}
{{- end -}}

{{- define "depot-mr.backupStore" -}}{{ .Values.postgres.clusterName }}-backup{{- end -}}

{{/* "true" when this region's cluster archives WAL and takes base backups. */}}
{{- define "depot-mr.backsUp" -}}
{{- if and .Values.backup.enabled (ne .Values.region.role "PASSIVE") -}}true{{- end -}}
{{- end -}}

{{- define "depot-mr.labels" -}}
app.kubernetes.io/part-of: depot
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
depot.dev/region: {{ .Values.region.id }}
depot.dev/region-role: {{ .Values.region.role }}
{{- end -}}

{{/* "true" when backups go to Azure Blob storage. */}}
{{- define "depot-mr.azureBlob" -}}
{{- if eq .Values.backup.store "azure-blob" -}}true{{- end -}}
{{- end -}}

{{/* "true" when this region archives WAL to Azure Blob as a workload identity (no storage key). */}}
{{- define "depot-mr.azureWorkloadIdentity" -}}
{{- if and (include "depot-mr.backsUp" .) (include "depot-mr.azureBlob" .) .Values.backup.azure.useWorkloadIdentity -}}true{{- end -}}
{{- end -}}

{{/* Azure Blob destination: https://<account>.<blob suffix of the cloud profile>/<container>/ */}}
{{- define "depot-mr.azureDestination" -}}
{{- $blob := required "global.endpoints.blobSuffix is required for backup.store azure-blob: render with deploy/cloud-profiles/values/<profile>.yaml" ((.Values.global | default dict).endpoints | default dict).blobSuffix -}}
https://{{ required "backup.azure.storageAccount is required for backup.store azure-blob" .Values.backup.azure.storageAccount }}.{{ $blob }}/{{ .Values.backup.azure.container }}/
{{- end -}}
