# Scorched API — build image
# NOTE: not verified with a real Go toolchain in this project's sandbox —
# review before relying on it. See AUDIT.md.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
