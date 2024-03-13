# Deploying Kelemetry for Production Clusters

> Note: Due to the variety of cloud providers and cluster management solutions,
> deploying Kelemetry for production might be tricky.
> If you just want to try out the features of Kelemetry,
> follow the [quick start guide](QUICK_START.md) instead,
> which sets up a basic stack locally using Docker.

To minimize data loss and latency and ensure high availability,
we recommend deploying Kelemetry in 3 separate components:
consumers, informers and storage plugin.

```mermaid
graph LR
    subgraph sg:self["From Helm chart"]
        subgraph sg:k["Kelemetry"]
            subgraph sg:informers["Informers (3 instances)"]
                event["Event controller (1 leader)"]
                diff/controller["Diff controller (2 leaders)"]
            end
            subgraph sg:consumer["Audit consumer"]
                audit/consumer[Audit consumer]
            end
            subgraph sg:plugin[Storage plugin]
                jaeger/transform[Kelemetry storage plugin]
            end
        end

        subgraph sg:kv["KV store (etcd/redis)"]
            diff/cache[Diff cache]
            spancache[Span cache]
        end

        subgraph sg:jaeger["Jaeger"]
            jaeger/query["Jaeger Query UI"]
            jaeger/storage["Jaeger storage"]
        end
    end

    subgraph sg:cloud[From cloud provider]
        cloud/mq[Audit message queue]
        %% k8s[Kubernetes cluster]
    end

    event --> spancache
    audit/consumer --> spancache
    %% k8s --> cloud/mq
    diff/controller --> diff/cache --> audit/consumer
    %% audit/consumer --> k8s
    %% event --> k8s
    %% diff/controller --> k8s
    audit/consumer --> jaeger/storage
    jaeger/transform --> jaeger/storage
    event --> jaeger/storage
    jaeger/query --> jaeger/transform
    cloud/mq --> audit/consumer

    classDef red fill:#ff8888
    class sg:cloud red
    classDef green fill:#88ff88
    class sg:k green
    classDef blue fill:#98daff
    class sg:jaeger,sg:kv blue
```

This setup is bundled into a Helm chart.

## Steps

1. Download [`values.yaml`](/charts/kelemetry/values.yaml) and configure the settings.
2. Install the chart: `helm install kelemetry oci://ghcr.io/kubewharf/kelemetry-chart --values values.yaml`
3. If you use an audit webhook directly, remember to
   [configure the apiserver](https://kubernetes.io/docs/tasks/debug/debug-cluster/audit/#webhook-backend)
   to send audit logs to the webhook:

The default configuration is designed for single-cluster deployment.
For multi-cluster deployment, configure the `sharedEtcd` and `storageBackend` to use a common database.

### Troubleshooting
Run `kubectl exec kelemetry-scan-0 -- scan`.
It should report a few key metrics and provide suggestions if the metrics look wrong.

All containers running the Kelemetry image export Prometheus metrics on the `metrics` (9090) port.
Jaeger containers export Prometheus metrics on the [`admin` port](https://www.jaegertracing.io/docs/latest/deployment/).
You may set up your own monitoring based on the available metrics.
