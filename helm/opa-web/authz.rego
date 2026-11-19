package system.authz

# This is the authorization policy for the web pod's OPA only, not Trino's. OPA consults it when started with
# --authorization=basic. It keeps the API to reading documents and asking for decisions: GET and POST under
# /v1/data. Without it, OPA accepts unauthenticated writes, and anyone who can reach its port could replace
# the policy that decides who sees the docs and admin pages.

default allow := false

allow if {
	input.method in {"GET", "POST"}
	input.path[0] == "v1"
	input.path[1] == "data"
}
