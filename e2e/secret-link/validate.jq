include "assert";
include "graph";

.data[0]
| ._ = (
  # check tags
  .spans
    | root
    | .tags | from_entries
    | assertEq("root span is deployment"; .resource; "deployments")
    | assertEq("root span has the correct name"; .name; "reader")
) | ._ = (
  # check that secret is linked
  .spans
    | map(select(.tags | from_entries | .resource == "secrets" and .name == "content"))
    | assertEq("has content"; length; 1)
) | null
