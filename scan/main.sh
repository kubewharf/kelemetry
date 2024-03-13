#!//bin/ash

set -euo pipefail

kubectl get pod -o json -n ${RELEASE_NAMESPACE} \
	-l app.kubernetes.io/name=kelemetry \
	-l app.kubernetes.io/instance=${RELEASE_NAME} \
	| jq -c '.items[]' >pods.jsonl

<pods.jsonl jq -r '. as $pod | .status.conditions[] | select(.status == "False") | $pod.metadata.name + ": " + .message'

<pods.jsonl jq -r '
	. as $pod
	| .spec.containers[].ports // []
	| .[]
	| select(.name == "metrics" or .name == "admin")
	| $pod.metadata.labels["app.kubernetes.io/component"] + " " + $pod.metadata.name + " " + $pod.status.podIP + " " + (.containerPort | tostring)
' | \
	while read comp pod addr port; do
		curl -sSo tmp.prom http://${addr}:${port}/metrics || continue
		prom2json tmp.prom | jq -c \
				--arg comp $comp \
				--arg pod $pod \
			'.[]
				| .name as $name
				| .type as $type
				| .metrics[]
				| {comp: $comp, pod: $pod, name: $name} + .
				| if $type == "HISTOGRAM" then
						.count |= tonumber
						| .sum |= tonumber
						| .buckets |= map_values(tonumber)
					elif $type == "SUMMARY" then
						.count |= tonumber
						| .sum |= tonumber
						| if has("quantiles") then
								.quantiles |= map_values(tonumber)
							else . end
					else
						.value |= tonumber
					end'
	done >metrics.jsonl

webhook_requests=$(jq -sr 'map(select(.name == "audit_webhook_request_count")) | map(.value) | add' metrics.jsonl)
echo Received $webhook_requests webhook requests
if [ $webhook_requests -eq 0 ]; then
	echo "[!]" Check if audit webhook was configured correctly
fi

consumed_audits=$(jq -sr 'map(select(.name == "audit_consumer_event_count" and .labels.hasTrace == "true")) | map(.value) | add' metrics.jsonl)
echo Consumed $consumed_audits audit events with an audit span

handled_events=$(jq -sr 'map(select(.name == "event_handle_count")) | map(.value) | add' metrics.jsonl)
ok_events=$(jq -sr 'map(select(.name == "event_handle_count" and .labels.error == "<nil>")) | map(.value) | add' metrics.jsonl)
event_errs=$(jq -sr '
	map(select(.name == "event_handle_count" and .value > 0 and .labels.error != "<nil>" and .labels.error != "BeforeRestart" and .labels.error != "Filtered"))
	| group_by(.labels.error)
	| map({error: .[0].labels.error, value: map(.value) | add})
	| sort_by(-.value)
	| map(.error + " (" + (.value | tostring) + ")")
	| join(", ")
' metrics.jsonl)

echo -n Handled $handled_events k8s events, $ok_events have spans
if [ ! -z "$event_errs" ]; then
	echo , with errors: "$event_errs"
else
	echo
fi

sent_spans=$(jq -sr 'map(select(.name == "aggregator_send_count")) | map(.value) | add' metrics.jsonl)
echo Sent $sent_spans non-pseudo spans

jq -sr \
	'map(
		select((.name == "jaeger_collector_spans_received_total" or .name == "jaeger_collector_spans_rejected_total") and .value > 0)
		| {
			svc: .labels.svc,
			action: (if .name == "jaeger_collector_spans_received_total" then "received" else "rejected" end),
			value: .value,
		}
	)
	| group_by([.svc, .action])
	| map({svc: .[0].svc, action: .[0].action, value: map(.value) | add})
	| sort_by(-.value)
	| map("jaeger-collector " + .action + " " + (.value | tostring) + " " + .svc + " spans")
	| join("\n")
	' metrics.jsonl
