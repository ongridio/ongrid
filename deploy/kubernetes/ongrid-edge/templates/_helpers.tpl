{{- define "ongrid-edge.name" -}}
{{- $architecture := default "" .Values.image.architecture -}}
{{- if and $architecture (ne $architecture "amd64") (ne $architecture "arm64") -}}
{{- fail "image.architecture must be empty, amd64, or arm64" -}}
{{- end -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ongrid-edge.fullname" -}}
{{- printf "%s" (include "ongrid-edge.name" .) -}}
{{- end -}}

{{- define "ongrid-edge.labels" -}}
app.kubernetes.io/name: {{ include "ongrid-edge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
ongrid.io/k8s-mode: {{ .Values.mode | quote }}
ongrid.io/cluster-id: {{ .Values.enrollment.clusterID | quote }}
{{- end -}}

{{- define "ongrid-edge.nodeServiceAccount" -}}
{{- default (printf "%s-node" (include "ongrid-edge.fullname" .)) .Values.node.serviceAccountName -}}
{{- end -}}

{{- define "ongrid-edge.controllerServiceAccount" -}}
{{- default (printf "%s-controller" (include "ongrid-edge.fullname" .)) .Values.controller.serviceAccountName -}}
{{- end -}}

{{- define "ongrid-edge.kubeStateMetricsName" -}}
{{- printf "%s-kube-state-metrics" (include "ongrid-edge.fullname" .) -}}
{{- end -}}

{{- define "ongrid-edge.telemetryGatewayName" -}}
{{- printf "%s-telemetry-gateway" (include "ongrid-edge.fullname" .) -}}
{{- end -}}

{{- define "ongrid-edge.controllerCredentialSecretName" -}}
{{- printf "%s-controller-credentials" (include "ongrid-edge.fullname" .) -}}
{{- end -}}

{{- define "ongrid-edge.telemetryGatewayEnabled" -}}
{{- $gw := default dict .Values.telemetryGateway -}}
{{- if kindIs "bool" $gw.enabled -}}
{{- if $gw.enabled -}}true{{- else -}}false{{- end -}}
{{- else -}}
true
{{- end -}}
{{- end -}}

{{- define "ongrid-edge.kubeStateMetricsEnabled" -}}
{{- $ksm := default dict .Values.kubeStateMetrics -}}
{{- if kindIs "bool" $ksm.enabled -}}
{{- if $ksm.enabled -}}true{{- else -}}false{{- end -}}
{{- else -}}
true
{{- end -}}
{{- end -}}

{{- define "ongrid-edge.kubeStateMetricsServiceAccount" -}}
{{- $ksm := default dict .Values.kubeStateMetrics -}}
{{- default (include "ongrid-edge.kubeStateMetricsName" .) $ksm.serviceAccountName -}}
{{- end -}}

{{- define "ongrid-edge.kubeStateMetricsEndpoint" -}}
{{- $ksm := default dict .Values.kubeStateMetrics -}}
{{- $port := default 8080 $ksm.port -}}
{{- printf "http://%s.%s.svc:%v/metrics" (include "ongrid-edge.kubeStateMetricsName" .) .Release.Namespace $port -}}
{{- end -}}

{{- define "ongrid-edge.k8sMetricsEndpoint" -}}
{{- $controllerMetrics := default dict .Values.controller.metrics -}}
{{- if $controllerMetrics.endpoint -}}
{{- $controllerMetrics.endpoint -}}
{{- else if eq (include "ongrid-edge.kubeStateMetricsEnabled" .) "true" -}}
{{- include "ongrid-edge.kubeStateMetricsEndpoint" . -}}
{{- end -}}
{{- end -}}

{{- define "ongrid-edge.k8sMetricsEnabled" -}}
{{- $controllerMetrics := default dict .Values.controller.metrics -}}
{{- if or (default false $controllerMetrics.enabled) (eq (include "ongrid-edge.kubeStateMetricsEnabled" .) "true") -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{- define "ongrid-edge.kubeStateMetricsResources" -}}
{{- $ksm := default dict .Values.kubeStateMetrics -}}
{{- if $ksm.collectors -}}
{{- join "," $ksm.collectors -}}
{{- else -}}
{{- "pods,deployments,statefulsets,daemonsets,replicasets,jobs,cronjobs,services,nodes,namespaces" -}}
{{- end -}}
{{- end -}}

{{- define "ongrid-edge.image" -}}
{{- $repo := default "docker.cnb.cool/ongridio/ongrid-edge" .Values.image.repository -}}
{{- printf "%s:%s" $repo (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
