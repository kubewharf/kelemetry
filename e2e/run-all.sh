#!/usr/bin/env bash

set -euo pipefail

cd $(dirname $0)

if [[ ! -v DISPLAY_MODES ]]; then
	export DISPLAY_MODES="01000000 21000000"
fi

run_test() {
	local test_name=$1

	local tmpdir=$(mktemp -d)

	if [[ ! -v IS_RERUN ]]; then
		bash -x ${test_name}/client.sh
	fi

	source ${test_name}/curl-params.sh
	curl -i -G --data-urlencode ts="$(date --iso-8601=seconds)" \
		--data-urlencode cluster=${cluster} \
		--data-urlencode group=${group} \
		--data-urlencode resource=${resource} \
		--data-urlencode namespace=${namespace} \
		--data-urlencode name=${name} \
		-o ${tmpdir}/curl-output.http \
		localhost:8080/redirect

	local full_trace_id=$(grep -P "^Location: /trace/" ${tmpdir}/curl-output.http | cut -d/ -f3 | tr -d '\r')
	if [[ -z $full_trace_id ]]; then
		echo "Trace not found for the parameters"
		cat curl-output.http
		exit 1
	fi
	local fixed_id=${full_trace_id:10}

	if [[ -v OUTPUT_TRACE ]]; then
		mkdir -p output/api/traces
		for mode in ${DISPLAY_MODES}; do
			local mode_trace_id=ff${mode}${fixed_id}
			curl -o output/api/traces/${mode_trace_id} localhost:16686/api/traces/${mode_trace_id}
		done
	fi

	local test_mode_trace_id=ff21000000${fixed_id}
	curl -o ${tmpdir}/${test_mode_trace_id}.json localhost:16686/api/traces/${test_mode_trace_id}
	go run github.com/itchyny/gojq/cmd/gojq -f ${test_name}/validate.jq ${tmpdir}/${test_mode_trace_id}.json
}

for client in */client.sh; do
	test_name=$(basename $(dirname $client))
	run_test ${test_name} &
done
wait
