{{- define "kelemetry.etcd-labels" }}
{{- include "kelemetry.default-labels-raw" . | fromYaml | merge (dict "app.kubernetes.io/component" "etcd") | toJson }}
{{- end }}

{{- define "kelemetry.informers-labels" }}
{{- include "kelemetry.default-labels-raw" . | fromYaml | merge (dict "app.kubernetes.io/component" "informers") | toJson }}
{{- end }}

{{- define "kelemetry.consumer-labels" }}
{{- include "kelemetry.default-labels-raw" . | fromYaml | merge (dict "app.kubernetes.io/component" "consumer") | toJson }}
{{- end }}

{{- define "kelemetry.default-labels-raw" }}
app.kubernetes.io/name: kelemetry
app.kubernetes.io/instance: {{.Release.Name}}
{{- end }}

{{- define "kelemetry.container-boilerplate"}}
{{- end }}

{{- define "kelemetry.pod-boilerplate"}}
securityContext: {{ toJson .podSecurityContext }}
nodeSelector: {{ toJson .nodeSelector }}
affinity: {{ toJson .affinity }}
tolerations: {{ toJson .tolerations }}
{{- end }}

{{- define "kelemetry.container-boilerplate"}}
securityContext: {{ toJson .containerSecurityContext }}
resources: {{ toJson .resources }}
{{- end }}

{{- define "kelemetry.yaml-to-args" }}
{{- range $key, $value := (fromYaml .) }}
{{- if kindIs "map" $value }}
{{- $value = include "kelemetry.map-to-arg" $value }}
{{- else if kindIs "slice" $value }}
{{- $value = include "kelemetry.slice-to-arg" $value}}
{{- end }}
{{- printf "--%v=%v" $key $value | toJson }},
{{- end }}
{{- end }}

{{- define "kelemetry.map-to-arg" }}
{{- include "kelemetry.map-to-arg-raw" . | trimSuffix "," }}
{{- end }}
{{- define "kelemetry.map-to-arg-raw" }}
{{- range $key, $value := . }}
{{- printf "%v=%v" $key $value }},
{{- end }}
{{- end }}

{{- define "kelemetry.slice-to-arg" }}
{{- include "kelemetry.slice-to-arg-raw" . | trimSuffix "," }}
{{- end }}
{{- define "kelemetry.slice-to-arg-raw" }}
{{- range . }}
{{- printf "%v" . }},
{{- end }}
{{- end }}

{{- define "kelemetry.aggregator-options" }}
{{- include "kelemetry.aggregator-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.aggregator-options-raw" }}
{{/* AGGREGATOR */}}
aggregator-event-span-global-tags: {{ .Values.aggregator.globalTags.eventSpan | toJson }}
aggregator-pseudo-span-global-tags: {{ .Values.aggregator.globalTags.pseudoSpan | toJson }}
aggregator-reserve-ttl: {{ .Values.aggregator.spanCache.reserveTtl | toJson }}
aggregator-span-extra-ttl: {{ .Values.aggregator.spanExtraTtl | toJson }}
aggregator-span-follow-ttl: {{ .Values.aggregator.spanFollowTtl | toJson }}
aggregator-span-ttl: {{ .Values.aggregator.spanTtl | toJson }}

{{/* SPAN CACHE */}}
{{- if .Values.aggregator.spanCache.type | eq "etcd" }}

span-cache: etcd
span-cache-etcd-dial-timeout: {{ .Values.aggregator.spanCache.etcd.dialTimeout | toJson }}
{{- if .Values.aggregator.spanCache.etcd.externalEndpoint }}
span-cache-etcd-endpoints: {{ .Values.aggregator.spanCache.etcd.externalEndpoint | toJson }}
{{- else }}
span-cache-etcd-endpoints: {{.Release.Name}}-etcd.{{.Release.Namespace}}.svc:2379
{{- end }}
span-cache-etcd-prefix: {{ .Values.aggregator.spanCache.etcd.prefix | toJson }}

{{- else }}
{{ printf "Unsupported span cache type %q" .Values.aggregator.spanCache.type | fail }}
{{- end }}

{{/* LINKERS */}}
annotation-linker-enable: {{ .Values.linkers.annotation }}
owner-linker-enable: {{ .Values.linkers.ownerReference }}

