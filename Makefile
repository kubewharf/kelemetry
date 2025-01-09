default: run

SHELL := /usr/bin/env bash

RUN_ID := $(shell head /dev/urandom | cksum | md5sum | head -c8)

NUM_PARTITIONS ?= 5
PARTITIONS ?= $(shell seq -s, 0 $$(($(NUM_PARTITIONS) - 1)) | sed 's/,$$//')

KUBECONFIG ?= ~/.kube/config
CLUSTER_NAME ?= tracetest
KUBECONFIGS ?= $(CLUSTER_NAME)=$(KUBECONFIG)

PORT ?= 8080
LOG_LEVEL ?= info
KLOG_VERBOSITY ?= 3

RACE_ARG := -race
ifdef SKIP_DETECT_RACE
	RACE_ARG :=
endif

DUMP_FILE ?= dump.json
ifneq ($(DUMP_FILE),)
	DUMP_ARG := --audit-dump-file="$(DUMP_FILE)"
	DUMP_ROTATE_DEP := dump-rotate
endif

DUMP_ROTATE ?= 5

ifdef LOG_FILE
	LOG_FILE_ARG ?= --log-file="$(LOG_FILE)"
else
	LOG_FILE_ARG ?=
endif

LINKER_WORKER_COUNT ?= 1

CONTROLLERS ?= audit-consumer,audit-producer,audit-webhook,event-informer,annotation-linker,owner-linker,resource-object-tag,resource-event-tag,diff-decorator,diff-controller,diff-api,pprof,jaeger-storage-plugin,jaeger-redirect-server,kelemetrix
ifeq ($(CONTROLLERS),)
	ENABLE_ARGS ?=
else
	ENABLE_ARGS ?= --$(shell echo $(CONTROLLERS) | sed -e 's/,/-enable --/g')-enable
endif

ifdef ENABLE_TLS
	TLS_ARGS := --http-tls-cert=hack/cert --http-tls-key=hack/key
else
	TLS_ARGS :=
endif

OTEL_EXPORTER_OTLP_ENDPOINT ?= 127.0.0.1:4317

INTEGRATION_ARG :=
ifdef INTEGRATION
	INTEGRATION_ARG := -tags=integration
endif

ETCD_OR_LOCAL := local
ifdef USE_ETCD
	ETCD_OR_LOCAL := etcd
endif

OS_NAME ?= $(shell uname -s)
ifeq ($(OS_NAME), Darwin)
	TAG := $(shell git describe --always --dirty --tags)
else
	TAG := $(shell git describe --always)-$(shell git diff --exit-code >/dev/null && echo clean || (echo dirty- && git ls-files | xargs cat --show-all | crc32 /dev/stdin))
endif

KWOK_VERSION ?= latest

