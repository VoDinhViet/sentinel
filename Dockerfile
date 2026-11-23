# Sentinel image: the binary built by `make build` plus the tools it runs: restic, rclone, pg_dump, mariadb-dump and mongodump.
#
#   make docker                      # build + docker build -t sentinel:latest
#   docker run --rm sentinel --help
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata restic rclone postgresql-client mariadb-client mongodb-tools \
	&& adduser -D -u 10001 sentinel && install -d -o sentinel /var/lib/sentinel
COPY bin/sentinel /usr/local/bin/sentinel
# Runs as root by default so file backups can read everything; database-only
# setups can use `user: "10001"` in compose.
ENTRYPOINT ["sentinel"]
CMD ["--help"]
