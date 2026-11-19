# The docs and admin web server (web/), with its Vue pages compiled to static files.
# The Vue pages (web/frontend/), compiled to plain files; Node isn't in the final image.
FROM node:22.23.3-alpine3.24 AS frontend
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# Based on: https://hub.docker.com/_/golang#start-a-go-instance-in-your-app
FROM golang:1.27.3 AS build

WORKDIR /usr/src/app

# pre-copy/cache go.mod for pre-downloading dependencies and only redownloading them in subsequent builds if they change
#COPY go.mod go.sum ./ (add back go.sum if dependencies are added)
COPY go.mod ./
RUN go mod download

COPY . .
# Static, so it doesn't depend on the runtime image's libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/web .

# Alpine rather than distroless: the Kubernetes liveness probe runs wget.
FROM alpine:3.24.2

COPY --from=build /out/web /usr/local/bin/web
COPY --from=frontend /app/dist/ /srv/www/
USER nobody
ENTRYPOINT ["web"]