.PHONY: run dump-rotate test usage dot kwok kwok-apiserver-host stack pre-commit fmt local-docker-build e2e
run: output/kelemetry $(DUMP_ROTATE_DEP)
	GIN_MODE=debug \
		$(RUN_PREFIX) ./output/kelemetry $(RUN_SUFFIX) \
		--mq=local \
		--audit-consumer-partition=$(PARTITIONS) \
		--http-address=0.0.0.0 \
		--http-port=$(PORT) \
		$(DUMP_ARG) \
		$(TLS_ARGS) \
		--kube-target-cluster=$(CLUSTER_NAME) \
		--kube-target-rest-burst=100 \
		--kube-target-rest-qps=100 \
		--kube-config-paths $(KUBECONFIGS) \
		--klog-v=$(KLOG_VERBOSITY) \
		--log-level=$(LOG_LEVEL) \
		--log-file=$(LOG_FILE) \
		--aggregator-pseudo-span-global-tags=runId=$(RUN_ID) \
		--aggregator-event-span-global-tags=run=$(RUN_ID) \
		--linker-worker-count=$(LINKER_WORKER_COUNT) \
		--pprof-addr=:6030 \
		--diff-cache=$(ETCD_OR_LOCAL) \
		--diff-cache-etcd-endpoints=127.0.0.1:2379 \
		--diff-cache-wrapper-enable \
		--diff-controller-leader-election-enable=false \
		--diff-writer-leader-election-enable=false \
		--event-informer-leader-election-enable=false \
		--span-cache=$(ETCD_OR_LOCAL) \
		--span-cache-etcd-endpoints=127.0.0.1:2379 \
		--tracer-otel-endpoint=$(OTEL_EXPORTER_OTLP_ENDPOINT) \
		--tracer-otel-insecure \
		--object-cache-size=16777216 \
		--jaeger-cluster-names=$(CLUSTER_NAME) \
		--jaeger-storage-plugin-address=0.0.0.0:17271 \
		--jaeger-backend=jaeger-storage \
		--jaeger-trace-cache=$(ETCD_OR_LOCAL) \
		--jaeger-trace-cache-etcd-endpoints=127.0.0.1:2379 \
		--jaeger-storage.span-storage.type=grpc \
		--jaeger-storage.grpc-storage.server=127.0.0.1:17272 \
		$(ENABLE_ARGS) \
		$(REST_ARGS)

dump-rotate:
	for i in $(shell seq 1 $(DUMP_ROTATE) | tac); do \
		if [ -f dump$$i.json ]; then \
			mv dump$$i.json dump$$(( i + 1 )).json; \
		fi; \
	done
	[ ! -f dump.json ] || mv dump.json dump1.json

test:
	go test -v -race -coverpkg=./pkg/... -coverprofile=coverage.out $(INTEGRATION_ARG) $(BUILD_ARGS) ./pkg/...

usage: output/kelemetry
	$(RUN_PREFIX) ./output/kelemetry $(RUN_SUFFIX) --usage=USAGE.txt

dot: output/kelemetry
	$(RUN_PREFIX) ./output/kelemetry $(RUN_SUFFIX) --dot=depgraph.dot
	dot -Tpng depgraph.dot >depgraph.png
	dot -Tsvg depgraph.dot >depgraph.svg

FIND_PATH =
ifeq ($(OS_NAME), Darwin)
	FIND_PATH = .
endif

output/kelemetry: go.mod go.sum $(shell find $(FIND_PATH) -type f -name "*.go")
	go build -v $(RACE_ARG) -gcflags=$(GCFLAGS) -ldflags=$(LDFLAGS) -o $@ $(BUILD_ARGS) .

kwok:
	command -v kwokctl || go install sigs.k8s.io/kwok/cmd/kwokctl@$(KWOK_VERSION)
	command -v kwok || go install sigs.k8s.io/kwok/cmd/kwok@$(KWOK_VERSION)

	[ ! -d ~/.kwok/clusters/tracetest ] || kwokctl delete cluster --name tracetest
	for file in audit-kubeconfig tracing-config; do \
		cp hack/$${file}.yaml hack/$${file}.local.yaml; \
	done

	cd hack && kwokctl create cluster --name tracetest --config kwok-cluster.yaml

	[[ $(OS_NAME) == Darwin ]] || make kwok-apiserver-host

	kubectl --context=kwok-tracetest create -f hack/kwok-node.yaml

kwok-apiserver-host:
	sed "s/host.docker.internal/$$( \
		docker network inspect kwok-tracetest -f '{{(index .IPAM.Config 0).Gateway}}' \
	)/g" hack/audit-kubeconfig.yaml >hack/audit-kubeconfig.local.yaml
	sed "s/host.docker.internal/$$( \
		docker network inspect kwok-tracetest -f '{{(index .IPAM.Config 0).Gateway}}' \
	)/g" hack/tracing-config.yaml >hack/tracing-config.local.yaml

	# Restart apiserver to reload config changes
	docker restart kwok-tracetest-kube-apiserver

