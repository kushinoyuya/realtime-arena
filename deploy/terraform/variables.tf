variable "image" {
  description = "Container image to run (build it first: docker build -t realtime-arena:latest ..)"
  type        = string
  default     = "realtime-arena:latest"
}

variable "port" {
  description = "Host port to expose the game on"
  type        = number
  default     = 8080
}
