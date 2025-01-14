group "default" {
  targets = ["avs-init-container"]
}

target "avs-init-container" {
  context    = "."                             
  dockerfile = "./Dockerfile"
  platforms  = ["linux/amd64", "linux/arm64"]
  tags       = [
  ]
}
