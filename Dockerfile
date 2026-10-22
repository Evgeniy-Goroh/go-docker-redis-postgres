FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /
COPY ./bin/transport /transport

EXPOSE 3001

ENTRYPOINT ["/transport"]
CMD ["run"]