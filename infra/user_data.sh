#!/bin/bash
set -euo pipefail

# t3.micro has no swap by default; k3s's control plane alone gets close to the
# 1GB RAM ceiling, and with zero swap the kernel thrashes instead of degrading
# gracefully under pressure. A 1GB swapfile keeps the documented RAM budget
# honest while giving it headroom to survive spikes (e.g. right after boot,
# while containerd/apt/cloud-init are all competing for CPU credits too).
fallocate -l 1G /swapfile
chmod 600 /swapfile
mkswap /swapfile
swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab

curl -sfL https://get.k3s.io | sh -
mkdir -p /home/ubuntu/.kube
cp /etc/rancher/k3s/k3s.yaml /home/ubuntu/.kube/config
chown -R ubuntu:ubuntu /home/ubuntu/.kube
sed -i "s/127.0.0.1/$(curl -s http://169.254.169.254/latest/meta-data/local-ipv4)/" /home/ubuntu/.kube/config
echo "KUBECONFIG=/home/ubuntu/.kube/config" >> /etc/environment
