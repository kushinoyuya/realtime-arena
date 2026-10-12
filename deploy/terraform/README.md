# Terraform — run the arena as a managed container

Runs the built image via the Docker provider: no cloud account needed, but the
same declarative shape as a real deployment (immutable image, declared ports,
restart policy).

```bash
# from the repo root, build the image first:
docker build -t realtime-arena:latest .

cd deploy/terraform
terraform init
terraform apply      # -> outputs http://localhost:8080
terraform destroy    # tear down
```

To deploy for real, replace `docker_container` with
`google_cloud_run_v2_service` or `kubernetes_deployment` + `kubernetes_service`
and point the image at a registry. The server is a single stateless process
(rooms are in-memory and rebuild as clients reconnect), so it scales out by
running more replicas.
