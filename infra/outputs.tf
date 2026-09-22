output "public_ip" {
  value = aws_instance.snip.public_ip
}

output "ssh_command" {
  value = "ssh ubuntu@${aws_instance.snip.public_ip}"
}
