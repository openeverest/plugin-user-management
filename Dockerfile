# ──────────────────────────────────────────────────────────────────
# Stage 1 — Build the Go backend.
# Expects dist/main.js to be pre-built (npm run build) and present
# in the Docker build context.
# Runs on the build machine's platform and cross-compiles for the target:
# building arm64 under QEMU emulation takes over 20 minutes.
# ──────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend-builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ .
COPY dist/main.js ./dist/main.js

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o server .

# ──────────────────────────────────────────────────────────────────
# Stage 2 — Minimal runtime image.
# ──────────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk --no-cache add ca-certificates

COPY --from=backend-builder /app/server /usr/local/bin/server

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["server"]
