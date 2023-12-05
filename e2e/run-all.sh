#!/usr/bin/env bash

set -euo pipefail

cd $(dirname $0)

export REPO_PATH=$(realpath ..)

export TEST_DISPLAY_MODE="21000000"

export DISPLAY_MODES="${TEST_DISPLAY_MODE} ${DISPLAY_MODES:-}"

run_test() {
	local test_name=$1

	local tmpdir=$(mktemp -d)

	if [[ ! -v IS_RERUN ]]; then
		bash -x ${test_name}/client.sh
	fi

	source ${test_name}/config.sh

	local curl_param_string=""
	for curl_param_key in "${!TRACE_SEARCH_TAGS[@]}"; do
		local curl_param_value="${TRACE_SEARCH_TAGS[$curl_param_key]}"
		curl_param_string="${curl_param_string} --data-urlencode ${curl_param_key}=${curl_param_value}"
	done

	curl -i -G --data-urlencode ts="$(date --iso-8601=seconds)" \
		${curl_param_string} \
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
		test -d ${OUTPUT_TRACE}/api/traces || mkdir -p ${OUTPUT_TRACE}/api/traces || true
		test -d ${OUTPUT_TRACE}/trace-${test_name} || mkdir ${OUTPUT_TRACE}/trace-${test_name}
		echo "${TRACE_DISPLAY_NAME}" >${OUTPUT_TRACE}/trace-${test_name}/trace_display_name
		echo ${fixed_id} >${OUTPUT_TRACE}/trace-${test_name}/trace_id

		for mode in ${DISPLAY_MODES}; do
			local mode_trace_id=ff${mode}${fixed_id}
			curl -o ${OUTPUT_TRACE}/api/traces/${mode_trace_id} localhost:16686/api/traces/${mode_trace_id}
		done
	fi

	local test_mode_trace_id=ff${TEST_DISPLAY_MODE}${fixed_id}
	curl -o ${tmpdir}/${test_mode_trace_id}.json localhost:16686/api/traces/${test_mode_trace_id}
	go run github.com/itchyny/gojq/cmd/gojq -L lib -f ${test_name}/validate.jq ${tmpdir}/${test_mode_trace_id}.json
}

declare -A pids

for client in */client.sh; do
	test_name=$(basename $(dirname $client))
	run_test ${test_name} &
	pids[$test_name]=$!
done

failed_tests=()
for test_name in ${!pids[@]}; do
	wait ${pids[$test_name]} || failed_tests+=($test_name)
done
if [[ ${#failed_tests[@]} -gt 0 ]]; then
	echo "Tests failed: ${failed_tests[@]}"
	exit 1
fi
