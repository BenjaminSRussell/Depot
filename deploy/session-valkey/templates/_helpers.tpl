{{- define "session-valkey.prefix" -}}
{{ replace "{env}" (required "externalSecret.environment is required" .Values.externalSecret.environment) .Values.externalSecret.pathPrefix }}
{{- end }}

{{- /* The ESO template variable of a client's password: Go template identifiers have no "-". */ -}}
{{- define "session-valkey.var" -}}
{{ replace "-" "_" . }}
{{- end }}

{{- define "session-valkey.clients" -}}
{{- if not .Values.clients }}{{ fail "clients is required: list the pods that may reach the session store" }}{{ end }}
{{- range .Values.clients }}
{{- if not (and .name .namespace .podName .keyPrefix) }}{{ fail "each client needs name, namespace, podName and keyPrefix" }}{{ end }}
{{- end }}
{{- end }}
