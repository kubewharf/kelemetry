default: run

SHELL := /usr/bin/env bash

RUN_ID := $(shell head /dev/urandom | cksum | md5sum | head -c8)

NUM_PARTITIONS ?= 5
PARTITIONS ?= $(shell seq -s, 0 $$(($(NUM_PARTITIONS) - 1)) | sed 's/,$$//')

KUBECONFIG ?= ~/.kube/config
CLUSTER_NAME ?= tracetest
KUBECONFIGS ?= $(CLUSTER_NAME)=$(KUBECONFIG)

PORT ?= 8080
LOG_LEVEL ?= debug
KLOG_VERBOSITY ?= 5

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

.PHONY: run dump-rotate test usage dot kind stack pre-commit
run: output/kelemetry $(DUMP_ROTATE_DEP)
	GIN_MODE=debug \
		./output/kelemetry \
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
		--pprof-addr=:6030 \
		--diff-cache=$(ETCD_OR_LOCAL) \
		--diff-cache-etcd-endpoints=127.0.0.1:2379 \
		--diff-cache-wrapper-enable \
		--diff-controller-leader-election-enable=false \
		--event-informer-leader-election-enable=false \
		--span-cache=$(ETCD_OR_LOCAL) \
		--span-cache-etcd-endpoints=127.0.0.1:2379 \
		--tracer-otel-endpoint=$(OTEL_EXPORTER_OTLP_ENDPOINT) \
		--tracer-otel-insecure \
		--jaeger-cluster-names=$(CLUSTER_NAME) \
		--jaeger-storage-plugin-address=0.0.0.0:17271 \
		--jaeger-backend=jaeger-storage \
		--jaeger-trace-cache=$(ETCD_OR_LOCAL) \
		--jaeger-trace-cache-etcd-endpoints=127.0.0.1:2379 \
		--jaeger-storage.span-storage.type=grpc-plugin \
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
	./output/kelemetry --usage=USAGE.txt

dot: output/kelemetry
	./output/kelemetry --dot=depgraph.dot
	dot -Tpng depgraph.dot >depgraph.png
	dot -Tsvg depgraph.dot >depgraph.svg

output/kelemetry: go.mod go.sum $(shell find -type f -name "*.go")
	go build -v $(RACE_ARG) -ldflags=$(LDFLAGS) -o $@ $(BUILD_ARGS) .

kind:
	kind delete cluster --name tracetest
	docker network create kind || true # create if not exist; if fail, next step will fail anyway
	sed "s/host.docker.internal/$$( \
		docker network inspect kind -f '{{(index .IPAM.Config 0).Gateway}}' \
	)/g" hack/audit-kubeconfig.yaml >hack/audit-kubeconfig.local.yaml
	cd hack && kind create cluster --config kind-cluster.yaml

COMPOSE_COMMAND ?= up --build -d --remove-orphans

stack:
	docker-compose -f dev.docker-compose.yaml up --no-recreate --no-start # create network only
	docker-compose \
		-f dev.docker-compose.yaml \
		-f <(jq -n \
			--arg GATEWAY_ADDR $$(docker network inspect kelemetry_default -f '{{(index .IPAM.Config 0).Gateway}}') \
			'.version = "2.2" | .services["jaeger-query"].environment.GRPC_STORAGE_SERVER = $$GATEWAY_ADDR + ":17271"' \
		) \
		$(COMPOSE_COMMAND)

define QUICKSTART_JQ_PATCH
		.version = "2.2" |
			if $$KELEMETRY_IMAGE == "" then .services.kelemetry.build = "." else . end |
			if $$KELEMETRY_IMAGE != "" then .services.kelemetry.image = $$KELEMETRY_IMAGE else . end
endef

export QUICKSTART_JQ_PATCH
quickstart:
	docker-compose -f quickstart.docker-compose.yaml \
		-f <(jq -n --arg KELEMETRY_IMAGE "$(KELEMETRY_IMAGE)" "$$QUICKSTART_JQ_PATCH") \
		up --no-recreate --no-start
	kubectl config view --raw --minify --flatten --merge >hack/client-kubeconfig.local.yaml
	sed -i "s/0\.0\.0\.0/$$(docker network inspect kelemetry_default -f '{{(index .IPAM.Config 0).Gateway}}')/g" hack/client-kubeconfig.local.yaml
	sed -i 's/certificate-authority-data: .*$$/insecure-skip-tls-verify: true/' hack/client-kubeconfig.local.yaml
	docker-compose -f quickstart.docker-compose.yaml \
		-f <(jq -n --arg KELEMETRY_IMAGE "$(KELEMETRY_IMAGE)" "$$QUICKSTART_JQ_PATCH") \
		$(COMPOSE_COMMAND)

pre-commit: dot usage test
	golangci-lint run --new-from-rev=main

fmt:
	git add -A
	gofumpt -l -w .
	golines -m140 --base-formatter=gofumpt -w .
	gci write -s standard -s default -s 'prefix(github.com/kubewharf/kelemetry)' .
