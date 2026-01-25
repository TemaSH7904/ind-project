#!/bin/bash
set -e

# Configuration
VM_NAME="project-sem-1-hard"
ZONE="ru-central1-b"
IMAGE_ID="fd8mmisarrj57od5613m" # Ubuntu 22.04 LTS

# SSH Key Setup
if [ -n "$CI" ]; then
    mkdir -p ~/.ssh
    echo "$SSH_PRIVATE_KEY" > ~/.ssh/id_ed25519
    chmod 600 ~/.ssh/id_ed25519
    SSH_KEY_PATH="$HOME/.ssh/id_ed25519"
else
    SSH_KEY_PATH="$HOME/.ssh/deploy_key"
fi

echo "[INFO] Starting Infrastructure Check..."

# 1. Check VM status
if yc compute instance get --name "$VM_NAME" > /dev/null 2>&1; then
    echo "[INFO] VM exists."
    
    STATUS=$(yc compute instance get --name "$VM_NAME" --format json | jq -r '.status')
    if [ "$STATUS" != "RUNNING" ]; then
        echo "[INFO] VM status is $STATUS. Starting instance..."
        yc compute instance start --name "$VM_NAME" > /dev/null 2>&1
    else
        echo "[INFO] VM is already running."
    fi
    
    INSTANCE_ID=$(yc compute instance get --name "$VM_NAME" --format json | jq -r '.id')
else
    echo "[INFO] VM not found. Creating new instance in $ZONE..."
    
    INSTANCE_ID=$(yc compute instance create \
      --name "$VM_NAME" \
      --zone "$ZONE" \
      --network-interface subnet-name=default-$ZONE,nat-ip-version=ipv4 \
      --create-boot-disk image-id=$IMAGE_ID,size=15 \
      --ssh-key "$SSH_KEY_PATH.pub" \
      --format json | jq -r '.id')
    
    echo "[INFO] VM Created. ID: $INSTANCE_ID"
fi

# 2. Retrieve Public IP
IP=$(yc compute instance get --id $INSTANCE_ID --format json | jq -r '.network_interfaces[0].primary_v4_address.one_to_one_nat.address')

if [ "$IP" == "null" ] || [ -z "$IP" ]; then
    echo "[ERROR] Failed to retrieve Public IP."
    exit 1
fi

# Output for GitHub Actions
echo "DYNAMIC_IP_ADDRESS=$IP"
echo "[INFO] Public IP: $IP"

echo "[INFO] Proceeding to Deployment..."

# 3. Wait for SSH availability
echo "[INFO] Waiting for SSH connection..."
for i in {1..30}; do
    if ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 -i "$SSH_KEY_PATH" yc-user@$IP "echo ready" &>/dev/null; then
        echo "[INFO] SSH connection established."
        break
    fi
    sleep 5
done

# 4. File Transfer
echo "[INFO] Uploading configuration files..."
scp -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" docker-compose.yaml init.sql yc-user@$IP:~/

# 5. Remote Docker Execution
echo "[INFO] Executing Docker Compose on remote server..."
ssh -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" yc-user@$IP <<EOF
    set -e
    # Install Docker if missing
    if ! command -v docker &> /dev/null; then
        echo "[REMOTE] Installing Docker..."
        curl -fsSL https://get.docker.com -o get-docker.sh
        sudo sh get-docker.sh
    fi
    
    # Update images and restart services
    echo "[REMOTE] Pulling images and restarting containers..."
    sudo docker compose pull
    sudo docker compose up -d --force-recreate
EOF

echo "[INFO] Deployment completed successfully."