{{/* TRACER */}}
tracer-otel-endpoint: jaeger-collector.{{.Release.Namespace}}.svc:4317
{{- end }}

{{- define "kelemetry.object-cache-options" }}
{{- include "kelemetry.object-cache-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.object-cache-options-raw"}}
object-cache-fetch-timeout: {{ .Values.objectCache.fetchTimeout | toJson }}
object-cache-store-ttl: {{ .Values.objectCache.storeTtl | toJson }}
object-cache-size: {{ int64 .Values.objectCache.size | printf "%d" | toJson }}
{{- end }}

{{- define "kelemetry.in-cluster-config-options" }}
{{- include "kelemetry.in-cluster-config-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.in-cluster-config-options-raw"}}
kube-apiservers:
  {{- range .Values.multiCluster.clusters }}
  {{toJson .name}}: {{toJson .master}}
  {{- end }}
kube-config-paths:
  {{- range .Values.multiCluster.clusters }}
  {{- if .kubeconfig.type | eq "in-cluster" }}
  {{.name}}: ""
  {{- else if .kubeconfig.type | eq "literal" }}
  {{.name}}: /mnt/cluster-kubeconfig/{{.name}}/kubeconfig
  {{- else if .kubeconfig.type | eq "secret" }}
  {{.name}}: {{ printf "/mnt/cluster-kubeconfig/%s/%s" .name .kubeconfig.secret.key | toJson }}
  {{- else }}
  {{ printf "Unsupported kubeconfig type %q" .kubeconfig.type | fail }}
  {{- end }}
  {{- end }}
kube-target-cluster: {{ .Values.multiCluster.currentClusterName | default (.Values.multiCluster.clusters | first).name | toJson }}
{{- end }}

{{- define "kelemetry.logging-options" }}
{{- include "kelemetry.logging-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.logging-options-raw"}}
pprof-enable: {{.pprof}}
log-level: {{.logLevel}}
klog-v: {{.klogLevel}}
kube-target-rest-qps: {{.clusterQps}}
kube-target-rest-burst: {{.clusterBurst}}
kube-other-rest-qps: {{.otherClusterQps}}
kube-other-rest-burst: {{.otherClusterBurst}}
{{- end }}

{{- define "kelemetry.diff-cache-options" }}
{{- include "kelemetry.diff-cache-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.diff-cache-options-raw" }}
diff-cache-wrapper-enable: {{.Values.diffCache.memoryWrapper}}

{{- if .Values.diffCache.resourceVersionIndex | eq "Before" }}
diff-cache-use-old-rv: true
{{- else if .Values.diffCache.resourceVersionIndex | eq "After" }}
diff-cache-use-old-rv: false
{{- else }}
{{ printf "Unsupported resource version index type %q" .Values.diffCache.resourceVersionIndex }}
{{- end }}

{{- if .Values.diffCache.type | eq "etcd" }}
diff-cache: etcd
diff-cache-etcd-dial-timeout: {{ .Values.diffCache.etcd.dialTimeout | toJson }}
{{- if .Values.diffCache.etcd.externalEndpoint }}
diff-cache-etcd-endpoints: {{ .Values.diffCache.etcd.externalEndpoint | toJson }}
{{- else }}
diff-cache-etcd-endpoints: {{.Release.Name}}-etcd.{{.Release.Namespace}}.svc:2379
{{- end }}
diff-cache-etcd-prefix: {{ .Values.diffCache.etcd.prefix | toJson }}
{{- else }}
{{ printf "Unsupported diff cache type %q" .Values.diffCache.type | fail }}
{{- end }}
{{- end }}

{{- define "kelemetry.diff-controller-options" }}
{{- include "kelemetry.diff-controller-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.diff-controller-options-raw" }}
diff-controller-enable: true
diff-controller-leader-election-name: {{.Values.informers.diff.leaderElection.name}}
diff-controller-leader-election-namespace: {{.Values.informers.diff.leaderElection.namespace}}
diff-controller-leader-election-lease-duration: {{.Values.informers.diff.leaderElection.leaseDuration}}
diff-controller-leader-election-renew-duration: {{.Values.informers.diff.leaderElection.renewDuration}}
diff-controller-leader-election-retry-period: {{.Values.informers.diff.leaderElection.retryPeriod}}
diff-controller-leader-election-num-leaders: {{.Values.informers.diff.leaderElection.retryPeriod}}
diff-controller-redact-pattern: {{toJson .Values.informers.diff.redactPattern}}
diff-controller-store-timeout: {{toJson .Values.informers.diff.storeTimeout}}
diff-controller-deletion-snapshot: {{toJson .Values.informers.diff.snapshots.deletion}}
diff-controller-worker-count: {{toJson .Values.informers.diff.workerCount}}
diff-cache-patch-ttl: {{toJson .Values.informers.diff.persistDuration.patch}}
diff-cache-snapshot-ttl: {{toJson .Values.informers.diff.persistDuration.snapshot}}
{{- end }}

