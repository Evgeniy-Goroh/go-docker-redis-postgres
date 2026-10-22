# mg-transport-vk
The service for connecting VK to MG

# 1. Перезапустить с новым .env
docker compose down
docker compose up -d

# 2. Проверить, что Redis требует пароль
docker compose exec redis redis-cli ping
# Должно быть: (error) NOAUTH Authentication required.

# 3. Проверить с паролем
docker compose exec redis redis-cli -a "$REDIS_PASSWORD" ping
# Должно быть: PONG

# 4. Проверить приложение
docker compose logs mg_vk
curl http://localhost:3001/health
# {"postgres":"ok","redis":"ok","status":"ok"}
