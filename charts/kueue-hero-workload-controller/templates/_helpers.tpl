{{- define "hero.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "hero.fullname" -}}
{{- if contains (include "hero.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "hero.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "hero.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{ include "hero.selectorLabels" . }}
{{- end -}}

{{- define "hero.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hero.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- /* Resolve a Kubernetes memory quantity to a plain byte count. */ -}}
{{- define "hero.memoryBytes" -}}
{{- $q := trim (toString .) -}}
{{- $n := $q -}}
{{- $mult := 1.0 -}}
{{- if hasSuffix "Ki" $q -}}{{- $n = trimSuffix "Ki" $q -}}{{- $mult = 1024.0 -}}
{{- else if hasSuffix "Mi" $q -}}{{- $n = trimSuffix "Mi" $q -}}{{- $mult = 1048576.0 -}}
{{- else if hasSuffix "Gi" $q -}}{{- $n = trimSuffix "Gi" $q -}}{{- $mult = 1073741824.0 -}}
{{- else if hasSuffix "Ti" $q -}}{{- $n = trimSuffix "Ti" $q -}}{{- $mult = 1099511627776.0 -}}
{{- else if hasSuffix "k" $q -}}{{- $n = trimSuffix "k" $q -}}{{- $mult = 1000.0 -}}
{{- else if hasSuffix "M" $q -}}{{- $n = trimSuffix "M" $q -}}{{- $mult = 1000000.0 -}}
{{- else if hasSuffix "G" $q -}}{{- $n = trimSuffix "G" $q -}}{{- $mult = 1000000000.0 -}}
{{- else if hasSuffix "T" $q -}}{{- $n = trimSuffix "T" $q -}}{{- $mult = 1000000000000.0 -}}
{{- end -}}
{{- if not (regexMatch "^[0-9]+(\\.[0-9]+)?$" $n) -}}
{{- fail (printf "unsupported memory quantity %q" $q) -}}
{{- end -}}
{{- printf "%d" (int64 (floor (mulf (float64 $n) $mult))) -}}
{{- end -}}

{{- /*
GOMEMLIMIT for the manager, as a byte count derived from
resources.limits.memory. Empty when no memory limit is set, since there is
nothing to take a percentage of.
*/ -}}
{{- define "hero.goMemLimit" -}}
{{- $limit := dig "limits" "memory" "" (.Values.resources | default dict) -}}
{{- if and $limit .Values.goMemLimitPercentage -}}
{{- $bytes := float64 (include "hero.memoryBytes" $limit) -}}
{{- printf "%d" (int64 (floor (mulf $bytes (divf (float64 .Values.goMemLimitPercentage) 100.0)))) -}}
{{- end -}}
{{- end -}}

{{- define "hero.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "hero.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
