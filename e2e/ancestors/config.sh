# script
kubectl create deployment ancestors --image=alpine:3.16 --replicas=2 -- sleep infinity
local first_rs_name=$(kubectl get replicasets --selector app=ancestors -o json | jq -r '.items[0].metadata.name')
sleep 5
kubectl set image deployments ancestors alpine=alpine:3.17
sleep 30
kubectl delete deployments ancestors

# config
local TEST_DISPLAY_MODE="22000000" # tracing, ancestors only
local TRACE_DISPLAY_NAME="Deployment"

TRACE_SEARCH_TAGS[cluster]=tracetest
TRACE_SEARCH_TAGS[group]=apps
TRACE_SEARCH_TAGS[resource]=replicasets
TRACE_SEARCH_TAGS[namespace]=default
TRACE_SEARCH_TAGS[name]=$first_rs_name
