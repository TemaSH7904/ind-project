# --- Этап 1: Сборка (Builder) ---
# Берем образ с Go версии 1.22 на базе Alpine Linux
FROM golang:1.23-alpine AS builder

# Рабочая папка внутри сборщика
WORKDIR /app

# Сначала копируем файлы зависимостей (чтобы кэшировать их)
COPY go.mod go.sum ./
# Скачиваем зависимости
RUN go mod download

# Теперь копируем весь остальной исходный код
COPY . .

# Собираем приложение
# CGO_ENABLED=0 — отключает зависимость от системных библиотек C (важно для Alpine)
# GOOS=linux — указываем, что собираем под Linux
# -o app — имя выходного файла
RUN CGO_ENABLED=0 GOOS=linux go build -o app main.go

# --- Этап 2: Запуск (Runner) ---
# Берем чистый, легкий Alpine Linux
FROM alpine:latest

WORKDIR /root/

# Копируем ТОЛЬКО скомпилированный файл из первого этапа
COPY --from=builder /app/app .

# Говорим Докеру, что контейнер слушает порт 8080 (требование задания)
EXPOSE 8080

# Команда запуска
CMD ["./app"]