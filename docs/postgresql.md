# PostgreSQL: Arranque y configuracion

Este proyecto usa PostgreSQL para persistir usuarios, tokens, historial de reproducciones y playlists generadas.

## Opcion A: Podman (recomendada)

1. Levantar contenedor:

```bash
podman run --name spotify-monthly-postgres \
  -e POSTGRES_USER=spotify_app \
  -e POSTGRES_PASSWORD=spotify_pass \
  -e POSTGRES_DB=spotify_monthly \
  -p 5432:5432 \
  -d postgres:16
```

2. Verificar que esta levantado:

```bash
podman ps
```

3. Probar conexion:

```bash
psql "postgres://spotify_app:spotify_pass@localhost:5432/spotify_monthly?sslmode=disable" -c "SELECT now();"
```

## Opcion B: PostgreSQL local (brew)

1. Instalar:

```bash
brew install postgresql@16
```

2. Arrancar servicio:

```bash
brew services start postgresql@16
```

3. Crear usuario y base de datos:

```bash
createuser -P spotify_app
createdb -O spotify_app spotify_monthly
```

4. Probar:

```bash
psql "postgres://spotify_app:spotify_pass@localhost:5432/spotify_monthly?sslmode=disable" -c "SELECT current_database();"
```

## Variables requeridas

Configura estas variables (puedes copiar `.env.example` a `.env` y exportarlas con tu metodo preferido):

- `DATABASE_URL`
- `DATABASE_MAX_CONNS`

Ejemplo:

```bash
export DATABASE_URL="postgres://spotify_app:spotify_pass@localhost:5432/spotify_monthly?sslmode=disable"
export DATABASE_MAX_CONNS=10
```

## Migraciones

La app ejecuta migraciones automaticamente al arrancar leyendo archivos SQL de `migrations/`.

- `schema_migrations` guarda versiones aplicadas.
- Las migraciones son idempotentes y se aplican en orden lexicografico.

## Seguridad recomendada para desarrollo y produccion

1. No uses credenciales por defecto fuera de local.
2. Usa contrasena larga para DB y rota secretos periodicamente.
3. En produccion activa TLS (`sslmode=require` o equivalente).
4. Limita acceso de red al puerto 5432 (firewall/VPC).
5. Crea rol de aplicacion con privilegios minimos.
6. Habilita backups y prueba restauracion regularmente.
