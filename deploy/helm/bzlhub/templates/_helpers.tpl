{{/*
Standard name helpers — name, fullname, chart, labels, selectorLabels.
Mirrors the conventions emitted by `helm create` so users reading the
manifests find what they expect.
*/}}

{{- define "bzlhub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bzlhub.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "bzlhub.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bzlhub.labels" -}}
helm.sh/chart: {{ include "bzlhub.chart" . }}
{{ include "bzlhub.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "bzlhub.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bzlhub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Safe name for CronJob resources. k8s name budget:
  pod = "<job>-<5>" = "<cronjob>-<8>-<5>" → cronjob ≤ 63-8-5-2 = 48 chars.
Truncate the fullname FIRST, leaving room for the suffix — otherwise
a long release name would let the trunc eat the suffix and both
cronjobs would collide on the same name. We reserve 10 chars of
suffix budget (room for "-ingest" / "-drift" / future siblings),
leaving 38 chars for the base.
*/}}
{{- define "bzlhub.cronName" -}}
{{- $base := include "bzlhub.fullname" .root | trunc 38 | trimSuffix "-" -}}
{{- printf "%s-%s" $base .suffix | trunc 48 | trimSuffix "-" -}}
{{- end -}}

{{/*
Render a merged `annotations:` block — commonAnnotations from values
unioned with a per-resource annotations dict (resource-specific keys
win). Emits nothing when both are empty so we don't leave dangling
`annotations:` keys in the rendered YAML.

Caller signature:
  {{- include "bzlhub.annotations" (dict "ctx" . "specific" .Values.service.annotations) | nindent 2 }}
*/}}
{{- define "bzlhub.annotations" -}}
{{- $common := default (dict) .ctx.Values.commonAnnotations -}}
{{- $specific := default (dict) .specific -}}
{{- $merged := merge (deepCopy $specific) $common -}}
{{- if $merged }}
annotations:
{{- toYaml $merged | nindent 2 }}
{{- end -}}
{{- end -}}

{{- define "bzlhub.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "bzlhub.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image reference helper. Composes registry + repository + (digest|tag)
in one place so no template ever assembles an image string inline.

Order of precedence:
  1. .Values.global.imageRegistry, if set, wins over .Values.image.registry.
  2. .Values.image.digest, if set, wins over tag (sha256:… pinning).
  3. .Values.image.tag, if empty, falls back to .Chart.AppVersion.

Empty registry is allowed (`image.registry: ""`) for the kind/minikube
local-build flow: helper omits the leading "registry/" segment.
*/}}
{{- define "bzlhub.image" -}}
{{- $reg := default .Values.image.registry .Values.global.imageRegistry -}}
{{- $repo := .Values.image.repository -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- if .Values.image.digest -}}
{{- if $reg -}}{{ $reg }}/{{- end -}}{{ $repo }}@{{ .Values.image.digest }}
{{- else -}}
{{- if $reg -}}{{ $reg }}/{{- end -}}{{ $repo }}:{{ $tag }}
{{- end -}}
{{- end -}}

{{/*
Merged imagePullSecrets — union of image.pullSecrets and
global.imagePullSecrets, deduped by `name`. Global appears first to
match the precedence convention enterprise users expect from charts
that honor global.* overrides.

Emits the entire `imagePullSecrets:` block (key + list) or nothing
at all — callers wrap with `{{- include ... | nindent N }}` and rely
on the helper's whitespace-clean output (no stray blank lines).
*/}}
{{- define "bzlhub.imagePullSecrets" -}}
{{- $seen := dict -}}
{{- $secrets := list -}}
{{- range concat .Values.global.imagePullSecrets .Values.image.pullSecrets -}}
  {{- $name := .name -}}
  {{- if and $name (not (hasKey $seen $name)) -}}
    {{- $_ := set $seen $name true -}}
    {{- $secrets = append $secrets . -}}
  {{- end -}}
{{- end -}}
{{- if $secrets -}}
imagePullSecrets:
{{- range $secrets }}
  - name: {{ .name }}
{{- end }}
{{- end -}}
{{- end -}}

