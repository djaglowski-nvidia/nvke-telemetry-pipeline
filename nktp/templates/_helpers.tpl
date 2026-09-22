{{/*
Resolve an image map ({registry, repository, tag}) into an
OpenTelemetryCollector spec.image / container image string. Used by
otelcol_image, otel-lgtm.image, and kube-state-metrics.image.
*/}}
{{- define "nktp.image" -}}
{{- if .registry -}}
{{- printf "%s/%s:%s" .registry .repository .tag -}}
{{- else -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}
{{- end -}}
