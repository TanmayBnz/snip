#!/bin/bash
set -euo pipefail
curl -sfL https://get.k3s.io | sh -
mkdir -p /home/ubuntu/.kube
cp /etc/rancher/k3s/k3s.yaml /home/ubuntu/.kube/config
chown -R ubuntu:ubuntu /home/ubuntu/.kube
sed -i "s/127.0.0.1/$(curl -s http://169.254.169.254/latest/meta-data/local-ipv4)/" /home/ubuntu/.kube/config
