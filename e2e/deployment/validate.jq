include "assert";
include "graph";

.data[0]
| ._ = (
  # check tags
  .spans
    | root
    | .tags | from_entries
    | assertEq("root span is deployment"; .resource; "deployments")
    | assertEq("root span has the correct name"; .name; "demo")
) | ._ = (
  .spans
    | root.logs
    | map(.fields |= from_entries)
    | assert(
      "delete operation was logged (" + (map(.fields.audit) | @json) + ")";
      any(.fields.audit != null and (.fields.audit | endswith(" delete")))
    )
    | assertEq(
      "delete operation contains snapshot"; "demo";
      .[]
        | select(.fields.audit != null and (.fields.audit | endswith(" delete")))
        | .fields.snapshot
        | fromjson
        | .metadata.name
    )
    | assert(
      "status update was logged";
      any(.fields.audit == "kwok-admin update status")
    )
    | assert(
      "status update contains diff (" + (map(.fields.audit) | @json) + ")";
      map(select(.fields.diff))
        | any(.fields.diff | contains("status.observedGeneration 1 -> 2"))
    )
    | assert(
      "diff never contains managedFields";
      map(select(.fields.diff))
        | any(.fields.diff | contains("metadata.managedFields")) | not
    )
) | ._ = (
  .spans
    | children(root.spanID)
    | assertEq("there are two child replicasets"; length; 2)
)
| null
