# Build stage: compile a fully static linux/amd64 binary, the Fly Machine target.
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Cache dependencies before copying source so small source changes do not
# invalidate the module download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary (SQLite is the pure-Go modernc driver),
# which is what the minimal runtime image requires.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/gamedrop ./cmd/gamedrop

# ---------------------------------------------------------------------------
# Runtime stage.
#
# The default base is distroless static: no shell, no package manager, CA
# certificates included (needed for HTTPS to storefronts and Telegram), and a
# pre-created non-root user. There is deliberately no `RUN` instruction here:
# distroless has no shell, so image setup must happen in the builder stage.
#
# Registry mirror without distroless? Build with:
#   docker build --build-arg GAMEDROP_BASE=alpine:3.21 -t gamedrop .
# An alpine base has no `nonroot` user; that fallback therefore also needs a
# matching user or a modified USER line. Distroless nonroot is the supported
# image because it provides the user and CA certificates out of the box.
# ---------------------------------------------------------------------------
ARG GAMEDROP_BASE=gcr.io/distroless/static-debian12:nonroot
FROM ${GAMEDROP_BASE}

# Production database location: /data is the Fly Volume mount point. The
# Dockerfile does not mount anything; the Machine definition
# (`fly machine run --volume ...:/data`) does. The process creates the directory
# if it is missing (for example when running the image locally without a volume).
ENV DATABASE_PATH=/data/offers.db
ENV APP_MODE=production
ENV LOG_LEVEL=info

COPY --from=builder /out/gamedrop /gamedrop

# Distroless nonroot already runs as uid 65532 (nonroot). Stating it explicitly
# keeps the intent clear and fails loudly if a base without that user is used.
USER nonroot:nonroot

# GameDrop is a one-shot worker: the container runs one collection cycle and
# exits. There is no server, so no ports are exposed.
CMD ["/gamedrop"]
