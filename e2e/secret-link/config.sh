kubectl create -f ${REPO_PATH}/crds/samples/pod-volume.yaml

kubectl create secret generic content

kubectl create -f - << EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: reader
  labels:
    app: reader
spec:
  selector:
    matchLabels:
      app: reader
  replicas: 2
  template:
    metadata:
      labels:
        app: reader
    spec:
      containers:
        - name: reader
          command: ["sleep", "infinity"]
          image: alpine:3.16
          volumeMounts:
            - name: content
              mountPath: /mnt/content
      volumes:
        - name: content
          secret:
            secretName: content
EOF
sleep 10

kubectl delete deployment/reader secret/content
sleep 20

# config
local TEST_DISPLAY_MODE="24100000" # tracing, owned objects, volumes
local TRACE_DISPLAY_NAME="Volume links"

TRACE_SEARCH_TAGS[cluster]=tracetest
TRACE_SEARCH_TAGS[group]=apps
TRACE_SEARCH_TAGS[resource]=deployments
TRACE_SEARCH_TAGS[namespace]=default
TRACE_SEARCH_TAGS[name]=reader
