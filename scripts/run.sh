#!/bin/bash
set -e

# Configuration
VM_NAME="project-sem-1-hard-$(date +%s)"
ZONE="ru-central1-a"
IMAGE_FAMILY="ubuntu-2204-lts"
SSH_KEY_PATH="$HOME/.ssh/deploy_key"
SSH_PUB_KEY_PATH="$HOME/.ssh/deploy_key.pub"

# Validate SSH keys
if [ ! -f "$SSH_PUB_KEY_PATH" ]; then
    echo "Error: SSH public key not found at $SSH_PUB_KEY_PATH"
    exit 1
fi

echo "Creating Yandex Cloud VM: $VM_NAME..."

# Create VM instance
INSTANCE_ID=$(yc compute instance create \
  --name $VM_NAME \
  --zone $ZONE \
  --network-interface subnet-name=default-$ZONE,nat-ip-version=ipv4 \
  --create-boot-disk image-family=$IMAGE_FAMILY,size=15 \
  --ssh-key $SSH_PUB_KEY_PATH \
  --format json | grep -oP '"id": "\K[^"]+')

echo "VM Created. ID: $INSTANCE_ID"
echo "Retrieving IP address..."
sleep 5

# Get Public IP
IP=$(yc compute instance get --id $INSTANCE_ID --format json | grep -oP '"address": "\K[^"]+' | head -1)

if [ -z "$IP" ]; then
    echo "Error: Failed to get IP address"
    exit 1
fi

echo "IP Address: $IP"
echo "Waiting for SSH availability..."

# Wait for SSH availability
for i in {1..40}; do
    if ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 -i $SSH_KEY_PATH yc-user@$IP "echo ready" &>/dev/null; then
        echo "SSH is ready."
        break
    fi
    sleep 5
done

# Transfer files (Configs only)
echo "Uploading configuration files..."
ssh -o StrictHostKeyChecking=no -i $SSH_KEY_PATH yc-user@$IP "mkdir -p ~/app"
scp -o StrictHostKeyChecking=no -i $SSH_KEY_PATH \
    docker-compose.yaml init.sql \
    yc-user@$IP:~/app/

# Remote execution: Install Docker and Deploy
echo "Installing Docker and deploying application..."
ssh -o StrictHostKeyChecking=no -i $SSH_KEY_PATH yc-user@$IP <<EOF
    # Docker installation
    curl -fsSL https://get.docker.com -o get-docker.sh
    sudo sh get-docker.sh > /dev/null 2>&1
    
    # Deployment
    cd ~/app
    echo "Pulling image from Docker Hub and starting..."
    sudo docker compose up -d --pull always
EOF

echo "Deployment successful."
# Output IP for tests
echo "$IP"