# syntax=docker/dockerfile:1

# --- web: собрать панель в server/internal/web/dist ---
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml tsconfig.base.json eslint.config.js ./
COPY apps/web/package.json apps/web/
COPY packages/api-client/package.json packages/api-client/
COPY packages/i18n/package.json packages/i18n/
RUN pnpm install --frozen-lockfile
COPY api ./api
COPY apps ./apps
COPY packages ./packages
RUN pnpm -F web build

# --- go: статический бинарник с вшитым вебом ---
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server ./
COPY --from=web /src/server/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags webdist \
      -ldflags "-s -w \
        -X github.com/varrcan/koreader-sync-board/server/internal/buildinfo.Version=${VERSION} \
        -X github.com/varrcan/koreader-sync-board/server/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/ksb ./cmd/ksb \
 && mkdir /data-skel

# --- final: без shell и Node ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/ksb /ksb
# В distroless нет shell для mkdir/chown: каталог с нужным владельцем приносится
# копированием, и новый именованный том при первом монтировании его унаследует.
COPY --from=go --chown=65532:65532 /data-skel /data
ENV KSB_DATA_DIR=/data KSB_LISTEN=:8080
VOLUME /data
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/ksb", "healthcheck"]
ENTRYPOINT ["/ksb"]
CMD ["serve"]
