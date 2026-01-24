#!/bin/bash
set -e

echo "Building Docker image (Local)"

docker build -t docker7904/project-sem-1:latest .

echo "✅ Build success!"