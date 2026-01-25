#!/bin/bash
set -e

# --- КОНФИГУРАЦИЯ ---
VM_NAME="project-sem-1-hard"
ZONE="ru-central1-b" 
IMAGE_ID="fd8mmisarrj57od5613m"

# --- НАСТРОЙКА КЛЮЧЕЙ ---
if [ -n "$CI" ]; then
    mkdir -p ~/.ssh
    echo "$SSH_PRIVATE_KEY" > ~/.ssh/id_ed25519
    chmod 600 ~/.ssh/id_ed25519
    echo "$SSH_PUBLIC_KEY" > ~/.ssh/id_ed25519.pub
    SSH_KEY_PATH="$HOME/.ssh/id_ed25519"
    SSH_PUB_KEY_PATH="$HOME/.ssh/id_ed25519.pub"
else
    SSH_KEY_PATH="$HOME/.ssh/deploy_key"
    SSH_PUB_KEY_PATH="$HOME/.ssh/deploy_key.pub"
fi

echo "🔍 Checking infrastructure in zone $ZONE..."

# --- ПРОВЕРКА МАШИНЫ ---
if yc compute instance get --name "$VM_NAME" > /dev/null 2>&1; then
    echo "✅ VM exists. Ensuring it is running..."
    yc compute instance start --name "$VM_NAME" > /dev/null 2>&1 || true
    INSTANCE_ID=$(yc compute instance get --name "$VM_NAME" --format json | grep -oP '"id": "\K[^"]+')
else
    echo "🚀 Creating NEW VM in zone $ZONE..."
    # Добавлена проверка сети, если вдруг дефолтная отличается
    INSTANCE_ID=$(yc compute instance create \
      --name "$VM_NAME" \
      --zone "$ZONE" \
      --network-interface subnet-name=default-$ZONE,nat-ip-version=ipv4 \
      --create-boot-disk image-id=$IMAGE_ID,size=15 \
      --ssh-key "$SSH_PUB_KEY_PATH" \
      --format json | grep -oP '"id": "\K[^"]+')
fi

# --- ПОЛУЧЕНИЕ ПУБЛИЧНОГО IP ---
echo "🎯 Fetching public IP..."
IP=$(yc compute instance get --id $INSTANCE_ID --format json | jq -r '.network_interfaces[0].primary_v4_address.one_to_one_nat.address' 2>/dev/null || echo "null")

if [ "$IP" == "null" ] || [ -z "$IP" ]; then
    IP=$(yc compute instance get --id $INSTANCE_ID --format json | grep -oP '"one_to_one_nat": \{ "address": "\K[^"]+')
fi

if [ -z "$IP" ]; then
    echo "❌ Error: Could not find public IP address!"
    exit 1
fi

# КРИТИЧЕСКИ ВАЖНО ДЛЯ ТВОЕГО ОБНОВЛЕННОГО CI/CD:
# Печатаем IP в формате, который легко поймать через grep в workflow
echo "DYNAMIC_IP_ADDRESS=$IP" 

echo "🎯 Public IP: $IP"

# --- ОЖИДАНИЕ SSH ---
echo "⏳ Waiting for SSH to become ready..."
for i in {1..20}; do
    if ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 -i "$SSH_KEY_PATH" yc-user@$IP "echo ready" &>/dev/null; then
        echo "✅ SSH ready."
        break
    fi
    echo "..."
    sleep 5
done

# --- ДЕПЛОЙ ФАЙЛОВ ---
echo "📂 Uploading configurations to ~/app..."
ssh -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" yc-user@$IP "mkdir -vp ~/app"
scp -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" docker-compose.yaml init.sql yc-user@$IP:~/app/
echo "✅ Files uploaded."

# --- ЗАПУСК DOCKER ---
echo "🐳 Starting Docker deployment process..."
ssh -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" yc-user@$IP <<EOF
    set -e
    if ! command -v docker &> /dev/null; then
        echo "📥 Docker not found. Installing..."
        curl -fsSL https://get.docker.com -o get-docker.sh
        sudo sh get-docker.sh
        echo "✅ Docker installed."
    fi

    cd ~/app
    echo "🔄 Pulling latest images..."
    sudo docker compose pull
    echo "🚀 Launching containers..."
    sudo docker compose up -d --force-recreate
EOF

echo "✅ Deployment Done!"