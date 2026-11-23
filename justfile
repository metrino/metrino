gofmt_then_test := 'unformatted=$(gofmt -l .); [ -z "$unformatted" ] || { echo "Not gofmt-formatted:"; echo "$unformatted"; exit 1; }; go test ./...'

default:
	just --list

compose-up:
	docker compose up --build

compose-down:
	docker compose down

reset:
	docker compose down -v
	just compose-up

test:
	postman collection run "./postman/collections/Integration Tests" \
		-e postman/environments/Testing\ Environment.environment.yaml

# The reverse proxy's tests and the shared Metabase client's (trino/identity), then the web server's (web).
# Each first fails on Go that gofmt would change, as CI does. Needs docker.
test-go:
	docker run --rm -v "{{justfile_directory()}}/trino/identity:/src" -w /src golang:1.27.3 sh -c '{{gofmt_then_test}}'
	docker run --rm -v "{{justfile_directory()}}/web:/src" -w /src golang:1.27.3 sh -c '{{gofmt_then_test}}'

# The OPA policy tests, with the sample rules compose uses. The second run is the web OPA's authorization
# policy, which keeps its API read-only. Needs docker.
test-opa:
	docker run --rm -v "{{justfile_directory()}}/helm/opa:/policy:ro" -v "{{justfile_directory()}}/opa:/rules:ro" openpolicyagent/opa:1.21.2-static test /policy /rules -v
	docker run --rm -v "{{justfile_directory()}}/helm/opa-web:/authz:ro" openpolicyagent/opa:1.21.2-static test /authz -v

web-dev:
	cd web/frontend && npm run dev

helm-template image_tag:
	helm template my-amazing-chart ./helm -n abcd \
		--set trino.identityImage.tag="{{image_tag}}" \
		-f example-values.yaml
