include "assert";
include "graph";

.data[0]
| ._ = (
  # check root
  .spans
    | root
    | .tags | from_entries
    | assertEq("root span is deployment"; .resource; "deployments")
    | assertEq("root span has the correct name"; .name; "ancestors")
) | ._ = (
  # root only has one child
  .spans
    | children(root.spanID)
    | assertEq("there is only one child replicaset"; length; 1)
)
| null
