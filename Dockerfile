# syntax=docker/dockerfile:1

# Stage 1 — Build UI
FROM node:22-alpine AS ui-builder
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
ARG VITE_SESSION_ID=default-session
ENV VITE_SESSION_ID=$VITE_SESSION_ID
RUN npm run build

# Stage 2 — Build Go binary with embedded UI
FROM golang:1.26-alpine AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui-builder /ui/dist ./ui/dist
RUN CGO_ENABLED=0 go build -o /app/ag0 ./cmd/ag0

# Stage 3 — Runtime
FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=go-builder /app/ag0 /app/ag0
# Ship example templates only. Real configs are gitignored and must be
# mounted at runtime. The binary errors out with a copy-the-example hint
# if /app/agents.yaml is missing — coordinator.yaml and context.md are
# optional (defaults / no-op when absent).
#
# Example:
#   docker run --rm -p 9090:9090 \
#     -v $(pwd)/agents.yaml:/app/agents.yaml \
#     -v $(pwd)/coordinator.yaml:/app/coordinator.yaml \
#     -v $(pwd)/context.md:/app/context.md \
#     -e ANTHROPIC_API_KEY=... ag0
COPY agents.example.yaml /app/agents.example.yaml
COPY coordinator.example.yaml /app/coordinator.example.yaml
COPY context.example.md /app/context.example.md
EXPOSE 9090
ENV PORT=9090
ENTRYPOINT ["/app/ag0"]