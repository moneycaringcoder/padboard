# Build
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /padboard .

# Run
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 padboard
USER padboard
WORKDIR /app
COPY --from=build /padboard /app/padboard
ENV PORT=8080 DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/app/padboard"]
