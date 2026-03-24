FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY migrations/ ./migrations/
COPY skills/ ./skills/
RUN CGO_ENABLED=0 go build -o wonka .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/wonka .
COPY --from=builder /app/migrations ./migrations/

EXPOSE 8090

CMD ["./wonka"]