{{- define "kelemetry.event-informer-options" }}
{{- include "kelemetry.event-informer-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.event-informer-options-raw" }}
event-informer-enable: true
event-informer-configmap-name: {{toJson .Values.informers.event.stateConfig.name}}
event-informer-configmap-namespace: {{toJson .Values.informers.event.stateConfig.namespace}}
event-informer-configmap-sync-interval: {{toJson .Values.informers.event.stateConfig.syncInterval}}
event-informer-leader-election-name: {{.Values.informers.event.leaderElection.name}}
event-informer-leader-election-namespace: {{.Values.informers.event.leaderElection.namespace}}
event-informer-leader-election-lease-duration: {{.Values.informers.event.leaderElection.leaseDuration}}
event-informer-leader-election-renew-duration: {{.Values.informers.event.leaderElection.renewDuration}}
event-informer-leader-election-retry-period: {{.Values.informers.event.leaderElection.retryPeriod}}
event-informer-worker-count: {{toJson .Values.informers.diff.workerCount}}
{{- end }}

{{- define "kelemetry.audit-options" }}
{{- include "kelemetry.audit-options-raw" . | include "kelemetry.yaml-to-args" }}
{{- end }}
{{- define "kelemetry.audit-options-raw" }}
audit-consumer-enable: true
audit-consumer-filter-cluster-name: "" # TODO: review whether this option should be set
audit-consumer-group-failures: false # TODO: add option to enable this when tf plugins support it

{{- if .Values.consumer.source.type | eq "webhook" }}
audit-webhook-enable: true
audit-producer-enable: true
audit-forward-url: {{toJson .Values.consumer.source.webhook.forward}}
audit-consumer-partition: {{until (int .Values.consumer.source.webhook.workerCount) | toJson}}
{{- if .Values.consumer.source.webhook.tls.enabled }}
http-tls-cert: /mnt/tls/tls.crt
http-tls-key: /mnt/tls/tls.key
{{- end }}
{{- end }}
{{- end }}

{{- define "kelemetry.audit-volume-mounts" }}
{{- if .Values.consumer.source.type | eq "webhook" | and .Values.consumer.source.webhook.tls.enabled }}
{
  name: mnt-audit-webhook-tls,
  mountPath: /mnt/tls,
},
{{- end }}
{{- end }}

{{- define "kelemetry.audit-volumes" }}
{{- if .Values.consumer.source.type | eq "webhook" | and .Values.consumer.source.webhook.tls.enabled }}
{
  name: mnt-audit-webhook-tls,
  secret: {
    secretName: {{printf "%s-audit-webhook-tls" .Release.Name | toJson}},
  },
},
{{- end }}
{{- end }}

{{- define "kelemetry.kubeconfig-volume-mounts" }}
{{- range .Values.multiCluster.clusters }}
{{- if (.kubeconfig.type | eq "literal") | or (.kubeconfig.type | eq "secret") }}
{
  name: mnt-cluster-kubeconfig-{{.name}},
  mountPath: {{ printf "/mnt/cluster-kubeconfig/%s" .name | toJson }}
},
{{- end }}
{{- end }}
{{- end }}

{{- define "kelemetry.kubeconfig-volumes" }}
{{- range .Values.multiCluster.clusters }}
{{- if .kubeconfig.type | eq "literal" }}
{
  name: mnt-cluster-kubeconfig-{{.name}},
  secret: {
    secretName: {{printf "%s-cluster-kubeconfig-%s" $.Release.Name .name | toJson}},
  },
},
{{- else if .kubeconfig.type | eq "secret" }}
{
  name: mnt-cluster-kubeconfig-{{.name}},
  secret: {
    secretName: {{toJson .kubeconfig.secret.name}}
  },
},
{{- end }}
{{- end }}
{{- end }}
