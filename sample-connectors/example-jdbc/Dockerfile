# syntax=docker/dockerfile:1
# Built from sample-connectors/, so the image holds both data folders and loads them on first start without
# any mounts (Kubernetes runs it with an empty data directory, so every pod starts from these files).
FROM postgres:18
COPY example-jdbc/database.sql /docker-entrypoint-initdb.d/
COPY example-jdbc/data /data
COPY example-http/data /data-http
