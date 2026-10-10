FROM node:22-bookworm-slim AS frontend
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS gonetia
WORKDIR /src/gonetia
RUN git init && git remote add origin https://github.com/yapishu/gonetia && git fetch --depth=1 origin 73ed8b7bd29fde4b22f7fed35e6270b54d9471ea && git checkout --detach FETCH_HEAD && git submodule update --init --recursive
RUN CGO_ENABLED=0 go build -trimpath -o /gonetia .

FROM golang:1.26-bookworm AS backend
WORKDIR /src/netsub-store
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -o /store ./cmd/store

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home store && mkdir /data && chown store:store /data
WORKDIR /app
COPY --from=backend /store /usr/local/bin/store
COPY --from=gonetia /gonetia /usr/local/bin/gonetia
COPY --chmod=755 scripts/container-entrypoint.sh /usr/local/bin/container-entrypoint
COPY --from=frontend /app/web/dist ./web/dist
USER store
ENV LISTEN=0.0.0.0:8080 DB_PATH=/data/store.db WEB_DIR=/app/web/dist
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["container-entrypoint"]