CHOWN_BADGER ?= yes
ifeq ($(CHOWN_BADGER), yes)
	PROFILE_PREFIX := --profile chown-badger-volume
endif

COMPOSE_COMMAND ?= $(PROFILE_PREFIX) up --build -d --remove-orphans

stack:
	docker compose -f dev.docker-compose.yaml up --no-recreate --no-start # create network only
	docker compose \
		-f dev.docker-compose.yaml \
		-f <(jq -n \
			--arg GATEWAY_ADDR $$(docker network inspect kelemetry_default -f '{{(index .IPAM.Config 0).Gateway}}') \
			'.services["jaeger-query"].environment.GRPC_STORAGE_SERVER = $$GATEWAY_ADDR + ":17271"' \
		) \
		$(COMPOSE_COMMAND)

define QUICKSTART_JQ_PATCH
		if $$KELEMETRY_IMAGE == "" then .services.kelemetry.build |= (.dockerfile = "./Dockerfile" | .context = ".") else . end |
		if $$KELEMETRY_IMAGE != "" then .services.kelemetry.image = $$KELEMETRY_IMAGE else . end
endef

SED_I_FLAG =
ifeq ($(OS_NAME), Darwin)
  SED_I_FLAG = ''
endif

ifneq (, $(shell command -v go))
	export GOPROXY=$(shell go env GOPROXY)
	export GOPRIVATE=$(shell go env GOPRIVATE)
	export GONOPROXY=$(shell go env GONOPROXY)
	export GONOSUMDB=$(shell go env GONOSUMDB)
endif

export QUICKSTART_JQ_PATCH
quickstart:
	docker compose -f quickstart.docker-compose.yaml \
		-f <(jq -n --arg KELEMETRY_IMAGE "$(KELEMETRY_IMAGE)" "$$QUICKSTART_JQ_PATCH") \
		up --no-recreate --no-start $(BUILD_ARGS)
	docker inspect kwok-tracetest-kube-apiserver -f '{{.NetworkSettings.Networks.kelemetry_default}}' | grep kwok-tracetest-kube-apiserver >/dev/null || \
		docker network connect kelemetry_default kwok-tracetest-kube-apiserver || true
	kubectl --context=kwok-tracetest config view --raw --minify --flatten --merge >hack/client-kubeconfig.local.yaml

	sed -i $(SED_I_FLAG) "s/127\.0\.0\.1:[0-9]*/kwok-tracetest-kube-apiserver:6443/g" hack/client-kubeconfig.local.yaml
	sed -i $(SED_I_FLAG) 's/certificate-authority-data: .*$$/insecure-skip-tls-verify: true/' hack/client-kubeconfig.local.yaml

	docker compose -f quickstart.docker-compose.yaml \
		-f <(jq -n --arg KELEMETRY_IMAGE "$(KELEMETRY_IMAGE)" "$$QUICKSTART_JQ_PATCH") \
		$(COMPOSE_COMMAND)

pre-commit: dot usage test
	golangci-lint run --new-from-rev=main

fmt:
	git add -A
	go run mvdan.cc/gofumpt -l -w .
	go run github.com/segmentio/golines -m140 --base-formatter=gofumpt -w .
	go run golang.org/x/tools/cmd/goimports -l -w .
	go run github.com/daixiang0/gci write -s standard -s default -s 'prefix(github.com/kubewharf/kelemetry)' .

local-docker-build:
	make output/kelemetry
	cp hack/tfconfig.yaml output
	docker build --build-arg BIN_FILE=kelemetry --build-arg TFCONFIG=tfconfig.yaml -f ./hack/local.Dockerfile -t kelemetry:local output

e2e: local-docker-build
	make quickstart COMPOSE_COMMAND='down --remove-orphans --volumes' KELEMETRY_IMAGE=kelemetry:local
	make quickstart COMPOSE_COMMAND='--profile chown-badger-volume up --build -d --remove-orphans' KELEMETRY_IMAGE=kelemetry:local
	bash e2e/run-all.sh
