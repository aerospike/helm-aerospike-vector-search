group "default" {
  targets = ["avs-init-container"]
}

target "avs-init-container" {
  dockerfile = "./avs-init-container/Dockerfile"
  context    = "./avs-init-container"
  platforms  = ["linux/amd64", "linux/arm64"]
  tags       = [
  ]
}
