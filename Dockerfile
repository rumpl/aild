# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /aild ./cmd/aild

FROM scratch
COPY --from=builder /aild /bin/aild
ENTRYPOINT ["/bin/aild"]
