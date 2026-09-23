variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "instance_type" {
  type    = string
  default = "t3.small"
}

variable "ssh_allowed_cidr" {
  description = "Your IP in CIDR form, e.g. 203.0.113.5/32"
  type        = string
}

variable "public_key_path" {
  type    = string
  default = "~/.ssh/id_ed25519.pub"
}
