#!/bin/bash
set -e

# --- КОНФИГУРАЦИЯ ---
VM_NAME="project-sem-1-hard"
ZONE="ru-central1-a"
IMAGE_FAMILY="ubuntu-22-04-lts"

# --- НАСТРОЙКА КЛЮЧЕЙ ---
if [ -n "$CI" ]; then
    # В GitHub Actions создаем файлы ключей из секретов
    mkdir -p ~/.ssh
    echo "$SSH_PRIVATE_KEY" > ~/.ssh/id_ed25519
    chmod 600 ~/.ssh/id_ed25519
    echo "$SSH_PUBLIC_KEY" > ~/.ssh/id_ed25519.pub
    SSH_KEY_PATH="$HOME/.ssh/id_ed25519"
    SSH_PUB_KEY_PATH="$HOME/.ssh/id_ed25519.pub"
else
    # Локально используем твои пути (проверь, что путь верный!)
    SSH_KEY_PATH="$HOME/.ssh/deploy_key"
    SSH_PUB_KEY_PATH="$HOME/.ssh/deploy_key.pub"
fi

echo "🔍 Checking infrastructure..."

# --- ПРОВЕРКА МАШИНЫ ---
if yc compute instance get --name "$VM_NAME" > /dev/null 2>&1; then
    echo "✅ VM exists. Ensuring it is running..."
    yc compute instance start --name "$VM_NAME" > /dev/null 2>&1 || true
    INSTANCE_ID=$(yc compute instance get --name "$VM_NAME" --format json | grep -oP '"id": "\K[^"]+')
else
    echo "🚀 Creating NEW VM..."
    INSTANCE_ID=$(yc compute instance create \
      --name "$VM_NAME" \
      --zone "$ZONE" \
      --network-interface subnet-name=default-$ZONE,nat-ip-version=ipv4 \
      --create-boot-disk image-family=$IMAGE_FAMILY,size=15 \
      --ssh-key "$SSH_PUB_KEY_PATH" \
      --format json | grep -oP '"id": "\K[^"]+')
fi

# --- ПОЛУЧЕНИЕ IP ---
IP=$(yc compute instance get --id $INSTANCE_ID --format json | grep -oP '"address": "\K[^"]+' | head -1)
echo "🎯 IP: $IP"

if [ -n "$CI" ]; then
    echo "DEPLOY_IP=$IP" >> $GITHUB_ENV
fi

# --- ОЖИДАНИЕ SSH ---
echo "⏳ Waiting for SSH..."
for i in {1..20}; do
    if ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 -i "$SSH_KEY_PATH" yc-user@$IP "echo ready" &>/dev/null; then
        echo "SSH ready."
        break
    fi
    sleep 5
done

# --- ДЕПЛОЙ ---
echo "📂 Uploading configs..."
ssh -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" yc-user@$IP "mkdir -p ~/app" || true
scp -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" docker-compose.yaml init.sql yc-user@$IP:~/app/

echo "🐳 Deploying Docker..."
ssh -o StrictHostKeyChecking=no -i "$SSH_KEY_PATH" yc-user@$IP <<EOF
    if ! command -v docker &> /dev/null; then
        curl -fsSL https://get.docker.com -o get-docker.sh
        sudo sh get-docker.sh > /dev/null 2>&1
    fi
    cd ~/app
    sudo docker compose pull
    sudo docker compose up -d --force-recreate
EOF
echo "✅ Deployment Done!"