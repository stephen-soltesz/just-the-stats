# just-the-stats
Read Docker stats for kubernetes pods

```sh
# Build the docker image.
docker build -t pod-exporter:10 .

# Run with /var/run mounted for access to /var/run/docker.sock and local port
# for metrics.
docker run -v /var/run:/var/run -p 9988:9988 -it pod-exporter:10 \
  -prometheusx.listen-address=:9988
```
