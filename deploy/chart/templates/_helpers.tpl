{{- define "avsys.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* The name of every object: the release's, or avsys-<release>. */}}
{{- define "avsys.fullname" -}}
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

{{/*
The panel's pods are picked by name alone, as deploy/install.yaml did before
there was a chart: a Deployment's selector cannot change, and this way an
install made from that file can be taken over by the chart in place. One
release per namespace.
*/}}
{{- define "avsys.selectorLabels" -}}
app.kubernetes.io/name: {{ include "avsys.name" . }}
{{- end -}}

{{- define "avsys.labels" -}}
{{ include "avsys.selectorLabels" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ include "avsys.tag" . | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "avsys.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "avsys.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is needed when serviceAccount.create is false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "avsys.tag" -}}
{{- default .Chart.AppVersion .Values.image.tag -}}
{{- end -}}

{{- define "avsys.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if has (include "avsys.tag" .) (list "edge" "latest") -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}

{{/* The Secret the chart makes for secrets given in values. */}}
{{- define "avsys.secretName" -}}
{{- include "avsys.fullname" . -}}
{{- end -}}

{{/* Whether any secret is given in values, and so the chart makes one. */}}
{{- define "avsys.ownSecret" -}}
{{- $v := .Values -}}
{{- $any := false -}}
{{- if not $v.signIn.existingSecret -}}
{{- range $id := list "github" "discord" "google" "oidc" -}}
{{- $p := index $v.signIn $id -}}
{{- if and $p.clientId $p.clientSecret -}}{{- $any = true -}}{{- end -}}
{{- end -}}
{{- end -}}
{{- if and $v.curseforge.apiKey (not $v.curseforge.existingSecret) -}}{{- $any = true -}}{{- end -}}
{{- if and $v.offsite.repository $v.offsite.password (not $v.offsite.existingSecret) -}}{{- $any = true -}}{{- end -}}
{{- if $any -}}true{{- end -}}
{{- end -}}

{{/* key=value,... from a map, sorted, for a flag that takes annotations. */}}
{{- define "avsys.pairs" -}}
{{- $out := list -}}
{{- range $k, $v := . -}}
{{- $out = append $out (printf "%s=%s" $k $v) -}}
{{- end -}}
{{- join "," $out -}}
{{- end -}}

{{/* Checks of values that would otherwise make an install that does not work. */}}
{{- define "avsys.validate" -}}
{{- $e := .Values.expose -}}
{{- if not (has $e.mode (list "LoadBalancer" "NodePort" "hostPort")) -}}
{{- fail (printf "expose.mode is %q; it is LoadBalancer, NodePort or hostPort" $e.mode) -}}
{{- end -}}
{{- if eq $e.mode "NodePort" -}}
{{- if or (not $e.nodePorts.ssh) (not $e.nodePorts.minecraft) -}}
{{- fail "expose.mode NodePort needs expose.nodePorts.ssh and expose.nodePorts.minecraft: they are shown in addresses" -}}
{{- end -}}
{{- end -}}
{{- if and .Values.packs.listener.enabled (not .Values.packs.url) -}}
{{- fail "packs.listener needs packs.url, the address players reach it at" -}}
{{- end -}}
{{- if and (eq $e.mode "hostPort") (not $e.node) (not .Values.nodeSelector) -}}
{{- fail "expose.mode hostPort needs expose.node (or a nodeSelector), the node whose ports are opened" -}}
{{- end -}}
{{- range $id := list "github" "discord" "google" "oidc" -}}
{{- $p := index $.Values.signIn $id -}}
{{- if and $p.clientSecret (not $p.clientId) -}}
{{- fail (printf "signIn.%s has a clientSecret but no clientId" $id) -}}
{{- end -}}
{{- if and $p.clientId (not $p.clientSecret) (not $.Values.signIn.existingSecret) -}}
{{- fail (printf "signIn.%s needs a clientSecret, or signIn.existingSecret" $id) -}}
{{- end -}}
{{- end -}}
{{- if not (has .Values.signIn.twoFactor (list "nobody" "admins" "operators" "everyone")) -}}
{{- fail (printf "signIn.twoFactor is %q; it is nobody, admins, operators or everyone" .Values.signIn.twoFactor) -}}
{{- end -}}
{{- $oidc := .Values.signIn.oidc -}}
{{- if and $oidc.clientId (not $oidc.issuer) -}}
{{- fail "signIn.oidc needs an issuer" -}}
{{- end -}}
{{- if and $oidc.issuer (not $oidc.clientId) -}}
{{- fail "signIn.oidc has an issuer but no clientId" -}}
{{- end -}}
{{- if not .Values.passwords -}}
{{- $any := false -}}
{{- range $id := list "github" "discord" "google" "oidc" -}}
{{- if (index $.Values.signIn $id).clientId -}}{{- $any = true -}}{{- end -}}
{{- end -}}
{{- if not $any -}}
{{- fail "passwords false needs a provider in signIn to sign in through" -}}
{{- end -}}
{{- end -}}
{{- $o := .Values.offsite -}}
{{- if and $o.repository (not $o.existingSecret) (not $o.password) -}}
{{- fail "offsite.repository needs a password: offsite.existingSecret or offsite.password" -}}
{{- end -}}
{{- if and $o.secretEnv (not $o.existingSecret) -}}
{{- fail "offsite.secretEnv names keys of offsite.existingSecret, which is not set" -}}
{{- end -}}
{{- end -}}

{{/* The port players reach the router on, and SSH on, given how they are exposed. */}}
{{- define "avsys.publicPort" -}}
{{- $e := .root.Values.expose -}}
{{- if eq $e.mode "NodePort" -}}
{{- index $e.nodePorts .port -}}
{{- else if eq $e.mode "hostPort" -}}
{{- index $e.hostPorts .port -}}
{{- else -}}
{{- index $e.ports .port -}}
{{- end -}}
{{- end -}}
