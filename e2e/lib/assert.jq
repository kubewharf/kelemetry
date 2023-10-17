def assert(msg; condition):
  if condition then . else
    error("Failed assertion: " + msg)
  end
;

def assertEq(msg; left; right):
  assert(
    msg + " (" + (left | @json) + " != " + (right | @json) + ")";
    left == right
  )
;
