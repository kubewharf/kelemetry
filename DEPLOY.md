# Deploying Kelemetry for Production Clusters

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
TODO
