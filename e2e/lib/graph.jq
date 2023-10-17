include "assert";

def root:
  map(select(.references | length == 0))
  | assertEq("only one root span"; length; 1)
  | .[0]
;

def children($spanId):
  map(select(.references | any(.spanID == $spanId)))
;
