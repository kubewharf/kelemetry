def assert(msg; condition):
  if condition then . else error(msg) end
;

def assertEq(msg; left; right):
  assert(
    msg + " (" + (left | @json) + " != " + (right | @json) + ")";
    left == right)
;

def root($spans):
  $spans
  | map(select(.references | length == 0))
  | assertEq("only one root span"; length; 1)
  | .[0]
;

def children($spans; $spanId):
  $spans
  | map(select(.references | any(.spanID == $spanId)))
;

.data[0]
| ._ = (
  # check tags
  .spans
    | root(.)
    | .tags | from_entries
    | assertEq("root span is deployment"; .resource; "deployments")
    | assertEq("root span has the correct name"; .name; "demo")
)  | ._ = (
  .spans
    | root(.).logs
    | map(.fields |= from_entries)
    | assert(
      "delete operation was logged";
      any(.fields.audit == "kubernetes-admin delete")
    )
    | assertEq(
      "delete operation contains snapshot"; "demo";
      .[] | select(.fields.audit == "kubernetes-admin delete")
        | .fields.snapshot
        | fromjson
        | .metadata.name
    )
    | assert(
      "status update was logged";
      any(.fields.audit == "system:serviceaccount:kube-system:deployment-controller update status")
    )
    | assert(
      "status update contains diff";
      map(select(.fields.diff))
        | any(.fields.diff | contains("status.observedGeneration 1 -> 2"))
    )
)  | ._ = (
  .spans
    | children(.; root(.).spanID)
    | assertEq("there are two child replicasets"; length; 2)
)
| null
