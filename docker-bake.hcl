variable "IMAGE" {
  default = "loom"
}

variable "REVISION" {
  default = "local"
}

group "default" {
  targets = ["api", "file-worker"]
}

target "_common" {
  context = "."
  platforms = ["linux/amd64"]
}

target "api" {
  inherits = ["_common"]
  target = "api"
  tags = ["${IMAGE}:latest", "${IMAGE}:sha-${REVISION}"]
}

target "file-worker" {
  inherits = ["_common"]
  target = "file-worker"
  tags = ["${IMAGE}:file-worker-latest", "${IMAGE}:file-worker-sha-${REVISION}"]
}
