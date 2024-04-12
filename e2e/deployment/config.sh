# script
kubectl create deployment demo --image=alpine:3.16 --replicas=2 -- sleep infinity
sleep 5
kubectl scale deployment demo --replicas=4
sleep 5
kubectl set image deployments demo alpine=alpine:3.17
sleep 5
kubectl scale deployment demo --replicas=2
sleep 5
kubectl delete deployment demo
sleep 30

# config
local TEST_DISPLAY_MODE="21000000" # tracing, full tree
local TRACE_DISPLAY_NAME="Deployment"

TRACE_SEARCH_TAGS[cluster]=tracetest
TRACE_SEARCH_TAGS[group]=apps
TRACE_SEARCH_TAGS[resource]=deployments
TRACE_SEARCH_TAGS[namespace]=default
TRACE_SEARCH_TAGS[name]=demo
