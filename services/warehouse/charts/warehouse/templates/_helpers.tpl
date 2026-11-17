{{- define "warehouse.image" -}}
{{- if .Values.image.digest -}}
{{ (include "depot.imageRepository" $) }}@{{ .Values.image.digest }}
{{- else -}}
{{ (include "depot.imageRepository" $) }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}
