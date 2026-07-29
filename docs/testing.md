# Testing

## 1) Unit tests

Ejecuta:

```bash
go test ./...
```

Incluye tests de:

- Cifrado/descifrado de refresh tokens.
- Validacion de CSRF.

## 2) Smoke test API

Con la aplicacion levantada y sesion iniciada en navegador:

```bash
curl -i http://localhost:8080/health
```

Debe responder `200` con `{"status":"ok"}`.

## 3) End-to-end funcional

1. Login Spotify.
2. Sync manual.
3. Obtener meses.
4. Crear playlist mensual.
5. Crear playlist recomendaciones.

Resultado esperado:

- Endpoints devuelven `200`.
- En Spotify aparecen las playlists nuevas.

## 4) Prueba de seguridad basica

1. Llama un endpoint POST sin `X-CSRF-Token` y debe devolver `403`.
2. Usa token CSRF correcto y debe aceptar la solicitud.
3. Modifica cookie de sesion y debe rechazar por firma invalida.
