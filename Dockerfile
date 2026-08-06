# syntax=docker/dockerfile:1

# ---- build stage -------------------------------------------------------
# The frontend (web/app.css) is committed, so the build needs no Node.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /lookout .

# ---- runtime stage -----------------------------------------------------
FROM alpine:3.20
RUN apk add --no-cache tzdata
COPY --from=build /lookout /usr/local/bin/lookout
EXPOSE 7070
ENTRYPOINT ["lookout"]
CMD ["-addr", ":7070"]
