{{- define "workflow.image" -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" (include "depot.imageRepository" $) .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" (include "depot.imageRepository" $) (default .Chart.AppVersion .Values.image.tag) }}
{{- end -}}
{{- end -}}
