# spotify-monthly-playlist

Aplicacion en Go para:

1. Sincronizar historial reciente escuchado de Spotify.
2. Seleccionar meses y crear una playlist con canciones mas escuchadas.
3. Crear una segunda playlist con recomendaciones basadas en ese historial.

## Estado actual

Implementacion inicial (MVP base):

- OAuth Authorization Code Flow con Spotify.
- Persistencia en PostgreSQL.
- Migraciones automaticas al arrancar.
- Sync manual y autosync periodico de `recently-played`.
- Endpoint para meses disponibles.
- Creacion de playlist mensual.
- Creacion de playlist de recomendaciones.
- Seguridad avanzada: cookies HttpOnly firmadas, validacion state OAuth, CSRF para POST, cifrado AES-GCM de refresh token, renovacion automatica de access token, security headers y rate-limit basico.

## Requisitos

- Go 1.26+
- PostgreSQL 16+
- Credenciales Spotify Developer App

## Configuracion rapida

1. Copia variables:

```bash
cp .env.example .env
```

2. Reemplaza valores sensibles en `.env`:

- `SPOTIFY_CLIENT_ID`
- `SPOTIFY_CLIENT_SECRET`
- `SESSION_SECRET`
- `TOKEN_ENCRYPTION_KEY_BASE64`
- `DATABASE_URL`

Para generar `TOKEN_ENCRYPTION_KEY_BASE64` (32 bytes):

```bash
openssl rand -base64 32
```

3. En Spotify Developer Dashboard configura Redirect URI:

- `http://localhost:8080/auth/callback`

4. Exporta variables al entorno (ejemplo):

```bash
set -a
source .env
set +a
```

5. Ejecuta servidor:

```bash
go run ./cmd/server
```

6. Abre:

- `http://localhost:8080`

## Endpoints principales

- `GET /health`
- `GET /auth/login`
- `GET /auth/callback`
- `POST /auth/logout`
- `POST /sync/manual`
- `GET /api/months`
- `POST /api/playlists/monthly`
- `POST /api/playlists/recommendations`

## Seguridad actual

Incluido:

- Estado OAuth efimero y validado.
- Cookie de sesion `HttpOnly` y firmada.
- `SESSION_SECRET` obligatorio con longitud minima.
- `TOKEN_ENCRYPTION_KEY_BASE64` obligatorio (32 bytes en base64).
- Security headers (CSP, X-Frame-Options, nosniff).
- Rate limit global en memoria por IP.
- Cifrado AES-GCM del `refresh_token` antes de persistir en PostgreSQL.
- Validacion CSRF (`X-CSRF-Token`/campo hidden) en endpoints `POST`.
- Renovacion automatica de `access_token` (on-demand y en autosync).

Pendiente recomendado:

1. Persistir sesiones en Redis/DB para escalado horizontal.
2. Reintentos/backoff con parser de `Retry-After` para 429 de Spotify.
3. Tests de integracion con mocks de Spotify y DB de pruebas.

## Como probar la aplicacion

### Prueba automatizada

```bash
go test ./...
```

### Prueba manual end-to-end

1. Levanta PostgreSQL (ver [docs/postgresql.md](docs/postgresql.md)).
2. Copia `.env.example` a `.env` y rellena secretos.
3. Exporta variables de entorno.
4. Arranca la app con `go run ./cmd/server`.
5. En navegador abre `http://localhost:8080`.
6. Pulsa "Conectar con Spotify" y acepta permisos.
7. Pulsa "Sincronizar historial".
8. Pulsa "Ver meses disponibles" y usa esos meses para crear playlists.
9. Crea playlist mensual y playlist de recomendaciones.
10. Verifica en Spotify que ambas playlists existen y son privadas.

## PostgreSQL

Documentacion de arranque en [docs/postgresql.md](docs/postgresql.md).

## Testing

Guia adicional de testing en [docs/testing.md](docs/testing.md).
