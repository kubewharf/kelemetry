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

By default, only the trace for a single object is displayed.
More traces are available by configuration:

- `full tree`: view the full tree from the deepest ancestor
- `ancestors`: include transitive owners
- `children`: include child objects

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

Kelemetry merges and truncates traces based on the time range given in the user input.
Only spans and events within this range are displayed.
Some display modes further truncate the time range to the duration from the earliest to the latest event,
so refer to the "Trace start" timestamp indicated in the trace view page.

## Trace view

Click on a search result in trace view to access it.
If the current half-hour is still in progress, reload the page to load the new data.

For the recommended `tracing` display mode,

- The timestamps are relative to the trace start.
- Click on the arrow button on the left to collapse/expand a span.
- Click on the empty space on a span row to reveal details of the span.
- Hover cursor over a black vertical line on the span to reveal the events.

> Protip: The 3rd to 10th characters in the trace ID (`20000000` in `ff20000000e1e3e7`) is the display mode ID.
> Just edit it in the URL directly to switch to another display mode.
