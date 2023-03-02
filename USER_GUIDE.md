# User guide

Kelemetry uses Jaeger UI for trace view.

## Trace search

The first page is for locating a search.

We override the semantics of some terms in the Jaeger search form:

### Display mode

The "Service" field selects one of the display modes:

- `tracing` (**recommended**): One object takes one span.
  The span hierarchy indicates the ownership relation between objects.
  Events are displayed as logs under the object.
- `grouped`: Similar to `tracing`, but each data source displays as a child span.
- `tree`: Similar to `tracing`, except events have their own spans under the object.
  Additional information is available in event tags.
- `timeline`: All events are displayed as children of the root object.

By default, the whole trace is displayed, including parent and sibling objects of the searched object.
Enabling the `exclusive` option limits the output to the subtree under the object matched in the search.

### Cluster

Kelemetry supports multi-cluster tracing.
The "Operation" field limits the search results to a single cluster.

### Tags

Tags are used to search traces based on the actual object.
Currently, the following tags are supported:

- `group`: Kubernetes API group, e.g. `apps`.
- `resource`: Kubernetes API resource plural name, e.g. `deployments.
- `namespace`: Namespace of the API object. Empty for cluster-scoped objects.
- `name`: Name of the API object.

### Time

Currently, all traces are **rounded down** to the **newest half-hour before the event**.
That is, if you want to look for an event that happened at 12:34,
you should search for the trace at 12:30 instead.
Searching between 12:33 and 12:35 will yield **no search results**.

Each trace lasts for exactly 30 minutes, so the max/min duration fields are unsupported.

## Trace view

Click on a search result in trace view to access it.
If the current half-hour is still in progress, reload the page to load the new data.

For the recommended `tracing` display mode,

- The timestamps are relative to the trace start, which is either `:00` or `:30` of an hour.
- Click on the arrow button on the left to collapse/expand a span.
- Click on the empty space on a span row to reveal details of the span.
- Hover cursor over a black vertical line on the span to reveal the events.

> Protip: The 3rd to 10th characters in the trace ID (`20000000` in `ff20000000e1e3e7`) is the display mode ID.
> Just edit it in the URL directly to switch to another display mode.
