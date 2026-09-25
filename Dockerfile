FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nextupdate ./cmd/nextupdate

FROM alpine:3.22
RUN apk add --no-cache docker-cli docker-cli-compose ca-certificates tzdata
COPY --from=build /out/nextupdate /usr/local/bin/nextupdate
ENV NEXTUPDATE_DATA=/data
VOLUME /data
ENTRYPOINT ["nextupdate"]
CMD ["check"]
