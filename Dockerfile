FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY skills/ ./skills/
RUN CGO_ENABLED=0 go build -o wonka .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/wonka .

EXPOSE 8090
VOLUME /app/pb_data

CMD ["./wonka", "serve", "--http=0.0.0.0:8090"]
