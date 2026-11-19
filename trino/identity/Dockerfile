# Every program in cmd/ (the reverse proxy and the Metabase sync), built into one image. It has no entrypoint:
# compose and Helm pick the program with the container's command, e.g. ["reverse-proxy"].
# Based on: https://hub.docker.com/_/golang#start-a-go-instance-in-your-app
FROM golang:1.27.3 AS build

WORKDIR /usr/src/app

# pre-copy/cache go.mod for pre-downloading dependencies and only redownloading them in subsequent builds if they change
#COPY go.mod go.sum ./ (add back go.sum if dependencies are added)
COPY go.mod ./
RUN go mod download

COPY . .
RUN go test -v ./...
# One binary per program in cmd/, named after its directory. Static, so they don't depend on the runtime
# image's libc.
RUN CGO_ENABLED=0 go build -v -trimpath -ldflags='-s -w' -o /out/ ./cmd/...

# Alpine rather than distroless so probes and admins have a shell.
FROM alpine:3.24.2

COPY --from=build /out/ /usr/local/bin/
# Where the Metabase sync writes Trino's group file. Compose mounts a fresh named volume here, which takes this
# directory's owner, so the sync can write to it.
RUN mkdir /groups && chown nobody:nobody /groups
USER nobody
