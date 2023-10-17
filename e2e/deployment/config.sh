local TRACE_DISPLAY_NAME="Deployment"

declare -A TRACE_SEARCH_TAGS
TRACE_SEARCH_TAGS[cluster]=tracetest
TRACE_SEARCH_TAGS[group]=apps
TRACE_SEARCH_TAGS[resource]=deployments
TRACE_SEARCH_TAGS[namespace]=default
TRACE_SEARCH_TAGS[name]=demo
