# Infrastructure as code: run the arena as a managed container.
# This uses the Docker provider so it runs locally with no cloud account, while
# staying the same shape as a real deployment (immutable image, declared ports,
# restart policy). Swapping in google_cloud_run_v2_service / kubernetes_deployment
# would be the next step.
#
#   docker build -t realtime-arena:latest ..
#   terraform init && terraform apply
#   terraform destroy   # tear down

terraform {
  required_version = ">= 1.3"
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
  }
}

provider "docker" {}

resource "docker_image" "arena" {
  name         = var.image
  keep_locally = true
}

resource "docker_container" "arena" {
  name    = "realtime-arena"
  image   = docker_image.arena.image_id
  restart = "unless-stopped"

  ports {
    internal = 8080
    external = var.port
  }
}

output "url" {
  value = "http://localhost:${var.port}"
}
