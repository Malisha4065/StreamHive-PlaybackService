# Simple playback service build
FROM golang:1.23 as build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o playback ./cmd/playback

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /app/playback /playback
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/playback"]
